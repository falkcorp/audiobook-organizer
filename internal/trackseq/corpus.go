// file: internal/trackseq/corpus.go
// version: 1.0.0
// guid: c40f8b12-7e93-4d6a-a15c-93b7e2d80c41
// last-edited: 2026-08-24

package trackseq

// Corpus is the shared vocabulary every caller must agree on. It is exported so
// the scanner's and the repair classifier's own tests can assert THEIR public
// entry points against the same cases -- which is the control that was missing
// when the two implementations silently diverged.
//
// Total is 0 where the filename does not state one.
type Case struct {
	Stem  string
	Num   int
	Total int
	OK    bool
}

// Corpus deliberately mixes the two vocabularies that had drifted apart: the
// keyword-anchored forms the importer knew, and the bare leading/trailing forms
// the repair classifier knew.
var Corpus = []Case{
	// Keyword-anchored. These MUST outrank the looser forms below: a stem like
	// "Part 1 of 8" contains a trailing number too, and reading 8 instead of 1
	// makes every file in a folder sort identically.
	{"Chapter 01", 1, 0, true},
	{"chapter_05", 5, 0, true},
	{"Part 1 of 8", 1, 8, true},
	{"Part 3", 3, 0, true},
	{"Track 11", 11, 0, true},
	{"Disc 2", 2, 0, true},
	{"CD 03", 3, 0, true},
	{"Book Title (3 of 9)", 3, 9, true},
	{"Something (76/85)", 76, 85, true},
	{"Title 01 of 85", 1, 85, true},

	// Leading ordinal.
	{"01 - The Beginning", 1, 0, true},
	{"002. Chapter Two Arrives", 2, 0, true},
	{"7_intro", 7, 0, true},
	{"12", 12, 0, true},

	// TRAILING ordinal -- the form the importer could not read until 2026-08-24
	// and the repair classifier always could. This is the production case.
	{"Pratchett 001", 1, 0, true},
	{"Pratchett 080", 80, 0, true},
	{"Carpe Jugulum 03", 3, 0, true},
	{"Foo_12", 12, 0, true},
	{"The Hierarchies-7", 7, 0, true},

	// No sequence information at all.
	{"Introduction", 0, 0, false},
	{"", 0, 0, false},
	{"The Hierarchies", 0, 0, false},
}
