// file: internal/dedup/collectors_window_test.go
// version: 1.2.0
// guid: 2d6f9a43-b8e1-4c07-9f52-1a3e7b0c5d96
// last-edited: 2026-10-12

package dedup

import (
	"context"
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
	e.putSlots(t, fileID, [3]uint64{seed * 10, seed*10 + 1, seed*10 + 2}, 3600, pipeline)
}

// putSlots writes the three ws1 windows with the audio of slotSeeds[i] at
// slot i (1000/5000/9000) and durSec as the duration the plan used.
func (e *windowEnv) putSlots(t *testing.T, fileID string, slotSeeds [3]uint64, durSec float64, pipeline string) {
	t.Helper()
	for i, slot := range []int{1000, 5000, 9000} {
		raw := le(walk(slotSeeds[i], winFrames))
		require.NoError(t, e.s.PutFingerprintWindow(&database.FingerprintWindow{
			Ref: database.FileWindowRef(fileID), Kind: database.WindowKindWindow, SlotBP: slot,
			WindowSet: fingerprint.WindowSetWS1, OffsetSec: float64(slot) / 10000 * durSec, LengthSec: 120,
			DecodedSec: 120, DurationUsedSec: durSec, DurationSource: "book_file", Frames: winFrames,
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
	sigs := CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, cfg)
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
	sigs := CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, DefaultWindowAcoustIDConfig())
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
	sigs := CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, cfg)
	require.Empty(t, sigs, "incomparable prints emit nothing")
	require.Equal(t, int64(1), stats.Incomparable.Load(), "and are counted")
	t.Logf("provenance mismatch: %d signal(s), %d incomparable", len(sigs), stats.Incomparable.Load())
}

// TestCollectWindowAcoustID_LowConfidenceIsSkipped: the same audio in every
// slot, but the two files' durations are 10 percent apart, so
// WindowSetSimilarity pairs every slot with every slot and flags the result
// LowConfidence. That is not a dedup signal however high the score.
func TestCollectWindowAcoustID_LowConfidenceIsSkipped(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	_, bFiles := e.book(t, 1)
	e.putSlots(t, aFiles[0], [3]uint64{10, 11, 12}, 3600, fingerprint.WindowPipelineID)
	e.putSlots(t, bFiles[0], [3]uint64{10, 11, 12}, 3960, fingerprint.WindowPipelineID)
	require.NoError(t, e.s.SetWindowIndexBuilt())

	query, err := e.s.GetBookFileByFileID(aFiles[0])
	require.NoError(t, err)
	var stats WindowCollectStats
	cfg := DefaultWindowAcoustIDConfig()
	cfg.Stats = &stats
	sigs := CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, cfg)
	require.Empty(t, sigs, "a LowConfidence comparison emits nothing")
	require.Equal(t, int64(1), stats.Candidates.Load(), "the candidate was found and compared")
	require.Equal(t, int64(1), stats.LowConfidence.Load())
}

// TestCollectWindowAcoustID_BelowMinSimilarityIsSkipped: one slot holds the
// same audio, so the index proposes the pair (all 16 bands of that window
// hit), but the other two slots are unrelated and the median similarity is
// far under MinSimilarity.
func TestCollectWindowAcoustID_BelowMinSimilarityIsSkipped(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	_, bFiles := e.book(t, 1)
	e.putSlots(t, aFiles[0], [3]uint64{20, 21, 22}, 3600, fingerprint.WindowPipelineID)
	e.putSlots(t, bFiles[0], [3]uint64{20, 91, 92}, 3600, fingerprint.WindowPipelineID)
	require.NoError(t, e.s.SetWindowIndexBuilt())

	query, err := e.s.GetBookFileByFileID(aFiles[0])
	require.NoError(t, err)
	var stats WindowCollectStats
	cfg := DefaultWindowAcoustIDConfig()
	cfg.Stats = &stats
	sigs := CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, cfg)
	require.Empty(t, sigs, "one matching slot of three is below MinSimilarity")
	require.Equal(t, int64(1), stats.Candidates.Load(), "the band index did propose the pair")
	require.Equal(t, int64(1), stats.BelowMinSimilarity.Load())
	require.Zero(t, stats.LowConfidence.Load())
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
	sigs := CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, cfg)
	require.Empty(t, sigs, "no signal until the build op has completed a pass")
	require.Zero(t, stats.Probed.Load(), "and no probe either: no O(N) fallback")

	require.NoError(t, e.s.SetWindowIndexBuilt())
	sigs = CollectWindowAcoustIDForBook(e.s, []database.BookFile{*query}, aBook, cfg)
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

// windowEngine builds an Engine over the env's PebbleStore, with the window
// collector wired exactly as registry_wire.go wires it when wire is true.
func (e *windowEnv) windowEngine(t *testing.T, wire bool) (*Engine, *database.EmbeddingStore) {
	t.Helper()
	es := database.NewEmbeddingStore(e.s.DB())
	eng, err := NewEngine(es, e.s, nil, nil, nil, unified.DefaultScoreConfig())
	require.NoError(t, err)
	if wire {
		eng.SetWindowStore(e.s)
	}
	return eng, es
}

// asinBook creates a one-file book carrying asin, under its own parent folder
// and with a fixed numberless title, so PairEligibility passes the pair (a
// shared parent is same_dir_multi_file, differing digits series_volume_differs).
func (e *windowEnv) asinBook(t *testing.T, asin string) (*database.Book, string) {
	t.Helper()
	e.n++
	dir := "/lib/" + string(rune('a'+e.n)) + "/Window Pair Title"
	a := asin
	b, err := e.s.CreateBook(&database.Book{Title: "Window Pair Title", FilePath: dir, ASIN: &a})
	require.NoError(t, err)
	f := &database.BookFile{BookID: b.ID, FilePath: dir + "/0.m4b", Format: "m4b"}
	require.NoError(t, e.s.CreateBookFile(f))
	return b, f.ID
}

func windowSignalsIn(sigs []unified.Signal) []unified.Signal {
	var out []unified.Signal
	for _, s := range sigs {
		if s.Kind == unified.SigWindowAcoustID {
			out = append(out, s)
		}
	}
	return out
}

// TestWindowSignal_ReachesUnifiedScan proves the operational unified scan
// (runUnifiedScoringForBook, what dedup.full-scan and CheckBook run) collects
// the window signal and persists it in the pair's ScoreBreakdown once the
// engine has a window store, and that an engine without one (a non-Pebble
// backend) scores the same pair without it. The shared ASIN puts the pair in
// a band so it is persisted at all: window evidence is supporting-only.
func TestWindowSignal_ReachesUnifiedScan(t *testing.T) {
	for _, wire := range []bool{true, false} {
		t.Run(map[bool]string{true: "wired", false: "unwired"}[wire], func(t *testing.T) {
			e := newWindowEnv(t)
			a, aFile := e.asinBook(t, "B0WINTEST1")
			b, bFile := e.asinBook(t, "B0WINTEST1")
			e.putWindows(t, aFile, 1, fingerprint.WindowPipelineID)
			e.putWindows(t, bFile, 1, fingerprint.WindowPipelineID)
			require.NoError(t, e.s.SetWindowIndexBuilt())

			eng, es := e.windowEngine(t, wire)
			eng.AutoMergeEnabled = false
			// Seed the pending pair directly as an embedding-layer row, the way
			// the embedding pass files one: an exact-layer row is never
			// re-scored by the upsert, and the exact emitters' own gates are
			// not what this test is about.
			sim := 0.9
			require.NoError(t, eng.upsertCandidateWithLiveLabel(database.DedupCandidate{
				EntityType: "book", EntityAID: a.ID, EntityBID: b.ID,
				Layer: "embedding", Similarity: &sim, Status: "pending",
			}))
			require.Len(t, pendingCandidates(t, es), 1, "setup: one seeded pending pair")
			require.NoError(t, eng.runUnifiedScoringForBook(context.Background(), a, "", eng.ScoreConfig()))

			cands := pendingCandidates(t, es)
			require.Len(t, cands, 1)
			require.NotNil(t, cands[0].ScoreBreakdown, "the unified pass re-scored the pair")
			win := windowSignalsIn(cands[0].ScoreBreakdown.Signals)
			if !wire {
				require.Empty(t, win, "no window store, no window signal")
				return
			}
			require.Len(t, win, 1, "the scan collected exactly one window signal for the pair")
			require.InDelta(t, 1.0, win[0].Raw, 1e-9)
			require.Contains(t, win[0].Evidence, b.ID)
		})
	}
}

// TestWindowSignal_ReachesRescorePath proves dedup.rescore-labeled-examples'
// scorer (ScorePairsForBook) gathers the window signal too, with no other
// evidence on the pair.
func TestWindowSignal_ReachesRescorePath(t *testing.T) {
	e := newWindowEnv(t)
	aBook, aFiles := e.book(t, 1)
	bBook, bFiles := e.book(t, 1)
	e.putWindows(t, aFiles[0], 1, fingerprint.WindowPipelineID)
	e.putWindows(t, bFiles[0], 1, fingerprint.WindowPipelineID)
	require.NoError(t, e.s.SetWindowIndexBuilt())

	eng, _ := e.windowEngine(t, true)
	res, err := eng.ScorePairsForBook(context.Background(), aBook, []RescorePairInput{{OtherID: bBook}})
	require.NoError(t, err)
	require.Len(t, res, 1)
	require.NotNil(t, res[0].Score, "the window signal alone gives the pair a breakdown")
	win := windowSignalsIn(res[0].Score.Signals)
	require.Len(t, win, 1)
	require.Contains(t, win[0].Evidence, bBook)
}
