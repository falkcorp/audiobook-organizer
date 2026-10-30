// file: internal/organizer/pathbuild_test.go
// version: 1.1.0
// guid: 6b4e0d97-1a52-4c8f-93de-77c0a1e5b204
// last-edited: 2026-10-30

package organizer

import (
	"slices"
	"strings"
	"testing"
)

// Format specs ({track:02d}) were a scheme-#2-only feature that BuildPath lost
// in the first unification pass. They are not cosmetic: the leftover-placeholder
// guard treats an unexpanded "{track:02d}" as a broken pattern and BuildPath
// returns an ERROR, so a default file pattern containing one would fail every
// book in the library. These tests pin both halves of the behaviour the default
// depends on -- padded when the track exists, segment dropped when it does not.
func TestBuildPath_TrackFormatSpec(t *testing.T) {
	opts := BuildOpts{AuthorFallback: placeholderAuthor, TitleFallback: defaultTitle}

	tests := []struct {
		name    string
		pattern string
		vars    PathVars
		want    string
	}{
		{
			name:    "single-file book drops the track segment entirely",
			pattern: "{title} - {track:02d}",
			vars:    PathVars{Title: "Foundation"},
			want:    "Foundation",
		},
		{
			name:    "multi-file book zero-pads the track",
			pattern: "{title} - {track:02d}",
			vars:    PathVars{Title: "Foundation", Track: 3, TotalTracks: 12},
			want:    "Foundation - 03",
		},
		{
			name:    "bare {track} still works alongside a spec",
			pattern: "{title} - {track} of {total_tracks}",
			vars:    PathVars{Title: "Foundation", Track: 3, TotalTracks: 12},
			want:    "Foundation - 3 of 12",
		},
		{
			name:    "total_tracks accepts a spec too",
			pattern: "{title} - {track:02d} of {total_tracks:02d}",
			vars:    PathVars{Title: "Foundation", Track: 3, TotalTracks: 12},
			want:    "Foundation - 03 of 12",
		},
		{
			name:    "a spec inside a folder pattern survives the component split",
			pattern: "{author}/{title}/{track:03d}",
			vars:    PathVars{Author: "Isaac Asimov", Title: "Foundation", Track: 7, TotalTracks: 9},
			want:    "Isaac Asimov/Foundation/007",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BuildPath(tt.pattern, tt.vars, opts)
			if err != nil {
				t.Fatalf("BuildPath(%q) returned error: %v", tt.pattern, err)
			}
			if got != tt.want {
				t.Errorf("BuildPath(%q):\n  want: %q\n  got:  %q", tt.pattern, tt.want, got)
			}
		})
	}
}

// An unknown placeholder must still be an error -- restoring format specs must
// not turn the leftover guard into a no-op.
func TestBuildPath_UnknownPlaceholderStillErrors(t *testing.T) {
	_, err := BuildPath("{title} - {not_a_field}", PathVars{Title: "Foundation"}, BuildOpts{})
	if err == nil {
		t.Fatal("BuildPath with an unknown placeholder: want error, got nil")
	}
}

// A format spec on a field that does not take one is a pattern bug, not
// something to silently pass through into the path.
func TestBuildPath_FormatSpecOnNonNumericFieldErrors(t *testing.T) {
	_, err := BuildPath("{title:02d}", PathVars{Title: "Foundation"}, BuildOpts{})
	if err == nil {
		t.Fatal("BuildPath with a format spec on {title}: want error, got nil")
	}
}

// TestNamingPatternTokens_EveryTokenResolves is the guard behind the Settings
// help text: each token NamingPatternTokens advertises must come out of
// BuildPath as a real value on a fully populated book, never as literal braces
// and never as an unresolved-placeholder error. Without this the exported list
// could name a token the resolver cannot expand, which is the drift it replaced.
func TestNamingPatternTokens_EveryTokenResolves(t *testing.T) {
	v := PathVars{
		Author:       "Bruce Wayne",
		Title:        "The Long Halloween",
		Series:       "Gotham Chronicles",
		SeriesNumber: "3",
		Narrator:     "Alfred Pennyworth",
		Publisher:    "DC Audio",
		Language:     "English",
		Edition:      "Unabridged",
		PrintYear:    "1996",
		ISBN10:       "1401207529",
		ISBN13:       "9781401207526",
		Bitrate:      "64kbps",
		Codec:        "AAC",
		Quality:      "64kbps AAC",
		TrackTitle:   "Part One",
		Track:        2,
		TotalTracks:  13,
		Ext:          "m4b",
	}
	opts := BuildOpts{AuthorFallback: placeholderAuthor, TitleFallback: defaultTitle}

	tokens := NamingPatternTokens()
	if len(tokens) == 0 {
		t.Fatal("NamingPatternTokens returned nothing")
	}
	if !slices.IsSorted(tokens) {
		t.Errorf("tokens not sorted: %v", tokens)
	}
	for _, token := range []string{"{series_prefix}", "{edition_suffix}", "{title}", "{track}"} {
		if !slices.Contains(tokens, token) {
			t.Errorf("expected %s in %v", token, tokens)
		}
	}
	for _, phantom := range []string{"{track_number}", "{audiobook_release_year}"} {
		if slices.Contains(tokens, phantom) {
			t.Errorf("%s is advertised but BuildPath cannot resolve it", phantom)
		}
	}

	for _, token := range tokens {
		got, err := BuildPath(token, v, opts)
		if err != nil {
			t.Errorf("BuildPath(%q) error: %v", token, err)
			continue
		}
		if strings.TrimSpace(got) == "" || strings.ContainsAny(got, "{}") {
			t.Errorf("BuildPath(%q) = %q, want a resolved non-empty value", token, got)
		}
	}
}
