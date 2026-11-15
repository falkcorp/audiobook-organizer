// file: core/internal/outboxstore/store.go
// version: 1.2.1
// guid: 754dc428-ceb7-4876-824c-cde4fb15a4f3
// last-edited: 2026-11-15

package outboxstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// Errors of the store. A row that does not decode is a *RowError: of another
// version it wraps codec.ErrUnknownVersion, otherwise ErrCorruptRow; either
// way it is returned, never defaulted or skipped.
var (
	// ErrCorruptRow: a row of the right version does not decode. The error
	// names the row and never carries the decoder's text: a JSON error quotes
	// the value it rejected, and a row's value is a payload.
	ErrCorruptRow = errors.New("outboxstore: a row does not decode")
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

// RowError is a row that failed to decode. Where names it (the family, topic,
// consumer, partition and seq: names and numbers, never a value) and Err is
// ErrCorruptRow or codec.ErrUnknownVersion.
type RowError struct {
	Where string
	Err   error
}

func (e *RowError) Error() string { return e.Where + ": " + e.Err.Error() }
func (e *RowError) Unwrap() error { return e.Err }

// rowErr is the error of the row named where, which failed to decode with err.
// Only the class of err survives.
func rowErr(where string, err error) error {
	if errors.Is(err, codec.ErrUnknownVersion) {
		return &RowError{Where: where, Err: codec.ErrUnknownVersion}
	}
	return &RowError{Where: where, Err: ErrCorruptRow}
}

// Store is the c/outbox/ keyspace. It holds the handle, which is the
// capability to write it: Stage binds it to a producer's batch.
type Store struct {
	ks storage.Keyspace
	// beforeRepairCommit is a test seam: RepairDeadCounts runs it with its fixes
	// computed and before the batch that applies them is opened. Nil in production.
	beforeRepairCommit func()
	// scanned counts the keys the dead-letter counting reads have visited
	// (DeadTotals, DeadCounts, RepairDeadCounts); see KeysScanned.
	scanned atomic.Int64
}

// KeysScanned is how many keys the dead-letter counting reads (DeadTotals,
// DeadCounts, RepairDeadCounts) have visited since the store was made. A test
// reads it to show that dead-lettering costs the same per letter however many
// letters the store holds.
func (s *Store) KeysScanned() int64 { return s.scanned.Load() }

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
			return rows, delayed, rowErr(fmt.Sprintf("intake row %s/%s", topic, id), derr)
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
			return n, false, oldest, rowErr("intake row of "+topic, derr)
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
		return High{}, false, rowErr(fmt.Sprintf("seq row %s", topic), err)
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
			return High{}, rowErr(fmt.Sprintf("seq row %s", topic), err)
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
			return out, rowErr(fmt.Sprintf("message row %s/%02d/%d", topic, pp, seq), derr)
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
				return n, false, oldest, rowErr(fmt.Sprintf("message row %s/%02d/%d", topic, pp, after+1), derr)
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
			return out, rowErr(fmt.Sprintf("message row %s/%02d/%d", topic, pp, seq), derr)
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
			return st, rowErr(fmt.Sprintf("cursor row %s/%s/%02d", p.Consumer, p.Topic, p.PP), err)
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
		return st, rowErr(fmt.Sprintf("try row %s/%s/%02d", p.Consumer, p.Topic, p.PP), err)
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
			return st, rowErr(fmt.Sprintf("cursor row %s/%s/%02d", p.Consumer, p.Topic, p.PP), err)
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
// A letter that is new also raises its pair's counter (deadn/), in this same
// batch; one that replaces a letter of the same key leaves it where it was.
func (s *Store) StageDead(ctx context.Context, b *storage.Batch, consumer, topic string, seq uint64, d Dead) error {
	raw, err := deadCodec().Encode(d)
	if err != nil {
		return err
	}
	b.Bind(s.ks)
	key := DeadKey(consumer, topic, seq)
	_, err = b.GetForUpdate(ctx, s.ks, key)
	created := errors.Is(err, storage.ErrNotFound)
	if err != nil && !created {
		return err
	}
	if err := b.In(s.ks).Set(key, raw); err != nil {
		return err
	}
	if !created {
		return nil
	}
	return s.bumpDeadCount(ctx, b, consumer, topic, d.Part, 1)
}

// bumpDeadCount adds delta to the counter of (consumer, topic, partition part) through
// b: the counter is read with GetForUpdate (a repeated read in one batch sees
// the batch's own earlier bump and keeps the first read's precondition), so two
// batches that bump one counter from the same value cannot both commit. Every
// write raises the counter's Rev, so its bytes change on every bump, including
// one that leaves N where it was. A zero count has no row. A bump that would
// take N below zero (a letter removed against an absent or zero counter: drift)
// writes a zero row instead of deleting a key that may already be absent: the
// row is a write RepairDeadCounts's precondition can see, and RepairDeadCounts
// treats any zero row as a counter to correct.
func (s *Store) bumpDeadCount(ctx context.Context, b *storage.Batch, consumer, topic string, part, delta int) error {
	key := DeadCountKey(consumer, topic, part)
	var cur DeadCount
	raw, err := b.GetForUpdate(ctx, s.ks, key)
	switch {
	case errors.Is(err, storage.ErrNotFound):
	case err != nil:
		return err
	default:
		var derr error
		if cur, derr = deadCountCodec().Decode(raw); derr != nil {
			return rowErr(fmt.Sprintf("dead count %s/%s/%s", consumer, topic, shardOf(part)), derr)
		}
	}
	next := DeadCount{N: cur.N + delta, Rev: cur.Rev + 1}
	w := b.In(s.ks)
	if next.N == 0 && delta < 0 && cur.N > 0 {
		return w.Delete(key) // the counter reached zero by counting: no row
	}
	next.N = max(0, next.N)
	enc, err := deadCountCodec().Encode(next)
	if err != nil {
		return err
	}
	return w.Set(key, enc)
}

// StageCursorInit creates the cursors of a newly registered consumer, one per
// partition that has none: at the topic's high-water mark ("now"), or at 0
// with fromBeginning (the oldest retained message). It reads seq/<topic> with
// GetForUpdate and each cursor with LoadPair, so a cursor or a message
// sequenced meanwhile fails the commit. It returns how many it created. A
// stamp that differs from partitions is ErrPartitionsChanged and creates
// nothing: cursors for the wrong partition set would strand messages.
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
			return 0, rowErr(fmt.Sprintf("seq row %s", topic), err)
		}
		if hi.Partitions != partitions {
			return 0, fmt.Errorf("%w: %s was sequenced over %d partitions and now declares %d", ErrPartitionsChanged, topic, hi.Partitions, partitions)
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
// with the rows before it and the error. Retention and the relay read the
// cursors through it: a floor computed without a cursor would let a sweep
// delete what that cursor still needs.
func (s *Store) AllCursors(ctx context.Context) ([]CursorRow, error) {
	rows, bad, err := s.scanCursors(ctx, false)
	if err == nil && len(bad) > 0 {
		err = bad[0].Err
	}
	return rows, err
}

// UnreadableCursor is a cursor row whose value did not decode: the pair it
// belongs to (from its key) and the RowError.
type UnreadableCursor struct {
	Pair Pair
	Err  error
}

// ReadCursors is AllCursors for a reader that has to go on past a bad row, an
// operator's status read: a row whose value does not decode is left out of rows
// and named in unreadable, and the scan continues. Only a failure of the store
// itself, or of a key, ends it. Nothing that decides what to delete may use it.
func (s *Store) ReadCursors(ctx context.Context) (rows []CursorRow, unreadable []UnreadableCursor, err error) {
	return s.scanCursors(ctx, true)
}

// scanCursors is the one scan behind both: with skip it carries on past a row
// that does not decode, without it it stops at the first one.
func (s *Store) scanCursors(ctx context.Context, skip bool) (out []CursorRow, bad []UnreadableCursor, err error) {
	root := CurRoot()
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: root, End: keys.PrefixEnd(root)}) {
		if serr != nil {
			return out, bad, serr
		}
		c, t, pp, perr := ParseCurKey(kv.Key)
		if perr != nil {
			return out, bad, perr
		}
		pair := Pair{Consumer: c, Topic: t, PP: pp}
		cur, derr := curCodec().Decode(kv.Value)
		if derr != nil {
			bad = append(bad, UnreadableCursor{Pair: pair, Err: rowErr(fmt.Sprintf("cursor row %s/%s/%02d", c, t, pp), derr)})
			if !skip {
				return out, bad, nil
			}
			continue
		}
		out = append(out, CursorRow{Pair: pair, Cur: cur})
	}
	return out, bad, nil
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
		return Dead{}, false, rowErr(fmt.Sprintf("dead row %s/%s/%d", consumer, topic, seq), err)
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
			return out, nil, rowErr(fmt.Sprintf("dead row %s/%s/%d", c, t, seq), derr)
		}
		out = append(out, DeadRow{Consumer: c, Topic: t, Seq: seq, Dead: d})
		last = append(last[:0], kv.Key...)
	}
	if len(out) < limit || limit <= 0 {
		return out, nil, nil
	}
	return out, last, nil
}

// DeadCounts counts the dead letters by consumer (keys only). A key that is not
// a dead-letter key is not counted and not an error: unparseable says how many
// there were, so the caller can name them.
func (s *Store) DeadCounts(ctx context.Context) (counts map[string]int, unparseable int, err error) {
	root := DeadRoot()
	out := map[string]int{}
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: root, End: keys.PrefixEnd(root), KeysOnly: true}) {
		if serr != nil {
			return out, unparseable, serr
		}
		s.scanned.Add(1)
		c, _, _, perr := ParseDeadKey(kv.Key)
		if perr != nil {
			unparseable++
			continue
		}
		out[c]++
	}
	return out, unparseable, nil
}

// UnreadableCount is a counter row that does not decode: Err is a *RowError.
type UnreadableCount struct {
	Consumer, Topic, Shard string
	Err                    error
}

// DeadTotal is what DeadTotals read: the dead letters by consumer, summed from
// the counters that decode, the counters that did not (named, and left out of
// the sums; RepairDeadCounts rewrites them), and how many keys under deadn/ are
// no counter's at all.
type DeadTotal struct {
	ByConsumer  map[string]int
	Unreadable  []UnreadableCount
	Unparseable int
}

// DeadTotals is the dead letters by consumer, summed from the per-pair
// counters (deadn/): one row per (consumer, topic, partition) that holds a
// letter, however many letters that is. A counter that does not decode does
// not fail the read: it is named in Unreadable, because it is derived data and
// the next repair rewrites it.
func (s *Store) DeadTotals(ctx context.Context) (DeadTotal, error) {
	root := DeadCountRoot()
	out := DeadTotal{ByConsumer: map[string]int{}}
	for kv, serr := range s.ks.Scan(ctx, storage.ScanOptions{Start: root, End: keys.PrefixEnd(root)}) {
		if serr != nil {
			return DeadTotal{}, serr
		}
		s.scanned.Add(1)
		c, t, shard, perr := ParseDeadCountKey(kv.Key)
		if perr != nil {
			out.Unparseable++
			continue
		}
		n, derr := deadCountCodec().Decode(kv.Value)
		if derr != nil {
			out.Unreadable = append(out.Unreadable, UnreadableCount{Consumer: c, Topic: t, Shard: shard,
				Err: rowErr(fmt.Sprintf("dead count %s/%s/%s", c, t, shard), derr)})
			continue
		}
		out.ByConsumer[c] += n.N
	}
	return out, nil
}

// DeadCountFix is one counter RepairDeadCounts rewrote: Had is what the counter
// held (zero when it had no row), Want what the letters add up to.
type DeadCountFix struct {
	Consumer, Topic, Shard string
	Had, Want              int
	Absent                 bool // the counter had no row
	Unreadable             bool // the counter's row did not decode
}

// DeadCountRepair is what RepairDeadCounts did: the counters it rewrote, and
// how many keys under dead/ and deadn/ it could not parse and so left alone.
type DeadCountRepair struct {
	Fixes       []DeadCountFix
	Unparseable int
}

// repairAttempts is how often RepairDeadCounts re-reads after a writer got
// between its read and its commit.
const repairAttempts = 5

// RepairDeadCounts makes every deadn/ counter equal to the dead/ letters it
// counts, and returns each counter it had to change. It is what builds the
// counters for letters written before deadn/ existed (the first start after an
// upgrade), and what the periodic audit calls when the counters and the letters
// disagree; it never changes a counter that is right.
//
// It reads dead/ and deadn/ from ONE snapshot. A writer changes a letter and
// its counter in one batch, so the snapshot always holds letters and counters
// that agree about every write that landed, and a difference it finds is a
// real one, not a write caught half way. The fix commits in a batch that reads
// each counter it rewrites with GetForUpdate and fails (ErrConflict, re-read)
// if that counter is not byte-for-byte what the snapshot held: a writer that
// touched a pair since the snapshot changed its counter too, so a repair can
// never overwrite a count that moved. A crash anywhere before the commit leaves
// the store as it was; the next run starts over. A second run finds nothing.
// A letter that does not decode is counted under UnreadShard.
func (s *Store) RepairDeadCounts(ctx context.Context) (DeadCountRepair, error) {
	var fixes []DeadCountFix
	unparseable := 0
	err := storage.RetryOnConflict(ctx, repairAttempts, func(ctx context.Context) (err error) {
		fixes, unparseable = nil, 0
		snap, err := s.ks.Snapshot()
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, snap.Close()) }()

		type shardRef struct{ consumer, topic, shard string }
		refs := map[string]shardRef{}
		want := map[string]int{}
		droot := DeadRoot()
		for kv, serr := range snap.Scan(ctx, storage.ScanOptions{Start: droot, End: keys.PrefixEnd(droot)}) {
			if serr != nil {
				return serr
			}
			s.scanned.Add(1)
			c, t, _, perr := ParseDeadKey(kv.Key)
			if perr != nil {
				unparseable++ // no consumer to count it for: named by the caller, left in place
				continue
			}
			shard := UnreadShard
			if d, derr := deadCodec().Decode(kv.Value); derr == nil {
				shard = shardOf(d.Part)
			}
			k := string(deadCountKey(c, t, shard))
			refs[k] = shardRef{c, t, shard}
			want[k]++
		}
		have := map[string][]byte{}
		croot := DeadCountRoot()
		for kv, serr := range snap.Scan(ctx, storage.ScanOptions{Start: croot, End: keys.PrefixEnd(croot)}) {
			if serr != nil {
				return serr
			}
			s.scanned.Add(1)
			c, t, shard, perr := ParseDeadCountKey(kv.Key)
			if perr != nil {
				unparseable++
				continue
			}
			k := string(kv.Key)
			refs[k] = shardRef{c, t, shard}
			have[k] = bytes.Clone(kv.Value)
		}
		ordered := make([]string, 0, len(refs))
		for k := range refs {
			ordered = append(ordered, k)
		}
		slices.Sort(ordered)
		for _, k := range ordered {
			raw, present := have[k]
			fix := DeadCountFix{Consumer: refs[k].consumer, Topic: refs[k].topic, Shard: refs[k].shard, Want: want[k], Absent: !present}
			if present {
				c, derr := deadCountCodec().Decode(raw)
				fix.Had, fix.Unreadable = c.N, derr != nil
			}
			// A zero row is always a counter to correct: counting never leaves
			// one, only a delete against a missing counter does.
			if present && !fix.Unreadable && fix.Had == fix.Want && fix.Had != 0 {
				continue
			}
			if !present && fix.Want == 0 {
				continue
			}
			fixes = append(fixes, fix)
		}
		if len(fixes) == 0 {
			return nil
		}

		if s.beforeRepairCommit != nil {
			s.beforeRepairCommit()
		}
		b := s.OpenBatch()
		defer func() {
			if cerr := b.Close(); cerr != nil && err == nil {
				err = fmt.Errorf("outboxstore: close repair batch: %w", cerr)
			}
		}()
		b.Bind(s.ks)
		w := b.In(s.ks)
		for _, fix := range fixes {
			key := deadCountKey(fix.Consumer, fix.Topic, fix.Shard)
			now, gerr := b.GetForUpdate(ctx, s.ks, key)
			switch {
			case errors.Is(gerr, storage.ErrNotFound):
				if _, was := have[string(key)]; was {
					return storage.ErrConflict
				}
			case gerr != nil:
				return gerr
			default:
				if !bytes.Equal(now, have[string(key)]) {
					return storage.ErrConflict
				}
			}
			if fix.Want == 0 {
				if err := w.Delete(key); err != nil {
					return err
				}
				continue
			}
			// Rev stays monotone across a repair: the bytes after it never equal
			// bytes any earlier reader holds. An unreadable row restarts at 1.
			// A row that does not decode is the Unreadable fix recorded above (named
			// by the caller); it restarts at 1.
			var rev uint64
			if prev, derr := deadCountCodec().Decode(now); derr == nil {
				rev = prev.Rev
			} else if !fix.Unreadable && !fix.Absent {
				return rowErr(fmt.Sprintf("dead count %s/%s/%s", fix.Consumer, fix.Topic, fix.Shard), derr)
			}
			enc, eerr := deadCountCodec().Encode(DeadCount{N: fix.Want, Rev: rev + 1})
			if eerr != nil {
				return eerr
			}
			if err := w.Set(key, enc); err != nil {
				return err
			}
		}
		return b.Commit(ctx, storage.Sync)
	})
	if err != nil {
		return DeadCountRepair{}, err
	}
	return DeadCountRepair{Fixes: fixes, Unparseable: unparseable}, nil
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
		return Dead{}, false, rowErr(fmt.Sprintf("dead row %s/%s/%d", consumer, topic, seq), err)
	}
	return d, true, nil
}

// StageDeleteDead stages the removal of one dead letter, and lowers its pair's
// counter in the same batch. It reads the letter through b with GetForUpdate
// itself (dead/ is Serialized, so a delete that did not read first is a blind
// write and fails at commit; a read here also names the letter's partition, which
// is the counter's shard), so a caller that called LoadDead first has read it
// twice, which costs nothing and keeps the first read's precondition. It
// reports whether the letter was there; a letter that does not decode is an
// error, because it names no shard to lower.
func (s *Store) StageDeleteDead(ctx context.Context, b *storage.Batch, consumer, topic string, seq uint64) (found bool, err error) {
	b.Bind(s.ks)
	key := DeadKey(consumer, topic, seq)
	raw, err := b.GetForUpdate(ctx, s.ks, key)
	if errors.Is(err, storage.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	d, err := deadCodec().Decode(raw)
	if err != nil {
		return false, rowErr(fmt.Sprintf("dead row %s/%s/%d", consumer, topic, seq), err)
	}
	if err := b.In(s.ks).Delete(key); err != nil {
		return false, err
	}
	return true, s.bumpDeadCount(ctx, b, consumer, topic, d.Part, -1)
}
