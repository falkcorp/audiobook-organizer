// file: internal/dedup/collectors_window_test.go
// version: 1.0.0
// guid: 2d6f9a43-b8e1-4c07-9f52-1a3e7b0c5d96
// last-edited: 2026-10-11

package dedup

import (
	"encoding/binary"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/dedup/unified"
	"github.com/falkcorp/audiobook-organizer/internal/fingerprint"
)

// Window-print collector tests (02-PR8). Synthetic books ("Title 0001") and
// synthetic prints only.

type windowEnv struct {
	s *database.PebbleStore
	n int
}

func newWindowEnv(t *testing.T) *windowEnv {
	t.Helper()
	s, err := database.NewPebbleStoreInMemory(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	s.WaitForWarmup()
	return &windowEnv{s: s}
}

// book creates a book with one file per entry and returns (bookID, fileIDs).
func (e *windowEnv) book(t *testing.T, files int) (string, []string) {
	t.Helper()
	e.n++
	b, err := e.s.CreateBook(&database.Book{Title: "Title " + strings.Repeat("0", 3) + string(rune('0'+e.n%10)), FilePath: "/lib/w/" + string(rune('a'+e.n))})
	require.NoError(t, err)
	var ids []string
	for i := 0; i < files; i++ {
		f := &database.BookFile{BookID: b.ID, FilePath: "/lib/w/" + string(rune('a'+e.n)) + "/" + string(rune('0'+i)) + ".m4b", Format: "m4b"}
		require.NoError(t, e.s.CreateBookFile(f))
		ids = append(ids, f.ID)
	}
	return b.ID, ids
}

// walk is a synthetic window print: a random walk over uint32 frames.
func walk(seed uint64, n int) []uint32 {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e37))
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

func le(frames []uint32) []byte {
	out := make([]byte, 4*len(frames))
	for i, f := range frames {
		binary.LittleEndian.PutUint32(out[i*4:], f)
	}
	return out
}

const winFrames = 969

// putWindows writes ws1 windows (slots 1000/5000/9000) for fileID. seed picks
// the audio of each slot (same seed = same audio); pipeline is the
// provenance stamp.
func (e *windowEnv) putWindows(t *testing.T, fileID string, seed uint64, pipeline string) {
	t.Helper()
	for i, slot := range []int{1000, 5000, 9000} {
		raw := le(walk(seed*10+uint64(i), winFrames))
		require.NoError(t, e.s.PutFingerprintWindow(&database.FingerprintWindow{
			Ref: database.FileWindowRef(fileID), Kind: database.WindowKindWindow, SlotBP: slot,
			WindowSet: fingerprint.WindowSetWS1, OffsetSec: float64(slot) / 10000 * 3600, LengthSec: 120,
			DecodedSec: 120, DurationUsedSec: 3600, DurationSource: "book_file", Frames: winFrames,
			Raw: raw, Algorithm: 2, Pipeline: pipeline, FpcalcVersion: "1.6.0", FFmpegVersion: "8.0.1",
			ComputedAt: time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC), Host: "mac-test",
		}))
	}
}

func TestCollectWindowAcoustID_SameContentEmitsSignal(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	bBook, bFiles := e.book(t, 1)
	e.putWindows(t, aFiles[0], 1, fingerprint.WindowPipelineID)
	e.putWindows(t, bFiles[0], 1, fingerprint.WindowPipelineID)
	require.NoError(t, e.s.SetWindowIndexBuilt())

	query, err := e.s.GetBookFileByFileID(aFiles[0])
	require.NoError(t, err)
	var stats WindowCollectStats
	cfg := DefaultWindowAcoustIDConfig()
	cfg.Stats = &stats
	sigs, err := CollectWindowAcoustID(e.s, query, aBook, cfg)
	require.NoError(t, err)
	require.Len(t, sigs, 1, "a synthetic pair built from the same audio yields exactly one signal")
	require.Equal(t, unified.SigWindowAcoustID, sigs[0].Kind)
	require.InDelta(t, 1.0, sigs[0].Raw, 1e-9)
	require.Contains(t, sigs[0].Evidence, bBook, "evidence names the candidate book (the per-pair filter reads it)")
	require.Equal(t, int64(1), stats.Emitted.Load())
	require.Zero(t, stats.Incomparable.Load())
	t.Logf("same-content pair: %d signal(s), raw %.4f, incomparable %d", len(sigs), sigs[0].Raw, stats.Incomparable.Load())
}

func TestCollectWindowAcoustID_DifferentContentNoSignal(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	_, bFiles := e.book(t, 1)
	e.putWindows(t, aFiles[0], 1, fingerprint.WindowPipelineID)
	e.putWindows(t, bFiles[0], 2, fingerprint.WindowPipelineID)
	require.NoError(t, e.s.SetWindowIndexBuilt())

	query, err := e.s.GetBookFileByFileID(aFiles[0])
	require.NoError(t, err)
	sigs, err := CollectWindowAcoustID(e.s, query, aBook, DefaultWindowAcoustIDConfig())
	require.NoError(t, err)
	require.Empty(t, sigs, "an unrelated pair yields no signal")
	t.Logf("unrelated pair: %d signal(s)", len(sigs))
}

func TestCollectWindowAcoustID_ProvenanceMismatchIsIncomparable(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	_, bFiles := e.book(t, 1)
	// The same audio, so the bands collide, but cut by another pipeline:
	// the prints are not comparable, which is missing evidence.
	e.putWindows(t, aFiles[0], 1, fingerprint.WindowPipelineID)
	e.putWindows(t, bFiles[0], 1, "ffpcm-s16le-11025-mono/v0-test")
	require.NoError(t, e.s.SetWindowIndexBuilt())

	query, err := e.s.GetBookFileByFileID(aFiles[0])
	require.NoError(t, err)
	var stats WindowCollectStats
	cfg := DefaultWindowAcoustIDConfig()
	cfg.Stats = &stats
	sigs, err := CollectWindowAcoustID(e.s, query, aBook, cfg)
	require.NoError(t, err)
	require.Empty(t, sigs, "incomparable prints emit nothing")
	require.Equal(t, int64(1), stats.Incomparable.Load(), "and are counted")
	t.Logf("provenance mismatch: %d signal(s), %d incomparable", len(sigs), stats.Incomparable.Load())
}

func TestCollectWindowAcoustID_GatedOnIndexBuilt(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	_, bFiles := e.book(t, 1)
	e.putWindows(t, aFiles[0], 1, fingerprint.WindowPipelineID)
	e.putWindows(t, bFiles[0], 1, fingerprint.WindowPipelineID)
	query, err := e.s.GetBookFileByFileID(aFiles[0])
	require.NoError(t, err)

	var stats WindowCollectStats
	cfg := DefaultWindowAcoustIDConfig()
	cfg.Stats = &stats
	sigs, err := CollectWindowAcoustID(e.s, query, aBook, cfg)
	require.NoError(t, err)
	require.Empty(t, sigs, "no signal until the build op has completed a pass")
	require.Zero(t, stats.Probed.Load(), "and no probe either: no O(N) fallback")

	require.NoError(t, e.s.SetWindowIndexBuilt())
	sigs, err = CollectWindowAcoustID(e.s, query, aBook, cfg)
	require.NoError(t, err)
	require.Len(t, sigs, 1)

	// A nil store (non-Pebble backend) is off, not an error.
	require.Empty(t, CollectWindowAcoustIDForBook(nil, []database.BookFile{*query}, aBook, cfg))
}

// TestSupportingKind_WindowCannotMakeCandidateAlone: a twenty-file book whose
// every file matches the same candidate yields ONE signal for the pair, and
// window evidence alone (with the other supporting kinds) stays below the
// review band.
func TestSupportingKind_WindowCannotMakeCandidateAlone(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 20)
	bBook, bFiles := e.book(t, 1)
	for _, f := range aFiles {
		e.putWindows(t, f, 1, fingerprint.WindowPipelineID)
	}
	e.putWindows(t, bFiles[0], 1, fingerprint.WindowPipelineID)
	require.NoError(t, e.s.SetWindowIndexBuilt())

	files, err := e.s.GetBookFiles(aBook)
	require.NoError(t, err)
	require.Len(t, files, 20)
	sigs := CollectWindowAcoustIDForBook(e.s, files, aBook, DefaultWindowAcoustIDConfig())
	require.Len(t, sigs, 1, "one signal per candidate book, not one per matching file")
	require.Contains(t, sigs[0].Evidence, bBook)

	cfg := unified.DefaultScoreConfig()
	composed := unified.ComposeScore(append(sigs,
		unified.Signal{Kind: unified.SigDuration},
		unified.Signal{Kind: unified.SigFolderPath},
	), nil, cfg, canonicalPairIDs(aBook, bBook))
	require.Less(t, composed.Score, cfg.BandReviewMin)
	require.Empty(t, composed.Band, "supporting evidence alone never makes a candidate")
}
