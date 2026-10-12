// file: internal/plugins/maintenance/fragment_window_containment_test.go
// version: 1.0.0
// guid: 8f3c6a20-5d1e-4b97-a2c4-0e9b7d1f5a63
// last-edited: 2026-10-11

package maintenance

import (
	"encoding/binary"
	"math/rand/v2"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// Window containment as fragment evidence (02-PR8). Synthetic random-walk
// prints and synthetic paths only.

const containWinFrames = 969

func containWalk(seed uint64, n int) []uint32 {
	rng := rand.New(rand.NewPCG(seed, seed^0x2545f491))
	out := make([]uint32, n)
	out[0] = rng.Uint32()
	for i := 1; i < n; i++ {
		var mask uint32
		for bit := 0; bit < 32; bit++ {
			if rng.Float64() < 0.15 {
				mask |= 1 << bit
			}
		}
		out[i] = out[i-1] ^ mask
	}
	return out
}

func containRaw(frames []uint32) []byte {
	out := make([]byte, 4*len(frames))
	for i, f := range frames {
		binary.LittleEndian.PutUint32(out[i*4:], f)
	}
	return out
}

// putWindow stores one ws1 window of frames for the book_file row fileID.
func putContainWindow(t *testing.T, s *database.PebbleStore, fileID string, slot int, frames []uint32) {
	t.Helper()
	require.NoError(t, s.PutFingerprintWindow(&database.FingerprintWindow{
		Ref: database.FileWindowRef(fileID), Kind: database.WindowKindWindow, SlotBP: slot,
		WindowSet: fingerprint.WindowSetWS1, LengthSec: float64(len(frames)) / fingerprint.FramesPerSec,
		DurationUsedSec: 600, DurationSource: "book_file", Frames: len(frames), Raw: containRaw(frames),
		Algorithm: 2, Pipeline: fingerprint.WindowPipelineID, FpcalcVersion: "1.6.0", FFmpegVersion: "8.0.1",
		ComputedAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC),
	}))
}

var containLine = regexp.MustCompile(fragEvWindowContainment + ` ([0-9.]+) of the fragment's bands`)

// containmentOf returns the containment value of the row's window line.
func containmentOf(t *testing.T, r repairs.Row) float64 {
	t.Helper()
	for _, e := range r.Evidence {
		if m := containLine.FindStringSubmatch(e); m != nil {
			v, err := strconv.ParseFloat(m[1], 64)
			require.NoError(t, err)
			return v
		}
	}
	t.Fatalf("row %s has no window containment line: %q", r.RowID, r.Evidence)
	return 0
}

// seedUnprovenMoved is TestFragmentFixer_MovedNameSizeOnlyIsUnproven's shape:
// the parent's row 02 is gone from disk and a fragment elsewhere matches it
// by original name and size only (no import path, no hash).
func seedUnprovenMoved(t *testing.T, f *fragFixture) {
	t.Helper()
	x1 := f.file(t, "lib/X/01.mp3", 101)
	parent := f.book(t, "parent", "Title 0001", f.path("lib/X"), nil)
	f.row(t, "p01", parent, x1, "01.mp3", 101, 600, 1)
	f.row(t, "p02", parent, f.path("lib/X/02.mp3"), "02.mp3", 102, 600, 2)
	imp := f.file(t, "lib/Elsewhere/02.mp3", 102)
	frag := f.book(t, "frag", "02", imp, nil)
	f.row(t, "f02", frag, imp, "02.mp3", 102, 600, 0)
	f.organize(t, frag, imp, f.path("lib/Y/02/02.mp3"))
}

func TestFragmentFixer_WindowContainmentIsEvidenceOnly(t *testing.T) {
	t.Parallel()
	f := newFragFixture(t)
	seedUnprovenMoved(t, f)
	before := findRow(t, f.plan(t, "op-plan-0"), fragRowMovedUnproven+":"+f.ids["parent"])

	// The fragment's window is a slice of the parent row's window: strong
	// containment, and still no hash or import-path evidence.
	audio := containWalk(1, containWinFrames)
	putContainWindow(t, f.s, f.rowIDs["p02"], 5000, audio)
	putContainWindow(t, f.s, f.rowIDs["f02"], 0, audio[containWinFrames/20:])

	res := f.plan(t, "op-plan")
	r := findRow(t, res, fragRowMovedUnproven+":"+f.ids["parent"])
	c := containmentOf(t, r)
	t.Logf("evidence-only fragment: containment %.2f, applicable=%v, skipped=%q", c, r.Applicable(), r.Skipped)
	require.GreaterOrEqual(t, c, 0.8)
	require.Equal(t, fragSkipMovedUnproven, r.Skipped, "containment never proves a match")
	require.False(t, r.Applicable())
	require.Equal(t, "1 supporting, 0 weak", r.Current["window_containment"])
	require.Equal(t, before.Fingerprint, r.Fingerprint, "the containment line is not part of the row's decision")
	require.Equal(t, before.Evidence, r.Evidence[:len(before.Evidence)], "existing evidence lines keep their place; the containment line is appended")

	out := f.apply(t, "op-plan", "op-apply", []string{r.RowID}, nil)
	require.Zero(t, out.Applied)
	require.Equal(t, f.path("lib/X/02.mp3"), f.fileRow(t, "parent", "p02").FilePath, "not repointed")
}

func TestFragmentFixer_ProvenMatchUnchanged(t *testing.T) {
	t.Parallel()
	// The proof table is exactly what it was: containment text proves nothing
	// in any row kind, and every existing evidence kind keeps its answer.
	kinds := []string{fragClassMoved, fragClassCopy, fragClassGhost, fragRowMovedUnproven, fragRowCopyUnproven}
	want := map[string]map[string]bool{
		fragEvImportPath:     {fragClassMoved: true, fragClassCopy: true, fragClassGhost: true, fragRowMovedUnproven: true, fragRowCopyUnproven: true},
		fragEvHash:           {fragClassMoved: true, fragClassCopy: true, fragClassGhost: true, fragRowMovedUnproven: true, fragRowCopyUnproven: true},
		fragEvDone:           {fragClassMoved: true, fragClassCopy: true, fragClassGhost: true, fragRowMovedUnproven: true, fragRowCopyUnproven: true},
		fragEvNameSizeFolder: {fragClassMoved: true},
		fragEvNameSize:       {},
		fragEvWindowContainment + " 0.97 of the fragment's bands appear in the parent": {},
	}
	for ev, proven := range want {
		for _, k := range kinds {
			require.Equal(t, proven[k], provenMatch(k, ev), "provenMatch(%q, %q)", k, ev)
		}
	}

	// A proven row stays proven, with the same fingerprint, once both files
	// carry windows.
	f := newFragFixture(t)
	f.seed(t)
	rowID := "moved:" + f.ids["parent"]
	before := findRow(t, f.plan(t, "op-plan-0"), rowID)
	require.True(t, before.Applicable(), before.SkipReason)
	audio := containWalk(2, containWinFrames)
	putContainWindow(t, f.s, f.rowIDs["p03"], 5000, audio)
	putContainWindow(t, f.s, f.rowIDs["f03"], 0, audio)
	after := findRow(t, f.plan(t, "op-plan-1"), rowID)
	require.True(t, after.Applicable(), after.SkipReason)
	require.Equal(t, before.Skipped, after.Skipped)
	require.Equal(t, before.Fingerprint, after.Fingerprint)
	require.InDelta(t, 1.0, containmentOf(t, after), 1e-9)
	require.Contains(t, after.Evidence[0], fragEvImportPath, "the proof line stays first")
}

func TestFragmentFixer_SlicedParentShowsContainment(t *testing.T) {
	t.Parallel()
	t.Run("sliced", func(t *testing.T) {
		f := newFragFixture(t)
		seedUnprovenMoved(t, f)
		audio := containWalk(3, containWinFrames)
		putContainWindow(t, f.s, f.rowIDs["p02"], 5000, audio)
		// The fragment covers 95 percent of the parent's window, starting
		// six seconds in: a slice of the parent's frames.
		start := containWinFrames / 20
		putContainWindow(t, f.s, f.rowIDs["f02"], 0, audio[start:])
		r := findRow(t, f.plan(t, "op-plan"), fragRowMovedUnproven+":"+f.ids["parent"])
		c := containmentOf(t, r)
		t.Logf("sliced fragment (frames %d..%d of %d): containment %.2f", start, containWinFrames, containWinFrames, c)
		require.GreaterOrEqual(t, c, 0.8)
		require.True(t, strings.Contains(strings.Join(r.Evidence, "\n"), "(supporting, threshold 0.50"))
	})
	t.Run("unrelated", func(t *testing.T) {
		f := newFragFixture(t)
		seedUnprovenMoved(t, f)
		putContainWindow(t, f.s, f.rowIDs["p02"], 5000, containWalk(4, containWinFrames))
		putContainWindow(t, f.s, f.rowIDs["f02"], 0, containWalk(5, containWinFrames))
		r := findRow(t, f.plan(t, "op-plan"), fragRowMovedUnproven+":"+f.ids["parent"])
		c := containmentOf(t, r)
		t.Logf("unrelated fragment: containment %.2f", c)
		require.LessOrEqual(t, c, 0.05)
		require.Equal(t, "0 supporting, 1 weak", r.Current["window_containment"])
	})
	t.Run("no member lists, no line", func(t *testing.T) {
		f := newFragFixture(t)
		seedUnprovenMoved(t, f)
		r := findRow(t, f.plan(t, "op-plan"), fragRowMovedUnproven+":"+f.ids["parent"])
		for _, e := range r.Evidence {
			require.NotContains(t, e, fragEvWindowContainment)
		}
		require.NotContains(t, r.Current, "window_containment")
	})
}
