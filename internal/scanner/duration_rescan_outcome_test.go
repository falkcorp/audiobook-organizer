// file: internal/scanner/duration_rescan_outcome_test.go
// version: 1.0.0
// guid: 3a0c6d3e-6b0a-4f0e-9a58-0f6c2c1d7e41
// last-edited: 2026-11-04

package scanner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/testutil/durtest"
)

// countingFFprobe is a stub ffprobe that appends a line to a counter file on
// every run and then behaves as script says. runs() is how many times the
// scanner actually probed.
func countingFFprobe(t *testing.T, script string) (runs func() int) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "runs")
	require.NoError(t, os.WriteFile(counter, nil, 0o600))
	stubFFprobeOnPath(t, "echo run >> '"+counter+"'\n"+script)
	return func() int {
		b, err := os.ReadFile(counter)
		require.NoError(t, err)
		return strings.Count(string(b), "run")
	}
}

// takeoverFixture is the shape in which the scanner's create loop meets a stored
// row for the path it is building: a row owned by ANOTHER book sits at the
// file's path, and a book with no rows of its own is scanned over it. The upsert
// merges into the stored row (the stored BookID wins), so the shell book stays
// row-less and every rescan walks the same loop again.
type takeoverFixture struct {
	store    database.Store
	owner    *database.Book // owns the stored row
	shell    *database.Book // the book being scanned; no rows
	file     string
	dir      string
	rowID    string
	hashes   map[string]string
	segments []string
}

func newTakeoverFixture(t *testing.T, stored database.BookFile) *takeoverFixture {
	t.Helper()
	store, cleanup := setupPebbleStore(t)
	t.Cleanup(cleanup)
	SetStore(store)
	t.Cleanup(func() { SetStore(nil) })

	dir := t.TempDir()
	file := filepath.Join(dir, "Part 01.mp3")
	require.NoError(t, os.WriteFile(file, make([]byte, 480_000), 0o600))

	owner, err := store.CreateBook(&database.Book{Title: "Batman: Year One", FilePath: filepath.Join(dir, "elsewhere")})
	require.NoError(t, err)
	shell, err := store.CreateBook(&database.Book{Title: "Batman: Year One (rescan)", FilePath: dir})
	require.NoError(t, err)

	stored.BookID, stored.FilePath = owner.ID, file
	if stored.FileHash == "" {
		stored.FileHash = "hash-a"
	}
	require.NoError(t, store.CreateBookFile(&stored))
	rows, err := store.GetBookFiles(owner.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return &takeoverFixture{
		store: store, owner: owner, shell: shell, file: file, dir: dir, rowID: rows[0].ID,
		hashes: map[string]string{file: "hash-a"}, segments: []string{file},
	}
}

func (f *takeoverFixture) rescan(t *testing.T) {
	t.Helper()
	createBookFilesForBook(f.dir, f.segments, logger.New("test"), normalizeToDirectory, f.hashes)
}

func (f *takeoverFixture) row(t *testing.T) database.BookFile {
	t.Helper()
	rows, err := f.store.GetBookFiles(f.owner.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}

// S6(a): a file whose bytes never change and whose probe keeps failing is
// rescanned through createBookFilesForBookWithAudio; each scan adds ONE attempt
// to the stored count and the third makes it unreadable. The merge never sums
// attempts, so what carries the count is seedDurationStage (mutation check:
// with it a no-op every scan writes Attempts=1 and the test stays at ProbeFailed).
// Then N4: at the ceiling the file is no longer probed and Attempts stays 3.
func TestScannerRescanOfSameFailingBytesReachesUnreadableAndStops(t *testing.T) {
	runs := countingFFprobe(t, "exit 1")
	f := newTakeoverFixture(t, database.BookFile{})

	for want := 1; want <= database.DeepScanMaxAttempts; want++ {
		f.rescan(t)
		row := f.row(t)
		require.Equal(t, want, row.Scan.DurationOK.Attempts, "scan %d", want)
		require.Nil(t, row.Duration)
		require.Equal(t, want, runs(), "one probe per scan")
	}
	row := f.row(t)
	require.Equal(t, database.DurationUnreadable, row.DurationState())

	// Past the ceiling: no more probes, no more attempts, until ResetDuration.
	for i := 0; i < 3; i++ {
		f.rescan(t)
	}
	require.Equal(t, database.DeepScanMaxAttempts, runs(), "an unreadable row must not be probed again")
	row = f.row(t)
	require.Equal(t, database.DeepScanMaxAttempts, row.Scan.DurationOK.Attempts, "Attempts climbed past the ceiling")
	require.Equal(t, database.DurationUnreadable, row.DurationState())
	require.NotEmpty(t, row.Scan.DurationOK.LastError)
}

// An explicit reset puts an unreadable row back in play: the scan probes it again.
func TestScannerRescanProbesAgainAfterExplicitReset(t *testing.T) {
	runs := countingFFprobe(t, "echo 30.0") // plausible for the 480 kB fixture; the write chokepoint repairs ms-looking values
	f := newTakeoverFixture(t, database.BookFile{
		Scan: database.ScanState{DurationOK: database.StageStatus{Attempts: database.DeepScanMaxAttempts, LastError: "exit status 1"}},
	})
	f.rescan(t)
	require.Equal(t, 0, runs(), "unreadable: not probed")
	require.Equal(t, database.DurationUnreadable, f.row(t).DurationState())

	_, err := f.store.ModifyBookFile(f.owner.ID, f.rowID, func(bf *database.BookFile) error {
		bf.ResetDuration()
		return nil
	})
	require.NoError(t, err)
	f.rescan(t)
	require.Equal(t, 1, runs())
	row := f.row(t)
	require.Equal(t, 30, durtest.Sec(row.Duration))
	require.Equal(t, database.StageStatus{Done: true}, row.Scan.DurationOK)
}

// S2: the stored row already has a duration and the hashes are not known to
// differ (equal, or one side unknown): the merge keeps the stored value, so the
// scan must not probe, and no failure may be left beside the kept value.
func TestScannerRescanSkipsProbeWhenStoredRowIsKnown(t *testing.T) {
	cases := map[string]struct {
		storedHash string // "-": the stored row has no hash
		scanHash   string
	}{
		"same hash":           {"hash-a", "hash-a"},
		"stored hash unknown": {"-", "hash-a"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runs := countingFFprobe(t, "exit 1")
			stored := database.BookFile{Duration: database.Secs(5400), Scan: database.ScanState{DurationOK: database.StageStatus{Done: true}}}
			if tc.storedHash != "-" {
				stored.FileHash = tc.storedHash
			}
			f := newTakeoverFixtureNoHash(t, stored, tc.storedHash == "-")
			f.hashes = map[string]string{f.file: tc.scanHash}
			f.rescan(t)

			require.Equal(t, 0, runs(), "the stored duration is kept by the merge; probing is wasted work")
			row := f.row(t)
			require.Equal(t, 5400, durtest.Sec(row.Duration))
			require.Equal(t, database.StageStatus{Done: true}, row.Scan.DurationOK)
			require.Equal(t, database.DurationKnown, row.DurationState())
		})
	}
}

// newTakeoverFixtureNoHash is newTakeoverFixture, except the stored row may have
// no hash at all (newTakeoverFixture gives it "hash-a" by default).
func newTakeoverFixtureNoHash(t *testing.T, stored database.BookFile, noHash bool) *takeoverFixture {
	t.Helper()
	f := newTakeoverFixture(t, stored)
	if noHash {
		_, err := f.store.ModifyBookFile(f.owner.ID, f.rowID, func(bf *database.BookFile) error {
			bf.FileHash = ""
			return nil
		})
		require.NoError(t, err)
	}
	return f
}

// The guard against the opposite mistake: a REPLACED file (hashes known and
// unequal) is still probed even though the stored row was Known or Unreadable,
// and starts its record from zero.
func TestScannerRescanOfReplacedFileIsProbedFromZero(t *testing.T) {
	for name, stage := range map[string]database.BookFile{
		"stored known": {Duration: database.Secs(5400), Scan: database.ScanState{DurationOK: database.StageStatus{Done: true}}},
		"stored unreadable": {Scan: database.ScanState{DurationOK: database.StageStatus{
			Attempts: database.DeepScanMaxAttempts, LastError: "exit status 1"}}},
	} {
		t.Run(name, func(t *testing.T) {
			runs := countingFFprobe(t, "exit 1")
			f := newTakeoverFixture(t, stage)
			f.hashes = map[string]string{f.file: "hash-b"} // different bytes
			f.rescan(t)
			require.Equal(t, 1, runs())
			row := f.row(t)
			require.Nil(t, row.Duration)
			require.Equal(t, 1, row.Scan.DurationOK.Attempts, "the replacement's record starts over")
		})
	}
}
