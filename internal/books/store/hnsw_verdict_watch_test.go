// file: internal/books/store/hnsw_verdict_watch_test.go
// version: 1.0.2
// guid: 5f0c0b8e-6a0f-4a43-9d7e-2b8c1f4e9a61
// last-edited: 2026-11-09

package store

import (
	"os"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// R1-15 follow-up review, MAJOR-1(B): the Pebble side of the clean-stop
// marker. Each test pins one guard, so removing it fails the test.

func TestIsVerdictKey(t *testing.T) {
	for key, want := range map[string]bool{
		"emb:v:book:venom":          true,
		"emb:v:author:7":            true,
		"book:venom":                true,
		"book:01HZ0000000000000000": true,
		"book:":                     false, // the bare prefix is no row
		"book:path:venom":           false, // an index family under book:
		"book:work:42:venom":        false,
		"book_sig:venom":            false,
		"book_ver:venom:1":          false,
		"tombstone:venom":           false,
		"emb:c:book:venom":          false, // the candidate keyspace
		"setting:joker":             false,
	} {
		if got := isVerdictKey([]byte(key)); got != want {
			t.Errorf("isVerdictKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestRangeTouchesVerdictKeys(t *testing.T) {
	for _, tc := range []struct {
		start, end string
		want       bool
	}{
		{"emb:v:book:", "emb:v:book;", true},
		{"book:", "book;", true},
		{"a", "z", true},
		{"setting:", "setting;", false},
		{"emb:c:", "emb:c;", false},
		{"book;", "c", false},
	} {
		if got := rangeTouchesVerdictKeys([]byte(tc.start), []byte(tc.end)); got != tc.want {
			t.Errorf("rangeTouchesVerdictKeys(%q, %q) = %v, want %v", tc.start, tc.end, got, tc.want)
		}
	}
}

// verdictCoreDB is the GatedDB of an in-memory PebbleStore: books' claims on a
// core store, the backend every book and embedding row is written through.
func verdictCoreDB(t *testing.T) *GatedDB {
	t.Helper()
	s, err := NewPebbleStoreInMemory("verdict")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.DB().lr == nil {
		t.Fatal("NewPebbleStoreInMemory: want the core backend")
	}
	return s.DB()
}

// verdictRawDB is a GatedDB over an engine it was handed, the backend of the AI
// scan store and the OpenLibrary cache.
func verdictRawDB(t *testing.T) *GatedDB {
	t.Helper()
	db, err := pebble.Open("verdict.pebble", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewGatedDB(db)
}

// countsOne fails the test unless write adds exactly want verdict writes.
func countsOne(t *testing.T, db *GatedDB, what string, want int64, write func() error) {
	t.Helper()
	before := db.verdict.count()
	if err := write(); err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if got := db.verdict.count() - before; got != want {
		t.Errorf("%s added %d verdict write(s), want %d", what, got, want)
	}
}

// TestVerdictWatch_EveryGatedWritePath: on each backend, each GatedDB and
// GatedBatch write of a verdict key counts, a write of another key does not,
// and a batch counts once, when it commits. A write a recording batch staged
// counts when the batch it was applied to commits, with the judgement it was
// recorded with.
func TestVerdictWatch_EveryGatedWritePath(t *testing.T) {
	for name, open := range map[string]func(*testing.T) *GatedDB{"core": verdictCoreDB, "raw": verdictRawDB} {
		t.Run(name, func(t *testing.T) { verdictWatchEveryPath(t, open(t)) })
	}
}

func verdictWatchEveryPath(t *testing.T, db *GatedDB) {
	row, other := []byte("emb:v:book:venom"), []byte("setting:joker")

	countsOne(t, db, "Set", 1, func() error { return db.Set(row, []byte("{}"), pebble.Sync) })
	countsOne(t, db, "Delete", 1, func() error { return db.Delete(row, pebble.Sync) })
	countsOne(t, db, "DeleteRange", 1, func() error { return db.DeleteRange([]byte("book:"), []byte("book;"), pebble.Sync) })
	countsOne(t, db, "Set of another key", 0, func() error { return db.Set(other, []byte("1"), pebble.Sync) })
	countsOne(t, db, "DeleteRange of another family", 0, func() error {
		return db.DeleteRange([]byte("setting:"), []byte("setting;"), pebble.Sync)
	})

	// applied stages one write into a recording batch and applies it to b.
	applied := func(record func(r *GatedBatch) error) func(b *GatedBatch) error {
		return func(b *GatedBatch) error {
			r := db.newRecordingBatch()
			defer r.Close()
			if err := record(r); err != nil {
				return err
			}
			return b.Apply(r, nil)
		}
	}
	batches := map[string]func() *GatedBatch{"batch": func() *GatedBatch { return db.NewBatch() },
		"indexed batch": db.NewIndexedBatch}
	for kind, newBatch := range batches {
		for name, stage := range map[string]func(b *GatedBatch) error{
			"Set":         func(b *GatedBatch) error { return b.Set(row, []byte("{}"), nil) },
			"Delete":      func(b *GatedBatch) error { return b.Delete(row, nil) },
			"DeleteRange": func(b *GatedBatch) error { return b.DeleteRange([]byte("emb:v:"), []byte("emb:v;"), nil) },
			"setJudged(true)": func(b *GatedBatch) error {
				return b.setJudged([]byte("book:venom"), []byte("{}"), true)
			},
			"Apply of a recorded Set":    applied(func(r *GatedBatch) error { return r.Set(row, []byte("{}"), nil) }),
			"Apply of a recorded Delete": applied(func(r *GatedBatch) error { return r.Delete(row, nil) }),
			"Apply of a recorded DeleteRange": applied(func(r *GatedBatch) error {
				return r.DeleteRange([]byte("book:"), []byte("book;"), nil)
			}),
			"Apply of a recorded setJudged(true)": applied(func(r *GatedBatch) error {
				return r.setJudged([]byte("book:venom"), []byte("{}"), true)
			}),
		} {
			b := newBatch()
			if err := stage(b); err != nil {
				t.Fatalf("%s %s: %v", kind, name, err)
			}
			countsOne(t, db, kind+" "+name, 1, func() error { return b.Commit(pebble.Sync) })
			_ = b.Close()
		}
	}

	// Staging counts nothing; the commit counts once, however many verdict
	// keys the batch holds.
	b := db.NewBatch()
	countsOne(t, db, "staging two verdict keys", 0, func() error {
		if err := b.Set(row, []byte("{}"), nil); err != nil {
			return err
		}
		return b.Delete([]byte("book:venom"), nil)
	})
	countsOne(t, db, "committing a batch with two verdict keys", 1, func() error { return b.Commit(pebble.Sync) })
	_ = b.Close()

	for name, stage := range map[string]func(b *GatedBatch) error{
		"batch Set of another key": func(b *GatedBatch) error { return b.Set(other, []byte("1"), nil) },
		"batch setJudged(false)": func(b *GatedBatch) error {
			return b.setJudged([]byte("book:venom"), []byte("{}"), false)
		},
		"batch Apply of a recorded setJudged(false)": applied(func(r *GatedBatch) error {
			return r.setJudged([]byte("book:venom"), []byte("{}"), false)
		}),
		"batch Apply of a recorded Set of another key": applied(func(r *GatedBatch) error {
			return r.Set(other, []byte("1"), nil)
		}),
	} {
		b := db.NewBatch()
		if err := stage(b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		countsOne(t, db, name, 0, func() error { return b.Commit(pebble.Sync) })
		_ = b.Close()
	}

	// A recording batch never commits, so staging into one counts nothing.
	r := db.newRecordingBatch()
	countsOne(t, db, "recording a verdict write", 0, func() error { return r.Set(row, []byte("{}"), nil) })
	_ = r.Close()
}

// verdictStore is an in-memory PebbleStore and the embedding store sharing
// its database, as production wires them: on the core backend.
func verdictStore(t *testing.T) (*PebbleStore, *EmbeddingStore) {
	t.Helper()
	ps, err := NewPebbleStoreInMemory("verdict")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ps.Close() })
	if ps.DB().lr == nil {
		t.Fatal("verdictStore: want the core backend books' store runs on")
	}
	return ps, NewEmbeddingStore(ps.DB())
}

func intp(v int) *int    { return &v }
func boolp(v bool) *bool { return &v }

// TestVerdictWatch_StoreChokepoints: the store writes the reconcile's verdict
// reads (embedding rows, book existence, the primary flag and the series
// fields) count, and a book update that leaves those fields alone does not.
func TestVerdictWatch_StoreChokepoints(t *testing.T) {
	ps, es := verdictStore(t)
	db := ps.DB()
	emb := Embedding{EntityType: "book", EntityID: "venom", Vector: []float32{1, 0, 0, 0}, Model: "bge-m3"}

	countsOne(t, db, "EmbeddingStore.Upsert", 1, func() error { return es.Upsert(emb) })
	countsOne(t, db, "EmbeddingStore.Delete", 1, func() error { return es.Delete("book", "venom") })
	countsOne(t, db, "CreateBook", 1, func() error {
		_, err := ps.CreateBook(&Book{ID: "venom", Title: "Venom", IsPrimaryVersion: boolp(true)})
		return err
	})

	update := func(what string, want int64, edit func(b *Book)) {
		t.Helper()
		countsOne(t, db, "UpdateBook: "+what, want, func() error {
			b, err := ps.GetBookByID("venom")
			if err != nil {
				return err
			}
			edit(b)
			_, err = ps.UpdateBook("venom", b)
			return err
		})
	}
	desc := "a Gotham Knights tie-in"
	update("description only", 0, func(b *Book) { b.Description = &desc })
	update("primary flag", 1, func(b *Book) { b.IsPrimaryVersion = boolp(false) })
	update("primary flag unset (reads as primary)", 1, func(b *Book) { b.IsPrimaryVersion = nil })
	update("primary flag set true from unset", 0, func(b *Book) { b.IsPrimaryVersion = boolp(true) })
	update("series id", 1, func(b *Book) { b.SeriesID = intp(3) })
	update("title without a series sequence", 1, func(b *Book) { b.Title = "Venom, Book 2" })
	update("series sequence", 1, func(b *Book) { b.SeriesSequence = intp(2) })
	update("title with a series sequence", 0, func(b *Book) { b.Title = "Venom (Knightfall)" })
	countsOne(t, db, "ModifyBook primary flag", 1, func() error {
		_, err := ps.ModifyBook("venom", func(b *Book) error { b.IsPrimaryVersion = boolp(false); return nil })
		return err
	})
	countsOne(t, db, "SetRaw of a book row", 1, func() error { return ps.SetRaw("book:bane", []byte("{}")) })
	// One batch deletes the row and its embedding row together.
	countsOne(t, db, "DeleteBook", 1, func() error { return ps.DeleteBook("venom") })
}

// TestVerdictWatch_SigMigrationIsNotAVerdictWrite: the signature-sidecar
// migration rewrites the book row with only the five signature keys removed.
func TestVerdictWatch_SigMigrationIsNotAVerdictWrite(t *testing.T) {
	env := newBookSigEnv(t)
	const id = "01VERDICTSIGMIGRATE00000"
	migrateSeedLegacyRow(t, env.store, id, "verdict", nil)
	countsOne(t, env.store.DB(), "MigrateBookSigToSidecar", 0, func() error {
		outcome, err := env.store.MigrateBookSigToSidecar(id, false)
		if err == nil && outcome != BookSigMigrateMigrated {
			t.Fatalf("outcome = %v, want migrated", outcome)
		}
		return err
	})
}

// boundRogues is exportedRogues whose store is bound to db and model.
func boundRogues(t *testing.T, dir string, db *GatedDB, model string) (*HNSWEmbeddingStore, func(string) (int, error)) {
	t.Helper()
	s, truth := exportedRogues(t, dir)
	s.BindTruthSource(db, model)
	return s, truth
}

// TestMarkCleanStop_RefusesAfterAPebbleVerdictWrite is the review's probe at
// the store: a write the graph never saw (here an embedding row deleted
// straight from Pebble) since the store opened means the graph may not be the
// rows, whatever the snapshot says.
func TestMarkCleanStop_RefusesAfterAPebbleVerdictWrite(t *testing.T) {
	_, es := verdictStore(t)
	dir := t.TempDir()
	s, _ := boundRogues(t, dir, es.PebbleDB(), "bge-m3")
	if err := es.Delete("book", "bane"); err != nil {
		t.Fatal(err)
	}
	// The refusal is decided before any file I/O: in a directory it cannot
	// write, a refusal is still a refusal, not a failed write.
	readOnlyDir(t, dir)
	written, reason, err := s.MarkCleanStop(dir)
	if err != nil || written {
		t.Fatalf("MarkCleanStop = %v, %q, %v; want refused", written, reason, err)
	}
	if !strings.Contains(reason, "Pebble write(s) since the store opened") {
		t.Errorf("reason = %q", reason)
	}
	if markerExists(t, dir) {
		t.Error("a refused MarkCleanStop left a marker")
	}
}

// readOnlyDir makes dir unwritable for the rest of the test (and writable
// again for its cleanup). Skipped as root, who writes anyway.
func readOnlyDir(t *testing.T, dir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
}

// TestCleanStop_APebbleVerdictWriteAfterTheMarkerRemovesIt: a writer that
// outlived the stop and wrote Pebble after the marker removes it.
func TestCleanStop_APebbleVerdictWriteAfterTheMarkerRemovesIt(t *testing.T) {
	ps, _ := verdictStore(t)
	dir := t.TempDir()
	s, _ := boundRogues(t, dir, ps.DB(), "bge-m3")
	markCleanStop(t, s, dir)
	if _, err := ps.CreateBook(&Book{ID: "scarecrow", Title: "Fear State"}); err != nil {
		t.Fatal(err)
	}
	if markerExists(t, dir) {
		t.Error("a verdict write after the marker must remove it")
	}
}

// TestCleanStop_AVerdictWriteBeforeTheImportReconciles: a write the boot made
// to Pebble before it loaded the snapshot is in no graph.
func TestCleanStop_AVerdictWriteBeforeTheImportReconciles(t *testing.T) {
	dir := t.TempDir()
	src, truth := exportedRogues(t, dir)
	markCleanStop(t, src, dir)

	_, es := verdictStore(t)
	if err := es.Upsert(Embedding{EntityType: "author", EntityID: "7", Vector: []float32{1, 0, 0, 0}, Model: "bge-m3"}); err != nil {
		t.Fatal(err)
	}
	next := NewHNSWEmbeddingStore(4)
	next.BindTruthSource(es.PebbleDB(), "")
	if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
		t.Fatal(err)
	}
	if !next.NeedsReconcile() {
		t.Error("a verdict write before the import must make the boot reconcile")
	}
}

// TestCleanStop_AnotherEmbeddingModelReconciles: the marker records the model
// the rows were classified against, and a boot on another model reconciles.
func TestCleanStop_AnotherEmbeddingModelReconciles(t *testing.T) {
	for _, tc := range []struct {
		name, next string
		stale      bool
	}{
		{"same model", "bge-m3", false},
		{"another model", "text-embedding-3-large", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			src, truth := exportedRogues(t, dir)
			src.BindTruthSource(verdictCoreDB(t), "bge-m3")
			markCleanStop(t, src, dir)

			next := NewHNSWEmbeddingStore(4)
			next.BindTruthSource(verdictCoreDB(t), tc.next)
			if err := next.ImportWithStalenessCheck(dir, truth); err != nil {
				t.Fatal(err)
			}
			if got := next.NeedsReconcile(); got != tc.stale {
				t.Errorf("NeedsReconcile = %v, want %v", got, tc.stale)
			}
		})
	}
}

// TestCleanStop_AMarkerThatDoesNotParseVouchesForNothing: only a marker this
// build wrote vouches; anything else at the path is removed and reconciled.
func TestCleanStop_AMarkerThatDoesNotParseVouchesForNothing(t *testing.T) {
	for name, body := range map[string]string{
		"empty":         "",
		"plain text":    "clean stop\n",
		"other version": `{"version":2,"model":""}`, // the model matches: only the version refuses
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			_, truth := exportedRogues(t, dir)
			if err := os.WriteFile(cleanStopPath(dir), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			s := NewHNSWEmbeddingStore(4)
			if err := s.ImportWithStalenessCheck(dir, truth); err != nil {
				t.Fatal(err)
			}
			if !s.NeedsReconcile() {
				t.Error("a marker that does not vouch must not let the boot skip the reconcile")
			}
			if markerExists(t, dir) {
				t.Error("the boot must remove whatever marker is there")
			}
		})
	}
}

// TestMarkCleanStop_AVerdictWriteRacingTheMarkerRemovesIt: a Pebble write
// that counts after MarkCleanStop's check, but before the marker is live,
// finds no marker to remove; MarkCleanStop's second read of the count must.
func TestMarkCleanStop_AVerdictWriteRacingTheMarkerRemovesIt(t *testing.T) {
	_, es := verdictStore(t)
	dir := t.TempDir()
	s, _ := boundRogues(t, dir, es.PebbleDB(), "bge-m3")
	markCleanStopAfterCheckHook = func() {
		if err := es.Delete("book", "bane"); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { markCleanStopAfterCheckHook = nil })

	written, reason, err := s.MarkCleanStop(dir)
	if err != nil || written {
		t.Fatalf("MarkCleanStop = %v, %q, %v; want refused", written, reason, err)
	}
	if markerExists(t, dir) {
		t.Error("a verdict write racing the marker left it on disk")
	}
}
