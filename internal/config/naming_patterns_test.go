// file: internal/config/naming_patterns_test.go
// version: 1.0.0
// guid: 3a97c05e-6b21-4d8f-9e4a-1c60b7d3f582
// last-edited: 2026-08-16

package config

import (
	"strings"
	"testing"
)

func TestValidateNamingPatterns(t *testing.T) {
	const okFolder = "{author}/{series}/{title} ({print_year})"

	cases := []struct {
		name          string
		folder, file  string
		wantErr       bool
		wantSubstring string
	}{
		{
			name:   "the current default",
			folder: okFolder,
			file:   "{title} - {track:02d}",
		},
		{
			name:   "track_title also varies per track",
			folder: okFolder,
			file:   "{track_title}",
		},
		{
			name:   "bare track placeholder",
			folder: okFolder,
			file:   "{title} - {track}",
		},
		{
			// The exact production value on 2026-08-16, and a shipped default
			// before c54721c7. Reasonable-looking, fine for a single-file book,
			// and it stranded 35.2 GB of multi-file books.
			name:          "no per-track placeholder collides",
			folder:        okFolder,
			file:          "{title} - {author} - read by {narrator}",
			wantErr:       true,
			wantSubstring: "every file of a multi-file book would expand to the same name",
		},
		{
			// The segment_title_format shape, written into the file pattern.
			name:          "separator in the file pattern",
			folder:        okFolder,
			file:          "{title} - {track}/{total_tracks}",
			wantErr:       true,
			wantSubstring: "contains a path separator",
		},
		{
			name:          "backslash in the file pattern",
			folder:        okFolder,
			file:          `{title}\{track:02d}`,
			wantErr:       true,
			wantSubstring: "contains a path separator",
		},
		{
			// Separators are what the folder pattern is FOR.
			name:   "separators in the folder pattern are structure",
			folder: "{author}/{series}/{title}",
			file:   "{title} - {track:02d}",
		},
		{
			// "Empty means unset, viper supplies the default" is the convention
			// the rest of Validate follows; erroring here would reject every
			// partially-populated Config.
			name:   "unset file pattern is not an error",
			folder: okFolder,
			file:   "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateNamingPatterns(tc.folder, tc.file)

			if tc.wantErr && len(errs) == 0 {
				t.Fatalf("validateNamingPatterns(%q, %q) accepted a pattern that strands files", tc.folder, tc.file)
			}
			if !tc.wantErr && len(errs) > 0 {
				t.Fatalf("validateNamingPatterns(%q, %q) rejected a valid pattern: %v", tc.folder, tc.file, errs)
			}
			if tc.wantSubstring != "" {
				joined := strings.Join(errs, " | ")
				if !strings.Contains(joined, tc.wantSubstring) {
					t.Errorf("error did not explain the failure.\n  got:  %s\n  want substring: %q", joined, tc.wantSubstring)
				}
			}
		})
	}
}

// TestValidate_RejectsTheProductionPatternThatStrandedFiles wires the check
// through the real Config.Validate rather than calling the helper directly --
// a validation rule that is not reachable from Validate protects nothing.
func TestValidate_RejectsTheProductionPatternThatStrandedFiles(t *testing.T) {
	cfg := &Config{
		DatabaseType:        "pebble",
		FolderNamingPattern: DefaultFolderNamingPattern,
		FileNamingPattern:   "{title} - {author} - read by {narrator}",
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("Config.Validate() accepted the file pattern that stranded 2,584 files in production")
	}
	// Validate aggregates every problem it finds, and this bare Config has
	// other ones (unset paths). Assert on the message that matters rather than
	// on Validate failing at all, which it would do regardless.
	if !strings.Contains(err.Error(), "multi-file book") {
		t.Errorf("Validate() failed, but not for the reason under test: %v", err)
	}
}

// TestValidate_DefaultsAreValid guards against a rule that rejects the
// product's own shipped configuration.
func TestValidate_DefaultsAreValid(t *testing.T) {
	for _, e := range validateNamingPatterns(DefaultFolderNamingPattern, DefaultFileNamingPattern) {
		t.Errorf("the shipped defaults fail their own validation: %s", e)
	}
}
