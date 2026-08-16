// file: internal/organizer/path_format_test.go
// version: 2.0.0
// guid: a7b3c1d2-e4f5-6789-abcd-ef0123456f01
// last-edited: 2026-08-15

package organizer

import (
	"strings"
	"testing"
)

// The folder + file pattern pair equivalent to the old DefaultPathFormat
// ("{author}/{series_prefix}{title}/{track_title}"). Retargeted from FormatPath,
// which is deleted — the prod bug these tests pin is a property of whatever
// builder is live, not of the function that happened to carry it in May.
const (
	testFolderPattern = "{author}/{series_prefix}{title}"
	testFilePattern   = "{track_title}"
)

// TestBuildPath_SlashInVariableDoesNotCreateDirectory exercises the prod
// bug from 2026-05-28: book 01KQGDQTJ44FCAPW5Z9D2KNQDE had its 85-chapter
// audiobook split into 85 single-file books because the Title metadata
// contained a "/" which the path formatter passed through unescaped,
// turning into a real directory boundary on disk.
//
// Worth stating plainly: until 2026-08-15 this test only ever guarded
// internal/organizer. The LIVE metadata-apply path ran a hand-copied twin in
// internal/metafetch that had no scrubVar at all, so the bug this test pins was
// never actually fixed on the path that renames books after an apply. The twin
// is gone and both paths run the builder tested here.
func TestBuildPath_SlashInVariableDoesNotCreateDirectory(t *testing.T) {
	cases := []struct {
		name string
		vars PathVars
	}{
		{
			name: "title contains slash",
			vars: PathVars{
				Author:      "James Luceno",
				Title:       "Tarkin - Star Wars - 3/85", // <-- the killer
				Ext:         "mp3",
				Track:       3,
				TotalTracks: 85,
			},
		},
		{
			name: "series contains slash",
			vars: PathVars{
				Author: "Test Author",
				Series: "Foo/Bar Series",
				Title:  "Book One",
				Ext:    "mp3",
			},
		},
		{
			name: "author contains slash",
			vars: PathVars{
				Author: "First / Last",
				Title:  "Title",
				Ext:    "mp3",
			},
		},
		{
			name: "narrator contains slash",
			vars: PathVars{
				Author:   "Test Author",
				Title:    "Title",
				Narrator: "Reader A / Reader B",
				Ext:      "mp3",
			},
		},
		{
			name: "title begins with dot (hidden file)",
			vars: PathVars{
				Author: "Test Author",
				Title:  ".hidden",
				Ext:    "mp3",
			},
		},
	}

	// The template has exactly two separators: one inside the folder pattern,
	// one joining folder to file. Any extra "/" means a variable leaked one.
	const templateSlashes = 2

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := BuildRelPath(testFolderPattern, testFilePattern, tc.vars, BuildOpts{})
			if err != nil {
				t.Fatalf("BuildRelPath: %v", err)
			}
			if gotSlashes := strings.Count(got, "/"); gotSlashes != templateSlashes {
				t.Errorf("BuildRelPath(%+v) = %q\n  has %d '/' separators; template has %d.\n  A variable value leaked a path separator into the result.",
					tc.vars, got, gotSlashes, templateSlashes)
			}
		})
	}
}

// TestBuildPath_TarkinReproducesAsOneFile reproduces the exact prod path that
// would have been written by the buggy code, and confirms the scrubbed result
// lands in ONE directory, not 85.
func TestBuildPath_TarkinReproducesAsOneFile(t *testing.T) {
	vars := PathVars{
		Author:       "James Luceno",
		Series:       "Star Wars",
		SeriesNumber: "24",
		Title:        "Tarkin",
		Track:        3,
		TotalTracks:  85,
		Ext:          "mp3",
	}
	got, err := BuildRelPath(testFolderPattern, testFilePattern, vars, BuildOpts{})
	if err != nil {
		t.Fatalf("BuildRelPath: %v", err)
	}

	// Expected: James Luceno/Star Wars 24 - Tarkin/Tarkin - 3_85
	// Each segment is one path component; the "3_85" uses underscore (the
	// segment-title default), so no rogue directory.
	if strings.HasSuffix(got, "/85") {
		t.Fatalf("BuildRelPath emitted %q — a '/85' suffix means the per-track total leaked as a directory separator", got)
	}
	if strings.Count(got, "/") != 2 {
		t.Fatalf("BuildRelPath emitted %q — expected exactly 2 path separators (author/, title-folder/), got %d", got, strings.Count(got, "/"))
	}
}

func TestScrubVar(t *testing.T) {
	cases := map[string]string{
		"":               "",
		"normal":         "normal",
		"with/slash":     "with slash",
		"back\\slash":    "back slash",
		"both/and\\both": "both and both",
		".hidden":        "hidden",
		"..parent":       "parent",
		"....many":       "many",
		"trailing.":      "trailing.",
		"middle.dot.ok":  "middle.dot.ok",
	}
	for in, want := range cases {
		if got := scrubVar(in); got != want {
			t.Errorf("scrubVar(%q) = %q; want %q", in, got, want)
		}
	}
}
