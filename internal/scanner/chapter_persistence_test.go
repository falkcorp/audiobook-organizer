// file: internal/scanner/chapter_persistence_test.go
// version: 1.3.0
// guid: cb2ed4a4-974b-4d88-8d46-0a0f365ba430
// last-edited: 2026-09-22

package scanner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audioutil"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// odysseyM4B is the real, committed 115 MB fixture with 6 embedded chapters,
// mirroring internal/audioutil/chapters_test.go's fixture constant (same
// relative depth: internal/scanner is two levels below the repo root, same
// as internal/audioutil).
const chapterTestOdysseyM4B = "../../testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b"

// chapterTestOdysseyMP3Track fires the mp3 fixture for a given 1-based
// track number (1..6) in the 6-file version of the same book.
func chapterTestOdysseyMP3Track(n int) string {
	return filepath.Join("..", "..", "testdata", "audio", "librivox", "odyssey_butler_librivox",
		trackFilename(n))
}

func trackFilename(n int) string {
	return "odyssey_0" + string(rune('0'+n)) + "_homer_butler_64kb.mp3"
}

// odysseyTrackTitle is the known embedded `title` tag for track n (1..6),
// verified via `ffprobe -show_entries format_tags=title` during this task's
// authoring: "The Odyssey: Book 0N".
func odysseyTrackTitle(n int) string {
	return "The Odyssey: Book 0" + string(rune('0'+n))
}

// requireChapterTestFFprobe guards on ffprobe alone even though
// audioutil.ProbeDurationSeconds prefers mediainfo. That is still SUFFICIENT --
// ProbeDurationSeconds always falls back to ffprobe, so ffprobe on PATH means
// every probe in these tests can be answered. The only cost is a false skip on
// the unusual machine that has mediainfo but not ffprobe.
func requireChapterTestFFprobe(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not found on PATH, skipping")
	}
}

func requireChapterTestFixture(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixture missing at %s, skipping: %v", path, err)
	}
}

func TestPersistChaptersForBook_SingleFileM4B_UsesEmbeddedChapters(t *testing.T) {
	requireChapterTestFFprobe(t)
	requireChapterTestFixture(t, chapterTestOdysseyM4B)

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	book, err := store.CreateBook(&database.Book{FilePath: chapterTestOdysseyM4B, Title: "The Odyssey"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	ctx := context.Background()
	if err := PersistChaptersForBook(ctx, book.FilePath, nil); err != nil {
		t.Fatalf("PersistChaptersForBook: %v", err)
	}

	chs, err := store.GetChaptersForBook(book.ID)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(chs) != 12 {
		t.Fatalf("got %d chapters, want 12: %+v", len(chs), chs)
	}
	if chs[0].StartSec != 0 {
		t.Errorf("chs[0].StartSec = %v, want 0", chs[0].StartSec)
	}
	wantTitle := "The Odyssey: Book 01"
	if chs[0].Title != wantTitle {
		t.Errorf("chs[0].Title = %q, want %q", chs[0].Title, wantTitle)
	}
	const wantEnd = 21744.489070
	if diff := chs[11].EndSec - wantEnd; diff > 0.001 || diff < -0.001 {
		t.Errorf("chs[11].EndSec = %v, want within 0.001 of %v (m4b's own last-chapter end)", chs[11].EndSec, wantEnd)
	}
}

func TestPersistChaptersForBook_MultiFileMP3s_SynthesizesFromTrackTags(t *testing.T) {
	requireChapterTestFFprobe(t)
	for n := 1; n <= 6; n++ {
		requireChapterTestFixture(t, chapterTestOdysseyMP3Track(n))
	}

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	book, err := store.CreateBook(&database.Book{FilePath: chapterTestOdysseyMP3Track(1), Title: "The Odyssey"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}
	for n := 1; n <= 6; n++ {
		bf := &database.BookFile{
			BookID:      book.ID,
			FilePath:    chapterTestOdysseyMP3Track(n),
			Title:       odysseyTrackTitle(n),
			TrackNumber: n,
		}
		if err := store.CreateBookFile(bf); err != nil {
			t.Fatalf("CreateBookFile track %d: %v", n, err)
		}
	}

	ctx := context.Background()
	if err := PersistChaptersForBook(ctx, book.FilePath, nil); err != nil {
		t.Fatalf("PersistChaptersForBook: %v", err)
	}

	chs, err := store.GetChaptersForBook(book.ID)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(chs) != 6 {
		t.Fatalf("got %d chapters, want 6: %+v", len(chs), chs)
	}
	if chs[0].Title != "The Odyssey: Book 01" {
		t.Errorf("chs[0].Title = %q, want %q (from tag, not filename)", chs[0].Title, "The Odyssey: Book 01")
	}
	if chs[0].StartSec != 0 {
		t.Errorf("chs[0].StartSec = %v, want 0", chs[0].StartSec)
	}
	for i := 1; i < len(chs); i++ {
		if chs[i].StartSec <= chs[i-1].StartSec {
			t.Fatalf("chapter offsets not monotonically increasing at index %d: %v <= %v", i, chs[i].StartSec, chs[i-1].StartSec)
		}
	}

	// wantSumOfTracks is DERIVED, not a literal, and that is the point of this
	// block. It used to be the constant 9975.431111, which is what
	// `ffprobe -show_entries format=duration` sums to over these six mp3s. But
	// audioutil.ProbeDurationSeconds -- the function synthesizeMultiFileChapters
	// actually calls -- tries mediainfo FIRST and only falls back to ffprobe
	// (see the doc comment on ProbeDurationSeconds). On a machine with
	// mediainfo installed the same six files sum to 9975.827000, because
	// mediainfo counts frames where ffprobe estimates: measured per track with
	// MediaInfoLib v26.05, the two disagree by 52ms on tracks 1/2/3/5 and 94ms
	// on tracks 4/6. So the literal did not encode "the sum of the tracks" at
	// all -- it encoded "the sum of the tracks AS REPORTED BY WHICHEVER PROBE
	// BINARY HAPPENS TO BE INSTALLED", and the test failed by 0.396s on any
	// developer machine that has mediainfo. It never caught this in CI because
	// no workflow in .github/workflows installs ffmpeg or mediainfo, so the
	// requireChapterTestFFprobe guard skips this test on every CI run.
	//
	// Summing ProbeDurationSeconds here instead makes the tolerance TIGHTER,
	// not looser: SynthesizeChapters accumulates `start = start + dur` in
	// GetBookFiles order (audioutil/timeline.go), and the loop below performs
	// the identical float64 additions in the identical order, so the two agree
	// to the last bit and 1e-6 covers accumulation only. The alternative --
	// setting audioutil's mediainfo kill-switch env var to force ffprobe and
	// keep the absolute literal -- was rejected: it would pin a pretty number
	// by exercising a probe path production does not take on this machine.
	//
	// Two pre-existing assertions, not one, keep a derived expectation from
	// being vacuous, and they catch different regressions. The monotonic-offset
	// loop above catches a probe that returns zero durations with no error --
	// wantSumOfTracks would be 0, every chapter would start at 0, and it fails
	// there. The containerDuration check below catches a wrong-but-nonzero
	// duration source (Book.Duration, the container, a rounded value); it holds
	// under either prober, since the ffprobe sum misses the container by 0.049s
	// and the mediainfo sum by 0.347s, both outside its 0.01 window.
	var wantSumOfTracks float64
	for n := 1; n <= 6; n++ {
		dur, err := audioutil.ProbeDurationSeconds(ctx, "", chapterTestOdysseyMP3Track(n))
		if err != nil {
			t.Fatalf("ProbeDurationSeconds(track %d): %v", n, err)
		}
		wantSumOfTracks += dur
	}
	const containerDuration = 9975.480544
	if diff := chs[5].EndSec - wantSumOfTracks; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("chs[5].EndSec = %v, want within 1e-6 of sum-of-tracks %v", chs[5].EndSec, wantSumOfTracks)
	}
	if diff := chs[5].EndSec - containerDuration; diff > -0.01 && diff < 0.01 {
		t.Errorf("chs[5].EndSec = %v must NOT be close to container duration %v (this book has no container in this path)", chs[5].EndSec, containerDuration)
	}
}

// No requireChapterTestFFprobe / requireChapterTestFixture here, deliberately.
//
// This test is the one case in this file that never probes anything and never
// opens the file: PersistChaptersForBook returns at the `len(existingChapters)
// > 0` guard, before GetBookFiles and before any ffprobe call. The path below
// is only ever a map key. It carried both guards anyway until 2026-08-19, and
// the cost was not theoretical -- no workflow in .github/workflows installs
// ffmpeg, so on CI this SKIPPED every run. Verified by running it with an
// ffprobe-free PATH: "ffprobe not found on PATH, skipping".
//
// So the assertion that a rescan does not re-extract and overwrite existing
// chapters -- which is what stops every scan of every book rewriting its whole
// chapter list -- has never actually executed in CI. Its siblings above genuinely
// need the fixtures; this one only needed to be asked.
func TestPersistChaptersForBook_Idempotent_SkipsReExtraction(t *testing.T) {
	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	book, err := store.CreateBook(&database.Book{FilePath: chapterTestOdysseyM4B, Title: "The Odyssey"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	sentinel := []database.Chapter{{ID: 0, StartSec: 1, EndSec: 2, Title: "sentinel"}}
	if err := store.SaveChaptersForBook(book.ID, sentinel); err != nil {
		t.Fatalf("SaveChaptersForBook: %v", err)
	}

	ctx := context.Background()
	if err := PersistChaptersForBook(ctx, book.FilePath, nil); err != nil {
		t.Fatalf("PersistChaptersForBook: %v", err)
	}

	chs, err := store.GetChaptersForBook(book.ID)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if len(chs) != 1 || chs[0].Title != "sentinel" {
		t.Fatalf("expected untouched sentinel chapter list, got %+v", chs)
	}
}

func TestPersistChaptersForBook_NoEmbeddedChaptersSingleTrack_NoOp(t *testing.T) {
	requireChapterTestFFprobe(t)
	requireChapterTestFixture(t, chapterTestOdysseyMP3Track(1))

	store, cleanup := setupPebbleStore(t)
	defer cleanup()
	SetStore(store)
	defer SetStore(nil)

	book, err := store.CreateBook(&database.Book{FilePath: chapterTestOdysseyMP3Track(1), Title: "Track 1 Alone"})
	if err != nil {
		t.Fatalf("CreateBook: %v", err)
	}

	ctx := context.Background()
	if err := PersistChaptersForBook(ctx, book.FilePath, nil); err != nil {
		t.Fatalf("PersistChaptersForBook: %v", err)
	}

	chs, err := store.GetChaptersForBook(book.ID)
	if err != nil {
		t.Fatalf("GetChaptersForBook: %v", err)
	}
	if chs != nil {
		t.Fatalf("expected (nil, nil) for a single track with no embedded chapters, got %+v", chs)
	}
}

func TestPersistChaptersForBook_NonPebbleStore_LogsWarning(t *testing.T) {
	mockDB := &database.MockStore{
		GetBookByFilePathFunc: func(path string) (*database.Book, error) {
			return &database.Book{ID: "book-1", FilePath: path}, nil
		},
	}
	SetStore(mockDB)
	defer SetStore(nil)

	before := chapterStoreAssertErrCount.Load()

	ctx := context.Background()
	if err := PersistChaptersForBook(ctx, "/some/path.m4b", nil); err != nil {
		t.Fatalf("PersistChaptersForBook: expected nil error, got %v", err)
	}

	after := chapterStoreAssertErrCount.Load()
	if after != before+1 {
		t.Fatalf("chapterStoreAssertErrCount = %d, want %d (exactly one increment)", after, before+1)
	}
}
