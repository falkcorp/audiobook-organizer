// file: internal/config/naming_patterns.go
// version: 1.0.1
// guid: 8e4b7f21-c390-4a6d-b158-7d02f9ac4e63
// last-edited: 2026-09-02

// Validation for the two naming patterns that decide where every file in the
// library lives.
//
// Both rules here exist because the config that violated them shipped, ran for
// months, and stranded 35.2 GB across 82 books -- 77 of which were left with no
// other copy on disk. Neither failure announced itself: organizing "succeeded",
// and the files simply were not where anything looked for them.

package config

import (
	"fmt"
	"strings"
)

// The shipped naming defaults, declared ONCE.
//
// They used to be written out by hand in two places -- viper.SetDefault and
// ResetToDefaults -- with a comment on one of them reading "Must stay in step
// with the FileNamingPattern default below". A comment is not a mechanism, and
// the pair that mattered most is exactly the pair that drifted: the default was
// corrected in c54721c7 while production kept a stored copy of the old one.
//
// "{track:02d}" serves BOTH book layouts, which is what makes it safe as a
// default. A single-file book has no track, so BuildPath drops the whole " - "
// segment and the file is "Foundation.m4b". A multi-file book gets
// "Foundation - 01.m4b", zero-padded so any file manager sorts it correctly.
const (
	DefaultFolderNamingPattern = "{author}/{series}/{title} ({print_year})"
	DefaultFileNamingPattern   = "{title} - {track:02d}"
)

// trackPlaceholders are the placeholders that vary between the files of one
// multi-file book. A file pattern containing none of them is constant per book.
var trackPlaceholders = []string{"{track}", "{track_title}", "{track:"}

// validateNamingPatterns rejects the two shapes of naming pattern that silently
// destroy data rather than failing.
func validateNamingPatterns(_ /* folderPattern */, filePattern string) []string {
	var errs []string

	if strings.TrimSpace(filePattern) == "" {
		// Unset. viper supplies DefaultFileNamingPattern, which is itself
		// validated by TestValidate_DefaultsAreValid. Treating "" as an error
		// would reject every partially-populated Config, and "empty means
		// unset" is the convention the rest of Validate already follows.
		return errs
	}

	// 1. A separator in the FILE pattern manufactures a directory.
	//
	// The folder pattern is allowed -- indeed expected -- to contain "/",
	// because that is how it expresses directory levels. The file pattern
	// names exactly one component, so a separator there is never structure.
	// It produced directories like "Pink Bean Series - 1/" holding a single
	// orphan "9.m4b", one per track.
	//
	// BuildRelPath now collapses such a separator rather than obeying it, so
	// this check is about telling the operator their pattern is wrong at the
	// moment they set it, instead of quietly renaming their books for them.
	if strings.ContainsAny(filePattern, `/\`) {
		errs = append(errs, fmt.Sprintf(
			"file_naming_pattern %q contains a path separator; it names a single file, not a directory path — put directory levels in folder_naming_pattern instead",
			filePattern))
	}

	// 2. A file pattern with no per-track placeholder collides.
	//
	// This is the one that actually caused the damage, and it looks
	// completely reasonable: "{title} - {author} - read by {narrator}" was a
	// shipped default. It is fine for a single-file book and catastrophic for
	// a multi-file one, because every track expands to the SAME name. The
	// first file lands, and every subsequent file finds its target occupied
	// and is left behind as "<name>.tmp-rename-<nonce>".
	//
	// "{track:02d}" is the pattern that serves both layouts: a single-file
	// book has no track, so the whole " - " segment drops and the file is
	// "Foundation.m4b"; a multi-file book gets "Foundation - 01.m4b".
	if !containsTrackPlaceholder(filePattern) {
		errs = append(errs, fmt.Sprintf(
			"file_naming_pattern %q contains no {track}, {track:02d} or {track_title} placeholder; every file of a multi-file book would expand to the same name and all but the first would be stranded — use something like \"{title} - {track:02d}\"",
			filePattern))
	}

	return errs
}

// containsTrackPlaceholder reports whether the pattern varies per track.
// "{track:" matches the format-spec forms ({track:02d}) without enumerating
// every spec.
func containsTrackPlaceholder(pattern string) bool {
	lower := strings.ToLower(pattern)
	for _, p := range trackPlaceholders {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}
