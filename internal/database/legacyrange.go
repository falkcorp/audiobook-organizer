// file: internal/database/legacyrange.go
// version: 1.2.1
// guid: 4c7e1a92-6b3d-4f05-9e28-d1a5b7c3f604
// last-edited: 2026-11-08

package database

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
)

// THE CORE BACKEND (R1-23). From release R1 the main store is opened by core
// storage, and books reaches its families only through its strict claims on a
// storage.LegacyRange (05-storage §5S.3.4). The repository layer was written
// against GatedDB's pebble-shaped surface (271 Gets, 207 iterators, 187
// batches at the time of the flip), so GatedDB gets a second backend instead
// of every call site a rewrite: a GatedDB built by newLegacyGatedDB forwards
// each call to the LegacyRange, and the auxiliary stores that open their own
// engine (the AI scan store, the OpenLibrary cache) keep the raw backend.
//
// What the core backend changes, and why each is safe:
//   - Every key is checked against books' claims. A key no claim covers is
//     refused with storage.ErrOutsideKeyspace (strict) and iterators skip
//     it, so a family books writes must be registered in keyfamilies.go; the
//     claims are derived from it (booksclaims.go).
//   - Not found is pebble.ErrNotFound, bare, because call sites compare with
//     ==; a store core has closed is errStoreClosing (it wraps
//     pebble.ErrClosed) so the registry's log-and-skip paths keep working.
//   - A DeleteRange is split into the pieces books' claims cover
//     (stageClaimedDeleteRange): core refuses a legacy DeleteRange that runs
//     into a key no claim holds, and HEAD's ranges ([ident:, ident;), Reset's
//     whole keyspace) span gaps between registered families and the
//     reserved c/ and m/. Books cannot write a key into a gap under strict
//     claims, but a gap can still hold one an older release wrote under a
//     family the claims miss: the first core boot refuses only keys under c/
//     and m/ (ErrForeignKeys) and counts and logs the rest (core storage's
//     logUnclaimedKeys) without refusing. The split leaves such a key where
//     it is, where no handle can reach it, and never touches core's
//     keyspaces; HEAD's whole-range delete would have removed it. That is
//     the one difference, and it errs toward keeping bytes: registering the
//     family in a later build makes the key reachable again (after a Reset
//     too, which is why keyfamilies.go must list every family HEAD wrote).
//   - Durability is the caller's: pebble's nil and Sync write options commit
//     storage.Sync, NoSync commits storage.NoSync (pebble.WriteOptions.GetSync).
//   - A Get returns a copy and holds no engine lease past the call; its
//     closer is a no-op. Iterators, snapshots and commits hold this gate's
//     lease exactly as on the raw backend, so PebbleStore.Close still drains
//     books' own readers before it lets go of the handle; core's own leases
//     guard the engine.
//   - Store-wide administration (checkpoint, compaction, flush, size
//     estimates, the collector's sample) goes through core's storage.Admin,
//     which the opener hands over with setStorageAdmin; until it does, those
//     calls fail with errStorageAdminUnset rather than reaching an engine
//     books does not own. The estimated census lists sstables through
//     storage.Admin.Tables (GatedDB.tables); Pebble's own SSTables listing is
//     raw-backend only.

// storageAdmin is the part of storage.Admin the core backend uses.
type storageAdmin interface {
	Checkpoint(ctx context.Context, dir string) error
	Compact(ctx context.Context, start, end []byte) error
	CompactionStats() storage.CompactionStats
	EngineSample() (storage.EngineSample, error)
	EstimateDiskUsage(start, end []byte) (uint64, error)
	Flush(ctx context.Context) error
	Tables(ctx context.Context, o storage.TablesOptions) ([]storage.TableInfo, error)
}

var _ storageAdmin = (*storage.Admin)(nil)

// errStorageAdminUnset is returned by a store-wide administration call on the
// core backend before the opener handed over core's admin surface.
var errStorageAdminUnset = errors.New("pebble store: core storage's admin surface is not attached yet (store-wide administration runs once the app has booted)")

// errNoSSTables is returned by GatedDB.SSTables on the core backend.
var errNoSSTables = errors.New("pebble store: sstable listings are not available on the store core storage opened")

// legacyBackend is the core backend of a GatedDB: books' claims.
type legacyBackend struct {
	r     storage.LegacyRange
	admin atomic.Pointer[storageAdmin]
	// commits counts the non-empty writes committed through this handle:
	// the store-wide "did anything get written" signal tests read where the
	// raw backend has pebble's WAL byte counter.
	commits atomic.Uint64
	// closeStore closes what the opener opened (the standalone opener owns
	// its core DB); nil when the kernel owns the store and closes it itself.
	closeStore func() error
}

// CommittedWrites is how many non-empty writes this store and every store
// sharing its engine (activity, embedding, AI scan) have committed through
// books' claims since it was built. Books' Register reads it to prove its
// constructor commits none (05-storage §5S.4 step 12,
// TestBooksConstructorWritesNothing); 0 on a store on the raw engine.
func (p *PebbleStore) CommittedWrites() uint64 {
	if p.db == nil || p.db.lr == nil {
		return 0
	}
	return p.db.lr.commits.Load()
}

// newLegacyGatedDB wraps books' LegacyRange. closeStore is run by the owned
// close after the drain (nil: the kernel closes the store).
func newLegacyGatedDB(r storage.LegacyRange, closeStore func() error) *GatedDB {
	return &GatedDB{lr: &legacyBackend{r: r, closeStore: closeStore}, gate: newDBGate()}
}

// setStorageAdmin attaches core's admin surface to a core-backend GatedDB.
// A no-op on the raw backend, which administers its own engine.
func (d *GatedDB) setStorageAdmin(a storageAdmin) {
	if d.lr == nil || a == nil {
		return
	}
	d.lr.admin.Store(&a)
}

// onCore reports whether d is the core backend.
func (d *GatedDB) onCore() bool { return d.lr != nil }

// adminOf returns the attached admin surface, or errStorageAdminUnset.
func (l *legacyBackend) adminOf() (storageAdmin, error) {
	a := l.admin.Load()
	if a == nil {
		return nil, errStorageAdminUnset
	}
	return *a, nil
}

// legacyErr maps core storage's errors onto the pebble ones the repository
// layer tests for.
func legacyErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrNotFound):
		return pebble.ErrNotFound
	case errors.Is(err, storage.ErrClosed):
		return fmt.Errorf("%w (%v)", errStoreClosing, err)
	default:
		return err
	}
}

// durability is the core durability of pebble write options: nil and Sync
// sync, NoSync does not (pebble.WriteOptions.GetSync).
func durability(o *pebble.WriteOptions) storage.Durability {
	if o.GetSync() {
		return storage.Sync
	}
	return storage.NoSync
}

// write stages fn into a one-off batch and commits it with o's durability.
func (l *legacyBackend) write(o *pebble.WriteOptions, fn func(b *storage.Batch) error) error {
	b := l.r.NewBatch()
	defer func() { _ = b.Close() }()
	if err := fn(b); err != nil {
		return legacyErr(err)
	}
	n := b.Count()
	if err := b.Commit(context.Background(), durability(o)); err != nil {
		return legacyErr(err)
	}
	if n > 0 {
		l.commits.Add(1)
	}
	return nil
}

// scanOptions converts pebble iterator options. Only the bounds have a core
// equivalent, and the repository layer sets nothing else; any other field is
// refused rather than silently dropped.
func scanOptions(o *pebble.IterOptions) (storage.ScanOptions, error) {
	if o == nil {
		return storage.ScanOptions{}, nil
	}
	rest := *o
	rest.LowerBound, rest.UpperBound = nil, nil
	if !reflect.ValueOf(rest).IsZero() {
		return storage.ScanOptions{}, errors.New("pebble store: iterator options beyond the bounds are not supported on core storage")
	}
	return storage.ScanOptions{Start: o.LowerBound, End: o.UpperBound}, nil
}

// nopCloser is the closer of a core-backend Get: the value is a copy.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

var _ io.Closer = nopCloser{}

// iterBackend is what a GatedIter forwards to: *pebble.Iterator on the raw
// backend, *legacyIter on the core backend.
type iterBackend interface { //nolint:interfacebloat // one capability, the pebble iterator
	// surface GatedIter forwards whole (positioning, reads, bounds, stats, close).
	// Both implementations carry all of it and GatedIter uses every method, so
	// splitting it would only split the one forwarding type in two.
	First() bool
	Last() bool
	Next() bool
	Prev() bool
	SeekGE(key []byte) bool
	SeekLT(key []byte) bool
	Valid() bool
	Key() []byte
	Value() []byte
	ValueAndErr() ([]byte, error)
	Error() error
	SetBounds(lower, upper []byte)
	Stats() pebble.IteratorStats
	ResetStats()
	Close() error
}

var (
	_ iterBackend = (*pebble.Iterator)(nil)
	_ iterBackend = (*legacyIter)(nil)
)

// legacyIter adapts a core iterator. SetBounds reopens the iterator over the
// same source with the new bounds (core iterators have fixed bounds), and
// Stats is counted here, since core exposes no engine iterator stats: the
// keys this iterator landed on, their bytes and, for a census iterator
// (countValues), their value bytes. PointsCoveredByRangeTombstones is always
// 0: tombstones are invisible above the engine.
type legacyIter struct {
	it          *storage.Iterator
	open        func(start, end []byte) (*storage.Iterator, error)
	countValues bool
	stats       pebble.InternalIteratorStats
	err         error // a failed reopen; the iterator is then exhausted
}

func newLegacyIter(open func(start, end []byte) (*storage.Iterator, error), start, end []byte, countValues bool) (*legacyIter, error) {
	it, err := open(start, end)
	if err != nil {
		return nil, legacyErr(err)
	}
	return &legacyIter{it: it, open: open, countValues: countValues}, nil
}

// landed counts the entry a positioning call landed on.
func (l *legacyIter) landed(ok bool) bool {
	if !ok {
		return false
	}
	l.stats.PointCount++
	l.stats.KeyBytes += uint64(len(l.it.Key()))
	if l.countValues {
		l.stats.ValueBytes += uint64(len(l.it.Value()))
	}
	return true
}

func (l *legacyIter) usable() bool { return l.err == nil && l.it != nil }

func (l *legacyIter) First() bool   { return l.usable() && l.landed(l.it.First()) }
func (l *legacyIter) Last() bool    { return l.usable() && l.landed(l.it.Last()) }
func (l *legacyIter) Next() bool    { return l.usable() && l.landed(l.it.Next()) }
func (l *legacyIter) Prev() bool    { return l.usable() && l.landed(l.it.Prev()) }
func (l *legacyIter) Valid() bool   { return l.usable() && l.it.Valid() }
func (l *legacyIter) Key() []byte   { return l.it.Key() }
func (l *legacyIter) Value() []byte { return l.it.Value() }

func (l *legacyIter) SeekGE(key []byte) bool { return l.usable() && l.landed(l.it.SeekGE(key)) }
func (l *legacyIter) SeekLT(key []byte) bool { return l.usable() && l.landed(l.it.SeekLT(key)) }

func (l *legacyIter) ValueAndErr() ([]byte, error) {
	v := l.it.Value()
	return v, legacyErr(l.it.Err())
}

func (l *legacyIter) Error() error {
	if l.err != nil {
		return l.err
	}
	if l.it == nil {
		return nil
	}
	return legacyErr(l.it.Err())
}

func (l *legacyIter) SetBounds(lower, upper []byte) {
	if l.err != nil {
		return
	}
	if l.it != nil {
		if err := l.it.Close(); err != nil {
			l.err = legacyErr(err)
			l.it = nil
			return
		}
	}
	it, err := l.open(lower, upper)
	if err != nil {
		l.err, l.it = legacyErr(err), nil
		return
	}
	l.it = it
}

func (l *legacyIter) Stats() pebble.IteratorStats {
	return pebble.IteratorStats{InternalStats: l.stats}
}

func (l *legacyIter) ResetStats() { l.stats = pebble.InternalIteratorStats{} }

func (l *legacyIter) Close() error {
	if l.it == nil {
		return nil
	}
	err := l.it.Close()
	l.it = nil
	return legacyErr(err)
}

// batchOp is one operation a recording batch holds.
type batchOp struct {
	kind       uint8 // opSet, opDelete, opDeleteRange
	key, value []byte
}

const (
	batchOpSet = iota + 1
	batchOpDelete
	batchOpDeleteRange
)

// claimSpan is one piece of the key space books' claims cover: [lo, hi), or
// the one key lo when single.
type claimSpan struct {
	lo, hi []byte
	single bool
}

// booksClaimSpans are books' claims as sorted, disjoint spans: a prefix claim
// is [prefix, prefixUpperBound(prefix)), a key claim the key alone; nested
// claims (idx:user: inside idx:, setting:config_blob inside setting:) merge
// into their parent. A function of the build, computed once.
var booksClaimSpans = sync.OnceValue(func() []claimSpan {
	var spans []claimSpan
	for _, c := range BooksClaims() {
		if c.Key != "" {
			k := []byte(c.Key)
			spans = append(spans, claimSpan{lo: k, hi: append(slices.Clone(k), 0), single: true})
			continue
		}
		lo := []byte(c.Prefix)
		spans = append(spans, claimSpan{lo: lo, hi: prefixUpperBound(lo)})
	}
	slices.SortFunc(spans, func(a, b claimSpan) int { return bytes.Compare(a.lo, b.lo) })
	var out []claimSpan
	for _, sp := range spans {
		if n := len(out); n > 0 && bytes.Compare(sp.lo, out[n-1].hi) < 0 {
			if bytes.Compare(sp.hi, out[n-1].hi) > 0 {
				out[n-1].hi = sp.hi
			}
			out[n-1].single = false
			continue
		}
		out = append(out, sp)
	}
	return out
})

// claimedPieces is [start, end) cut to booksClaimSpans: the pieces of the
// range a claim covers, in order.
func claimedPieces(start, end []byte) []claimSpan {
	var out []claimSpan
	for _, sp := range booksClaimSpans() {
		if bytes.Compare(sp.hi, start) <= 0 {
			continue
		}
		if bytes.Compare(sp.lo, end) >= 0 {
			break
		}
		if sp.single {
			out = append(out, sp)
			continue
		}
		lo, hi := sp.lo, sp.hi
		if bytes.Compare(start, lo) > 0 {
			lo = start
		}
		if bytes.Compare(end, hi) < 0 {
			hi = end
		}
		out = append(out, claimSpan{lo: lo, hi: hi})
	}
	return out
}

// stageClaimedDeleteRange stages a delete of [start, end) into b as the
// pieces books' claims cover (see THE CORE BACKEND).
func stageClaimedDeleteRange(b *storage.Batch, start, end []byte) error {
	if end == nil || bytes.Compare(start, end) >= 0 {
		return b.DeleteRange(start, end) // core's own refusal names the bad range
	}
	for _, p := range claimedPieces(start, end) {
		var err error
		if p.single {
			err = b.Delete(p.lo)
		} else {
			err = b.DeleteRange(p.lo, p.hi)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
