// file: internal/scanner/chapter_sequence_test.go
// version: 1.0.0
// guid: 3f1f0c55-6a0e-4f7d-9d0c-5b8a2e7c41d9
// last-edited: 2026-09-19

package scanner

import "testing"

// Title shapes seen on single-file records that are really one chapter of a
// multi-file book. The titles are the real SHAPES; the words are invented.
func TestParseSequenceMarker_Titles(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		shape string
		disc  int
		index int
		total int
		resid string
	}{
		{"157", true, SeqShapeBare, 0, 157, 0, ""},
		{"012", true, SeqShapeBare, 0, 12, 0, ""},
		{"0007", true, SeqShapeBare, 0, 7, 0, ""},
		{"93", true, SeqShapeBare, 0, 93, 0, ""},
		{"108 of 310", true, SeqShapeNofM, 0, 108, 310, ""},
		{"3/12", true, SeqShapeNofM, 0, 3, 12, ""},
		{"006_Head of the Wyrm", true, SeqShapePrefix, 0, 6, 0, "Head of the Wyrm"},
		{"102 - Moon and Stone", true, SeqShapePrefix, 0, 102, 0, "Moon and Stone"},
		{"05 Ch05  The Glass Nexus", true, SeqShapePrefix, 0, 5, 0, "Ch05  The Glass Nexus"},
		{"02.Prologue", true, SeqShapePrefix, 0, 2, 0, "Prologue"},
		{"2-05 The Hollow Yard", true, SeqShapeDiscTrack, 2, 5, 0, "The Hollow Yard"},
		{"1-01 Part 01", true, SeqShapeDiscTrack, 1, 1, 0, "Part 01"},
		{"40 Heir of Ash 40-65", true, SeqShapePrefix, 0, 40, 65, "Heir of Ash"},
		{"05 Nightnet - 5", true, SeqShapePrefix, 0, 5, 0, "Nightnet"},
		{"Part 3", true, SeqShapeToken, 0, 3, 0, ""},
		{"Chapter 12", true, SeqShapeToken, 0, 12, 0, ""},
		{"Track 07", true, SeqShapeToken, 0, 7, 0, ""},
		{"Chapter 4 - The Hunter", true, SeqShapeToken, 0, 4, 0, "The Hunter"},

		// Not sequence markers.
		{"The Glass Nexus", false, "", 0, 0, 0, ""},
		{"11/22/63", false, "", 0, 0, 0, ""},
		{"84K", false, "", 0, 0, 0, ""},
		{"185917", false, "", 0, 0, 0, ""},                         // six digits: a timestamp, not a chapter
		{"11.3 - Bigfoot Goes to College", false, "", 0, 0, 0, ""}, // decimal series position
		{"86-Neon", false, "", 0, 0, 0, ""},
		{"5d6", false, "", 0, 0, 0, ""},
		{"", false, "", 0, 0, 0, ""},
		{"Book 2", false, "", 0, 0, 0, ""}, // a series number names another book
	}
	for _, c := range cases {
		m, ok := ParseSequenceMarker(c.in)
		if ok != c.ok {
			t.Errorf("%q: ok=%v want %v (%+v)", c.in, ok, c.ok, m)
			continue
		}
		if !ok {
			continue
		}
		if m.Shape != c.shape || m.Disc != c.disc || m.Index != c.index || m.Total != c.total || m.Residual != c.resid {
			t.Errorf("%q: got %+v, want shape=%s disc=%d index=%d total=%d residual=%q",
				c.in, m, c.shape, c.disc, c.index, c.total, c.resid)
		}
	}
}

// Legit single books whose title starts with a number are still parsed as
// markers when they have a marker SHAPE ("1984" is a bare number); what keeps
// them out of a group is the grouping rule, tested in
// TestDetectChapterGroups_LegitNumericTitlesDoNotGroup.
func TestParseSequenceMarker_NumericBookTitles(t *testing.T) {
	for _, in := range []string{"1984", "2001: A Space Voyage", "09 - Ruins of the Deep"} {
		if _, ok := ParseSequenceMarker(in); !ok {
			t.Errorf("%q: want a marker shape (grouping, not parsing, rejects it)", in)
		}
	}
}

// File stems additionally carry the index as a trailing "Title - NNN".
func TestParseFilenameSequence(t *testing.T) {
	cases := []struct {
		in    string
		ok    bool
		index int
		resid string
	}{
		{"Eldritch - 157", true, 157, "Eldritch"},
		{"Moon and Stone - Jane Author - 010", true, 10, "Moon and Stone - Jane Author"},
		{"Hunter of Night - 93", true, 93, "Hunter of Night"},
		{"Child of the Dawn (Unabridged) - 009", true, 9, "Child of the Dawn (Unabridged)"},
		{"112_The Wide Sea", true, 112, "The Wide Sea"},
		{"002", true, 2, ""},
		{"Wheel of Ages 01", false, 0, ""}, // bare trailing number after a space: a series number
		{"The Novel", false, 0, ""},
		{"20190223 185917", false, 0, ""},
	}
	for _, c := range cases {
		m, ok := ParseFilenameSequence(c.in)
		if ok != c.ok || (ok && (m.Index != c.index || m.Residual != c.resid)) {
			t.Errorf("%q: got %+v ok=%v, want index=%d residual=%q ok=%v", c.in, m, ok, c.index, c.resid, c.ok)
		}
	}
}

func TestSequenceResidualKey(t *testing.T) {
	cases := map[string]string{
		"":                               "",
		"Prologue":                       "",
		"Opening Credits":                "",
		"Part 01":                        "",
		"Ch05  The Glass Nexus":          "the glass nexus",
		"Child of the Dawn (Unabridged)": "child of the dawn",
		"Moon and Stone":                 "moon and stone",
	}
	for in, want := range cases {
		if got := sequenceResidualKey(in); got != want {
			t.Errorf("sequenceResidualKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChapterFolderKey(t *testing.T) {
	cases := map[string]string{
		"/lib/A/06_Head of the Wyrm":            "head of the wyrm",
		"/lib/A/Prism - 5 - The Burning Glass":  "the burning glass",
		"/lib/A/Moon and Stone":                 "moon and stone",
		"/lib/A/Child of the Dawn (Unabridged)": "child of the dawn",
	}
	for in, want := range cases {
		if got := chapterFolderKey(in); got != want {
			t.Errorf("chapterFolderKey(%q) = %q, want %q", in, got, want)
		}
	}
}
