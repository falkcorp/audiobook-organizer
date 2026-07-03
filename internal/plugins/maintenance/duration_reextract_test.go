// file: internal/plugins/maintenance/duration_reextract_test.go
// version: 1.5.0
// guid: 4a7d1e92-8c63-4f50-a1b8-3e6c9d2f5a04
// last-edited: 2026-07-03

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func mustReextractParams(t *testing.T, dryRun bool, limit int) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(durationReextractParams{DryRun: dryRun, Limit: limit})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	return b
}

// newReextractPlugin wires a MockStore over a fixed book slice. updates records
// every UpdateBook call so apply-path tests can assert writes.
func newReextractPlugin(books []database.Book) (*Plugin, *[]database.Book) {
	updates := make([]database.Book, 0)
	byID := make(map[string]database.Book, len(books))
	for _, b := range books {
		byID[b.ID] = b
	}
	store := &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			end := offset + limit
			if end > len(books) {
				end = len(books)
			}
			return books[offset:end], nil
		},
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			b, ok := byID[id]
			if !ok {
				return nil, nil
			}
			cp := b
			return &cp, nil
		},
		UpdateBookFunc: func(_ string, b *database.Book) (*database.Book, error) {
			updates = append(updates, *b)
			return b, nil
		},
	}
	return New(fakeDeps{store: store}), &updates
}

func intPtr(v int) *int { return &v }

// TestDurationReextract_Registered verifies the op def carries the expected ID,
// capabilities, and dry-run-friendly defaults.
func TestDurationReextract_Registered(t *testing.T) {
	p := New(fakeDeps{store: &database.MockStore{}})
	def := p.durationReextractDef()
	if def.ID != "maintenance.duration-reextract" {
		t.Errorf("def ID = %q, want maintenance.duration-reextract", def.ID)
	}
	if def.ConcurrencyKey != "maintenance.duration-reextract" {
		t.Errorf("ConcurrencyKey = %q", def.ConcurrencyKey)
	}
	if !def.Cancellable {
		t.Error("op must be cancellable")
	}
	if def.Run == nil {
		t.Error("op Run must be set")
	}
}

// TestDurationDiffMeaningful exercises the tolerance gate (>2% AND >5s).
func TestDurationDiffMeaningful(t *testing.T) {
	cases := []struct {
		old, new int
		want     bool
	}{
		{0, 100, true},      // no stored value, any real value is an improvement
		{0, 0, false},       // nothing usable
		{3600, 3600, false}, // identical
		{3600, 3603, false}, // 3s — under both floors
		{3600, 3700, true},  // 100s and ~2.8%
		{3600, 7200, true},  // exactly double (the m4b bug signature)
		{1000, 1004, false}, // 4s, under abs floor even though >0.2%
		{100, 110, true},    // 10s and 10%
	}
	for _, c := range cases {
		if got := durationDiffMeaningful(c.old, c.new); got != c.want {
			t.Errorf("durationDiffMeaningful(%d,%d) = %v, want %v", c.old, c.new, got, c.want)
		}
	}
}

// TestDurationReextract_DryRunWritesNothing runs the full dry-run path over a
// real on-disk file and confirms no UpdateBook calls occur. The temp file is not
// valid audio, so mediainfo.Extract yields a read path that exercises the op's
// error/skip accounting — the contract under test is "dry run never writes".
func TestDurationReextract_DryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "book.m4b")
	if err := os.WriteFile(fp, []byte("not real audio but present on disk"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	books := []database.Book{
		{ID: "b1", Title: "Present file", FilePath: fp, Duration: intPtr(100)},
		{ID: "b2", Title: "Missing file", FilePath: filepath.Join(dir, "gone.m4b"), Duration: intPtr(100)},
		{ID: "b3", Title: "No path", FilePath: ""},
	}
	p, updates := newReextractPlugin(books)

	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, true, 0), &fakeReporter{}); err != nil {
		t.Fatalf("dry-run returned error: %v", err)
	}
	if len(*updates) != 0 {
		t.Errorf("dry run must write nothing, got %d UpdateBook calls", len(*updates))
	}
}

// TestDurationReextract_NilParamsDefaultsDryRun verifies nil params -> dryRun=true.
func TestDurationReextract_NilParamsDefaultsDryRun(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "book.m4b")
	if err := os.WriteFile(fp, []byte("present"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	books := []database.Book{{ID: "b1", FilePath: fp, Duration: intPtr(100)}}
	p, updates := newReextractPlugin(books)

	if err := p.runDurationReextract(context.Background(), nil, &fakeReporter{}); err != nil {
		t.Fatalf("nil-params run returned error: %v", err)
	}
	if len(*updates) != 0 {
		t.Errorf("nil params must default to dry run, got %d writes", len(*updates))
	}
}

// TestDurationReextract_EmptyLibrary verifies a clean exit with no books.
func TestDurationReextract_EmptyLibrary(t *testing.T) {
	p, updates := newReextractPlugin(nil)
	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("empty-library run returned error: %v", err)
	}
	if len(*updates) != 0 {
		t.Errorf("empty library must write nothing, got %d writes", len(*updates))
	}
}

// TestDurationReextract_MultiFileMissingSegmentsSkipped exercises the v2
// multi-file branch: a book whose audio lives in BookFile segments. When a
// segment's file is missing on disk the book total can't be trusted, so the
// whole book is skipped — no Book or BookFile writes, even in apply mode.
func TestDurationReextract_MultiFileMissingSegmentsSkipped(t *testing.T) {
	writes := 0
	books := []database.Book{{ID: "bm", Title: "Multi", FilePath: "/lib/Multi", Duration: intPtr(100)}}
	store := &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			return books, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "s1", BookID: "bm", FilePath: "/nonexistent/01.mp3", Duration: 50},
				{ID: "s2", BookID: "bm", FilePath: "/nonexistent/02.mp3", Duration: 50},
			}, nil
		},
		UpdateBookFunc:     func(_ string, b *database.Book) (*database.Book, error) { writes++; return b, nil },
		UpdateBookFileFunc: func(_ string, _ *database.BookFile) error { writes++; return nil },
	}
	p := New(fakeDeps{store: store})
	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if writes != 0 {
		t.Errorf("multi-file book with missing segments must be skipped, got %d writes", writes)
	}
}

// TestDurationReextract_FingerprintDurationFirst is the v3 contract: when every
// segment carries a stored fingerprint duration (AcoustIDFingerprintDurationSec),
// the op corrects Book.Duration from those values WITHOUT touching the
// filesystem. The segment paths below do not exist on disk — under the v2 logic
// (os.Stat first) the whole book would be skipped, so a successful UpdateBookFile
// proves the fingerprint-first fast path ran with zero ffprobe/stat calls. Only
// the drifted segment is written; the already-correct one is left alone.
func TestDurationReextract_FingerprintDurationFirst(t *testing.T) {
	var written []database.BookFile
	books := []database.Book{{ID: "bf", Title: "Fingerprinted", FilePath: "/lib/Fingerprinted", Duration: intPtr(120)}}
	store := &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(_, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			return books, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				// Stored Duration wildly wrong (the estimate bug); fingerprint says 1800s.
				{ID: "s1", BookID: "bf", FilePath: "/nonexistent/01.mp3", Duration: 50, AcoustIDFingerprintDurationSec: 1800.0},
				// Stored Duration already within tolerance of the fingerprint value.
				{ID: "s2", BookID: "bf", FilePath: "/nonexistent/02.mp3", Duration: 1801, AcoustIDFingerprintDurationSec: 1800.0},
			}, nil
		},
		UpdateBookFileFunc: func(_ string, f *database.BookFile) error {
			written = append(written, *f)
			return nil
		},
	}
	p := New(fakeDeps{store: store})
	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("expected exactly 1 segment write (the drifted one), got %d", len(written))
	}
	if written[0].ID != "s1" {
		t.Errorf("expected segment s1 to be corrected, got %q", written[0].ID)
	}
	if written[0].Duration != 1800 {
		t.Errorf("corrected segment Duration = %d, want 1800 (rounded fingerprint)", written[0].Duration)
	}
}

// TestDurationReextract_MixedFingerprintAndFfprobe verifies the ffprobe fallback
// for iTunes-linked segments: one fingerprinted segment + one iTunes segment
// without a fingerprint. The iTunes segment falls through to ffprobe; its file
// is missing, so the whole book is skipped (trust invariant).
func TestDurationReextract_MixedFingerprintAndFfprobe(t *testing.T) {
	writes := 0
	itunesPID := "itunes-pid-mixed"
	bookPID := itunesPID
	books := []database.Book{{ID: "bm", Title: "Mixed", FilePath: "/lib/Mixed", Duration: intPtr(120), ITunesPersistentID: &bookPID}}
	store := &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(_, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			return books, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "s1", BookID: "bm", FilePath: "/nonexistent/01.mp3", Duration: 50, AcoustIDFingerprintDurationSec: 1800.0},
				// iTunes segment, no fingerprint → must fall back to ffprobe → missing file → skip
				{ID: "s2", BookID: "bm", FilePath: "/nonexistent/02.mp3", Duration: 50, ITunesPersistentID: itunesPID},
			}, nil
		},
		UpdateBookFileFunc: func(_ string, _ *database.BookFile) error { writes++; return nil },
		UpdateBookFunc:     func(_ string, b *database.Book) (*database.Book, error) { writes++; return b, nil },
	}
	p := New(fakeDeps{store: store})
	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if writes != 0 {
		t.Errorf("book with an unreadable iTunes segment must be skipped, got %d writes", writes)
	}
}

// TestDurationReextract_ParallelWorkers_AllBooksProcessed verifies that running
// with Workers>1 produces the same set of corrections as Workers=1. Uses 10
// books each with a drifted fingerprint segment — all must be written regardless
// of which worker picks them up.
func TestDurationReextract_ParallelWorkers_AllBooksProcessed(t *testing.T) {
	const n = 10
	var written []string // segment IDs written
	var mu sync.Mutex

	books := make([]database.Book, n)
	for i := range books {
		books[i] = database.Book{ID: fmt.Sprintf("b%d", i), FilePath: fmt.Sprintf("/lib/b%d", i), Duration: intPtr(50)}
	}

	store := &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return n, nil },
		GetAllBooksFunc: func(limit, offset int) ([]database.Book, error) {
			if offset >= n {
				return nil, nil
			}
			end := offset + limit
			if end > n {
				end = n
			}
			return books[offset:end], nil
		},
		GetBookFilesFunc: func(id string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: id + "-s1", BookID: id, FilePath: "/nonexistent/01.mp3", Duration: 50, AcoustIDFingerprintDurationSec: 3600.0},
			}, nil
		},
		UpdateBookFileFunc: func(_ string, f *database.BookFile) error {
			mu.Lock()
			written = append(written, f.ID)
			mu.Unlock()
			return nil
		},
	}

	params, _ := json.Marshal(durationReextractParams{DryRun: false, Workers: 4})
	p := New(fakeDeps{store: store})
	if err := p.runDurationReextract(context.Background(), params, &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(written) != n {
		t.Errorf("parallel run wrote %d segments, want %d", len(written), n)
	}
}

// TestProcessBookForReextract_StoredDuration_NonITunes verifies opt-2: when a
// segment has no fingerprint AND is not iTunes-linked (f.ITunesPersistentID==""
// and book.ITunesPersistentID==nil), the stored segment Duration is used without
// shelling out to ffprobe. The file path does not exist on disk, so any ffprobe
// attempt would produce a readErr and classify the book as ineligible. A result
// of eligible=true proves the stored-duration path ran instead.
func TestProcessBookForReextract_StoredDuration_NonITunes(t *testing.T) {
	store := &database.MockStore{
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				// no fingerprint, no iTunes PID, stored Duration=1800, file absent
				{ID: "s1", FilePath: "/nonexistent/01.mp3", Duration: 1800},
			}, nil
		},
	}
	book := database.Book{ID: "b1", Duration: intPtr(100)}
	res := processBookForReextract(context.Background(), store, book, time.Time{})

	if !res.eligible {
		t.Fatalf("expected eligible=true (stored duration used), got eligible=false readErr=%v", res.readErr)
	}
	if res.usedFfprobe {
		t.Error("non-iTunes segment with stored Duration must not use ffprobe")
	}
	if res.newDur != 1800 {
		t.Errorf("newDur = %d, want 1800 (stored segment Duration)", res.newDur)
	}
	if !res.wouldChange {
		t.Error("book.Duration=100 vs computed=1800 must set wouldChange=true")
	}
}

// TestProcessBookForReextract_StoredDuration_ITunes still routes to ffprobe:
// a segment that is iTunes-linked (ITunesPersistentID!="") may have the ms-bug
// duration and must be verified via ffprobe rather than trusting the stored value.
// Missing file → ffprobe fails → readErr, not eligible.
func TestProcessBookForReextract_StoredDuration_ITunes(t *testing.T) {
	pid := "itunes-pid-001"
	store := &database.MockStore{
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "s1", FilePath: "/nonexistent/01.mp3", Duration: 1800, ITunesPersistentID: pid},
			}, nil
		},
	}
	bookPID := pid
	book := database.Book{ID: "b1", Duration: intPtr(100), ITunesPersistentID: &bookPID}
	res := processBookForReextract(context.Background(), store, book, time.Time{})

	if res.eligible {
		t.Error("iTunes segment must go through ffprobe; file missing → not eligible")
	}
	if !res.readErr {
		t.Error("expected readErr=true (iTunes segment, missing file, ffprobe failed)")
	}
}

// TestProcessBookForReextract_StoredDuration_BookITunesLinked verifies that a
// segment without its own ITunesPersistentID still falls back to ffprobe when
// the BOOK is iTunes-linked (book.ITunesPersistentID!=nil). Some organized-library
// books are matched to an iTunes entry but the segment files themselves don't carry
// the PID; the book-level field is the definitive iTunes guard.
func TestProcessBookForReextract_StoredDuration_BookITunesLinked(t *testing.T) {
	pid := "book-itunes-pid"
	store := &database.MockStore{
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				// segment has no iTunes PID, but the BOOK is iTunes-linked
				{ID: "s1", FilePath: "/nonexistent/01.mp3", Duration: 1800},
			}, nil
		},
	}
	bookPID := pid
	book := database.Book{ID: "b1", Duration: intPtr(100), ITunesPersistentID: &bookPID}
	res := processBookForReextract(context.Background(), store, book, time.Time{})

	if res.eligible {
		t.Error("book-level iTunes link must prevent stored-duration shortcut; file missing → not eligible")
	}
	if !res.readErr {
		t.Error("expected readErr=true (book iTunes-linked, missing file, ffprobe failed)")
	}
}

// TestDurationReextract_FingerprintIdempotent verifies a re-run over already-correct
// books writes nothing: every segment's stored Duration already matches its
// fingerprint duration within tolerance, so there is nothing to correct.
func TestDurationReextract_FingerprintIdempotent(t *testing.T) {
	writes := 0
	books := []database.Book{{ID: "bi", Title: "Correct", FilePath: "/lib/Correct", Duration: intPtr(3600)}}
	store := &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(_, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			return books, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) {
			return []database.BookFile{
				{ID: "s1", BookID: "bi", FilePath: "/nonexistent/01.mp3", Duration: 1800, AcoustIDFingerprintDurationSec: 1800.0},
				{ID: "s2", BookID: "bi", FilePath: "/nonexistent/02.mp3", Duration: 1800, AcoustIDFingerprintDurationSec: 1800.0},
			}, nil
		},
		UpdateBookFileFunc: func(_ string, _ *database.BookFile) error { writes++; return nil },
		UpdateBookFunc:     func(_ string, b *database.Book) (*database.Book, error) { writes++; return b, nil },
	}
	p := New(fakeDeps{store: store})
	if err := p.runDurationReextract(context.Background(), mustReextractParams(t, false, 0), &fakeReporter{}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if writes != 0 {
		t.Errorf("already-correct book must be skipped on re-run, got %d writes", writes)
	}
}

// onlyMissingDurationTestBooks returns one book with a known positive
// Duration and one with Duration==nil, both virtual (no BookFile rows,
// empty FilePath so processBookForReextract short-circuits on noPath
// without touching the filesystem). Used by the OnlyMissingDuration tests
// below, which only care about whether a book was DISPATCHED (i.e. counted
// in "examined"), not how it was ultimately triaged.
func onlyMissingDurationTestBooks() []database.Book {
	return []database.Book{
		{ID: "known", Title: "Known duration", Duration: intPtr(3600)},
		{ID: "unknown", Title: "Unknown duration", Duration: nil},
	}
}

func newOnlyMissingDurationStore(books []database.Book) *database.MockStore {
	return &database.MockStore{
		CountAllBooksFunc: func() (int, error) { return len(books), nil },
		GetAllBooksFunc: func(_, offset int) ([]database.Book, error) {
			if offset >= len(books) {
				return nil, nil
			}
			return books, nil
		},
		GetBookFilesFunc: func(string) ([]database.BookFile, error) { return nil, nil },
	}
}

// examinedCountFromSummary extracts the "examined=N" value from the final
// summary log line emitted at the end of runDurationReextract.
func examinedCountFromSummary(t *testing.T, logs []string) int {
	t.Helper()
	for _, l := range logs {
		idx := strings.Index(l, "examined=")
		if idx < 0 {
			continue
		}
		var examined int
		if _, err := fmt.Sscanf(l[idx:], "examined=%d", &examined); err == nil {
			return examined
		}
	}
	t.Fatalf("no summary log line containing examined=N found in %v", logs)
	return -1
}

// TestDurationReextract_OnlyMissingDuration_SkipsKnownDuration is the scoped-skip
// half of the acceptance criteria: with OnlyMissingDuration=true, a book whose
// Duration is already known and positive must never be dispatched to a worker
// — it should not appear in the "examined" count at all (skipped in the
// producer, before dispatched++), leaving only the zero/nil-duration book.
func TestDurationReextract_OnlyMissingDuration_SkipsKnownDuration(t *testing.T) {
	books := onlyMissingDurationTestBooks()
	store := newOnlyMissingDurationStore(books)
	p := New(fakeDeps{store: store})
	reporter := &fakeReporter{}

	params, err := json.Marshal(durationReextractParams{DryRun: true, OnlyMissingDuration: true})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := p.runDurationReextract(context.Background(), params, reporter); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := examinedCountFromSummary(t, reporter.logs)
	if got != 1 {
		t.Errorf("examined = %d, want 1 (only the unknown-duration book should be dispatched)", got)
	}
}

// TestDurationReextract_OnlyMissingDuration_DefaultExaminesAll is the
// additive-not-breaking half: with OnlyMissingDuration left at its zero value
// (false), behavior must be unchanged from before this task — every book is
// still dispatched regardless of its current Duration.
func TestDurationReextract_OnlyMissingDuration_DefaultExaminesAll(t *testing.T) {
	books := onlyMissingDurationTestBooks()
	store := newOnlyMissingDurationStore(books)
	p := New(fakeDeps{store: store})
	reporter := &fakeReporter{}

	params, err := json.Marshal(durationReextractParams{DryRun: true})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if err := p.runDurationReextract(context.Background(), params, reporter); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := examinedCountFromSummary(t, reporter.logs)
	if got != 2 {
		t.Errorf("examined = %d, want 2 (OnlyMissingDuration=false must examine both books)", got)
	}
}
