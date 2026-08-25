// file: internal/database/perfile_scancache_test.go
// version: 1.3.0
// guid: e6e0e819-5a54-4dc7-9dd6-823894e21a75
// last-edited: 2026-08-24

package database

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func newScanCacheStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestScanCacheMapIsKeyedByFilePathNotBookPath is the measurement that fails
// today, and the reason the grain mismatch shipped: no existing test scans a
// MULTI-FILE book and then asserts every one of its files is represented.
//
// A single-file fixture cannot observe this bug. For a single-file book the book
// path and the file path coincide, so a book-keyed map answers correctly and the
// old implementation passes. The bug only appears once one book owns several
// files at paths that are not the book's path.
func TestScanCacheMapIsKeyedByFilePathNotBookPath(t *testing.T) {
	s := newScanCacheStore(t)

	dir := "/library/Terry Pratchett Carpe Jugulum"
	book, err := s.CreateBook(&Book{Title: "Carpe Jugulum", FilePath: dir})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	paths := []string{dir + "/Pratchett 001.mp3", dir + "/Pratchett 002.mp3", dir + "/Pratchett 003.mp3"}
	for i, p := range paths {
		if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: p}); err != nil {
			t.Fatalf("CreateBookFile %d: %v", i, err)
		}
		ok, err := s.UpdateBookFileScanCache(p, int64(1000+i), int64(2000+i))
		if err != nil {
			t.Fatalf("UpdateBookFileScanCache %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("UpdateBookFileScanCache reported no row for %q", p)
		}
	}

	m, err := s.GetScanCacheMap()
	if err != nil {
		t.Fatalf("GetScanCacheMap: %v", err)
	}

	for i, p := range paths {
		e, ok := m[p]
		if !ok {
			t.Fatalf("no scan-cache entry for %q; the map has %d entries and keys on the BOOK path "+
				"(%q) rather than each file's own path, so %d of %d files are re-read every scan",
				p, len(m), dir, len(paths)-len(m), len(paths))
		}
		if e.Mtime != int64(1000+i) || e.Size != int64(2000+i) {
			t.Fatalf("entry for %q = {mtime %d, size %d}, want {%d, %d}; the VALUE grain is wrong "+
				"(a directory inode's stat, not this file's)", p, e.Mtime, e.Size, 1000+i, 2000+i)
		}
	}
	if _, ok := m[dir]; ok {
		t.Fatalf("the map contains the BOOK directory %q as a key; after this change the scan cache "+
			"must not read Book.FilePath at all", dir)
	}
}

// TestScanCacheNeverScannedIsAbsentNotZero pins the ENCODING of the two states the
// pointer fields exist to distinguish: never scanned, and scanned-and-measured-zero.
// A never-scanned row must serialize with the keys ABSENT, and a real zero
// measurement must survive the round trip -- otherwise classifySkipFile compares
// against a measurement nobody took.
//
// What this test does NOT pin, despite an earlier version of this comment saying so:
// the omitzero-vs-omitempty choice. Mutation-tested 2026-08-24 -- swapping all three
// tags to omitempty leaves this test GREEN, under GOEXPERIMENT=jsonv2 and without it.
// The v2 omitempty hazard (0/false get EMITTED rather than omitted) applies to value
// types; a nil pointer encodes to null, which v2 omits. What actually enforces the
// distinction is the pointer itself, and the compiler defends that: changing these to
// value types fails to build at 7 sites. Do not read a pass here as evidence the tag
// is doing work.
func TestScanCacheNeverScannedIsAbsentNotZero(t *testing.T) {
	raw, err := json.Marshal(BookFile{ID: "f1", BookID: "b1", FilePath: "/x.mp3"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"last_scan_mtime", "last_scan_size", "needs_rescan"} {
		if v, present := generic[k]; present {
			t.Fatalf("%s must be ABSENT on a never-scanned row, got %v. A nil pointer that "+
				"serializes as 0/false is indistinguishable from a real zero measurement; "+
				"use omitzero, never omitempty", k, v)
		}
	}

	// And a real zero measurement must survive, or the pointer buys nothing.
	zero := int64(0)
	raw2, err := json.Marshal(BookFile{ID: "f2", LastScanMtime: &zero, LastScanSize: &zero})
	if err != nil {
		t.Fatalf("marshal zero: %v", err)
	}
	var g2 map[string]any
	if err := json.Unmarshal(raw2, &g2); err != nil {
		t.Fatalf("unmarshal zero: %v", err)
	}
	if _, present := g2["last_scan_mtime"]; !present {
		t.Fatal("an explicit zero mtime was omitted; 'never scanned' and 'scanned, measured zero' " +
			"have collapsed into the same encoding")
	}
}

// TestBackfillSeedsSingleFileBooksOnly covers the deploy-herd migration.
//
// Seeding a MULTI-file book's members from the book-level stamp would copy a
// directory inode's mtime/size onto every member -- asserting a measurement that
// was never taken for those files.
func TestBackfillSeedsSingleFileBooksOnly(t *testing.T) {
	s := newScanCacheStore(t)

	mtime, size := int64(4242), int64(999)

	single, err := s.CreateBook(&Book{Title: "Single", FilePath: "/lib/single.m4b",
		LastScanMtime: &mtime, LastScanSize: &size})
	if err != nil {
		t.Fatalf("CreateBook single: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: single.ID, FilePath: "/lib/single.m4b"}); err != nil {
		t.Fatalf("CreateBookFile single: %v", err)
	}

	multi, err := s.CreateBook(&Book{Title: "Multi", FilePath: "/lib/multi",
		LastScanMtime: &mtime, LastScanSize: &size})
	if err != nil {
		t.Fatalf("CreateBook multi: %v", err)
	}
	for _, p := range []string{"/lib/multi/01.mp3", "/lib/multi/02.mp3"} {
		if err := s.CreateBookFile(&BookFile{BookID: multi.ID, FilePath: p}); err != nil {
			t.Fatalf("CreateBookFile multi: %v", err)
		}
	}

	res, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	// Assert on the DATA first and the counters second. The counters are the
	// backfill reporting on its own behaviour; if the seeding rule regresses, the
	// rows are the evidence and the counter is only the claim.
	m, err := s.GetScanCacheMap()
	if err != nil {
		t.Fatalf("GetScanCacheMap: %v", err)
	}
	for _, p := range []string{"/lib/multi/01.mp3", "/lib/multi/02.mp3"} {
		if _, ok := m[p]; ok {
			t.Fatalf("%q was seeded from a MULTI-file book's stamp; that stamp describes the "+
				"directory inode (128 bytes observed on prod), not this file, so seeding it "+
				"would make the scan skip a file it has never actually measured", p)
		}
	}
	if e, ok := m["/lib/single.m4b"]; !ok || e.Mtime != mtime || e.Size != size {
		t.Fatalf("single-file book was not seeded from its book-level stamp: %+v (present=%v)", e, ok)
	}

	if res.Seeded != 1 {
		t.Fatalf("seeded %d, want exactly 1 (only the single-file book)", res.Seeded)
	}
	if res.SkippedMulti != 1 {
		t.Fatalf("skipped_multi_file = %d, want 1", res.SkippedMulti)
	}
}

// TestBackfillIsIdempotent — it runs on deploy and must be safe to re-run.
// TestBackfillSkipsSingleFileBookWhosePathIsTheDirectory covers the guard that
// mutation-testing found unprotected (2026-08-24: removing the path-equality check
// alone left every other test green).
//
// This is not an edge case, it is the common shape. createBookFilesForBook
// normalizes Book.FilePath to the CONTAINING DIRECTORY, so a book with exactly one
// file routinely ends up with book.FilePath="/lib/solo" and its file at
// "/lib/solo/solo.m4b". Such a book clears the len(files)==1 test, and only the
// path-equality guard stops it being seeded.
//
// Seeding it would be a silent data-correctness bug rather than a missed
// optimisation: the book-level stamp measures the directory inode (128 bytes was
// the observed value on prod), so the file would be marked scanned at a size and
// mtime nobody ever measured for it, and every future scan would skip it -- and
// therefore never notice the file changing. Cold-starting it costs one re-read.
func TestBackfillSkipsSingleFileBookWhosePathIsTheDirectory(t *testing.T) {
	s := newScanCacheStore(t)

	mtime, size := int64(4242), int64(128) // 128 = the directory inode size seen on prod
	book, err := s.CreateBook(&Book{Title: "Solo", FilePath: "/lib/solo",
		LastScanMtime: &mtime, LastScanSize: &size})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/solo/solo.m4b"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	res, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}

	m, err := s.GetScanCacheMap()
	if err != nil {
		t.Fatalf("GetScanCacheMap: %v", err)
	}
	if e, ok := m["/lib/solo/solo.m4b"]; ok {
		t.Fatalf("seeded %+v onto a file whose path differs from its book's; the book stamp "+
			"describes the directory %q, so this file would be skipped by every future scan "+
			"on a measurement nobody took", e, book.FilePath)
	}
	if res.Seeded != 0 {
		t.Fatalf("seeded %d, want 0", res.Seeded)
	}
	if res.SkippedPathMis != 1 {
		t.Fatalf("skipped_path_mismatch = %d, want 1", res.SkippedPathMis)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	s := newScanCacheStore(t)
	mtime, size := int64(7), int64(8)
	b, err := s.CreateBook(&Book{Title: "S", FilePath: "/a.m4b", LastScanMtime: &mtime, LastScanSize: &size})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: b.ID, FilePath: "/a.m4b"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}
	first, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("backfill 1: %v", err)
	}
	second, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("backfill 2: %v", err)
	}
	if first.Seeded != 1 || second.Seeded != 0 {
		t.Fatalf("seeded first=%d second=%d, want 1 then 0", first.Seeded, second.Seeded)
	}
}

// TestSeedingRuleIsIdenticalInBothImplementations is a conformance test over the
// two places this repo decides "does this book's stamp describe exactly one file?"
//
// There are deliberately two implementations and they cannot be collapsed:
// UpdateScanCache asks about ONE book and calls GetBookFiles, while the backfill
// asks about all 61k and builds a byBook map in a single pass to avoid 61k
// queries. Same judgement, two shapes, for a real performance reason.
//
// That is precisely the configuration this codebase has been bitten by before: a
// judgement implemented twice with nothing comparing the answers, where the copies
// drift and no test notices. So the control is the deliverable here -- this test
// fails the moment the mirror would stamp a row the backfill would refuse, or the
// reverse.
func TestSeedingRuleIsIdenticalInBothImplementations(t *testing.T) {
	cases := []struct {
		name      string
		bookPath  string
		filePaths []string
		want      bool // should the book's stamp be copied onto a file row?
	}{
		{"single file at the book path", "/lib/a.m4b", []string{"/lib/a.m4b"}, true},
		{"single file, book path is the DIRECTORY", "/lib/a", []string{"/lib/a/a.m4b"}, false},
		{"multi file", "/lib/b", []string{"/lib/b/01.mp3", "/lib/b/02.mp3"}, false},
		{"no file rows at all", "/lib/c.m4b", nil, false},
		{"single file at a wholly unrelated path", "/lib/d.m4b", []string{"/other/d.m4b"}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScanCacheStore(t)
			mtime, size := int64(7777), int64(4242)

			book, err := s.CreateBook(&Book{Title: tc.name, FilePath: tc.bookPath})
			if err != nil {
				t.Fatalf("CreateBook: %v", err)
			}
			for _, fp := range tc.filePaths {
				if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: fp}); err != nil {
					t.Fatalf("CreateBookFile %s: %v", fp, err)
				}
			}

			// Implementation 1: the live writer's mirror.
			bf, err := s.bookStampDescribesExactlyOneFile(book)
			if err != nil {
				t.Fatalf("bookStampDescribesExactlyOneFile: %v", err)
			}
			mirrorSeeds := bf != nil
			if mirrorSeeds != tc.want {
				t.Errorf("UpdateScanCache mirror would seed = %v, want %v", mirrorSeeds, tc.want)
			}

			// Implementation 2: the backfill, replaying the same rule over history.
			// Give the book a stamp so the backfill has something to copy.
			if err := s.UpdateScanCache(book.ID, mtime, size); err != nil {
				t.Fatalf("UpdateScanCache: %v", err)
			}
			// Clear any file stamp the mirror just wrote, so the backfill is
			// deciding from scratch rather than reading the other impl's answer.
			for _, fp := range tc.filePaths {
				row, err := s.GetBookFileByPath(fp)
				if err != nil || row == nil {
					continue
				}
				row.LastScanMtime, row.LastScanSize, row.NeedsRescan = nil, nil, nil
				if err := s.UpdateBookFile(row.ID, row); err != nil {
					t.Fatalf("clear stamp: %v", err)
				}
			}

			res, err := s.BackfillBookFileScanCache(false)
			if err != nil {
				t.Fatalf("backfill: %v", err)
			}
			backfillSeeds := res.Seeded == 1
			if backfillSeeds != tc.want {
				t.Errorf("backfill seeded %d (want seeded=%v); result %+v", res.Seeded, tc.want, res)
			}

			if mirrorSeeds != backfillSeeds {
				t.Fatalf("THE TWO IMPLEMENTATIONS DISAGREE for %q: mirror=%v backfill=%v. "+
					"They encode one rule and must give one answer; a row seeded by one path "+
					"and refused by the other is how the per-book/per-file grain split happened "+
					"in the first place", tc.name, mirrorSeeds, backfillSeeds)
			}
		})
	}
}

// TestRescanReArmSurvivesTheGrainSwitch replays the scanner's real sequence:
// writeBackScanCache calls UpdateScanCache (which CLEARS NeedsRescan by design)
// and then, when the file is still inside the rescan-age window, re-arms it with
// MarkNeedsRescan (scanner.go:709-726).
//
// classifySkipFile reads NeedsRescan out of the scan-cache ENTRY (scanner.go:549),
// and that entry is now built from the book_file row. So if the clear lands on the
// file row and the re-arm lands only on the book row, the re-arm is inert and a
// still-changing file gets treated as settled -- a wrong SKIP, which is the one
// failure direction this cache must never produce. Nothing else in the suite
// covers it: the pre-existing coverage test pairs MarkNeedsRescan with
// GetDirtyBookFolders, which reads book rows and keeps working either way.
func TestRescanReArmSurvivesTheGrainSwitch(t *testing.T) {
	s := newScanCacheStore(t)

	book, err := s.CreateBook(&Book{Title: "Rearm", FilePath: "/lib/rearm.m4b"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&BookFile{BookID: book.ID, FilePath: "/lib/rearm.m4b"}); err != nil {
		t.Fatalf("CreateBookFile: %v", err)
	}

	// 1. The write-back stamps the file and clears the rescan flag.
	if err := s.UpdateScanCache(book.ID, 100, 200); err != nil {
		t.Fatalf("UpdateScanCache: %v", err)
	}
	m, err := s.GetScanCacheMap()
	if err != nil {
		t.Fatalf("GetScanCacheMap: %v", err)
	}
	if m["/lib/rearm.m4b"].NeedsRescan {
		t.Fatal("UpdateScanCache must CLEAR NeedsRescan on the entry the scanner reads")
	}

	// 2. The file is inside the rescan-age window, so the scanner re-arms.
	if err := s.MarkNeedsRescan(book.ID); err != nil {
		t.Fatalf("MarkNeedsRescan: %v", err)
	}
	m, err = s.GetScanCacheMap()
	if err != nil {
		t.Fatalf("GetScanCacheMap: %v", err)
	}
	if !m["/lib/rearm.m4b"].NeedsRescan {
		t.Fatal("the rescan re-arm did not reach the entry classifySkipFile reads; " +
			"the clear landed on the file row and the re-arm on the book row, so a file " +
			"still inside the rescan-age window would be skipped as settled")
	}

	// 3. GetDirtyBookFolders still works off book rows -- the mirror must not have
	//    cost us the book-level view the maintenance side depends on.
	dirs, err := s.GetDirtyBookFolders()
	if err != nil {
		t.Fatalf("GetDirtyBookFolders: %v", err)
	}
	var found bool
	for _, d := range dirs {
		if d == "/lib" {
			found = true
		}
	}
	if !found {
		t.Fatalf("book-level dirty-folder view lost the book; got %v", dirs)
	}
}

// TestBackfillCreatesTheMissingSingleFileRow covers the population that would
// otherwise have been REGRESSED by the switch to a file-keyed scan cache.
//
// The scan never creates book_file rows for a genuinely single-file book
// (internal/server/server.go:1208); they appear only if the book passed through
// auto-organize. A book-keyed cache did not care because it read the book row. A
// file-keyed cache cannot see such a book at all -- so without this pass, books
// that cache CORRECTLY today would re-read on every scan forever, which is the
// same failure being fixed for multi-file books, just reintroduced on the other
// side. That is why this is part of the migration and not a follow-up.
func TestBackfillCreatesTheMissingSingleFileRow(t *testing.T) {
	s := newScanCacheStore(t)
	dir := t.TempDir()

	realFile := filepath.Join(dir, "solo.m4b")
	if err := os.WriteFile(realFile, []byte("audio-bytes-here"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	mtime, size := int64(555), int64(16)

	book, err := s.CreateBook(&Book{Title: "Solo", FilePath: realFile,
		LastScanMtime: &mtime, LastScanSize: &size})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	// Deliberately NO CreateBookFile: this is the shape the scan leaves behind.

	res, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if res.CreatedRows != 1 {
		t.Fatalf("created_rows = %d, want 1; result %+v", res.CreatedRows, res)
	}

	files, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d file rows, want 1", len(files))
	}
	got := files[0]
	if got.FilePath != realFile {
		t.Errorf("FilePath = %q, want %q", got.FilePath, realFile)
	}
	if got.OriginalFilename != "solo.m4b" || got.Format != "m4b" || got.TrackNumber != 1 {
		t.Errorf("row is not the same honest row ensureSingleFileBookFile writes: %+v", got)
	}
	if got.FileSize != 16 {
		t.Errorf("FileSize = %d, want 16 (measured by stat, not copied)", got.FileSize)
	}

	// And the whole point: the file is now visible to the file-keyed cache.
	m, err := s.GetScanCacheMap()
	if err != nil {
		t.Fatalf("GetScanCacheMap: %v", err)
	}
	e, ok := m[realFile]
	if !ok {
		t.Fatal("the created row carries no scan stamp, so this book still re-reads on " +
			"every scan -- the regression this pass exists to prevent")
	}
	if e.Mtime != mtime || e.Size != size {
		t.Errorf("stamp = %+v, want mtime=%d size=%d from the book row", e, mtime, size)
	}
}

// TestBackfillRefusesToInventRowsItCannotJustify pins the three refusals. Each is
// a case where creating a row would assert something untrue, and a wrong row here
// is worse than a missing one: it makes the scan skip a file on a measurement
// nobody took.
func TestBackfillRefusesToInventRowsItCannotJustify(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "multi")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cases := []struct {
		name     string
		filePath string
		why      string
	}{
		{"a DIRECTORY book", subdir,
			"a directory book with no rows is a different problem; one row pointing at the " +
				"folder would feed the cache a directory inode's size"},
		{"a path that does not exist", filepath.Join(dir, "gone.m4b"),
			"we do not know what the file is, so there is nothing honest to write"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newScanCacheStore(t)
			mtime, size := int64(1), int64(2)
			book, err := s.CreateBook(&Book{Title: tc.name, FilePath: tc.filePath,
				LastScanMtime: &mtime, LastScanSize: &size})
			if err != nil {
				t.Fatalf("CreateBook: %v", err)
			}

			res, err := s.BackfillBookFileScanCache(false)
			if err != nil {
				t.Fatalf("backfill: %v", err)
			}
			if res.CreatedRows != 0 {
				t.Fatalf("created %d row(s) for %s -- %s", res.CreatedRows, tc.name, tc.why)
			}
			if res.SkippedNotAFile != 1 {
				t.Errorf("skipped_not_a_regular_file = %d, want 1", res.SkippedNotAFile)
			}
			files, err := s.GetBookFiles(book.ID)
			if err != nil {
				t.Fatalf("GetBookFiles: %v", err)
			}
			if len(files) != 0 {
				t.Fatalf("invented %d row(s): %+v", len(files), files)
			}
		})
	}
}

// TestBackfillRowCreationIsIdempotent -- a migration that creates a duplicate row
// on its second run is worse than one that never ran, because book_file rows are
// what the organize filter and the dedup paths count.
func TestBackfillRowCreationIsIdempotent(t *testing.T) {
	s := newScanCacheStore(t)
	dir := t.TempDir()
	realFile := filepath.Join(dir, "once.m4b")
	if err := os.WriteFile(realFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	mtime, size := int64(9), int64(1)
	book, err := s.CreateBook(&Book{Title: "Once", FilePath: realFile,
		LastScanMtime: &mtime, LastScanSize: &size})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	first, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := s.BackfillBookFileScanCache(false)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.CreatedRows != 1 || second.CreatedRows != 0 {
		t.Fatalf("created %d then %d, want 1 then 0", first.CreatedRows, second.CreatedRows)
	}
	files, err := s.GetBookFiles(book.ID)
	if err != nil {
		t.Fatalf("GetBookFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d rows after two runs, want 1", len(files))
	}
}
