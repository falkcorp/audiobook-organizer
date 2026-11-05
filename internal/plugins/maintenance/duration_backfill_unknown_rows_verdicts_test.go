// file: internal/plugins/maintenance/duration_backfill_unknown_rows_verdicts_test.go
// version: 1.0.0
// guid: 6c557639-7962-4db8-ae44-9f66c5954f1d
// last-edited: 2026-11-05

package maintenance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// unknownRowActionable is the one rule the backfill's walk and the folder-books
// holds share: each verdict declines with a Why that names its remedy.
func TestUnknownRowActionable_EachVerdict(t *testing.T) {
	ok := func(mut func(*database.BookFileCore)) database.BookFileCore {
		r := database.BookFileCore{ID: "r", BookID: "b", FilePath: "/lib/Batman/Hush.mp3", FileSize: 10}
		if mut != nil {
			mut(&r)
		}
		return r
	}
	regular := func(size int64) *unknownRowDisk { return &unknownRowDisk{Size: size, Regular: true} }
	cases := []struct {
		name string
		row  database.BookFileCore
		disk *unknownRowDisk
		want unknownRowSettled
		why  string
	}{
		{"unprobed and plain is acted on", ok(nil), regular(10), unknownRowActs, ""},
		{"not looked at is acted on", ok(nil), nil, unknownRowActs, ""},
		{"flagged missing", ok(func(r *database.BookFileCore) { r.Missing = true }), regular(10), unknownRowMissing, "library scan refreshes the row"},
		{"no path", ok(func(r *database.BookFileCore) { r.FilePath = "" }), nil, unknownRowNoPath, "no file path"},
		{"excluded from scanning", ok(func(r *database.BookFileCore) { r.SkipScan = true }), nil, unknownRowUserExcluded, "excluded from scanning"},
		{"relative path", ok(func(r *database.BookFileCore) { r.FilePath = "Batman/Hush.mp3" }), nil, unknownRowBadPath, "not an absolute, clean path"},
		{"path with a dot-dot", ok(func(r *database.BookFileCore) { r.FilePath = "/lib/Batman/../Hush.mp3" }), nil, unknownRowBadPath, "not an absolute, clean path"},
		{"extension the probe does not read", ok(func(r *database.BookFileCore) { r.FilePath = "/lib/Batman/Hush.txt" }), nil, unknownRowNotAudio, `".txt" is not one the duration probe reads`},
		{"no extension", ok(func(r *database.BookFileCore) { r.FilePath = "/lib/Batman/Hush" }), nil, unknownRowNotAudio, `"" is not one`},
		{"an upper-case audio extension is audio", ok(func(r *database.BookFileCore) { r.FilePath = "/lib/Batman/Hush.M4B" }), nil, unknownRowActs, ""},
		{"not a regular file", ok(nil), &unknownRowDisk{Size: 10, Regular: false}, unknownRowNotRegular, "not a regular file"},
		{"size changed", ok(nil), regular(25), unknownRowChanged, "25 bytes on disk but the row records 10"},
		{"a row that records no size cannot have changed", ok(func(r *database.BookFileCore) { r.FileSize = 0 }), regular(25), unknownRowActs, ""},
		{"a fingerprint duration is promoted whatever the disk says",
			ok(func(r *database.BookFileCore) { r.AcoustIDFingerprintDurationSec = 30; r.FileSize = 480_000 }), regular(25), unknownRowActs, ""},
		{"a fingerprint duration is promoted whatever the extension",
			ok(func(r *database.BookFileCore) {
				r.AcoustIDFingerprintDurationSec = 30
				r.FilePath = "/lib/Batman/Hush.txt"
				r.FileSize = 480_000
			}),
			nil, unknownRowActs, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, unknownRowVerdict(c.row, c.disk))
			acts, why := unknownRowActionable(c.row, c.disk)
			require.Equal(t, c.want == unknownRowActs, acts)
			if acts {
				require.Empty(t, why)
				return
			}
			require.Contains(t, why, c.why)
		})
	}
}

// Rows the probe pass would decline for what their path or file shows are
// counted under their own reason, never as environment skips, and none starts a
// probe. A good row beside them is still measured.
func TestUnknownRows_DeclinedRowsAreCountedByTheirOwnReason(t *testing.T) {
	calls := stubUnknownRowsProbe(t)
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	urFile(t, s, book.ID, filepath.Join(dir, "01.mp3"), nil)
	urFile(t, s, book.ID, filepath.Join(dir, "notes.txt"), nil)
	// A directory where a file should be: stat succeeds, it is not a file.
	asDir := filepath.Join(dir, "03.mp3")
	require.NoError(t, os.Mkdir(asDir, 0o755))
	require.NoError(t, s.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: asDir, FileSize: urSize, TrackNumber: 3}))
	// A path that is not clean: the file is really there, the backfill refuses the spelling.
	unclean := filepath.Join(dir, "sub") + string(filepath.Separator) + ".." + string(filepath.Separator) + "04.mp3"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "04.mp3"), make([]byte, urSize), 0o600))
	require.NoError(t, s.CreateBookFile(&database.BookFile{BookID: book.ID, FilePath: unclean, FileSize: urSize, TrackNumber: 4}))

	dry, err := urRun(t, s, map[string]any{})
	require.NoError(t, err)
	require.Equal(t, 4, dry.Scanned)
	require.Equal(t, 1, dry.SkippedNotAudio)
	require.Equal(t, 1, dry.SkippedBadPath)
	require.Equal(t, 2, dry.WouldProbe, "a dry run does not touch the disk: the directory still reads as a probe")
	require.Len(t, dry.Books, 1)
	require.Equal(t, 1, dry.Books[0].SkippedNotAudio)
	require.Equal(t, 1, dry.Books[0].SkippedBadPath)
	require.Equal(t, 2, dry.Books[0].WouldProbe)
	require.EqualValues(t, 0, calls.Load())

	res, err := urRun(t, s, map[string]any{"dryRun": false})
	require.NoError(t, err)
	require.Equal(t, 4, res.Scanned)
	require.Equal(t, 1, res.ProbedKnown)
	require.Equal(t, 1, res.SkippedNotAudio)
	require.Equal(t, 1, res.SkippedBadPath)
	require.Equal(t, 1, res.SkippedNotRegular)
	require.Zero(t, res.EnvironmentSkipped, "each reason has its own counter, none is folded into environment skips")
	require.EqualValues(t, 1, res.ProbesRun)
	require.EqualValues(t, 1, calls.Load(), "only the good file is probed")
	require.Equal(t, res.Scanned, res.Borrowed+res.PromotedFromFingerprint+res.ProbedKnown+res.ProbeFailed+res.EnvironmentSkipped+
		res.SkippedChanged+res.SkippedUnreadable+res.SkippedNoPath+res.SkippedUserExcluded+res.SkippedBadPath+res.SkippedNotAudio+
		res.SkippedNotRegular+res.WriteErrors)
	require.True(t, strings.Contains(unknownRowsSummary(res), "skipped_not_regular=1"))
}

// The path and extension verdicts are for a row that needs a PROBE: a sibling's
// duration is still borrowed whatever the extension, and a fingerprint duration
// is still promoted.
func TestUnknownRows_NotAudioRowIsStillBorrowedAndPromoted(t *testing.T) {
	stubUnknownRowsProbe(t)
	s := newRepairPebble(t)
	dir := t.TempDir()
	book := urBook(t, s, "Batman: Hush", dir)
	other := urBook(t, s, "Batman: Year One", dir)
	txt := filepath.Join(dir, "notes.txt")
	urFile(t, s, other.ID, txt, func(f *database.BookFile) { f.Duration = database.Secs(100) })
	borrower := urFile(t, s, book.ID, txt, nil)
	promoted := urFile(t, s, book.ID, filepath.Join(dir, "cover.jpg"), func(f *database.BookFile) {
		f.AcoustIDFingerprintDurationSec = 30
	})

	res, err := urRun(t, s, map[string]any{"dryRun": false})
	require.NoError(t, err)
	require.Equal(t, 1, res.Borrowed)
	require.Equal(t, 1, res.PromotedFromFingerprint)
	require.Zero(t, res.SkippedNotAudio)
	sec, known := urRow(t, s, book.ID, borrower.ID).DurationSec()
	require.True(t, known)
	require.Equal(t, 100, sec)
	sec, known = urRow(t, s, book.ID, promoted.ID).DurationSec()
	require.True(t, known)
	require.Equal(t, 30, sec)
}
