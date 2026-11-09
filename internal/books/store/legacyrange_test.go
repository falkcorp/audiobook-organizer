// file: internal/books/store/legacyrange_test.go
// version: 1.1.1
// guid: 7a1e9c34-5b2d-4f68-8c07-e4d3b6a9f512
// last-edited: 2026-11-09

package store

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/falkcorp/mediacore/storage"
)

// coreGatedDB opens an in-memory core store with books' claims and returns a
// core-backend GatedDB over books' LegacyRange, with its owned close and
// core's admin surface (attached unless noAdmin).
func coreGatedDB(t *testing.T, noAdmin bool) (*GatedDB, func(time.Duration) error) {
	t.Helper()
	d, closeFn, _ := coreGatedDBWithStore(t, noAdmin)
	return d, closeFn
}

// coreGatedDBWithStore is coreGatedDB plus the core DB behind it.
func coreGatedDBWithStore(t *testing.T, noAdmin bool) (*GatedDB, func(time.Duration) error, *storage.DB) {
	t.Helper()
	ctx := context.Background()
	op, err := storage.Open(ctx, storage.OpenOptions{
		Path: filepath.Join(t.TempDir(), "gotham.pebble"), FS: storage.MemFS(), Build: "batcomputer-1",
		Owners: []storage.OwnerSpec{{Owner: BooksOwner, Enabled: true, Spec: storage.Spec{Claims: BooksClaims()}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	db, err := op.Prepare(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	r, ok := db.Legacy(BooksOwner)
	if !ok {
		t.Fatal("no books LegacyRange")
	}
	d := newLegacyGatedDB(r, db.Close)
	if !noAdmin {
		d.setStorageAdmin(db.Admin())
	}
	closeFn := d.ownedClose()
	t.Cleanup(func() { _ = closeFn(time.Second) })
	return d, closeFn, db
}

func TestCoreBackend_ReadWriteAndErrors(t *testing.T) {
	d, _, core := coreGatedDBWithStore(t, false)
	if !d.onCore() {
		t.Fatal("onCore() = false")
	}
	if _, _, err := d.Get([]byte("book:batman-year-one")); err != pebble.ErrNotFound {
		t.Fatalf("absent Get = %v, want bare pebble.ErrNotFound (call sites compare with ==)", err)
	}
	for i, o := range []*pebble.WriteOptions{nil, pebble.Sync, pebble.NoSync} {
		key := []byte{'b', 'o', 'o', 'k', ':', byte('a' + i)}
		if err := d.Set(key, []byte("the-long-halloween"), o); err != nil {
			t.Fatalf("Set with options %d: %v", i, err)
		}
		v, c, err := d.Get(key)
		if err != nil || string(v) != "the-long-halloween" {
			t.Fatalf("Get after Set = %q, %v", v, err)
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Set([]byte("arkham-unregistered:1"), []byte("x"), pebble.Sync); !errors.Is(err, storage.ErrOutsideKeyspace) {
		t.Fatalf("Set outside books' claims = %v, want ErrOutsideKeyspace", err)
	}
	if err := d.Delete([]byte("book:a"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Get([]byte("book:a")); err != pebble.ErrNotFound {
		t.Fatalf("Get after Delete = %v", err)
	}
	// A range inside one family deletes it.
	if err := d.DeleteRange([]byte("book:"), []byte("book;"), pebble.Sync); err != nil {
		t.Fatalf("DeleteRange inside book: = %v", err)
	}
	if _, _, err := d.Get([]byte("book:b")); err != pebble.ErrNotFound {
		t.Fatalf("Get after DeleteRange = %v", err)
	}
	// A range over gaps between families, and over core's reserved c/ and
	// m/, deletes the pieces books' claims cover and nothing else: HEAD's
	// Reset wipes the whole key space, and core's rows survive it.
	for _, k := range []string{"book:c", "ident:state:robin", "narrator_counter", "setting:config_blob"} {
		if err := d.Set([]byte(k), []byte("x"), pebble.Sync); err != nil {
			t.Fatalf("Set %s: %v", k, err)
		}
	}
	if err := d.DeleteRange([]byte{0x00}, []byte{0xff, 0xff}, pebble.Sync); err != nil {
		t.Fatalf("DeleteRange over the whole key space = %v", err)
	}
	for _, k := range []string{"book:c", "ident:state:robin", "narrator_counter", "setting:config_blob"} {
		if _, _, err := d.Get([]byte(k)); err != pebble.ErrNotFound {
			t.Fatalf("Get %s after the whole-key-space DeleteRange = %v, want ErrNotFound", k, err)
		}
	}
	parts, err := core.Admin().Census(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var coreKeys uint64
	for _, p := range parts {
		if bytes.HasPrefix(p.Start, []byte("c/")) {
			coreKeys += p.Keys
		}
	}
	if coreKeys == 0 {
		t.Fatalf("the whole-key-space DeleteRange removed core's c/ rows: census %+v", parts)
	}
	if got := d.FormatMajorVersion(); got != PebbleFormatMajorVersion {
		t.Fatalf("FormatMajorVersion = %v, want %v", got, PebbleFormatMajorVersion)
	}
	if err := d.Barrier(); err != nil {
		t.Fatalf("Barrier = %v", err)
	}
	if err := d.SyncWAL(); err != nil {
		t.Fatalf("SyncWAL = %v", err)
	}
}

func TestCoreBackend_IteratorsSnapshotsBatches(t *testing.T) {
	d, _ := coreGatedDB(t, false)
	for _, k := range []string{"book:1", "book:2", "book:3", "series:1"} {
		if err := d.Set([]byte(k), []byte("v-"+k), pebble.NoSync); err != nil {
			t.Fatal(err)
		}
	}
	keys := func(it *GatedIter) []string {
		t.Helper()
		var out []string
		for ok := it.First(); ok; ok = it.Next() {
			out = append(out, string(it.Key()))
		}
		if err := it.Error(); err != nil {
			t.Fatal(err)
		}
		if err := it.Close(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	it, err := d.NewIter(&pebble.IterOptions{LowerBound: []byte("book:"), UpperBound: []byte("book;")})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(it); !slices.Equal(got, []string{"book:1", "book:2", "book:3"}) {
		t.Fatalf("bounded scan = %q", got)
	}
	if _, err := d.NewIter(&pebble.IterOptions{LowerBound: []byte("a"), KeyTypes: pebble.IterKeyTypePointsAndRanges}); err == nil {
		t.Fatal("an iterator option beyond the bounds was silently dropped")
	}

	// SetBounds reopens over the new range; census stats count keys and,
	// for a census iterator, value bytes.
	cit, err := d.newCensusIter(&pebble.IterOptions{LowerBound: []byte("book:"), UpperBound: []byte("book;")})
	if err != nil {
		t.Fatal(err)
	}
	for ok := cit.First(); ok; ok = cit.Next() {
	}
	st := cit.Stats().InternalStats
	if st.PointCount != 3 || st.KeyBytes != 18 || st.ValueBytes != 24 {
		t.Fatalf("census stats = %+v, want 3 points, 18 key bytes, 24 value bytes", st)
	}
	cit.ResetStats()
	cit.SetBounds([]byte("series:"), []byte("series;"))
	if !cit.First() || string(cit.Key()) != "series:1" || cit.Next() {
		t.Fatalf("after SetBounds the iterator does not cover the new range")
	}
	if st := cit.Stats().InternalStats; st.PointCount != 1 {
		t.Fatalf("stats after ResetStats = %+v, want 1 point", st)
	}
	if err := cit.Close(); err != nil {
		t.Fatal(err)
	}

	// A snapshot does not see a later write.
	snap := d.NewSnapshot()
	if err := d.Set([]byte("book:4"), []byte("v"), pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if _, _, err := snap.Get([]byte("book:4")); err != pebble.ErrNotFound {
		t.Fatalf("snapshot Get of a later write = %v, want ErrNotFound", err)
	}
	sit, err := snap.NewIter(&pebble.IterOptions{LowerBound: []byte("book:"), UpperBound: []byte("book;")})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(sit); len(got) != 3 {
		t.Fatalf("snapshot scan = %q, want the 3 rows before the snapshot", got)
	}
	if err := snap.Close(); err != nil {
		t.Fatal(err)
	}

	// An indexed batch reads its own writes; a recording batch replays into
	// a core batch with Apply.
	ib := d.NewIndexedBatch()
	if !ib.Indexed() {
		t.Fatal("NewIndexedBatch is not indexed")
	}
	if err := ib.Set([]byte("book:5"), []byte("hush"), nil); err != nil {
		t.Fatal(err)
	}
	if v, _, err := ib.Get([]byte("book:5")); err != nil || string(v) != "hush" {
		t.Fatalf("indexed batch Get of its own write = %q, %v", v, err)
	}
	rec := d.newRecordingBatch()
	if err := rec.Set([]byte("book:6"), []byte("knightfall"), nil); err != nil {
		t.Fatal(err)
	}
	if err := rec.Delete([]byte("book:1"), nil); err != nil {
		t.Fatal(err)
	}
	if err := rec.Commit(nil); !errors.Is(err, errRecordingBatch) {
		t.Fatalf("Commit of a recording batch = %v, want errRecordingBatch", err)
	}
	if err := ib.Apply(rec, nil); err != nil {
		t.Fatal(err)
	}
	if ib.Count() != 3 || ib.Empty() {
		t.Fatalf("Count after Apply = %d, want 3", ib.Count())
	}
	if err := ib.Apply(d.NewBatch(), nil); err == nil {
		t.Fatal("Apply of a core batch succeeded; only recording batches replay")
	}
	if err := ib.Commit(pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	if err := ib.Close(); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"book:5": "hush", "book:6": "knightfall"} {
		if v, _, err := d.Get([]byte(k)); err != nil || string(v) != want {
			t.Fatalf("%s after commit = %q, %v; want %q", k, v, err, want)
		}
	}
	if _, _, err := d.Get([]byte("book:1")); err != pebble.ErrNotFound {
		t.Fatalf("book:1 after the replayed delete = %v", err)
	}
	b := d.NewBatch()
	if err := b.Set([]byte("joker-unregistered:1"), []byte("x"), nil); !errors.Is(err, storage.ErrOutsideKeyspace) {
		t.Fatalf("batch Set outside books' claims = %v, want ErrOutsideKeyspace", err)
	}
	_ = b.Close()
}

func TestCoreBackend_AdminAndClose(t *testing.T) {
	d, closeFn := coreGatedDB(t, true)
	if err := d.Flush(); !errors.Is(err, errStorageAdminUnset) {
		t.Fatalf("Flush before the admin is attached = %v, want errStorageAdminUnset", err)
	}
	if _, err := d.EstimateDiskUsage(nil, nil); !errors.Is(err, errStorageAdminUnset) {
		t.Fatalf("EstimateDiskUsage before the admin is attached = %v", err)
	}
	if s := CollectCompactionStats(d); !s.Absent {
		t.Fatalf("compaction stats before the admin is attached = %+v, want Absent", s)
	}
	if _, ok := PebbleSampleFromDB(d); ok {
		t.Fatal("a collector sample before the admin is attached")
	}
	if _, err := d.SSTables(); !errors.Is(err, errNoSSTables) {
		t.Fatalf("SSTables on the core backend = %v, want errNoSSTables", err)
	}
	if _, err := d.Metrics(); err == nil {
		t.Fatal("Metrics on the core backend returned a pebble.Metrics")
	}

	d2, closeFn2 := coreGatedDB(t, false)
	if err := d2.Set([]byte("book:1"), []byte("v"), pebble.NoSync); err != nil {
		t.Fatal(err)
	}
	if err := d2.Flush(); err != nil {
		t.Fatalf("Flush = %v", err)
	}
	if err := d2.Compact(context.Background(), nil, nil, false); err != nil {
		t.Fatalf("Compact = %v", err)
	}
	if s := CollectCompactionStats(d2); s.Absent {
		t.Fatal("compaction stats Absent with the admin attached")
	}
	if s, ok := PebbleSampleFromDB(d2); !ok || s.DiskUsageBytes == 0 {
		t.Fatalf("collector sample = %+v, %v", s, ok)
	}
	if err := closeFn2(time.Second); err != nil {
		t.Fatalf("owned close = %v", err)
	}
	if _, _, err := d2.Get([]byte("book:1")); !errors.Is(err, pebble.ErrClosed) {
		t.Fatalf("Get after close = %v, want a pebble.ErrClosed wrapper", err)
	}
	if err := closeFn(time.Second); err != nil {
		t.Fatalf("owned close of the store without an admin = %v", err)
	}
}

func TestDurabilityFollowsPebbleWriteOptions(t *testing.T) {
	for _, tc := range []struct {
		o    *pebble.WriteOptions
		want storage.Durability
	}{{nil, storage.Sync}, {pebble.Sync, storage.Sync}, {pebble.NoSync, storage.NoSync}} {
		if got := durability(tc.o); got != tc.want {
			t.Errorf("durability(%v) = %v, want %v", tc.o, got, tc.want)
		}
	}
}
