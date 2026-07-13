// file: internal/dedup/dataset/rules_test.go
// version: 1.3.0
// guid: c1d4e8b5-7f23-4a90-9b01-6e2c5d8f3a47
// last-edited: 2026-07-12

package dataset

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func TestCatchers(t *testing.T) {
	cases := []struct {
		name      string
		ex        database.LabeledExample
		wantFires bool
		wantLabel string
	}{
		{
			name: "part vs whole by duration ratio",
			ex: database.LabeledExample{
				A:             database.BookFeatures{TotalDurationSec: 36000, FilesExist: true},
				B:             database.BookFeatures{TotalDurationSec: 1200, FilesExist: true},
				DurationRatio: 1200.0 / 36000.0,
			},
			wantFires: true, wantLabel: "not_dup",
		},
		{
			// missingFile now emits unsure — file absence is evidence-free for
			// dup-ness, so it must never poison the gold set with a not_dup.
			name: "missing file one side => unsure",
			ex: database.LabeledExample{
				A: database.BookFeatures{FilesExist: true, TotalDurationSec: 100},
				B: database.BookFeatures{FilesExist: false},
			},
			wantFires: true, wantLabel: "unsure",
		},
		{
			name: "whole-book signature match => true_dup",
			ex: database.LabeledExample{
				A:                 database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				B:                 database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				SignatureRelation: "match", DurationRatio: 1.0,
			},
			wantFires: true, wantLabel: "true_dup",
		},
		{
			name: "no rule fires",
			ex: database.LabeledExample{
				A:                 database.BookFeatures{FilesExist: true, TotalDurationSec: 36000},
				B:                 database.BookFeatures{FilesExist: true, TotalDurationSec: 35900},
				DurationRatio:     35900.0 / 36000.0,
				SignatureRelation: "unknown",
			},
			wantFires: false,
		},
		{
			// The signature match catcher must fire BEFORE the missing-file catcher
			// to respect priority order. Both sigs present + "match" wins even if
			// one side is somehow also flagged missing (unlikely but tests priority).
			name: "signature match beats missing file (priority check)",
			ex: database.LabeledExample{
				A:                 database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				B:                 database.BookFeatures{FilesExist: false, WholeBookSigPresent: true},
				SignatureRelation: "match",
			},
			wantFires: true, wantLabel: "true_dup",
		},
		{
			// missing-file fires before part-vs-whole (priority preserved); it now
			// yields unsure rather than not_dup.
			name: "missing file beats part-vs-whole (priority check)",
			ex: database.LabeledExample{
				A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 36000},
				B:             database.BookFeatures{FilesExist: false, TotalDurationSec: 100},
				DurationRatio: 100.0 / 36000.0,
			},
			wantFires: true, wantLabel: "unsure",
		},
		{
			// disjoint signature should not trigger signature-match catcher
			name: "disjoint signature does not fire match catcher",
			ex: database.LabeledExample{
				A:                 database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				B:                 database.BookFeatures{FilesExist: true, WholeBookSigPresent: true, TotalDurationSec: 36000},
				DurationRatio:     1.0,
				SignatureRelation: "disjoint",
			},
			wantFires: false,
		},
		{
			// stub side (no duration, tiny file) is implausible audio → not_dup.
			// This is the post-cutover residual class missingFile + partVsWhole miss:
			// file records exist (FilesExist true) and duration=0 makes the ratio 0.
			name: "stub side (32 bytes, no duration) => not_dup",
			ex: database.LabeledExample{
				A: database.BookFeatures{FilesExist: true, TotalDurationSec: 36000, FileSizeBytes: 184_741_714},
				B: database.BookFeatures{FilesExist: true, TotalDurationSec: 0, FileSizeBytes: 32},
			},
			wantFires: true, wantLabel: "not_dup",
		},
		{
			// genuine unscanned copy: large file, zero duration → NOT suppressed.
			// This is the false-positive the catcher must avoid (a real duplicate
			// awaiting a scan). No catcher fires; left unlabeled.
			name: "genuine unscanned-large copy is not suppressed",
			ex: database.LabeledExample{
				A:                 database.BookFeatures{FilesExist: true, TotalDurationSec: 36000, FileSizeBytes: 184_741_714},
				B:                 database.BookFeatures{FilesExist: true, TotalDurationSec: 0, FileSizeBytes: 184_741_714},
				SignatureRelation: "unknown",
			},
			wantFires: false,
		},
		{
			// both sides are tiny placeholders matched by title → not_dup.
			name: "both stubs (182 bytes each) => not_dup",
			ex: database.LabeledExample{
				A: database.BookFeatures{FilesExist: true, TotalDurationSec: 0, FileSizeBytes: 182},
				B: database.BookFeatures{FilesExist: true, TotalDurationSec: 0, FileSizeBytes: 182},
			},
			wantFires: true, wantLabel: "not_dup",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label, reason, fires := Classify(tc.ex)
			if fires != tc.wantFires {
				t.Fatalf("fires=%v want %v (reason=%q)", fires, tc.wantFires, reason)
			}
			if fires && label != tc.wantLabel {
				t.Fatalf("label=%q want %q (reason=%q)", label, tc.wantLabel, reason)
			}
		})
	}
}

func TestClassify_ReasonContainsRatio(t *testing.T) {
	ex := database.LabeledExample{
		A:             database.BookFeatures{TotalDurationSec: 36000, FilesExist: true},
		B:             database.BookFeatures{TotalDurationSec: 1200, FilesExist: true},
		DurationRatio: 1200.0 / 36000.0,
	}
	label, reason, fires := Classify(ex)
	if !fires {
		t.Fatal("expected catcher to fire")
	}
	// Label assertion: must be not_dup.
	if label != "not_dup" {
		t.Fatalf("label = %q, want not_dup", label)
	}
	// Reason assertion: must describe the rule, not just echo the label.
	if !strings.Contains(reason, "part vs whole") {
		t.Fatalf("reason should describe the rule (want substring %q); got %q", "part vs whole", reason)
	}
}

// The following regression tests pin the not_dup-mining guards added to stop
// contaminated gold labels. Each part-vs-whole case is shaped like one of the
// 2026-07-08 hand-verified prod mislabels (a ms/sec duration-unit corruption on
// a pair that is really a duplicate), and must go unsure — never not_dup —
// because the pair shares hard identity.

// TestPartVsWholeSharedASINGoesUnsure mirrors "Way of the Wolf" (ASIN
// B002V8MAAM): same ASIN, same primary path, durations 21171 vs 20810840
// ("sec") — a ratio ≈ 0.001 that misfired the part-vs-whole rule.
func TestPartVsWholeSharedASINGoesUnsure(t *testing.T) {
	ex := database.LabeledExample{
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 21171, ASIN: "B002V8MAAM", PrimaryPath: "/lib/wolf/wolf.m4b"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 20810840, ASIN: "B002V8MAAM", PrimaryPath: "/lib/wolf/wolf.m4b"},
		DurationRatio: 21171.0 / 20810840.0,
	}
	label, reason, fires := partVsWhole(ex)
	if !fires || label != "unsure" {
		t.Fatalf("partVsWhole = (%q, fires=%v), want unsure/true; reason=%q", label, fires, reason)
	}
	if !strings.Contains(reason, "shares identity") {
		t.Fatalf("reason should flag the identity guard; got %q", reason)
	}
}

// TestPartVsWholeSharedVersionGroupGoesUnsure exercises the version-group arm of
// SharesIdentity: no ASIN, disjoint paths, but a shared VersionGroupID.
func TestPartVsWholeSharedVersionGroupGoesUnsure(t *testing.T) {
	ex := database.LabeledExample{
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 34535, VersionGroupID: "vg-empire", PrimaryPath: "/lib/foundation/a.m4b"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 57989869, VersionGroupID: "vg-empire", PrimaryPath: "/lib/foundation/b.m4b"},
		DurationRatio: 34535.0 / 57989869.0,
	}
	label, _, fires := partVsWhole(ex)
	if !fires || label != "unsure" {
		t.Fatalf("partVsWhole = (%q, fires=%v), want unsure/true", label, fires)
	}
}

// TestPartVsWholeSharedPathGoesUnsure mirrors "Alcatraz vs the Evil Librarians"
// (ASIN B005GGGC3M, same path): the path arm alone must trigger the guard even
// when ASIN and version group carry no matching evidence.
func TestPartVsWholeSharedPathGoesUnsure(t *testing.T) {
	ex := database.LabeledExample{
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 18000, PrimaryPath: "/lib/alcatraz/alcatraz.m4b"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 18000000, PrimaryPath: "/lib/alcatraz/alcatraz.m4b"},
		DurationRatio: 18000.0 / 18000000.0,
	}
	label, _, fires := partVsWhole(ex)
	if !fires || label != "unsure" {
		t.Fatalf("partVsWhole = (%q, fires=%v), want unsure/true", label, fires)
	}
}

// TestMissingFileGoesUnsure pins the missingFile rule to unsure on either side —
// file absence is evidence-free for dup-ness and can no longer emit not_dup.
func TestMissingFileGoesUnsure(t *testing.T) {
	for _, tc := range []struct {
		name string
		ex   database.LabeledExample
	}{
		{"side A missing", database.LabeledExample{A: database.BookFeatures{FilesExist: false}, B: database.BookFeatures{FilesExist: true}}},
		{"side B missing", database.LabeledExample{A: database.BookFeatures{FilesExist: true}, B: database.BookFeatures{FilesExist: false}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label, _, fires := missingFile(tc.ex)
			if !fires || label != "unsure" {
				t.Fatalf("missingFile = (%q, fires=%v), want unsure/true", label, fires)
			}
		})
	}
}

// TestPartVsWholeGenuineStillNotDup is the anti-over-suppression guard: a genuine
// part-vs-whole pair with disjoint identity (different ASINs, different paths, no
// version group, sane durations, ratio 0.3) is STILL not_dup — and a pair whose
// identity fields are all empty (unknown is non-disqualifying) likewise stays
// not_dup rather than being forced unsure.
func TestPartVsWholeGenuineStillNotDup(t *testing.T) {
	disjoint := database.LabeledExample{
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 36000, ASIN: "AAAAAAAAAA", PrimaryPath: "/lib/x/whole.m4b"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 10800, ASIN: "BBBBBBBBBB", PrimaryPath: "/lib/y/part.m4b"},
		DurationRatio: 10800.0 / 36000.0,
	}
	if label, _, fires := partVsWhole(disjoint); !fires || label != "not_dup" {
		t.Fatalf("disjoint identity: partVsWhole = (%q, fires=%v), want not_dup/true", label, fires)
	}
	if SharesIdentity(disjoint) {
		t.Fatal("disjoint identity must not report SharesIdentity")
	}

	bothEmpty := database.LabeledExample{
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 36000},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 10800},
		DurationRatio: 10800.0 / 36000.0,
	}
	if label, _, fires := partVsWhole(bothEmpty); !fires || label != "not_dup" {
		t.Fatalf("both-identity-empty: partVsWhole = (%q, fires=%v), want not_dup/true (unknown is non-disqualifying)", label, fires)
	}
	if SharesIdentity(bothEmpty) {
		t.Fatal("all-empty identity must not report SharesIdentity")
	}
}

// simPtr is a test helper for the *float64 LabeledExample.Similarity field.
func simPtr(v float64) *float64 { return &v }

// The following tests pin the same-title / high-similarity partVsWhole guard
// (2026-07-12). Each fixture is anchored on a real row pulled from the prod
// labeled-example export (/dedup/labels/export). The guard converts a same-title
// pair whose only negative evidence is a duration ratio into unsure — but only
// when it is NOT a compiled-in boilerplate ident and carries high candidate
// similarity, so it never over-suppresses the legitimate not_dup classes.

// TestPartVsWholeSameTitleHighSimGoesUnsure mirrors the real-book mislabels:
// e.g. "Foundation" (Asimov) at similarity 1.0 with a corrupt/part duration
// ratio (0.268) — same author, same work, but split into not_dup by the ratio.
// These must go unsure so dataset-backfill stops dismissing real duplicates.
func TestPartVsWholeSameTitleHighSimGoesUnsure(t *testing.T) {
	ex := database.LabeledExample{
		Layer:         "exact",
		Similarity:    simPtr(1.0),
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 82569, Title: "Foundation"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 22130, Title: "Foundation"},
		DurationRatio: 22130.0 / 82569.0,
	}
	label, reason, fires := partVsWhole(ex)
	if !fires || label != "unsure" {
		t.Fatalf("partVsWhole = (%q, fires=%v), want unsure/true; reason=%q", label, fires, reason)
	}
	if !strings.Contains(reason, "same title at high similarity") {
		t.Fatalf("reason should flag the same-work guard; got %q", reason)
	}
	// Full Classify path must also surface unsure (no earlier catcher steals it).
	if l, _, f := Classify(ex); !f || l != "unsure" {
		t.Fatalf("Classify = (%q, fires=%v), want unsure/true", l, f)
	}
}

// TestPartVsWholeBoilerplateIdentStaysNotDup is the anti-over-suppression carve-
// out: "Big Finish Ident" is embedding-identical to every copy of itself (sim
// 1.0) and hits the duration-ratio rule (0.483), but is a legitimate not_dup at
// the book level — the boilerplate exclusion must keep it not_dup (298 such
// pairs on prod, 2026-07-12).
func TestPartVsWholeBoilerplateIdentStaysNotDup(t *testing.T) {
	ex := database.LabeledExample{
		Layer:         "exact",
		Similarity:    simPtr(1.0),
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 8293, Title: "Big Finish Ident"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 4008, Title: "Big Finish Ident"},
		DurationRatio: 4008.0 / 8293.0,
	}
	label, reason, fires := partVsWhole(ex)
	if !fires || label != "not_dup" {
		t.Fatalf("boilerplate ident: partVsWhole = (%q, fires=%v), want not_dup/true; reason=%q", label, fires, reason)
	}
	if !strings.Contains(reason, "part vs whole") {
		t.Fatalf("reason should be the plain part-vs-whole not_dup; got %q", reason)
	}
}

// TestPartVsWholeSameTitleLowSimStaysNotDup pins the low-similarity path: a
// same-title pair whose candidate similarity is below the high-sim floor (e.g.
// the "The Improbable Adventures of Sherlock Holmes" anthology collision at
// 0.859) is a plausible genuine title collision and keeps not_dup — the guard
// requires positive high-similarity evidence before downgrading.
func TestPartVsWholeSameTitleLowSimStaysNotDup(t *testing.T) {
	ex := database.LabeledExample{
		Layer:         "embedding",
		Similarity:    simPtr(0.859),
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 40000, Title: "The Improbable Adventures of Sherlock Holmes"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 12000, Title: "The Improbable Adventures of Sherlock Holmes"},
		DurationRatio: 12000.0 / 40000.0,
	}
	if label, _, fires := partVsWhole(ex); !fires || label != "not_dup" {
		t.Fatalf("same-title low-sim: partVsWhole = (%q, fires=%v), want not_dup/true", label, fires)
	}
}

// TestPartVsWholeSameTitleUnknownSimStaysNotDup covers rows where Similarity is
// nil (unknown): the guard treats unknown as "not high" and stays not_dup.
func TestPartVsWholeSameTitleUnknownSimStaysNotDup(t *testing.T) {
	ex := database.LabeledExample{
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 36000, Title: "Foundation"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 10800, Title: "Foundation"},
		DurationRatio: 10800.0 / 36000.0,
	}
	if label, _, fires := partVsWhole(ex); !fires || label != "not_dup" {
		t.Fatalf("same-title unknown-sim: partVsWhole = (%q, fires=%v), want not_dup/true", label, fires)
	}
}

// TestPartVsWholeDifferentTitleHighSimStaysNotDup pins that the guard keys on
// TITLE identity, not similarity alone: a genuine part-vs-whole with DIFFERENT
// titles but high similarity remains not_dup.
func TestPartVsWholeDifferentTitleHighSimStaysNotDup(t *testing.T) {
	ex := database.LabeledExample{
		Layer:         "embedding",
		Similarity:    simPtr(0.97),
		A:             database.BookFeatures{FilesExist: true, TotalDurationSec: 36000, Title: "Foundation"},
		B:             database.BookFeatures{FilesExist: true, TotalDurationSec: 10800, Title: "Foundation and Empire"},
		DurationRatio: 10800.0 / 36000.0,
	}
	if label, _, fires := partVsWhole(ex); !fires || label != "not_dup" {
		t.Fatalf("different-title high-sim: partVsWhole = (%q, fires=%v), want not_dup/true", label, fires)
	}
}
