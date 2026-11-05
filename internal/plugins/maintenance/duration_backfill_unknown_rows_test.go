// file: internal/plugins/maintenance/duration_backfill_unknown_rows_test.go
// version: 1.1.0
// guid: 7e1c4b9a-52d8-4f36-a0b7-c9d13e6f2a84
// last-edited: 2026-11-05

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/aggtest"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// urReporter is a fakeReporter that also persists the op result, as the real
// reporter does (ReporterSetResult fails loudly on one that cannot).
type urReporter struct {
	fakeReporter
	mu     sync.Mutex
	result any
}

func (r *urReporter) SetResult(v any) error {
	r.mu.Lock()
	r.result = v
	r.mu.Unlock()
	return nil
}

func (r *urReporter) got(t *testing.T) UnknownRowsResult {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	res, ok := r.result.(UnknownRowsResult)
	require.True(t, ok, "the op persisted no UnknownRowsResult (got %T)", r.result)
	return res
}

// urRun runs mode=unknownRows with params (mode is added) and returns the
// persisted result.
func urRun(t *testing.T, s *database.PebbleStore, params map[string]any) (UnknownRowsResult, error) {
	t.Helper()
	return urRunCtx(t, context.Background(), s, params)
}

func urRunCtx(t *testing.T, ctx context.Context, s *database.PebbleStore, params map[string]any) (UnknownRowsResult, error) {
	t.Helper()
	if _, ok := params["mode"]; !ok {
		params["mode"] = durationModeUnknownRows
	}
	raw, err := json.Marshal(params)
	require.NoError(t, err)
	rep := &urReporter{}
	runErr := New(fakeDeps{store: s}).runDurationBackfill(ctx, raw, rep)
	rep.mu.Lock()
	has := rep.result != nil
	rep.mu.Unlock()
	if !has {
		return UnknownRowsResult{}, runErr
	}
	return rep.got(t), runErr
}

func urApply(t *testing.T, s *database.PebbleStore, params map[string]any) UnknownRowsResult {
	t.Helper()
	params["dryRun"] = false
	res, err := urRun(t, s, params)
	require.NoError(t, err)
	return res
}

// requireAccounted pins the invariant the result documents: every scanned row
// lands in exactly one outcome.
func requireAccounted(t *testing.T, r UnknownRowsResult) {
	t.Helper()
	var sum int
	if r.DryRun {
		sum = r.Borrowed + r.PromotedFromFingerprint + r.WouldProbe + r.SkippedUnreadable + r.SkippedNoPath
	} else {
		sum = r.Borrowed + r.PromotedFromFingerprint + r.ProbedKnown + r.ProbeFailed + r.EnvironmentSkipped +
			r.SkippedChanged + r.SkippedUnreadable + r.SkippedNoPath + r.WriteErrors
	}
	require.Equal(t, r.Scanned, sum, "scanned rows are not all accounted for: %+v", r)
}

const urSize = 480_000 // 128 kbps at 30 s: a plausible fixture

// urFile creates one on-disk junk file and a book_file row for it. Nothing here
// decodes audio; the stubbed ffprobe answers.
func urFile(t *testing.T, s *database.PebbleStore, bookID, path string, mutate func(*database.BookFile)) database.BookFile {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	if _, err := os.Stat(path); err != nil {
		require.NoError(t, os.WriteFile(path, make([]byte, urSize), 0o600))
	}
	f := database.BookFile{BookID: bookID, FilePath: path, FileSize: urSize, TrackNumber: 1}
	if mutate != nil {
		mutate(&f)
	}
	require.NoError(t, s.CreateBookFile(&f))
	return f
}

func urBook(t *testing.T, s *database.PebbleStore, title, dir string) database.Book {
	t.Helper()
	b, err := s.CreateBook(&database.Book{Title: title, FilePath: dir})
	require.NoError(t, err)
	return *b
}

func urRow(t *testing.T, s *database.PebbleStore, bookID, fileID string) database.BookFile {
	t.Helper()
	f, err := s.GetBookFileByID(bookID, fileID)
	require.NoError(t, err)
	require.NotNil(t, f)
	return *f
}

func urModify(t *testing.T, s *database.PebbleStore, bookID, fileID string, fn func(*database.BookFile)) {
	t.Helper()
	cur, err := s.ModifyBookFile(bookID, fileID, func(bf *database.BookFile) error { fn(bf); return nil })
	require.NoError(t, err)
	require.NotNil(t, cur)
}

// A row takes the known duration of a sibling row at the same path BEFORE any
// probe runs: the counting stub sees one run, for the one row with no sibling.
func TestUnknownRows_BorrowsFromSiblingBeforeProbing(t *testing.T) {
	runs := countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	hush := urBook(t, s, "Batman: Hush", dir)
	longHalloween := urBook(t, s, "Batman: The Long Halloween", dir)

	shared := filepath.Join(dir, "hush-01.mp3")
	donor := urFile(t, s, hush.ID, shared, func(f *database.BookFile) { f.Duration = database.Secs(42) })
	target := urFile(t, s, longHalloween.ID, shared, nil)
	loner := urFile(t, s, longHalloween.ID, filepath.Join(dir, "lh-02.mp3"), func(f *database.BookFile) { f.TrackNumber = 2 })

	res := urApply(t, s, map[string]any{})

	require.Equal(t, 1, runs(), "the borrowed row must not be probed")
	require.Equal(t, 2, res.Scanned)
	require.Equal(t, 1, res.Borrowed)
	require.Equal(t, 1, res.ProbedKnown)
	requireAccounted(t, res)

	got := urRow(t, s, longHalloween.ID, target.ID)
	require.Equal(t, 42, durtest.Sec(got.Duration), "the borrowed duration is the sibling's")
	require.Equal(t, database.StageStatus{Done: true}, got.Scan.DurationOK)
	require.Equal(t, 30, durtest.Sec(urRow(t, s, longHalloween.ID, loner.ID).Duration))
	require.Equal(t, 42, durtest.Sec(urRow(t, s, hush.ID, donor.ID).Duration), "the donor is untouched")
}

// The donor index covers the whole library: narrowing the run to the unknown
// row (what a held folder-books group's "Probe these files" will send) must not
// hide the OTHER book's row that settles it.
func TestUnknownRows_DonorOutsideTheNarrowingStillLends(t *testing.T) {
	runs := countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	hush := urBook(t, s, "Batman: Hush", dir)
	other := urBook(t, s, "Batman: Year One", dir)
	shared := filepath.Join(dir, "01.mp3")
	urFile(t, s, hush.ID, shared, func(f *database.BookFile) { f.Duration = database.Secs(27000 / 900) }) // 30 s
	target := urFile(t, s, other.ID, shared, nil)

	res := urApply(t, s, map[string]any{"fileIds": []string{target.ID}})

	require.Equal(t, 0, runs(), "a donor outside fileIds/bookIds must still be used")
	require.Equal(t, 1, res.Borrowed)
	require.Equal(t, 1, res.Scanned)
	require.Equal(t, 30, durtest.Sec(urRow(t, s, other.ID, target.ID).Duration))
}

// A sibling that cannot be the same file does not lend: other size, other hash,
// or marked Missing.
func TestUnknownRows_OnlyAMatchingPresentSiblingLends(t *testing.T) {
	countingStub(t, "exit 1")
	s := newRepairPebble(t)
	dir := t.TempDir()
	a := urBook(t, s, "Batman: Hush", dir)
	b := urBook(t, s, "Batman: Year One", dir)
	cases := map[string]func(*database.BookFile){
		"other size":     func(f *database.BookFile) { f.FileSize = urSize + 1 },
		"other hash":     func(f *database.BookFile) { f.FileHash = "hash-b" },
		"marked missing": func(f *database.BookFile) { f.Missing = true },
	}
	var targets []database.BookFile
	i := 0
	for name, mutate := range cases {
		i++
		path := filepath.Join(dir, fmt.Sprintf("%02d-%s.mp3", i, name[:5]))
		urFile(t, s, a.ID, path, func(f *database.BookFile) {
			f.Duration = database.Secs(30)
			f.FileHash = "hash-a"
			mutate(f)
		})
		targets = append(targets, urFile(t, s, b.ID, path, func(f *database.BookFile) { f.FileHash = "hash-a"; f.TrackNumber = i }))
	}
	res := urApply(t, s, map[string]any{"bookIds": []string{b.ID}})
	require.Zero(t, res.Borrowed, "no sibling here is the same file")
	require.Equal(t, len(cases), res.ProbeFailed)
	for _, tg := range targets {
		require.Nil(t, urRow(t, s, b.ID, tg.ID).Duration)
	}
}

// The fingerprint duration is promoted without a probe, rounded as the
// book-grain loop rounds it.
func TestUnknownRows_PromotesFingerprintDuration(t *testing.T) {
	runs := countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: The Killing Joke", dir)
	f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), func(f *database.BookFile) { f.AcoustIDFingerprintDurationSec = 41.6 })

	res := urApply(t, s, map[string]any{})

	require.Equal(t, 0, runs())
	require.Equal(t, 1, res.PromotedFromFingerprint)
	requireAccounted(t, res)
	got := urRow(t, s, book.ID, f.ID)
	require.Equal(t, 42, durtest.Sec(got.Duration))
	require.Equal(t, database.StageStatus{Done: true}, got.Scan.DurationOK)
}

// A dry run writes nothing, probes nothing, recomputes nothing, and says what an
// apply would do, per book.
func TestUnknownRows_DryRunWritesNothing(t *testing.T) {
	for name, params := range map[string]map[string]any{
		"default":          {},
		"dryRun true":      {"dryRun": true},
		"dry_run true":     {"dry_run": true},
		"dry_run + dryRun": {"dry_run": true, "dryRun": true},
	} {
		t.Run(name, func(t *testing.T) {
			runs := countingStub(t, "echo 30.0")
			s := newRepairPebble(t)
			dir := t.TempDir()
			a := urBook(t, s, "Batman: Hush", dir)
			b := urBook(t, s, "Batman: Year One", dir)
			shared := filepath.Join(dir, "01.mp3")
			urFile(t, s, a.ID, shared, func(f *database.BookFile) { f.Duration = database.Secs(30) })
			urFile(t, s, b.ID, shared, nil)
			urFile(t, s, b.ID, filepath.Join(dir, "02.mp3"), func(f *database.BookFile) { f.AcoustIDFingerprintDurationSec = 30; f.TrackNumber = 2 })
			urFile(t, s, b.ID, filepath.Join(dir, "03.mp3"), func(f *database.BookFile) { f.TrackNumber = 3 })
			before, err := s.GetAllBookFilesCore()
			require.NoError(t, err)
			logs := aggtest.Capture(t)

			res, err := urRun(t, s, params)
			require.NoError(t, err)

			require.Equal(t, 0, runs(), "a dry run does not run ffprobe")
			require.True(t, res.DryRun)
			require.Equal(t, 3, res.Scanned)
			require.Equal(t, 1, res.Borrowed)
			require.Equal(t, 1, res.PromotedFromFingerprint)
			require.Equal(t, 1, res.WouldProbe)
			require.Zero(t, res.ProbedKnown+res.ProbeFailed)
			requireAccounted(t, res)
			require.Equal(t, []UnknownRowsBookCounts{{BookID: b.ID, Rows: 3, Borrowed: 1, PromotedFromFingerprint: 1, WouldProbe: 1}}, res.Books)
			require.Zero(t, aggtest.CountInvocations(logs(), a.ID)+aggtest.CountInvocations(logs(), b.ID))
			after, err := s.GetAllBookFilesCore()
			require.NoError(t, err)
			require.Equal(t, before, after, "a dry run must not change a row")
		})
	}
}

// Both spellings sent and disagreeing is an error, never a guess.
func TestUnknownRows_ConflictingDryRunSpellingsAreRejected(t *testing.T) {
	s := newRepairPebble(t)
	_, err := urRun(t, s, map[string]any{"dryRun": false, "dry_run": true})
	require.Error(t, err)
}

// dry_run:false applies, as dryRun:false does.
func TestUnknownRows_SnakeCaseDryRunFalseApplies(t *testing.T) {
	countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
	res, err := urRun(t, s, map[string]any{"dry_run": false})
	require.NoError(t, err)
	require.False(t, res.DryRun)
	require.Equal(t, 30, durtest.Sec(urRow(t, s, book.ID, f.ID).Duration))
}

func TestUnknownRows_ConcurrencyIsClampedAndExplicit(t *testing.T) {
	for in, want := range map[int]int{-3: 4, 0: 4, 1: 1, 8: 8, 16: 16, 17: 16, 99: 16} {
		require.Equal(t, want, unknownRowsConcurrency(in), "workers=%d", in)
	}
}

// swapProbe replaces the pass-2 probe for one test; fn receives the real probe.
func swapProbe(t *testing.T, fn func(ctx context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error)) {
	t.Helper()
	prev := unknownRowsProbe
	unknownRowsProbe = func(ctx context.Context, path string) (*mediainfo.MediaInfo, error) {
		return fn(ctx, path, func() (*mediainfo.MediaInfo, error) { return prev(ctx, path) })
	}
	t.Cleanup(func() { unknownRowsProbe = prev })
}

// The probe pass really runs items concurrently: with the default 4 workers,
// four probes reach a barrier TOGETHER. A pool that quietly ran sequentially
// (RunItems' default when Concurrency is omitted) would never fill the barrier.
// Under -race this also exercises the Label closure's counters while workers
// write them.
func TestUnknownRows_ProbePassRunsConcurrently(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	const rows = 12
	for i := 0; i < rows; i++ {
		urFile(t, s, book.ID, filepath.Join(dir, fmt.Sprintf("%02d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 })
	}

	var inFlight, peak atomic.Int64
	barrier := make(chan struct{})
	var once sync.Once
	swapProbe(t, func(ctx context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		if n >= 4 {
			once.Do(func() { close(barrier) })
		}
		select {
		case <-barrier:
		case <-time.After(5 * time.Second): // fail fast, well under the 30 s extract timeout
		}
		return real()
	})

	res := urApply(t, s, map[string]any{})

	require.GreaterOrEqual(t, peak.Load(), int64(4), "four probes were never in flight together: the pool is not concurrent")
	require.LessOrEqual(t, peak.Load(), int64(4), "the default pool is 4 wide")
	require.Equal(t, rows, res.ProbedKnown)
	requireAccounted(t, res)
}

// An explicit workers value sizes the pool; 1 is sequential.
func TestUnknownRows_WorkersParamSizesThePool(t *testing.T) {
	for _, workers := range []int{1, 2, 7} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			stubFFprobe(t, "echo 30.0")
			s := newRepairPebble(t)
			dir := t.TempDir()
			book := urBook(t, s, "Batman: Hush", dir)
			for i := 0; i < 14; i++ {
				urFile(t, s, book.ID, filepath.Join(dir, fmt.Sprintf("%02d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 })
			}
			var inFlight, peak atomic.Int64
			swapProbe(t, func(ctx context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
				n := inFlight.Add(1)
				defer inFlight.Add(-1)
				for {
					p := peak.Load()
					if n <= p || peak.CompareAndSwap(p, n) {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
				return real()
			})
			urApply(t, s, map[string]any{"workers": workers})
			require.LessOrEqual(t, peak.Load(), int64(workers))
			if workers > 1 {
				require.Equal(t, int64(workers), peak.Load(), "the pool should fill")
			}
		})
	}
}

// A book's aggregates are recomputed exactly once however many of its rows were
// measured (one borrowed, two probed here), and not at all for a book whose
// rows only failed.
func TestUnknownRows_RecomputesOncePerBookAndNeverForFailuresOnly(t *testing.T) {
	stubFFprobe(t, `case "$*" in *bad_*) exit 1;; esac
echo 30.0`)
	s := newRepairPebble(t)
	dir := t.TempDir()
	donorBook := urBook(t, s, "Batman: Year One", dir)
	mixed := urBook(t, s, "Batman: Hush", dir)
	failing := urBook(t, s, "Batman: The Killing Joke", dir)

	shared := filepath.Join(dir, "yo-01.mp3")
	urFile(t, s, donorBook.ID, shared, func(f *database.BookFile) { f.Duration = database.Secs(30) })
	urFile(t, s, mixed.ID, shared, nil)
	urFile(t, s, mixed.ID, filepath.Join(dir, "h-02.mp3"), func(f *database.BookFile) { f.TrackNumber = 2 })
	urFile(t, s, mixed.ID, filepath.Join(dir, "h-03.mp3"), func(f *database.BookFile) { f.TrackNumber = 3 })
	for i := 0; i < 5; i++ {
		urFile(t, s, failing.ID, filepath.Join(dir, fmt.Sprintf("bad_%d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 })
	}

	logs := aggtest.Capture(t)
	res := urApply(t, s, map[string]any{})

	require.Equal(t, 1, res.Borrowed)
	require.Equal(t, 2, res.ProbedKnown)
	require.Equal(t, 5, res.ProbeFailed)
	require.Equal(t, 1, res.BooksRecomputed)
	requireAccounted(t, res)
	require.Equal(t, 1, aggtest.CountInvocations(logs(), mixed.ID), "one recompute for the whole measured book")
	require.Equal(t, 0, aggtest.CountInvocations(logs(), failing.ID), "failure records move nothing an aggregate reads")
	require.Equal(t, 0, aggtest.CountInvocations(logs(), donorBook.ID))

	got, err := s.GetBookByID(mixed.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.Equal(t, 90, *got.Duration, "the recompute saw all three measured rows")
	files, err := s.GetBookFiles(failing.ID)
	require.NoError(t, err)
	for _, f := range files {
		require.Nil(t, f.Duration)
		require.Equal(t, 1, f.Scan.DurationOK.Attempts)
	}
}

// Copies and books/itunes rows are in scope. The iTunes file is only READ: its
// bytes and mtime are unchanged and the row is written.
func TestUnknownRows_IncludesCopiesAndFrozenITunesRows(t *testing.T) {
	runs := countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	orig := urFile(t, s, book.ID, filepath.Join(dir, "Hush.mp3"), func(f *database.BookFile) { f.FileHash = "same-bytes" })
	cp := urFile(t, s, book.ID, filepath.Join(dir, "Hush_copy1.mp3"), func(f *database.BookFile) { f.FileHash = "same-bytes"; f.TrackNumber = 2 })
	require.True(t, database.IsBookFileCopy(orig, cp), "fixture: the second row must be a copy for this test to prove anything")

	itunesDir := filepath.Join(t.TempDir(), "books", "itunes", "Batman Hush")
	itunesPath := filepath.Join(itunesDir, "01.m4b")
	require.True(t, pathutil.UnderFrozenITunesTree(itunesPath), "fixture: the path must be inside the frozen tree")
	itunesBook := urBook(t, s, "Batman: Year One", itunesDir)
	it := urFile(t, s, itunesBook.ID, itunesPath, nil)
	infoBefore, err := os.Stat(itunesPath)
	require.NoError(t, err)

	res := urApply(t, s, map[string]any{})

	require.Equal(t, 3, runs())
	require.Equal(t, 3, res.ProbedKnown)
	requireAccounted(t, res)
	require.Equal(t, 30, durtest.Sec(urRow(t, s, book.ID, cp.ID).Duration), "a copy is measured")
	require.Equal(t, 30, durtest.Sec(urRow(t, s, itunesBook.ID, it.ID).Duration), "an iTunes-tree row is measured")
	infoAfter, err := os.Stat(itunesPath)
	require.NoError(t, err)
	require.Equal(t, infoBefore.Size(), infoAfter.Size())
	require.True(t, infoBefore.ModTime().Equal(infoAfter.ModTime()), "the iTunes file must not be touched")
}

// A row that is Unreadable for a reason other than a timeout is left alone; a
// timeout-Unreadable row is probed again; a Missing row is not looked at.
func TestUnknownRows_UnreadableMissingAndTimeoutCeilings(t *testing.T) {
	runs := countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	bad := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), func(f *database.BookFile) {
		f.Scan.DurationOK = database.StageStatus{Attempts: database.DeepScanMaxAttempts, LastError: "exit status 1"}
	})
	slow := urFile(t, s, book.ID, filepath.Join(dir, "02.mp3"), func(f *database.BookFile) {
		f.TrackNumber = 2
		f.Scan.DurationOK = database.StageStatus{Attempts: database.DeepScanMaxAttempts,
			LastError: database.DurationProbeTimeoutPrefix + "ffprobe: deadline exceeded"}
	})
	missing := urFile(t, s, book.ID, filepath.Join(dir, "03.mp3"), func(f *database.BookFile) { f.TrackNumber = 3; f.Missing = true })
	require.Equal(t, database.DurationUnreadable, urRow(t, s, book.ID, bad.ID).DurationState())
	require.Equal(t, database.DurationUnreadable, urRow(t, s, book.ID, slow.ID).DurationState())

	res := urApply(t, s, map[string]any{})

	require.Equal(t, 1, runs(), "only the timeout-unreadable row is probed")
	require.Equal(t, 2, res.Scanned)
	require.Equal(t, 1, res.SkippedUnreadable)
	require.Equal(t, 1, res.ProbedKnown)
	require.Equal(t, 1, res.SkippedMissing)
	requireAccounted(t, res)
	require.Nil(t, urRow(t, s, book.ID, bad.ID).Duration)
	require.Equal(t, database.DeepScanMaxAttempts, urRow(t, s, book.ID, bad.ID).Scan.DurationOK.Attempts, "attempts do not climb past the ceiling")
	require.Equal(t, 30, durtest.Sec(urRow(t, s, book.ID, slow.ID).Duration))
	require.Nil(t, urRow(t, s, book.ID, missing.ID).Duration)
}

// A file failure is an attempt on the row; an environment failure records
// nothing. The two outcomes are told apart by the shared classifier.
func TestUnknownRows_FailureVersusEnvironment(t *testing.T) {
	t.Run("file failure records an attempt", func(t *testing.T) {
		stubFFprobe(t, "echo broken >&2; exit 1")
		s := newRepairPebble(t)
		dir := t.TempDir()
		book := urBook(t, s, "Batman: Hush", dir)
		f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
		logs := aggtest.Capture(t)
		res := urApply(t, s, map[string]any{})
		require.Equal(t, 1, res.ProbeFailed)
		requireAccounted(t, res)
		got := urRow(t, s, book.ID, f.ID)
		require.Equal(t, database.DurationProbeFailed, got.DurationState())
		require.Equal(t, 1, got.Scan.DurationOK.Attempts)
		require.NotEmpty(t, got.Scan.DurationOK.LastError)
		require.Zero(t, aggtest.CountInvocations(logs(), book.ID))
	})
	t.Run("ffprobe not installed records nothing", func(t *testing.T) {
		stubFFprobe(t, "")
		s := newRepairPebble(t)
		dir := t.TempDir()
		book := urBook(t, s, "Batman: Hush", dir)
		f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
		res := urApply(t, s, map[string]any{})
		require.Equal(t, 1, res.EnvironmentSkipped)
		require.Zero(t, res.ProbeFailed)
		requireAccounted(t, res)
		got := urRow(t, s, book.ID, f.ID)
		require.Equal(t, database.DurationUnprobed, got.DurationState())
		require.Zero(t, got.Scan.DurationOK.Attempts)
	})
	t.Run("absent file records nothing", func(t *testing.T) {
		stubFFprobe(t, "echo 30.0")
		s := newRepairPebble(t)
		dir := t.TempDir()
		book := urBook(t, s, "Batman: Hush", dir)
		f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
		require.NoError(t, os.Remove(f.FilePath))
		res := urApply(t, s, map[string]any{})
		require.Equal(t, 1, res.EnvironmentSkipped)
		requireAccounted(t, res)
		require.Zero(t, urRow(t, s, book.ID, f.ID).Scan.DurationOK.Attempts)
	})
}

// A row that changes while its probe runs is not written: moved, rewritten
// bytes, marked missing, measured since. The same holds for a failure record.
func TestUnknownRows_RowChangedDuringProbeIsSkipped(t *testing.T) {
	changes := map[string]func(bf *database.BookFile){
		"rewritten hash": func(bf *database.BookFile) { bf.FileHash = "hash-b" },
		"rewritten size": func(bf *database.BookFile) { bf.FileSize++ },
		"marked missing": func(bf *database.BookFile) { bf.Missing = true },
		"measured since": func(bf *database.BookFile) { _ = bf.SetMeasuredDuration(77) },
		"moved":          func(bf *database.BookFile) { bf.FilePath += ".moved" },
	}
	for _, outcome := range []string{"measured", "failed"} {
		for name, change := range changes {
			t.Run(outcome+"/"+name, func(t *testing.T) {
				script := "echo 30.0"
				if outcome == "failed" {
					script = "exit 1"
				}
				stubFFprobe(t, script)
				s := newRepairPebble(t)
				dir := t.TempDir()
				book := urBook(t, s, "Batman: Hush", dir)
				f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), func(f *database.BookFile) { f.FileHash = "hash-a" })
				swapProbe(t, func(ctx context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
					info, err := real()
					urModify(t, s, book.ID, f.ID, change) // the row changes after the probe read the file
					return info, err
				})

				res := urApply(t, s, map[string]any{})

				require.Equal(t, 1, res.SkippedChanged)
				require.Zero(t, res.ProbedKnown+res.ProbeFailed)
				requireAccounted(t, res)
				got := urRow(t, s, book.ID, f.ID)
				if name == "measured since" {
					require.Equal(t, 77, durtest.Sec(got.Duration), "someone else's measurement must stand")
				} else {
					require.Nil(t, got.Duration)
					require.Zero(t, got.Scan.DurationOK.Attempts, "a failure of different bytes must not be recorded here")
				}
			})
		}
	}
}

// A row that vanished (merged away) between the walk and the write is skipped.
func TestUnknownRows_RowDeletedDuringProbeIsSkipped(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
	swapProbe(t, func(ctx context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
		info, err := real()
		require.NoError(t, s.DeleteBookFile(f.ID))
		return info, err
	})
	res := urApply(t, s, map[string]any{})
	require.Equal(t, 1, res.SkippedChanged)
	require.Zero(t, res.WriteErrors)
}

// fileIds / bookIds narrow the walk (together: intersection); a stale fileId is
// counted, not fatal; a narrowing with nothing in it writes nothing.
func TestUnknownRows_NarrowingByFileAndBookIDs(t *testing.T) {
	s := newRepairPebble(t)
	dir := t.TempDir()
	a := urBook(t, s, "Batman: Hush", dir)
	b := urBook(t, s, "Batman: Year One", dir)
	var af, bf []database.BookFile
	for i := 0; i < 3; i++ {
		af = append(af, urFile(t, s, a.ID, filepath.Join(dir, fmt.Sprintf("a%d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 }))
		bf = append(bf, urFile(t, s, b.ID, filepath.Join(dir, fmt.Sprintf("b%d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 }))
	}
	measured := func() (n int) {
		for _, set := range [][]database.BookFile{af, bf} {
			for _, f := range set {
				if urRow(t, s, f.BookID, f.ID).Duration != nil {
					n++
				}
			}
		}
		return n
	}
	reset := func() {
		for _, set := range [][]database.BookFile{af, bf} {
			for _, f := range set {
				urModify(t, s, f.BookID, f.ID, func(bf *database.BookFile) { bf.ResetDuration() })
			}
		}
	}

	cases := []struct {
		name     string
		params   map[string]any
		scanned  int
		notFound int
		wantA    []int // indexes into af measured
		wantB    []int
	}{
		{"fileIds", map[string]any{"fileIds": []string{af[0].ID, bf[2].ID}}, 2, 0, []int{0}, []int{2}},
		{"file_ids", map[string]any{"file_ids": []string{af[1].ID}}, 1, 0, []int{1}, nil},
		{"bookIds", map[string]any{"bookIds": []string{b.ID}}, 3, 0, nil, []int{0, 1, 2}},
		{"book_ids", map[string]any{"book_ids": []string{a.ID}}, 3, 0, []int{0, 1, 2}, nil},
		{"intersection", map[string]any{"fileIds": []string{af[0].ID, bf[0].ID}, "bookIds": []string{b.ID}}, 1, 0, nil, []int{0}},
		{"stale fileId", map[string]any{"fileIds": []string{af[2].ID, "no-such-row"}}, 1, 1, []int{2}, nil},
		{"only stale", map[string]any{"fileIds": []string{"no-such-row"}}, 0, 1, nil, nil},
		{"unknown book", map[string]any{"bookIds": []string{"no-such-book"}}, 0, 0, nil, nil},
		{"no narrowing", map[string]any{}, 6, 0, []int{0, 1, 2}, []int{0, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubFFprobe(t, "echo 30.0")
			reset()
			res := urApply(t, s, tc.params)
			require.Equal(t, tc.scanned, res.Scanned)
			require.Equal(t, tc.notFound, res.NotFound)
			require.Equal(t, tc.scanned, measured())
			for _, i := range tc.wantA {
				require.NotNil(t, urRow(t, s, a.ID, af[i].ID).Duration, "a%d", i)
			}
			for _, i := range tc.wantB {
				require.NotNil(t, urRow(t, s, b.ID, bf[i].ID).Duration, "b%d", i)
			}
		})
	}
}

func TestUnknownRows_LimitCapsTheRowsExamined(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	for i := 0; i < 5; i++ {
		urFile(t, s, book.ID, filepath.Join(dir, fmt.Sprintf("%02d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 })
	}
	res := urApply(t, s, map[string]any{"limit": 2})
	require.Equal(t, 2, res.Scanned)
	require.Equal(t, 2, res.ProbedKnown)
}

// Params that would be silently misread are errors, and write nothing.
func TestUnknownRows_ParamValidation(t *testing.T) {
	countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	f := urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
	for name, params := range map[string]map[string]any{
		"unknown mode":          {"mode": "unknownrows", "dryRun": false},
		"fileIds without mode":  {"mode": "", "fileIds": []string{f.ID}, "dryRun": false},
		"file_ids without mode": {"mode": "", "file_ids": []string{f.ID}, "dryRun": false},
		"with zeroRowsOnly":     {"mode": durationModeUnknownRows, "zeroRowsOnly": true, "dryRun": false},
		"with zero_rows_only":   {"mode": durationModeUnknownRows, "zero_rows_only": true, "dryRun": false},
	} {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(params)
			require.NoError(t, err)
			err = New(fakeDeps{store: s}).runDurationBackfill(context.Background(), raw, &urReporter{})
			require.Error(t, err)
			require.Nil(t, urRow(t, s, book.ID, f.ID).Duration)
		})
	}
}

// A cancelled run still brings the aggregates of the rows it wrote up to date,
// and says it was cancelled.
func TestUnknownRows_CancelledRunStillRecomputesWrittenBooks(t *testing.T) {
	stubFFprobe(t, "echo 30.0")
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	for i := 0; i < 3; i++ {
		urFile(t, s, book.ID, filepath.Join(dir, fmt.Sprintf("%02d.mp3", i)), func(f *database.BookFile) { f.TrackNumber = i + 1 })
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	swapProbe(t, func(c context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
		info, err := real()
		if calls.Add(1) == 2 {
			cancel() // the second probe returns into a cancelled run
		}
		return info, err
	})
	logs := aggtest.Capture(t)

	res, err := urRunCtx(t, ctx, s, map[string]any{"dryRun": false, "workers": 1})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, res.ProbedKnown, "the row measured before the cancel is written")
	require.Equal(t, 1, aggtest.CountInvocations(logs(), book.ID), "its book is recomputed on the way out")
	got, err := s.GetBookByID(book.ID)
	require.NoError(t, err)
	require.NotNil(t, got.Duration)
	require.Equal(t, 30, *got.Duration)
}

// The op is on demand only.
func TestUnknownRows_OpIsNotScheduled(t *testing.T) {
	def := New(fakeDeps{}).durationBackfillDef()
	require.Nil(t, def.Schedule, "unknownRows is on demand only; the op must carry no schedule")
}

// urSamePath creates one unknown row per hash at ONE path, each in its own book
// (the megarow shape: many rows claiming one file).
func urSamePath(t *testing.T, s *database.PebbleStore, hashes ...string) (rows []database.BookFile) {
	t.Helper()
	dir := t.TempDir()
	shared := filepath.Join(dir, "01.mp3")
	for i, h := range hashes {
		b := urBook(t, s, fmt.Sprintf("Batman: Issue %d", i+1), dir)
		rows = append(rows, urFile(t, s, b.ID, shared, func(f *database.BookFile) { f.FileHash = h }))
	}
	return rows
}

// Rows that are the same file (same path, size, hash) share ONE probe, and the
// outcome is applied to each of them; counts stay per row.
func TestUnknownRows_SameFileRowsShareOneProbe(t *testing.T) {
	t.Run("measured", func(t *testing.T) {
		runs := countingStub(t, "echo 30.0")
		s := newRepairPebble(t)
		rows := urSamePath(t, s, "", "", "")
		res := urApply(t, s, map[string]any{})
		require.Equal(t, 1, runs(), "three rows at one path are one file: one ffprobe run")
		require.Equal(t, 1, res.ProbesRun)
		require.Equal(t, 3, res.Scanned)
		require.Equal(t, 3, res.ProbedKnown)
		requireAccounted(t, res)
		for _, r := range rows {
			require.Equal(t, 30, durtest.Sec(urRow(t, s, r.BookID, r.ID).Duration))
		}
	})
	t.Run("failed", func(t *testing.T) {
		runs := countingStub(t, "exit 1")
		s := newRepairPebble(t)
		rows := urSamePath(t, s, "h1", "h1", "h1")
		res := urApply(t, s, map[string]any{})
		require.Equal(t, 1, runs())
		require.Equal(t, 1, res.ProbesRun)
		require.Equal(t, 3, res.ProbeFailed)
		requireAccounted(t, res)
		for _, r := range rows {
			require.Equal(t, 1, urRow(t, s, r.BookID, r.ID).Scan.DurationOK.Attempts, "each row records the one failure once")
		}
	})
	t.Run("environment", func(t *testing.T) {
		stubFFprobe(t, "")
		s := newRepairPebble(t)
		urSamePath(t, s, "", "")
		res := urApply(t, s, map[string]any{})
		require.Equal(t, 1, res.ProbesRun)
		require.Equal(t, 2, res.EnvironmentSkipped)
		requireAccounted(t, res)
	})
}

// Differing known hashes are never one group, and an unknown hash groups only
// with unknown hashes.
func TestUnknownRows_DifferentHashesAreProbedSeparately(t *testing.T) {
	cases := map[string]struct {
		hashes []string
		probes int
	}{
		"two known hashes":                 {[]string{"h1", "h1", "h2"}, 2},
		"unknown never joins a known hash": {[]string{"", "h1", "h1"}, 2},
		"all different":                    {[]string{"h1", "h2", ""}, 3},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runs := countingStub(t, "echo 30.0")
			s := newRepairPebble(t)
			rows := urSamePath(t, s, tc.hashes...)
			res := urApply(t, s, map[string]any{})
			require.Equal(t, tc.probes, runs())
			require.Equal(t, tc.probes, res.ProbesRun)
			require.Equal(t, len(rows), res.ProbedKnown, "every row still gets the result")
			requireAccounted(t, res)
		})
	}
}

// A row that changes while the shared probe runs is skipped; its siblings are
// still written.
func TestUnknownRows_ChangedRowInAGroupDoesNotBlockItsSiblings(t *testing.T) {
	runs := countingStub(t, "echo 30.0")
	s := newRepairPebble(t)
	rows := urSamePath(t, s, "h1", "h1", "h1")
	swapProbe(t, func(ctx context.Context, path string, real func() (*mediainfo.MediaInfo, error)) (*mediainfo.MediaInfo, error) {
		info, err := real()
		urModify(t, s, rows[1].BookID, rows[1].ID, func(bf *database.BookFile) { bf.FileHash = "rewritten" })
		return info, err
	})

	res := urApply(t, s, map[string]any{})

	require.Equal(t, 1, runs())
	require.Equal(t, 1, res.ProbesRun)
	require.Equal(t, 2, res.ProbedKnown)
	require.Equal(t, 1, res.SkippedChanged)
	requireAccounted(t, res)
	require.Equal(t, 30, durtest.Sec(urRow(t, s, rows[0].BookID, rows[0].ID).Duration))
	require.Nil(t, urRow(t, s, rows[1].BookID, rows[1].ID).Duration, "the changed row is not written")
	require.Equal(t, 30, durtest.Sec(urRow(t, s, rows[2].BookID, rows[2].ID).Duration))
}
