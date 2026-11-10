// file: core/audit/store.go
// version: 1.0.1
// guid: 58cf84f8-755a-46df-b0dd-c7b6f1a73e73
// last-edited: 2026-11-10

package audit

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/codec"
	"github.com/falkcorp/mediacore/storage/keys"
)

// maxUserAgent is the longest Actor.UserAgent a record keeps (§3.5).
const maxUserAgent = 256

// Store is the audit log over c/audit/ (05-rbac §7.2). It implements Logger
// and Reader. The storage layout, as keys.Join segments (05-storage §5S.3.3;
// §7.2 spells them with slashes):
//
//	rec/<seq:8B big-endian>                         -> record (codec.JSON, v1)
//	idx/actor/<userID>/<seq>                        -> empty
//	idx/action/<action>/<seq>                       -> empty
//	idx/target/<module>/<kind>/<id>/<seq>           -> empty
//
// seq comes from a storage.Sequence, which persists its high-water mark before
// handing out IDs, so a crash can skip a seq and never reissue one (DATA-19).
// seq is unique and increasing in allocation order; it has gaps (a discarded
// RecordIn batch, a crash) and is not strictly commit-ordered (COR-15).
type Store struct {
	opts Options
	ks   storage.Keyspace
	seq  *storage.Sequence
	log  *slog.Logger
	now  func() time.Time
	red  redactor
	ctr  counters

	// sink writes the async writer's batches: the store's own (storeSink),
	// or an in-memory one in the tests that run the writer under synctest.
	sink sink
	q    chan Record

	mu    sync.RWMutex // state and ran, held shared across an Emit's send
	state state
	ran   bool          // Start launched the writer
	stop  chan struct{} // closed by the first stop request
	done  chan struct{} // closed when the writer goroutine has exited
	// abandonCtx ends when Stop's context ends before the drain does (and
	// when Stop returns): the writer's writes then fail, it counts what is
	// left as dropped and exits.
	abandonCtx    context.Context
	abandonCancel context.CancelFunc
	stopOnce      sync.Once
}

// state is where the async writer is in its life; it only moves forward.
type state uint8

const (
	stateNew state = iota
	stateRunning
	stateStopped
)

// New builds the store. It writes nothing: the first Record or Emit takes the
// first sequence lease. The writer does not run until Start.
func New(ctx context.Context, opts Options, deps Deps) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := newStore(opts, deps)
	if err != nil {
		return nil, err
	}
	s.sink = storeSink{s: s}
	if deps.Metrics != nil {
		if err := s.Instrument(deps.Metrics); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// newStore is New without the sink and the instruments, for the tests that
// supply their own sink.
func newStore(opts Options, deps Deps) (*Store, error) {
	if deps.Keyspace.Prefix() == "" {
		return nil, errors.New("audit: Deps.Keyspace is the zero keyspace; the kernel registers core/audit's c/audit/ keyspace")
	}
	if deps.Seq == nil {
		return nil, errors.New("audit: Deps.Seq is nil")
	}
	opts = opts.withDefaults()
	s := &Store{
		opts: opts,
		ks:   deps.Keyspace,
		seq:  deps.Seq,
		log:  deps.Log,
		now:  deps.Now,
		red:  newRedactor(opts.DetailsMaxBytes),
		q:    make(chan Record, opts.AsyncBuffer),
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	s.abandonCtx, s.abandonCancel = context.WithCancel(context.Background())
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if s.now == nil {
		s.now = time.Now
	}
	return s, nil
}

var _ Logger = (*Store)(nil)
var _ Reader = (*Store)(nil)

// codec is the stored form of a record.
func recordCodec() codec.Codec[Record] { return codec.JSON[Record](1) }

// build turns an Event into a Record without a Seq: it checks the action and
// outcome, fills the actor from ctx when the event has none, truncates the
// user agent, redacts and caps Details (on a copy: the caller's map is never
// retained or changed) and stamps the time and ULID.
func (s *Store) build(ctx context.Context, e Event) (Record, error) {
	if !ValidAction(e.Action) {
		return Record{}, fmt.Errorf("%w: %q", ErrInvalidAction, e.Action)
	}
	switch e.Outcome {
	case Success, Failure, Denied, Error:
	default:
		return Record{}, fmt.Errorf("%w: %q (action %s)", ErrInvalidOutcome, e.Outcome, e.Action)
	}
	if e.Actor == (Actor{}) {
		if a, ok := ActorFrom(ctx); ok {
			e.Actor = a
		}
	}
	e.Actor.UserAgent = truncateUTF8(e.Actor.UserAgent, maxUserAgent)
	e.Details = s.red.details(e.Details)
	now := s.now().UTC()
	id, err := newULID(now, rand.Reader)
	if err != nil {
		return Record{}, fmt.Errorf("audit: record ID: %w", err)
	}
	return Record{ID: id, Time: now, Event: e}, nil
}

// Record writes e synchronously, in a batch of its own committed with Sync.
// A commit error means no record was written and the caller fails its
// operation; an error from closing the batch after a successful commit is
// returned too, though the record is then durable.
func (s *Store) Record(ctx context.Context, e Event) (err error) {
	rec, err := s.build(ctx, e)
	if err != nil {
		return err
	}
	b := s.ks.NewBatch()
	// A Close failure after a successful Commit still surfaces to the caller.
	defer func() { err = errors.Join(err, b.Close()) }()
	if err := s.assign(ctx, b, &rec); err != nil {
		s.ctr.writeError(pathSync)
		return err
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		s.ctr.writeError(pathSync)
		return fmt.Errorf("audit: commit %s: %w", rec.Action, err)
	}
	s.ctr.record(rec)
	return nil
}

// RecordIn stages the record into b, the caller's batch, so it commits with
// the change or not at all; a discarded b leaves no record (and a gap in seq).
// It never copies or commits b. audit_records_total counts the record when it
// is staged: the store cannot see b's commit, so a batch that is then
// discarded is counted too.
func (s *Store) RecordIn(ctx context.Context, b *storage.Batch, e Event) error {
	if b == nil {
		return ErrNilBatch
	}
	rec, err := s.build(ctx, e)
	if err != nil {
		return err
	}
	if err := s.assign(ctx, b, &rec); err != nil {
		s.ctr.writeError(pathSync)
		return err
	}
	s.ctr.record(rec)
	return nil
}

// assign takes rec's seq and stages it into b.
func (s *Store) assign(ctx context.Context, b *storage.Batch, rec *Record) error {
	seq, err := s.seq.NextIn(ctx, b)
	if err != nil {
		return fmt.Errorf("audit: allocate seq for %s: %w", rec.Action, err)
	}
	rec.Seq = seq
	return s.stage(b, *rec)
}

// stage writes rec and its index rows into b through the audit keyspace,
// binding it first (a participant joins the caller's batch, 05-storage §5S.3.2).
func (s *Store) stage(b *storage.Batch, rec Record) error {
	raw, err := recordCodec().Encode(rec)
	if err != nil {
		return fmt.Errorf("audit: encode %s: %w", rec.Action, err)
	}
	b.Bind(s.ks)
	if !slices.Contains(b.Participants(), s.ks.Owner()) {
		return errors.New("audit: the batch refused the c/audit/ keyspace (a handle of another store, or a migration step's)")
	}
	w := b.In(s.ks)
	if err := w.Set(recKey(rec.Seq), raw); err != nil {
		return fmt.Errorf("audit: stage %s: %w", rec.Action, err)
	}
	for _, k := range indexKeys(rec) {
		if err := w.Set(k, []byte{}); err != nil {
			return fmt.Errorf("audit: stage index of %s: %w", rec.Action, err)
		}
	}
	return nil
}

// storeSink is the async writer's sink: the sequence and the keyspace.
type storeSink struct{ s *Store }

// write assigns each record its seq, in order, and commits all of them in one
// batch with Sync.
func (k storeSink) write(ctx context.Context, recs []Record) (err error) {
	b := k.s.ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	for i := range recs {
		if err := k.s.assign(ctx, b, &recs[i]); err != nil {
			return err
		}
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("audit: commit %d async records: %w", len(recs), err)
	}
	return nil
}

// recKey is rec/<seq>.
func recKey(seq uint64) []byte { return keys.Join(keys.Str("rec"), keys.U64(seq)) }

// recPrefix is the range of every record.
func recPrefix() []byte { return keys.Prefix(keys.Str("rec")) }

// indexKeys are rec's secondary index rows: by actor (a record with an actor
// user), by action, and by target (a record with a target).
func indexKeys(rec Record) [][]byte {
	seq := keys.U64(rec.Seq)
	out := [][]byte{keys.Join(keys.Str("idx"), keys.Str("action"), keys.Str(rec.Action), seq)}
	if rec.Actor.UserID != "" {
		out = append(out, keys.Join(keys.Str("idx"), keys.Str("actor"), keys.Str(rec.Actor.UserID), seq))
	}
	if t := rec.Target; t.Module != "" || t.Kind != "" || t.ID != "" {
		out = append(out, keys.Join(keys.Str("idx"), keys.Str("target"), keys.Str(t.Module), keys.Str(t.Kind), keys.Str(t.ID), seq))
	}
	return out
}

// seqOfIndexKey is the seq in the last segment of an index key.
func seqOfIndexKey(key []byte) (uint64, error) {
	segs, err := keys.Split(key)
	if err != nil || len(segs) == 0 || len(segs[len(segs)-1]) != 8 {
		return 0, fmt.Errorf("audit: malformed index key %q", key)
	}
	return binary.BigEndian.Uint64(segs[len(segs)-1]), nil
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// crockford is the ULID alphabet (Crockford's base32).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newULID is a ULID: 48 bits of Unix milliseconds, 80 random bits, 26
// characters of Crockford base32. It lives here and not in httpmw's copy
// because httpmw is L6 and audit L4 (03 R4).
func newULID(t time.Time, r io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(r, b[6:]); err != nil {
		return "", err
	}
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], uint64(t.UnixMilli()))
	copy(b[:6], ts[2:])
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return string(out[:]), nil
}
