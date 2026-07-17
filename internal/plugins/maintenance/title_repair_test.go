// file: internal/plugins/maintenance/title_repair_test.go
// version: 1.0.0
// guid: 1ca153f8-f8b3-40d5-871c-7959e39314ea
// last-edited: 2026-07-17

package maintenance

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// agreedStub returns a fixed AgreedChapterTitle result and records whether it
// was called — provenance/single-file gates must skip the (expensive) tag read.
func agreedStub(agreed string, multi bool, called *bool) func(string) (string, bool) {
	return func(string) (string, bool) {
		if called != nil {
			*called = true
		}
		return agreed, multi
	}
}

// trStrPtr is a task-unique string-pointer helper (strPtr already exists in
// auto_match_transcribed_test.go in this package).
func trStrPtr(s string) *string { return &s }

func TestDecideTitleRepair_RetitleCase(t *testing.T) {
	in := titleRepairBook{
		Title:     "nobody103 (Jack Voraces)", // per-chapter tag residue
		FilePaths: []string{"/lib/mother-of-learning/ch1.mp3", "/lib/mother-of-learning/ch2.mp3"},
	}
	d := decideTitleRepair(in, agreedStub("Mother of Learning", true, nil))
	if d.Action != actionRetitle {
		t.Fatalf("Action = %v, want actionRetitle", d.Action)
	}
	if d.NewTitle != "Mother of Learning" {
		t.Fatalf("NewTitle = %q, want %q", d.NewTitle, "Mother of Learning")
	}
	if d.MixedDir {
		t.Fatalf("MixedDir = true for same-dir files")
	}
	if d.DirPath != "/lib/mother-of-learning" {
		t.Fatalf("DirPath = %q", d.DirPath)
	}
}

func TestDecideTitleRepair_SingleFileSkip(t *testing.T) {
	called := false
	in := titleRepairBook{
		Title:     "Some Book",
		FilePaths: []string{"/lib/book/book.m4b"},
	}
	d := decideTitleRepair(in, agreedStub("Other", true, &called))
	if d.Action != actionSkipSingleFile {
		t.Fatalf("Action = %v, want actionSkipSingleFile", d.Action)
	}
	if called {
		t.Fatalf("agreedFn called for single-file book — must be gated before tag reads")
	}
}

func TestDecideTitleRepair_SoftDeleteSkip(t *testing.T) {
	called := false
	in := titleRepairBook{
		Title:             "Some Book",
		MarkedForDeletion: true,
		FilePaths:         []string{"/lib/b/1.mp3", "/lib/b/2.mp3"},
	}
	d := decideTitleRepair(in, agreedStub("Other", true, &called))
	if d.Action != actionSkipDeleted {
		t.Fatalf("Action = %v, want actionSkipDeleted", d.Action)
	}
	if called {
		t.Fatalf("agreedFn called for soft-deleted book")
	}
}

func TestDecideTitleRepair_ProvenanceSkips(t *testing.T) {
	paths := []string{"/lib/b/1.mp3", "/lib/b/2.mp3"}
	cases := []struct {
		name string
		in   titleRepairBook
	}{
		{"override value", titleRepairBook{Title: "T", FilePaths: paths,
			TitleState: &database.MetadataFieldState{Field: "title", OverrideValue: trStrPtr(`"Custom"`)}}},
		{"override locked", titleRepairBook{Title: "T", FilePaths: paths,
			TitleState: &database.MetadataFieldState{Field: "title", OverrideLocked: true}}},
		{"fetched value", titleRepairBook{Title: "T", FilePaths: paths,
			TitleState: &database.MetadataFieldState{Field: "title", FetchedValue: trStrPtr(`"Fetched"`)}}},
		{"book metadata source", titleRepairBook{Title: "T", FilePaths: paths,
			MetadataSource: "audible"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			d := decideTitleRepair(tc.in, agreedStub("Other", true, &called))
			if d.Action != actionSkipProvenance {
				t.Fatalf("Action = %v, want actionSkipProvenance", d.Action)
			}
			if called {
				t.Fatalf("agreedFn called despite provenance guard")
			}
			if d.Reason == "" {
				t.Fatalf("Reason empty — skip reasons must be loggable")
			}
		})
	}
}

func TestDecideTitleRepair_NoAgreementSkip(t *testing.T) {
	in := titleRepairBook{
		Title:     "Some Book",
		FilePaths: []string{"/lib/b/1.mp3", "/lib/b/2.mp3"},
	}
	// Chapters disagree → agreed == "".
	if d := decideTitleRepair(in, agreedStub("", true, nil)); d.Action != actionSkipNoAgreement {
		t.Fatalf("disagreeing chapters: Action = %v, want actionSkipNoAgreement", d.Action)
	}
	// Directory holds <2 audio files on disk → multi == false.
	if d := decideTitleRepair(in, agreedStub("X", false, nil)); d.Action != actionSkipNoAgreement {
		t.Fatalf("!multi: Action = %v, want actionSkipNoAgreement", d.Action)
	}
}

// TestDecideTitleRepair_Idempotent proves the second run is a no-op: after a
// retitle, agreed == stored (case-insensitively) → actionSkipTitleOK.
func TestDecideTitleRepair_Idempotent(t *testing.T) {
	in := titleRepairBook{
		Title:     "Mother of Learning",
		FilePaths: []string{"/lib/b/1.mp3", "/lib/b/2.mp3"},
	}
	if d := decideTitleRepair(in, agreedStub("Mother of Learning", true, nil)); d.Action != actionSkipTitleOK {
		t.Fatalf("exact match: Action = %v, want actionSkipTitleOK", d.Action)
	}
	if d := decideTitleRepair(in, agreedStub("MOTHER OF LEARNING", true, nil)); d.Action != actionSkipTitleOK {
		t.Fatalf("case-insensitive match: Action = %v, want actionSkipTitleOK", d.Action)
	}
}

func TestDecideTitleRepair_MixedDirUsesMajority(t *testing.T) {
	in := titleRepairBook{
		Title: "wrong",
		FilePaths: []string{
			"/lib/majority/1.mp3",
			"/lib/majority/2.mp3",
			"/lib/stray/3.mp3",
		},
	}
	var gotDir string
	d := decideTitleRepair(in, func(dir string) (string, bool) {
		gotDir = dir
		return "Right Title", true
	})
	if !d.MixedDir {
		t.Fatalf("MixedDir = false for files spanning two dirs")
	}
	if gotDir != "/lib/majority" {
		t.Fatalf("agreement checked against %q, want majority dir /lib/majority", gotDir)
	}
	if d.Action != actionRetitle || d.NewTitle != "Right Title" {
		t.Fatalf("Action=%v NewTitle=%q", d.Action, d.NewTitle)
	}
}

func TestMajorityDir(t *testing.T) {
	dir, mixed := majorityDir([]string{"/a/1.mp3", "/a/2.mp3", "/b/3.mp3"})
	if dir != "/a" || !mixed {
		t.Fatalf("majorityDir = (%q, %v), want (/a, true)", dir, mixed)
	}
	dir, mixed = majorityDir([]string{"/a/1.mp3", "/a/2.mp3"})
	if dir != "/a" || mixed {
		t.Fatalf("majorityDir = (%q, %v), want (/a, false)", dir, mixed)
	}
	// Deterministic tie-break: lexicographically smallest dir wins.
	dir, mixed = majorityDir([]string{"/b/1.mp3", "/a/2.mp3"})
	if dir != "/a" || !mixed {
		t.Fatalf("tie: majorityDir = (%q, %v), want (/a, true)", dir, mixed)
	}
}

func TestTitleRepairWorkers_Cap(t *testing.T) {
	n := titleRepairWorkers()
	if n < 1 || n > 8 {
		t.Fatalf("titleRepairWorkers() = %d, want 1..8", n)
	}
}
