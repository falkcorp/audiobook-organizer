// file: internal/organizer/pathbuild.go
// version: 1.0.0
// guid: 2f7c4a19-6e58-4b03-9d21-8a05e3f16c74
// last-edited: 2026-08-15

// The single place a book's target path is computed.
//
// Until 2026-08-15 there were two, both live in production:
//
//	scheme #1  expandPattern + generateTargetPath, driven by
//	           folder_naming_pattern + file_naming_pattern
//	scheme #2  FormatPath + ComputeTargetPaths, driven by
//	           path_format + segment_title_format
//
// They disagreed. Under the production config of that date the same book got
// "Isaac Asimov/Foundation/Foundation (1951)/Foundation - Isaac Asimov.m4b"
// from one and "Isaac Asimov/Foundation - Foundation/Foundation.m4b" from the
// other -- four directory levels against two. Organize moved files toward the
// first, metadata-apply toward the second, and ReOrganizeInPlace is a true
// os.Rename, so a book could be dragged back and forth indefinitely.
//
// Unifying them was NOT a matter of keeping the better one, because neither was
// a superset. Each had grown the half its caller needed:
//
//	only #1: dropping " - " pattern segments whose placeholders are all empty,
//	         INCLUDING their connector words; erroring on unresolved
//	         placeholders; the quality vocabulary (publisher, language, edition,
//	         bitrate, codec, quality, isbn)
//	only #2: scrubbing every value BEFORE substitution so metadata cannot inject
//	         a path separator; per-component sanitization; the multi-file
//	         vocabulary (track, total_tracks, track_title, ext)
//
// The connector-word rule is the one that bites hardest if dropped: without it
// a missing narrator in "{title} - {author} - read by {narrator}" produced
// "Time Pebbles - read by Jerry Merritt", crediting the AUTHOR as the narrator.
// BuildPath keeps it, and path_builder_characterization_test.go fails if it
// ever stops keeping it.
//
// path_format and segment_title_format are gone. folder_naming_pattern and
// file_naming_pattern are the only path configuration.

package organizer

import (
	"fmt"
	"strings"
)

// PathVars is the union of both former variable sets. A caller fills in what it
// knows; anything left zero is treated as absent and its pattern segment is
// dropped rather than left half-substituted.
type PathVars struct {
	// Identity
	Author   string
	Title    string
	Series   string
	SeriesNumber string
	Narrator string

	// Quality / bibliographic (scheme #1 only, before unification)
	Publisher string
	Language  string
	Edition   string
	Codec     string
	Quality   string
	Bitrate   string
	ISBN10    string
	ISBN13    string
	PrintYear string

	// Multi-file structure (scheme #2 only, before unification).
	//
	// TrackTitle is the per-segment name for a book whose files are separate
	// tracks. Track/TotalTracks feed both {track}/{total_tracks} and the
	// generated TrackTitle when the caller does not supply one.
	TrackTitle  string
	Track       int
	TotalTracks int
	Ext         string
}

// BuildOpts carries the decisions that used to be hardcoded differently in each
// builder.
type BuildOpts struct {
	// AuthorFallback is substituted when Author is empty. Scheme #1 used
	// "Unknown Author"; scheme #2 collapsed the segment away entirely, which
	// dropped authorless books flat into the library root. Empty string keeps
	// the collapsing behaviour.
	AuthorFallback string

	// TitleFallback is substituted when Title is empty.
	TitleFallback string

	// SegmentTitleFormat generates TrackTitle when the caller leaves it empty
	// and Track > 0. Empty means DefaultSegmentTitleFormat.
	SegmentTitleFormat string
}

// replacements returns the placeholder -> value map, with fallbacks applied and
// every value already scrubbed of path separators.
//
// Scrubbing happens HERE, before substitution, and that ordering is the
// security property: the substituted result is later split on "/" to sanitize
// each component, so a title containing a separator would otherwise manufacture
// directory levels. See scrubVar.
func (v PathVars) replacements(opts BuildOpts) map[string]string {
	author := strings.TrimSpace(v.Author)
	if author == "" {
		author = opts.AuthorFallback
	}
	title := strings.TrimSpace(v.Title)
	if title == "" {
		title = opts.TitleFallback
	}

	trackTitle := v.TrackTitle
	if trackTitle == "" && v.Track > 0 {
		format := opts.SegmentTitleFormat
		if format == "" {
			format = DefaultSegmentTitleFormat
		}
		trackTitle = FormatSegmentTitle(format, title, v.Track, v.TotalTracks)
	}

	trackStr := ""
	totalStr := ""
	if v.Track > 0 {
		trackStr = fmt.Sprintf("%d", v.Track)
	}
	if v.TotalTracks > 0 {
		totalStr = fmt.Sprintf("%d", v.TotalTracks)
	}

	seriesPrefix := ""
	if s := strings.TrimSpace(v.Series); s != "" {
		seriesPrefix = s
		if v.SeriesNumber != "" {
			seriesPrefix += " " + v.SeriesNumber
		}
		seriesPrefix += " - "
	}

	raw := map[string]string{
		"{author}":          author,
		"{title}":           title,
		"{series}":          v.Series,
		"{series_number}":   v.SeriesNumber,
		"{series_position}": v.SeriesNumber,
		"{series_prefix}":   seriesPrefix,
		"{narrator}":        v.Narrator,
		"{publisher}":       v.Publisher,
		"{language}":        v.Language,
		"{lang}":            v.Language,
		"{edition}":         v.Edition,
		"{print_year}":      v.PrintYear,
		"{year}":            v.PrintYear,
		"{isbn10}":          v.ISBN10,
		"{isbn13}":          v.ISBN13,
		"{bitrate}":         v.Bitrate,
		"{codec}":           v.Codec,
		"{quality}":         v.Quality,
		"{track_title}":     trackTitle,
		"{track}":           trackStr,
		"{total_tracks}":    totalStr,
		"{ext}":             v.Ext,
	}

	out := make(map[string]string, len(raw))
	for k, val := range raw {
		out[k] = scrubVar(strings.TrimSpace(val))
	}
	return out
}

// BuildPath expands one naming pattern into one path component-set.
//
// The pattern may contain "/" to express directory levels; each resulting
// component is sanitized independently. The returned path is relative -- the
// caller joins it onto RootDir.
func BuildPath(pattern string, v PathVars, opts BuildOpts) (string, error) {
	// Normalize {Author} -> {author} so patterns are case-insensitive.
	result := placeholderNormalizeRegex.ReplaceAllStringFunc(pattern, strings.ToLower)

	replacements := v.replacements(opts)

	// Drop whole " - " segments whose placeholders are ALL empty, before any
	// substitution. This must run on the raw pattern: it is the only point at
	// which connector words ("read by", "narrated by") are still identifiable
	// as pattern text rather than as book metadata.
	empties := make(map[string]struct{}, len(replacements))
	for placeholder, value := range replacements {
		if strings.TrimSpace(value) == "" {
			empties[placeholder] = struct{}{}
		}
	}
	result = dropEmptyPatternSegments(result, empties)

	for placeholder, value := range replacements {
		if strings.TrimSpace(value) == "" {
			result = removeEmptySegment(result, placeholder)
			result = strings.ReplaceAll(result, placeholder, "")
		} else {
			result = strings.ReplaceAll(result, placeholder, value)
		}
	}

	result = cleanupPattern(result)

	if leftoverPlaceholderRegex.MatchString(result) {
		leftover := leftoverPlaceholderRegex.FindAllString(result, -1)
		return "", fmt.Errorf("naming pattern produced %q with unresolved placeholders %v — book is missing values for these fields, or the pattern references unknown placeholders", result, leftover)
	}

	result = CollapseEmptySegments(result)

	// Sanitize each path component independently.
	parts := strings.Split(result, "/")
	kept := parts[:0]
	for _, part := range parts {
		part = SanitizePathComponent(part)
		if part != "" {
			kept = append(kept, part)
		}
	}
	result = strings.Join(kept, "/")

	// Collapse "X - X" in the final component only (series name equal to title).
	if idx := strings.LastIndex(result, "/"); idx >= 0 {
		result = result[:idx+1] + collapseRedundantDup(result[idx+1:])
	} else {
		result = collapseRedundantDup(result)
	}

	return result, nil
}
