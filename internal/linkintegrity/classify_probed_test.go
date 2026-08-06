// file: internal/linkintegrity/classify_probed_test.go
// version: 1.0.0
// guid: 2d84f13b-6a09-4c72-9e51-8b3f0d6a4c27
// last-edited: 2026-08-06

package linkintegrity

import (
	"strings"
	"testing"
)

const testHour = 3600

// TestClassifyDirProbed_ChapterLengthFolderResolves is the case the nil
// durations were blocking. Tier 1 could only park this folder with "no
// durations are known"; measuring it must resolve it to one book.
func TestClassifyDirProbed_ChapterLengthFolderResolves(t *testing.T) {
	files := []string{
		"The Given Sacrifice_01_.mp3", "The Given Sacrifice_02_.mp3",
		"The Given Sacrifice_03_.mp3", "The Given Sacrifice_04_.mp3",
	}

	// Baseline: this is exactly what tier 1 sees, and why 1,019 folders queued.
	if v := ClassifyDir(files, 0, nil); v.OneBook {
		t.Fatal("precondition failed: tier 1 must NOT resolve this without durations")
	}

	probes := []ProbedDuration{
		{Name: files[0], Sec: 1500, OK: true},
		{Name: files[1], Sec: 1480, OK: true},
		{Name: files[2], Sec: 1520, OK: true},
		{Name: files[3], Sec: 1495, OK: true},
	}
	got := ClassifyDirProbed(files, 0, probes)
	if !got.OneBook {
		t.Errorf("OneBook = false, want true — chapter-length files sharing one stem are one book; reason=%q", got.Reason)
	}
	if got.ProbesOK != 4 || got.ProbesFailed != 0 {
		t.Errorf("ProbesOK/Failed = %d/%d, want 4/0", got.ProbesOK, got.ProbesFailed)
	}
}

// TestClassifyDirProbed_SeriesGuardFires is the "Super Sales on Super Heroes
// 1..5" shape: one shared stem, but every member is a whole novel. Duration is
// the ONLY signal that separates it from a chapter set, so this is the case that
// justifies probing at all — and it must stay in review.
func TestClassifyDirProbed_SeriesGuardFires(t *testing.T) {
	files := []string{
		"Super Sales on Super Heroes 1.m4b",
		"Super Sales on Super Heroes 2.m4b",
		"Super Sales on Super Heroes 3.m4b",
		"Super Sales on Super Heroes 4.m4b",
		"Super Sales on Super Heroes 5.m4b",
	}
	probes := make([]ProbedDuration, 0, len(files))
	for _, f := range files {
		probes = append(probes, ProbedDuration{Name: f, Sec: 9 * testHour, OK: true})
	}

	got := ClassifyDirProbed(files, 0, probes)
	if got.OneBook {
		t.Fatalf("OneBook = true — this would MERGE 5 distinct novels into one row; reason=%q", got.Reason)
	}
	if !strings.Contains(got.Reason, "whole books") {
		t.Errorf("reason must name the evidence that fired the series guard, got %q", got.Reason)
	}
}

// 🔴 TestClassifyDirProbed_PartialFailureExcludedNotZeroed is the regression
// test for the incident this op exists to avoid reproducing.
//
// The numbers are chosen so that EXCLUSION ALONE decides the verdict, which
// means the test cannot pass on the coverage guard by accident:
//
//	4 files, one stem. One measures 6,000s (book-length); three fail.
//	  counted as zero  → long=1, n=4 → 1*2 > 4 is FALSE → OneBook (WRONG)
//	  excluded         → long=1, n=1 → 1*2 > 1 is TRUE  → series guard fires
//
// If failed probes ever start contributing zeros, this flips to OneBook=true and
// the test fails — which is the whole point.
func TestClassifyDirProbed_PartialFailureExcludedNotZeroed(t *testing.T) {
	files := []string{
		"Wandering Inn 1.mp3", "Wandering Inn 2.mp3",
		"Wandering Inn 3.mp3", "Wandering Inn 4.mp3",
	}
	probes := []ProbedDuration{
		{Name: files[0], Sec: 6000, OK: true},
		{Name: files[1]}, // probe failed — OK false, Sec zero-valued
		{Name: files[2]},
		{Name: files[3]},
	}

	got := ClassifyDirProbed(files, 0, probes)
	if got.OneBook {
		t.Fatalf("OneBook = true — a failed probe was counted as a zero-length chapter; reason=%q", got.Reason)
	}
	if got.ProbesOK != 1 || got.ProbesFailed != 3 {
		t.Errorf("ProbesOK/Failed = %d/%d, want 1/3 — the failures must be COUNTED as unmeasured",
			got.ProbesOK, got.ProbesFailed)
	}
	// Pin the mechanism, not just the verdict: the durations slice handed to
	// ClassifyDir must have held one entry, so the guard's denominator was 1.
	if !strings.Contains(got.Reason, "whole books") {
		t.Errorf("expected the series guard to fire on the measured subset, got %q", got.Reason)
	}
}

// The reverse partial: the measured subset reads as chapter-length, so exclusion
// alone would happily auto-link a folder we have barely looked at. This is the
// case the coverage guard exists for.
func TestClassifyDirProbed_PartialFailureBlocksOneBookVerdict(t *testing.T) {
	files := []string{
		"Skyward 1.mp3", "Skyward 2.mp3", "Skyward 3.mp3", "Skyward 4.mp3",
	}
	probes := []ProbedDuration{
		{Name: files[0], Sec: 1200, OK: true}, // chapter-length
		{Name: files[1]},                      // unprobeable
		{Name: files[2]},
		{Name: files[3]},
	}

	got := ClassifyDirProbed(files, 0, probes)
	if got.OneBook {
		t.Fatalf("OneBook = true on 1 of 4 files measured — partial evidence must not confirm; reason=%q", got.Reason)
	}
	if !strings.Contains(got.Reason, "could not be probed") {
		t.Errorf("reason must name the unmeasured files as the blocker, got %q", got.Reason)
	}
}

// A successful ffprobe exit reporting zero is NOT a measurement. Callers build
// OK as (err == nil && secs > 0), but the classifier re-checks so a caller that
// forgets cannot smuggle a zero into the guard.
func TestClassifyDirProbed_ZeroSecondsIsNotEvidence(t *testing.T) {
	files := []string{"Nona 1.mp3", "Nona 2.mp3", "Nona 3.mp3", "Nona 4.mp3"}
	probes := []ProbedDuration{
		{Name: files[0], Sec: 6000, OK: true},
		{Name: files[1], Sec: 0, OK: true}, // header-only container, exit 0
		{Name: files[2], Sec: 0, OK: true},
		{Name: files[3], Sec: 0, OK: true},
	}

	got := ClassifyDirProbed(files, 0, probes)
	if got.OneBook {
		t.Fatalf("OneBook = true — a zero-second 'success' was admitted as evidence; reason=%q", got.Reason)
	}
	if got.ProbesOK != 1 || got.ProbesFailed != 3 {
		t.Errorf("ProbesOK/Failed = %d/%d, want 1/3 — zero-second results must count as unmeasured",
			got.ProbesOK, got.ProbesFailed)
	}
}

// A single audio file is one book whether or not it could be measured — its
// verdict never depended on duration, so the coverage guard must not park it.
func TestClassifyDirProbed_SingleFileExemptFromCoverageGuard(t *testing.T) {
	files := []string{"The Rising.m4b"}
	got := ClassifyDirProbed(files, 0, []ProbedDuration{{Name: files[0]}})
	if !got.OneBook {
		t.Errorf("OneBook = false — a lone audio file is one book regardless of probe success; reason=%q", got.Reason)
	}
}

// Folders whose files carry many distinct titles are decided by stems, before
// duration is consulted at all. Measuring must not rescue them.
func TestClassifyDirProbed_DistinctTitlesStayInReview(t *testing.T) {
	files := []string{
		"01 Skinwalker part 1.mp3", "02 Blood Cross Part 1.mp3",
		"03 Mercy Blade Part 1.mp3",
	}
	probes := []ProbedDuration{
		{Name: files[0], Sec: 1200, OK: true},
		{Name: files[1], Sec: 1200, OK: true},
		{Name: files[2], Sec: 1200, OK: true},
	}
	got := ClassifyDirProbed(files, 0, probes)
	if got.OneBook {
		t.Fatalf("OneBook = true — distinct novels sharing a folder must never merge; reason=%q", got.Reason)
	}
}
