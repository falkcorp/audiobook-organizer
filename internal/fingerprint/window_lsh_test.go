// file: internal/fingerprint/window_lsh_test.go
// version: 1.1.0
// guid: 3b0f7c55-2e4a-4d8b-9a61-0c7e5f2d9b14
// last-edited: 2026-10-11

package fingerprint

import (
	"encoding/binary"
	"math/rand/v2"
	"os"
	"testing"
)

// Synthetic prints only: a random walk over uint32 frames, where each next
// frame flips each bit of the previous one with probability 0.15. Real
// chromaprint frames are correlated frame to frame the same way; nothing here
// is derived from real audio.

// windowFrames is a 120 s ws1 window: WindowLengthSec * FramesPerSec.
var windowFrames = frames(WindowLengthSec) - 1

// threeSecFrames is a 3 s shift in frames.
var threeSecFrames = frames(3) - 1

func synthStream(rng *rand.Rand, n int) []uint32 {
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

// withNoise returns a copy of frames with each bit flipped with probability p.
func withNoise(rng *rand.Rand, frames []uint32, p float64) []uint32 {
	out := make([]uint32, len(frames))
	for i, f := range frames {
		var mask uint32
		for bit := 0; bit < 32; bit++ {
			if rng.Float64() < p {
				mask |= 1 << bit
			}
		}
		out[i] = f ^ mask
	}
	return out
}

func rawOf(frames []uint32) []byte {
	out := make([]byte, 4*len(frames))
	for i, f := range frames {
		binary.LittleEndian.PutUint32(out[i*4:], f)
	}
	return out
}

// sharedBands counts the band indexes whose hashes agree.
func sharedBands(a, b []WindowBand) int {
	n := 0
	for i := range a {
		if i < len(b) && a[i] == b[i] {
			n++
		}
	}
	return n
}

func mustBands(t testing.TB, raw []byte, b, r int, seeds []uint64) []WindowBand {
	t.Helper()
	bands, err := windowBandsWith(raw, b, r, seeds)
	if err != nil {
		t.Fatal(err)
	}
	if len(bands) != b {
		t.Fatalf("got %d bands, want %d", len(bands), b)
	}
	return bands
}

// measureRates returns (recall at noise, false-candidate rate) for (b, r)
// over trials pairs each, using minHits shared bands as "candidate".
func measureRates(t testing.TB, b, r, trials, minHits int, noise float64, seed uint64) (recall, falseRate float64) {
	t.Helper()
	seeds := makeWindowBandSeeds(b * r)
	rng := rand.New(rand.NewPCG(seed, seed^0xa5a5))
	hit, fp := 0, 0
	for i := 0; i < trials; i++ {
		a := synthStream(rng, windowFrames)
		noisy := withNoise(rng, a, noise)
		if sharedBands(mustBands(t, rawOf(a), b, r, seeds), mustBands(t, rawOf(noisy), b, r, seeds)) >= minHits {
			hit++
		}
		other := synthStream(rng, windowFrames)
		if sharedBands(mustBands(t, rawOf(a), b, r, seeds), mustBands(t, rawOf(other), b, r, seeds)) >= minHits {
			fp++
		}
	}
	return float64(hit) / float64(trials), float64(fp) / float64(trials)
}

// TestWindowBands_ParameterSweep is the experiment the (b, r) choice rests on.
// It is long, so it runs only when WINDOW_LSH_SWEEP=1:
//
//	WINDOW_LSH_SWEEP=1 go test ./internal/fingerprint/ -run WindowBands_ParameterSweep -v
func TestWindowBands_ParameterSweep(t *testing.T) {
	if os.Getenv("WINDOW_LSH_SWEEP") != "1" {
		t.Skip("set WINDOW_LSH_SWEEP=1 to run the (b, r) sweep")
	}
	for _, p := range []struct{ b, r int }{{8, 1}, {12, 1}, {16, 1}, {20, 1}, {24, 1}, {32, 2}, {40, 2}, {48, 2}} {
		recall, falseRate := measureRates(t, p.b, p.r, 1000, WindowMinBandHits, 0.01, 7)
		t.Logf("b=%d r=%d: recall@1%%=%.3f false-candidate=%.3f", p.b, p.r, recall, falseRate)
	}
}

func TestWindowBands_DeterministicAndPositionIndependent(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	stream := synthStream(rng, windowFrames+threeSecFrames)
	a := rawOf(stream[:windowFrames])
	shifted := rawOf(stream[threeSecFrames : threeSecFrames+windowFrames])

	first, err := WindowBands(a)
	if err != nil {
		t.Fatal(err)
	}
	again, err := WindowBands(a)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != WindowBandCount {
		t.Fatalf("got %d bands, want %d", len(first), WindowBandCount)
	}
	for i := range first {
		if first[i] != again[i] {
			t.Fatalf("band %d differs between two runs on the same print", i)
		}
		if first[i].Band != byte(i) {
			t.Fatalf("band %d carries index %d", i, first[i].Band)
		}
	}

	sh, err := WindowBands(shifted)
	if err != nil {
		t.Fatal(err)
	}
	shared := sharedBands(first, sh)
	t.Logf("3 s shift (%d frames): %d of %d bands shared", threeSecFrames, shared, WindowBandCount)
	if shared < WindowMinBandHits {
		t.Fatalf("the same audio shifted by 3 s shares %d bands, want >= %d", shared, WindowMinBandHits)
	}

	// The position-based Subprints scheme shares (almost) nothing on the same
	// shift: that is the miss this scheme exists to fix.
	subsA, bandsA, _ := Subprints(a)
	subsB, bandsB, _ := Subprints(shifted)
	same := 0
	for i := range subsA {
		if i < len(subsB) && bandsA[i] == bandsB[i] && subsA[i] == subsB[i] {
			same++
		}
	}
	t.Logf("position-based Subprints on the same shift: %d of %d shared", same, len(subsA))

	if _, err := WindowBands([]byte{1, 2, 3}); err == nil {
		t.Fatal("unaligned raw: want an error")
	}
	if got, err := WindowBands(rawOf(make([]uint32, windowFrames))); err != nil || got != nil {
		t.Fatalf("a constant window has one shingle: want nil, nil; got %v, %v", got, err)
	}
}

func TestWindowBands_RecallAtOnePercentNoise(t *testing.T) {
	recall, _ := measureRates(t, WindowBandCount, WindowBandRows, 500, WindowMinBandHits, 0.01, 11)
	t.Logf("b=%d r=%d: recall at 1%% per-bit noise = %.3f (500 pairs)", WindowBandCount, WindowBandRows, recall)
	if recall < 0.95 {
		t.Fatalf("recall %.3f < 0.95", recall)
	}
}

func TestWindowBands_FalseCandidateRate(t *testing.T) {
	_, falseRate := measureRates(t, WindowBandCount, WindowBandRows, 500, WindowMinBandHits, 0.01, 13)
	t.Logf("b=%d r=%d: false-candidate rate on unrelated windows = %.3f (500 pairs)", WindowBandCount, WindowBandRows, falseRate)
	if falseRate > 0.02 {
		t.Fatalf("false-candidate rate %.3f > 0.02", falseRate)
	}

	// A harder case outside the brief's target: otherwise unrelated windows
	// that share one 3 s sting (a series' chapter-break music), a fresh sting
	// per pair. 23 shared shingles of about 1,900 put the per-band match
	// chance near 0.012, so about 1.6 percent of pairs reach two hits. It is
	// reported, and guarded at 5 percent so a re-tune that makes shared
	// fragments of audio much louder is caught; the collector's
	// WindowSetSimilarity check rejects these pairs anyway.
	rng := rand.New(rand.NewPCG(17, 19))
	fp := 0
	const trials = 2000
	for i := 0; i < trials; i++ {
		sting := synthStream(rng, threeSecFrames)
		a := synthStream(rng, windowFrames)
		b := synthStream(rng, windowFrames)
		copy(a[100:], sting)
		copy(b[600:], sting)
		ba, _ := WindowBands(rawOf(a))
		bb, _ := WindowBands(rawOf(b))
		if sharedBands(ba, bb) >= WindowMinBandHits {
			fp++
		}
	}
	rate := float64(fp) / trials
	t.Logf("unrelated windows sharing a 3 s sting: false-candidate rate = %.3f (%d pairs)", rate, trials)
	if rate > 0.05 {
		t.Fatalf("shared-sting false-candidate rate %.3f > 0.05", rate)
	}
}

func TestWindowBandContainment_SliceAndUnrelated(t *testing.T) {
	// With r = 1 a slice's containment estimates the slice fraction itself
	// (MinHash: P(min of the parent's set lies in the slice) = |slice|/|parent|);
	// one window's 16 bands make a single estimate noisy, so report the mean
	// of 200 parents per fraction.
	rng := rand.New(rand.NewPCG(23, 29))
	for _, frac := range []float64{1.0, 0.95, 0.9, 0.8, 0.5} {
		const parents = 200
		sum := 0.0
		for i := 0; i < parents; i++ {
			parent := synthStream(rng, windowFrames)
			pb, _ := WindowBands(rawOf(parent))
			n := int(frac * float64(windowFrames))
			fb, _ := WindowBands(rawOf(parent[windowFrames-n:]))
			sum += WindowBandContainment(fb, pb)
		}
		t.Logf("slice %.0f%% of the parent window: mean containment %.3f (%d parents)", frac*100, sum/parents, parents)
	}
	pb, _ := WindowBands(rawOf(synthStream(rng, windowFrames)))
	other, _ := WindowBands(rawOf(synthStream(rng, windowFrames)))
	if c := WindowBandContainment(other, pb); c > 0.05 {
		t.Fatalf("unrelated containment %.2f > 0.05", c)
	}
	if WindowBandContainment(nil, pb) != 0 {
		t.Fatal("empty fragment: want 0")
	}
}

// TestWindowBandContainment_IsAsymmetric pins the denominator: |a ∩ b| / |a|,
// the share of the FRAGMENT's keys the parent has, not a symmetric overlap.
// A fragment whose four keys all appear among a parent's sixteen is fully
// contained (1.0); the parent is a quarter contained in the fragment. A
// Jaccard-style |a ∩ b| / |a ∪ b| would read 0.25 both ways.
func TestWindowBandContainment_IsAsymmetric(t *testing.T) {
	parent := make([]WindowBand, 16)
	for i := range parent {
		parent[i] = WindowBand{Band: byte(i), Hash: uint64(1000 + i)}
	}
	frag := []WindowBand{parent[0], parent[5], parent[9], parent[15], parent[5]} // a repeat counts once
	if got := WindowBandContainment(frag, parent); got != 1.0 {
		t.Fatalf("Containment(fragment, parent) = %v, want 1.0", got)
	}
	if got := WindowBandContainment(parent, frag); got != 0.25 {
		t.Fatalf("Containment(parent, fragment) = %v, want 0.25", got)
	}
	// Same band index, different hash: not a shared key.
	other := []WindowBand{{Band: 0, Hash: 1}, parent[1]}
	if got := WindowBandContainment(other, parent); got != 0.5 {
		t.Fatalf("Containment(other, parent) = %v, want 0.5", got)
	}
}
