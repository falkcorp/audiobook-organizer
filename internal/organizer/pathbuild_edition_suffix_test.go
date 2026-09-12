// file: internal/organizer/pathbuild_edition_suffix_test.go
// version: 1.0.0
// guid: d4902b3b-9c42-42c1-8561-d349bff5821d
// last-edited: 2026-09-12

package organizer

import (
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// Adding a key to the replacement map is not automatically inert: BuildPath
// walks the WHOLE map for every pattern, running removeEmptySegment and a
// ReplaceAll for each empty placeholder. These golden values were produced by
// the pathbuild.go that predates {edition_suffix} and must stay byte-identical
// -- a pattern that does not use the new token must not move a single file.
func TestBuildPath_EditionSuffix_ExistingPatternsUnchanged(t *testing.T) {
	opts := BuildOpts{AuthorFallback: placeholderAuthor, TitleFallback: defaultTitle}

	full := PathVars{
		Author:       "Frank Herbert",
		Title:        "Dune",
		Series:       "Dune Chronicles",
		SeriesNumber: "1",
		Narrator:     "Scott Brick",
		PrintYear:    "1965",
		Edition:      "Unabridged",
	}

	relTests := []struct {
		name string
		vars PathVars
		want string
	}{
		{
			name: "default patterns, series and edition, single file",
			vars: full,
			want: "Frank Herbert/Dune Chronicles/Dune (1965)/Dune",
		},
		{
			name: "default patterns, no series, no edition, no year",
			vars: PathVars{Author: "Frank Herbert", Title: "Dune"},
			want: "Frank Herbert/Dune/Dune",
		},
		{
			name: "default patterns, multi-file book with edition",
			vars: func() PathVars { v := full; v.Track, v.TotalTracks = 3, 12; return v }(),
			want: "Frank Herbert/Dune Chronicles/Dune (1965)/Dune - 03",
		},
	}
	for _, tt := range relTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildRelPath(config.DefaultFolderNamingPattern, config.DefaultFileNamingPattern, tt.vars, opts)
			if err != nil {
				t.Fatalf("BuildRelPath: %v", err)
			}
			if got != tt.want {
				t.Errorf("BuildRelPath = %q, want %q", got, tt.want)
			}
		})
	}

	noEdition := full
	noEdition.Edition = ""
	noNarrator := full
	noNarrator.Narrator = ""

	pathTests := []struct {
		name    string
		pattern string
		vars    PathVars
		want    string
	}{
		{"raw {edition} in parens, present", "{title} ({edition})", full, "Dune (Unabridged)"},
		{"raw {edition} in parens, absent", "{title} ({edition})", noEdition, "Dune"},
		{"raw {edition} as a dash segment, absent", "{title} - {edition}", noEdition, "Dune"},
		{"series_prefix pattern", "{author}/{series_prefix}{title}", full, "Frank Herbert/Dune Chronicles 1 - Dune"},
		{"connector words drop with their segment", "{title} - {author} - read by {narrator}", noNarrator, "Dune - Frank Herbert"},
	}
	for _, tt := range pathTests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPath(tt.pattern, tt.vars, opts)
			if err != nil {
				t.Fatalf("BuildPath(%q): %v", tt.pattern, err)
			}
			if got != tt.want {
				t.Errorf("BuildPath(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

func TestBuildPath_EditionSuffix_Present(t *testing.T) {
	opts := BuildOpts{AuthorFallback: placeholderAuthor, TitleFallback: defaultTitle}
	vars := PathVars{Author: "Frank Herbert", Title: "Dune", Series: "Dune Chronicles", PrintYear: "1965", Edition: "Unabridged"}

	tests := []struct {
		name    string
		pattern string
		want    string
	}{
		{"bare suffix", "{title}{edition_suffix}", "Dune (Unabridged)"},
		{"placeholder case is normalized", "{Title}{Edition_Suffix}", "Dune (Unabridged)"},
		{"suffix before print year", "{author}/{series}/{title}{edition_suffix} ({print_year})", "Frank Herbert/Dune Chronicles/Dune (Unabridged) (1965)"},
		{"suffix ends a folder component", "{author}/{title}{edition_suffix}/{series}", "Frank Herbert/Dune (Unabridged)/Dune Chronicles"},
		{"both {edition} and {edition_suffix} resolve independently", "{title}{edition_suffix} - {edition}", "Dune (Unabridged) - Unabridged"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPath(tt.pattern, vars, opts)
			if err != nil {
				t.Fatalf("BuildPath(%q): %v", tt.pattern, err)
			}
			if got != tt.want {
				t.Errorf("BuildPath(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

// The anti-over-suppression half: with no edition the token must vanish
// completely -- no dangling space, no "()", no double space, no trailing
// " -" or trailing space on a folder name. "/" and "..." are included because
// scrubVar turns them into " " and "" respectively AFTER TrimSpace ran.
func TestBuildPath_EditionSuffix_Empty(t *testing.T) {
	opts := BuildOpts{AuthorFallback: placeholderAuthor, TitleFallback: defaultTitle}

	tests := []struct {
		name    string
		pattern string
		vars    PathVars
		want    string
	}{
		{"bare suffix, no edition", "{title}{edition_suffix}", PathVars{Title: "Dune"}, "Dune"},
		{"whitespace-only edition", "{title}{edition_suffix}", PathVars{Title: "Dune", Edition: "   "}, "Dune"},
		{"edition that scrubs to a space", "{title}{edition_suffix}", PathVars{Title: "Dune", Edition: "/"}, "Dune"},
		{"edition that scrubs to nothing", "{title}{edition_suffix}", PathVars{Title: "Dune", Edition: "..."}, "Dune"},
		{"no double space before print year", "{author}/{series}/{title}{edition_suffix} ({print_year})", PathVars{Author: "Frank Herbert", Title: "Dune", Series: "Dune Chronicles", PrintYear: "1965"}, "Frank Herbert/Dune Chronicles/Dune (1965)"},
		{"no trailing space when print year is also absent", "{author}/{title}{edition_suffix} ({print_year})", PathVars{Author: "Frank Herbert", Title: "Dune"}, "Frank Herbert/Dune"},
		{"folder component ends cleanly", "{author}/{title}{edition_suffix}/{series}", PathVars{Author: "Frank Herbert", Title: "Dune", Series: "Dune Chronicles"}, "Frank Herbert/Dune/Dune Chronicles"},
		{"dash segment holding only the suffix is dropped", "{title} - {edition_suffix}", PathVars{Title: "Dune"}, "Dune"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPath(tt.pattern, tt.vars, opts)
			if err != nil {
				t.Fatalf("BuildPath(%q): %v", tt.pattern, err)
			}
			if got != tt.want {
				t.Errorf("BuildPath(%q) = %q, want %q", tt.pattern, got, tt.want)
			}
		})
	}
}

// The motivating case: two editions of one title with the same print year
// computed one target under the default pattern, so the second never got
// organized (ErrTargetOccupied). With the token they diverge.
func TestBuildRelPath_EditionSuffix_SeparatesEditions(t *testing.T) {
	opts := BuildOpts{AuthorFallback: placeholderAuthor, TitleFallback: defaultTitle}
	folder := "{author}/{series}/{title}{edition_suffix} ({print_year})"

	base := PathVars{Author: "Frank Herbert", Title: "Dune", PrintYear: "1965"}
	unabridged, abridged := base, base
	unabridged.Edition = "Unabridged"
	abridged.Edition = "Abridged"

	a, err := BuildRelPath(folder, config.DefaultFileNamingPattern, unabridged, opts)
	if err != nil {
		t.Fatalf("unabridged: %v", err)
	}
	b, err := BuildRelPath(folder, config.DefaultFileNamingPattern, abridged, opts)
	if err != nil {
		t.Fatalf("abridged: %v", err)
	}
	if a == b {
		t.Fatalf("two editions share a target path %q", a)
	}
	if want := "Frank Herbert/Dune (Unabridged) (1965)/Dune"; a != want {
		t.Errorf("unabridged = %q, want %q", a, want)
	}
	if want := "Frank Herbert/Dune (Abridged) (1965)/Dune"; b != want {
		t.Errorf("abridged = %q, want %q", b, want)
	}
}
