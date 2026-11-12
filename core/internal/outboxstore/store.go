// file: core/internal/outboxstore/store.go
// version: 1.0.0
// guid: 754dc428-ceb7-4876-824c-cde4fb15a4f3
// last-edited: 2026-11-12

package outboxstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Errors of the store. A decode failure of a row of another version wraps
// codec.ErrUnknownVersion; it is returned as it is, never defaulted.
var (
	// ErrPartitionsChanged: the topic's declared partition count differs from
	// the one its high-water mark was stamped with. Re-spreading the keys
	// would reorder them, so the topic is refused.
	ErrPartitionsChanged = errors.New("outboxstore: the topic's partition count changed since its messages were sequenced")
	// ErrAlreadyPast: the pair's cursor is already at or past the message.
	// Another writer (a zombie relay, an operator) got there first, and the
	// caller's staged work must be dropped, not committed.
	ErrAlreadyPast = errors.New("outboxstore: the pair's cursor is already past the message")
	// ErrNoCursor: the pair has no cursor row yet (a consumer registered
	// since the relay's last run); the relay creates it before delivering.
	ErrNoCursor = errors.New("outboxstore: the pair has no cursor")
)

// Store is the c/outbox/ keyspace. It holds the handle, which is the
// capability to write it: Stage binds it to a producer's batch.
type Store struct{ ks storage.Keyspace }

// New returns the store over ks, the c/outbox/ keyspace.
func New(ks storage.Keyspace) *Store { return &Store{ks: ks} }

// OpenBatch opens an indexed batch on the outbox's own handle, the relay
// batch of 05-events §5E.4 (an indexed one, because a run store refuses a
// plain batch for any write that reads first).
func (s *Store) OpenBatch() *storage.Batch { return s.ks.NewIndexedBatch() }

// ---- intake

// StageIntake binds the outbox handle to b and stages one intake row under
// the key in/<topic>/<id>. It writes only in/, whatever b's origin is
// (05-events §5E.4). id is the row's own key and normally rec.ID; a retried
// dead letter keeps its original rec.ID (the message's IdempotencyKey) under a
// fresh id, so two retries of one message can never share a key and overwrite
// each other.
func (s *Store) StageIntake(b *storage.Batch, topic, id string, rec Intake) error {
	if b == nil {
		return errors.New("outboxstore: StageIntake needs a batch")
	}
	raw, err := intakeCodec().Encode(rec)
	if err != nil {
		return err
	}
	return b.Bind(s.ks).In(s.ks).Set(InKey(topic, id), raw)
}

// IntakeRow is one decoded intake row.
type IntakeRow struct {
	ID     string
	Intake Intake
}

// ScanIntake returns the rows of topic that are due at now, in key (ULID)
// order, up to limit of them and, after the first, no more than maxBytes of
// payload. Rows not yet due are counted in delayed and left where they are.
// A row that fails to decode ends the scan: the rows before it come back with
// the error, so the caller sequences what precedes it and stops there. Memory
// is bounded by limit and maxBytes however many rows the prefix holds.
func (s *Store) ScanIntake(ctx context.Context, topic string, now time.Time, limit, maxBytes int) (rows []IntakeRow, delayed int, err error) {
	pre := InPrefix(topic)
	size := 0
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: pre, End: keys.PrefixEnd(pre)}) {
		if serr != nil {
			return rows, delayed, serr
		}
		_, id, perr := ParseInKey(kv.Key)
		if perr != nil {
			return rows, delayed, perr
		}
		in, derr := intakeCodec().Decode(kv.Value)
		if derr != nil {
			return rows, delayed, fmt.Errorf("intake row %s/%s: %w", topic, id, derr)
		}
		if in.NotBefore.After(now) {
			delayed++
			continue
		}
		if len(rows) > 0 && size+len(in.Payload) > maxBytes {
			break
		}
		rows = append(rows, IntakeRow{ID: id, Intake: in})
		size += len(in.Payload)
		if len(rows) >= limit {
			break
		}
	}
	return rows, delayed, nil
}

// IntakeStats counts the intake rows of topic (up to maxCount) and reads the
// oldest one's staged time; capped says the count stopped at maxCount.
func (s *Store) IntakeStats(ctx context.Context, topic string, maxCount int) (n int, capped bool, oldest time.Time, err error) {
	pre := InPrefix(topic)
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: pre, End: keys.PrefixEnd(pre)}) {
		if serr != nil {
			return n, false, oldest, serr
		}
		if n == maxCount {
			return n, true, oldest, nil
		}
		in, derr := intakeCodec().Decode(kv.Value)
		if derr != nil {
			return n, false, oldest, derr
		}
		if n == 0 || in.StagedAt.Before(oldest) {
			oldest = in.StagedAt
		}
		n++
	}
	return n, false, oldest, nil
}

// IntakeTopics lists the topics that have at least one intake row, by seeking
// past each topic's rows, so the cost is one seek per topic.
func (s *Store) IntakeTopics(ctx context.Context) (out []string, err error) {
	root := InRoot()
	it, err := s.ks.NewIterator(ctx, storage.ScanOptions{Start: root, End: keys.PrefixEnd(root), KeysOnly: true})
	if err != nil {
		return nil, err
	}
	defer func() {
		if cerr := it.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("outboxstore: close iterator: %w", cerr)
		}
	}()
	for ok := it.First(); ok; {
		topic, _, perr := ParseInKey(it.Key())
		if perr != nil {
			return out, perr
		}
		out = append(out, topic)
		ok = it.SeekGE(keys.PrefixEnd(InPrefix(topic)))
	}
	return out, it.Err()
}

// ---- sequencing

// ReadHigh reads seq/<topic> (committed state).
func (s *Store) ReadHigh(ctx context.Context, topic string) (High, bool, error) {
	raw, err := s.ks.Get(ctx, SeqKey(topic))
	if errors.Is(err, storage.ErrNotFound) {
		return High{}, false, nil
	}
	if err != nil {
		return High{}, false, err
	}
	h, err := highCodec().Decode(raw)
	if err != nil {
		return High{}, false, fmt.Errorf("seq row %s: %w", topic, err)
	}
	return h, true, nil
}

// StageSequence stages one topic chunk of commit-ordered intake into b: it
// reads seq/<topic> with GetForUpdate (an Expect checked at commit, so a relay
// whose read went stale fails its commit and can never number a message
// twice), writes each row to m/<topic>/<pp>/<seq> with consecutive seqs in
// rows' order, deletes its intake row, and advances seq/<topic>. The seqs and
// the high-water mark that covers them are one batch: a crash before its
// commit leaves the intake rows to be sequenced again, and nothing after it
// needs redoing. partitions is the topic's declared count; a stamp that
// differs is ErrPartitionsChanged and stages nothing.
func (s *Store) StageSequence(ctx context.Context, b *storage.Batch, topic string, partitions int, rows []IntakeRow) (High, error) {
	b.Bind(s.ks)
	hi := High{Partitions: partitions}
	raw, err := b.GetForUpdate(ctx, s.ks, SeqKey(topic))
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return High{}, err
	default:
		if hi, err = highCodec().Decode(raw); err != nil {
			return High{}, fmt.Errorf("seq row %s: %w", topic, err)
		}
		if hi.Partitions != partitions {
			return High{}, fmt.Errorf("%w: %s was sequenced over %d partitions and now declares %d", ErrPartitionsChanged, topic, hi.Partitions, partitions)
		}
	}
	w := b.In(s.ks)
	for _, r := range rows {
		hi.High++
		enc, err := msgCodec().Encode(r.Intake.Msg)
		if err != nil {
			return High{}, err
		}
		pp := Partition(r.Intake.Key, partitions)
		if err := w.Set(MsgKey(topic, pp, hi.High), enc); err != nil {
			return High{}, err
		}
		if err := w.Delete(InKey(topic, r.ID)); err != nil {
			return High{}, err
		}
	}
	enc, err := highCodec().Encode(hi)
	if err != nil {
		return High{}, err
	}
	if err := w.Set(SeqKey(topic), enc); err != nil {
		return High{}, err
	}
	return hi, nil
}

// ---- messages

// MsgRow is one decoded sequenced message.
type MsgRow struct {
	Seq uint64
	PP  int
	Msg Msg
}

// MessagesAfter returns up to limit messages of (topic, pp) with a seq above
// after, in seq order. A row that fails to decode ends the scan: the rows
// before it come back with the error.
func (s *Store) MessagesAfter(ctx context.Context, topic string, pp int, after uint64, limit int) ([]MsgRow, error) {
	pre := MsgPrefix(topic, pp)
	start := MsgKey(topic, pp, after+1)
	if after == ^uint64(0) {
		return nil, nil
	}
	var out []MsgRow
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: start, End: keys.PrefixEnd(pre), Limit: limit}) {
		if serr != nil {
			return out, serr
		}
		_, _, seq, perr := ParseMsgKey(kv.Key)
		if perr != nil {
			return out, perr
		}
		m, derr := msgCodec().Decode(kv.Value)
		if derr != nil {
			return out, fmt.Errorf("message row %s/%02d/%d: %w", topic, pp, seq, derr)
		}
		out = append(out, MsgRow{Seq: seq, PP: pp, Msg: m})
	}
	return out, nil
}

// MessageStats counts the messages of (topic, pp) above after (up to
// maxCount) and returns the oldest one's staged time, for lag and backlog.
func (s *Store) MessageStats(ctx context.Context, topic string, pp int, after uint64, maxCount int) (n int, capped bool, oldest time.Time, err error) {
	pre := MsgPrefix(topic, pp)
	if after == ^uint64(0) {
		return 0, false, oldest, nil
	}
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: MsgKey(topic, pp, after+1), End: keys.PrefixEnd(pre)}) {
		if serr != nil {
			return n, false, oldest, serr
		}
		if n == maxCount {
			return n, true, oldest, nil
		}
		if n == 0 {
			m, derr := msgCodec().Decode(kv.Value)
			if derr != nil {
				return n, false, oldest, derr
			}
			oldest = m.StagedAt
		}
		n++
	}
	return n, false, oldest, nil
}

// CountThrough counts the messages of (topic, pp) with a seq above after and at
// or below through, by keys alone.
func (s *Store) CountThrough(ctx context.Context, topic string, pp int, after, through uint64) (int, error) {
	if after >= through || after == ^uint64(0) || through == ^uint64(0) {
		return 0, nil
	}
	n := 0
	for _, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: MsgKey(topic, pp, after+1), End: MsgKey(topic, pp, through+1), KeysOnly: true}) {
		if serr != nil {
			return n, serr
		}
		n++
	}
	return n, nil
}

// StageDeleteThrough stages the removal of the messages of (topic, pp) with a
// seq at or below seq. m/ is not Serialized, so a range delete is allowed.
func (s *Store) StageDeleteThrough(b *storage.Batch, topic string, pp int, seq uint64) error {
	if seq == ^uint64(0) {
		return errors.New("outboxstore: StageDeleteThrough past the last seq")
	}
	return b.Bind(s.ks).In(s.ks).DeleteRange(MsgKey(topic, pp, 0), MsgKey(topic, pp, seq+1))
}

// OldestMessages returns the first limit messages of (topic, pp) (retention's
// age scan for a topic nobody declared a consumer for).
func (s *Store) OldestMessages(ctx context.Context, topic string, pp, limit int) ([]MsgRow, error) {
	pre := MsgPrefix(topic, pp)
	var out []MsgRow
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: pre, End: keys.PrefixEnd(pre), Limit: limit}) {
		if serr != nil {
			return out, serr
		}
		_, _, seq, perr := ParseMsgKey(kv.Key)
		if perr != nil {
			return out, perr
		}
		m, derr := msgCodec().Decode(kv.Value)
		if derr != nil {
			return out, fmt.Errorf("message row %s/%02d/%d: %w", topic, pp, seq, derr)
		}
		out = append(out, MsgRow{Seq: seq, PP: pp, Msg: m})
	}
	return out, nil
}

// ---- pairs

// Pair names one (consumer, topic, partition) the relay delivers.
type Pair struct {
	Consumer, Topic string
	PP              int
}

// PairState is a pair's cursor and retry record.
type PairState struct {
	Has bool // the cursor row exists
	Cur Cursor
	Try Try
}

// ReadPair reads a pair's state from committed storage, without guarding it.
func (s *Store) ReadPair(ctx context.Context, p Pair) (PairState, error) {
	var st PairState
	raw, err := s.ks.Get(ctx, CurKey(p.Consumer, p.Topic, p.PP))
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return st, err
	default:
		if st.Cur, err = curCodec().Decode(raw); err != nil {
			return st, fmt.Errorf("cursor row %s/%s/%02d: %w", p.Consumer, p.Topic, p.PP, err)
		}
		st.Has = true
	}
	return s.readTry(ctx, p, st)
}

func (s *Store) readTry(ctx context.Context, p Pair, st PairState) (PairState, error) {
	raw, err := s.ks.Get(ctx, TryKey(p.Consumer, p.Topic, p.PP))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return st, nil
	case err != nil:
		return st, err
	}
	if st.Try, err = tryCodec().Decode(raw); err != nil {
		return st, fmt.Errorf("try row %s/%s/%02d: %w", p.Consumer, p.Topic, p.PP, err)
	}
	return st, nil
}

// LoadPair reads the pair's cursor through b with GetForUpdate (an Expect
// checked at commit: absent stays absent, present stays at the Rev read) and
// its try record from committed state. Every writer of the pair's state calls
// it first and ends with StagePair, which bumps Rev, so no two batches that
// touched the pair can both commit.
func (s *Store) LoadPair(ctx context.Context, b *storage.Batch, p Pair) (PairState, error) {
	b.Bind(s.ks)
	var st PairState
	raw, err := b.GetForUpdate(ctx, s.ks, CurKey(p.Consumer, p.Topic, p.PP))
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return st, err
	default:
		if st.Cur, err = curCodec().Decode(raw); err != nil {
			return st, fmt.Errorf("cursor row %s/%s/%02d: %w", p.Consumer, p.Topic, p.PP, err)
		}
		st.Has = true
	}
	return s.readTry(ctx, p, st)
}

// StagePair writes the pair's state: the cursor with Rev+1 and At now, and the
// try record, deleted when it holds no entry. st must come from LoadPair on
// the same batch.
func (s *Store) StagePair(b *storage.Batch, p Pair, st PairState, now time.Time) error {
	st.Cur.Rev++
	st.Cur.At = now
	rawCur, err := curCodec().Encode(st.Cur)
	if err != nil {
		return err
	}
	w := b.In(s.ks)
	if err := w.Set(CurKey(p.Consumer, p.Topic, p.PP), rawCur); err != nil {
		return err
	}
	if len(st.Try.Entries) == 0 {
		return w.Delete(TryKey(p.Consumer, p.Topic, p.PP))
	}
	rawTry, err := tryCodec().Encode(st.Try)
	if err != nil {
		return err
	}
	return w.Set(TryKey(p.Consumer, p.Topic, p.PP), rawTry)
}

// StageDead stages a dead letter of consumer on topic at seq. dead/ is
// Serialized, so the key is read with GetForUpdate first: a letter another
// batch wrote or removed since fails this commit instead of being overwritten.
func (s *Store) StageDead(ctx context.Context, b *storage.Batch, consumer, topic string, seq uint64, d Dead) error {
	raw, err := deadCodec().Encode(d)
	if err != nil {
		return err
	}
	b.Bind(s.ks)
	key := DeadKey(consumer, topic, seq)
	if _, err := b.GetForUpdate(ctx, s.ks, key); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	return b.In(s.ks).Set(key, raw)
}

// StageCursorInit creates the cursors of a newly registered consumer, one per
// partition that has none: at the topic's high-water mark ("now"), or at 0
// with fromBeginning (the oldest retained message). It reads seq/<topic> with
// GetForUpdate and each cursor with LoadPair, so a cursor or a message
// sequenced meanwhile fails the commit. It returns how many it created.
func (s *Store) StageCursorInit(ctx context.Context, b *storage.Batch, consumer, topic string, partitions int, fromBeginning bool, now time.Time) (int, error) {
	b.Bind(s.ks)
	var hi High
	raw, err := b.GetForUpdate(ctx, s.ks, SeqKey(topic))
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return 0, err
	default:
		if hi, err = highCodec().Decode(raw); err != nil {
			return 0, fmt.Errorf("seq row %s: %w", topic, err)
		}
	}
	start := hi.High
	if fromBeginning {
		start = 0
	}
	created := 0
	for pp := range partitions {
		p := Pair{Consumer: consumer, Topic: topic, PP: pp}
		st, err := s.LoadPair(ctx, b, p)
		if err != nil {
			return created, err
		}
		if st.Has {
			continue
		}
		st.Cur = Cursor{Seq: start}
		if err := s.StagePair(b, p, st, now); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

// StageDropPair deletes a pair's cursor and try record (an orphan consumer's,
// once its holding time ran out), reading the cursor with GetForUpdate first.
func (s *Store) StageDropPair(ctx context.Context, b *storage.Batch, p Pair) error {
	b.Bind(s.ks)
	if _, err := b.GetForUpdate(ctx, s.ks, CurKey(p.Consumer, p.Topic, p.PP)); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	w := b.In(s.ks)
	if err := w.Delete(CurKey(p.Consumer, p.Topic, p.PP)); err != nil {
		return err
	}
	return w.Delete(TryKey(p.Consumer, p.Topic, p.PP))
}

// CursorRow is one cursor of the store.
type CursorRow struct {
	Pair Pair
	Cur  Cursor
}

// AllCursors lists every cursor, in key order: consumers x topics x
// partitions, a small closed set. A row that fails to decode ends the scan
// with the rows before it and the error.
func (s *Store) AllCursors(ctx context.Context) ([]CursorRow, error) {
	root := CurRoot()
	var out []CursorRow
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: root, End: keys.PrefixEnd(root)}) {
		if serr != nil {
			return out, serr
		}
		c, t, pp, perr := ParseCurKey(kv.Key)
		if perr != nil {
			return out, perr
		}
		cur, derr := curCodec().Decode(kv.Value)
		if derr != nil {
			return out, fmt.Errorf("cursor row %s/%s/%02d: %w", c, t, pp, derr)
		}
		out = append(out, CursorRow{Pair: Pair{Consumer: c, Topic: t, PP: pp}, Cur: cur})
	}
	return out, nil
}

// ---- dead letters

// DeadRow is one decoded dead letter.
type DeadRow struct {
	Consumer, Topic string
	Seq             uint64
	Dead            Dead
}

// GetDead reads one dead letter; false when there is none.
func (s *Store) GetDead(ctx context.Context, consumer, topic string, seq uint64) (Dead, bool, error) {
	raw, err := s.ks.Get(ctx, DeadKey(consumer, topic, seq))
	if errors.Is(err, storage.ErrNotFound) {
		return Dead{}, false, nil
	}
	if err != nil {
		return Dead{}, false, err
	}
	d, err := deadCodec().Decode(raw)
	if err != nil {
		return Dead{}, false, fmt.Errorf("dead row %s/%s/%d: %w", consumer, topic, seq, err)
	}
	return d, true, nil
}

// ScanDead returns up to limit dead letters after the key `after` (nil: from
// the start), in key order, and the key to pass as `after` for the next page
// (nil when this page reached the end). A row that fails to decode ends the
// page with the error.
func (s *Store) ScanDead(ctx context.Context, after []byte, limit int) ([]DeadRow, []byte, error) {
	root := DeadRoot()
	start := root
	if after != nil {
		start = append(append([]byte(nil), after...), 0)
	}
	var out []DeadRow
	var last []byte
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: start, End: keys.PrefixEnd(root), Limit: limit}) {
		if serr != nil {
			return out, nil, serr
		}
		c, t, seq, perr := ParseDeadKey(kv.Key)
		if perr != nil {
			return out, nil, perr
		}
		d, derr := deadCodec().Decode(kv.Value)
		if derr != nil {
			return out, nil, fmt.Errorf("dead row %s/%s/%d: %w", c, t, seq, derr)
		}
		out = append(out, DeadRow{Consumer: c, Topic: t, Seq: seq, Dead: d})
		last = append(last[:0], kv.Key...)
	}
	if len(out) < limit || limit <= 0 {
		return out, nil, nil
	}
	return out, last, nil
}

// DeadCounts counts the dead letters by consumer (keys only).
func (s *Store) DeadCounts(ctx context.Context) (map[string]int, error) {
	root := DeadRoot()
	out := map[string]int{}
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: root, End: keys.PrefixEnd(root), KeysOnly: true}) {
		if serr != nil {
			return out, serr
		}
		c, _, _, perr := ParseDeadKey(kv.Key)
		if perr != nil {
			return out, perr
		}
		out[c]++
	}
	return out, nil
}

// FindDead finds the dead letter of consumer at seq, whatever its topic: a
// consumer reads one topic, but an operator names only the consumer and the
// seq. It scans the consumer's dead-letter keys only.
func (s *Store) FindDead(ctx context.Context, consumer string, seq uint64) (topic string, d Dead, found bool, err error) {
	pre := DeadPrefix(consumer)
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: pre, End: keys.PrefixEnd(pre), KeysOnly: true}) {
		if serr != nil {
			return "", Dead{}, false, serr
		}
		_, t, sq, perr := ParseDeadKey(kv.Key)
		if perr != nil {
			return "", Dead{}, false, perr
		}
		if sq != seq {
			continue
		}
		d, ok, err := s.GetDead(ctx, consumer, t, seq)
		return t, d, ok, err
	}
	return "", Dead{}, false, nil
}

// LoadDead reads the dead letter at (consumer, topic, seq) through b with
// GetForUpdate: the read is an Expect checked at commit (the letter present at
// the value read, or absent), so two batches that read one letter can never
// both commit a write built from it. found is false when there is none.
func (s *Store) LoadDead(ctx context.Context, b *storage.Batch, consumer, topic string, seq uint64) (d Dead, found bool, err error) {
	b.Bind(s.ks)
	raw, err := b.GetForUpdate(ctx, s.ks, DeadKey(consumer, topic, seq))
	if errors.Is(err, storage.ErrNotFound) {
		return Dead{}, false, nil
	}
	if err != nil {
		return Dead{}, false, err
	}
	if d, err = deadCodec().Decode(raw); err != nil {
		return Dead{}, false, fmt.Errorf("dead row %s/%s/%d: %w", consumer, topic, seq, err)
	}
	return d, true, nil
}

// StageDeleteDead stages the removal of one dead letter. The batch must have
// read the letter with LoadDead first: dead/ is Serialized, and a delete
// without that read is refused at commit (storage.ErrBlindWrite) instead of
// removing a letter another operator already handled.
func (s *Store) StageDeleteDead(b *storage.Batch, consumer, topic string, seq uint64) error {
	return b.Bind(s.ks).In(s.ks).Delete(DeadKey(consumer, topic, seq))
}
