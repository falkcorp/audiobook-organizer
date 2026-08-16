// file: internal/organizer/pathbuild.go
// version: 1.1.0
// guid: 2f7c4a19-6e58-4b03-9d21-8a05e3f16c74
// last-edited: 2026-08-16

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
	"regexp"
	"strings"
)

// formatSpecPattern matches a placeholder carrying a printf-style format spec,
// e.g. "{track:02d}". Note that placeholderNormalizeRegex (\{[A-Za-z_]+\})
// deliberately does NOT match these -- a spec'd placeholder skips the
// case-normalizing pass, so expandFormatSpecs lowercases the name itself.
var formatSpecPattern = regexp.MustCompile(`\{([A-Za-z_]+):([^}]+)\}`)

// numericSpecPattern is the only spec shape accepted: a (possibly zero-padded)
// decimal. Anything else would reach fmt.Sprintf as an arbitrary verb and could
// emit "%!(BADVERB)" straight into a filename.
var numericSpecPattern = regexp.MustCompile(`^0?\d*d$`)

// expandFormatSpecs resolves "{track:02d}"-style placeholders.
//
// This runs BEFORE the empty-segment pass, and the ordering is load-bearing in
// both directions:
//
//   - After it, a present track is a plain number, so the segment reads as
//     non-empty and survives.
//   - When the track is ABSENT the spec collapses back to the bare "{track}",
//     which IS a key in the replacement map, so dropEmptyPatternSegments can
//     drop its whole " - " segment. Emitting "00" here instead would give every
//     single-file book a "- 00" suffix, which is exactly the shape of bug that
//     makes a default pattern unusable for one of the two book layouts.
//
// A spec on a field that does not take one is left untouched on purpose: the
// leftover-placeholder guard at the end of BuildPath then reports it as the
// pattern bug it is, rather than this function inventing a second error path.
func expandFormatSpecs(pattern string, v PathVars) (string, error) {
	var specErr error

	out := formatSpecPattern.ReplaceAllStringFunc(pattern, func(match string) string {
		parts := formatSpecPattern.FindStringSubmatch(match)
		name, spec := strings.ToLower(parts[1]), parts[2]

		var value int
		switch name {
		case "track":
			value = v.Track
		case "total_tracks":
			value = v.TotalTracks
		default:
			return match
		}

		if !numericSpecPattern.MatchString(spec) {
			specErr = fmt.Errorf("naming pattern contains unsupported format spec %q in %q — only decimal specs such as {track:02d} are supported", spec, match)
			return match
		}
		if value <= 0 {
			return "{" + name + "}"
		}
		return fmt.Sprintf("%"+spec, value)
	})

	if specErr != nil {
		return "", specErr
	}
	return out, nil
}

// BuildRelPath composes the folder pattern and the file pattern into ONE
// library-relative path, extension excluded -- the caller appends that, because
// only the caller knows which file of a multi-file book it is naming.
//
// Every target path in the codebase goes through here. That is the whole point:
// the organize path and the metadata-apply path used to compose their own, and
// they composed them differently (four directory levels against two), so each
// one dragged files back toward its own answer forever.
//
// The two patterns are expanded SEPARATELY rather than joined into
// "folder/file" first. dropEmptyPatternSegments works on " - "-delimited
// segments, and a joined pattern would let a folder segment and a file segment
// merge into one -- an absent series could then swallow part of the filename.
func BuildRelPath(folderPattern, filePattern string, v PathVars, opts BuildOpts) (string, error) {
	folder, err := BuildPath(folderPattern, v, opts)
	if err != nil {
		return "", fmt.Errorf("folder pattern: %w", err)
	}

	stem, err := BuildPath(filePattern, v, opts)
	if err != nil {
		return "", fmt.Errorf("file pattern: %w", err)
	}

	// The file pattern names ONE component. A "/" in it is not structure the
	// way it is in the folder pattern -- it is a directory the caller did not
	// ask for, and the two-phase rename then parks its payload inside as
	// "<n>.<ext>.tmp-rename" and fails.
	//
	// This is not a hypothetical, though the historical route into it is now
	// closed by a different guard. From 2026-03-03 (f29c3ce6) to 2026-08-15
	// (c54721c7) the SHIPPED DEFAULT of segment_title_format was
	// "{title} - {track}/{total_tracks}", and every multi-file book organized
	// in that window exploded one directory per track: 2,535 bogus
	// directories, 2,584 stranded files, 35.2 GB, 77 books with no other copy.
	//
	// Be precise about which guard covers which route, because the two are
	// easy to conflate and the wreckage on disk is identical:
	//
	//   - {track_title} is materialized into the replacement map and therefore
	//     scrubbed as a VALUE by scrubVar. A separator arriving via
	//     segment_title_format is caught there, not here. It was NOT caught in
	//     the internal/metafetch twin, which had no scrubVar at all -- which is
	//     why that route was still stranding files on 2026-08-14.
	//   - A separator written directly into file_naming_pattern is in the
	//     TEMPLATE, never passes through scrubVar, and reaches BuildPath, which
	//     splits on "/" and sanitizes each half. Nothing constrained the result
	//     to one component. That is the hole this line closes, and
	//     file_naming_pattern is user-configurable, so it is reachable today.
	//
	// Collapsing rather than erroring is deliberate: SanitizePathComponent maps
	// "/" to " ", which yields exactly the name the file should have had
	// ("Pink Bean Series - 1/9" -> "Pink Bean Series - 1 9"), so a bad pattern
	// degrades to a correct flat filename instead of taking organizing down
	// library-wide.
	stem = SanitizePathComponent(stem)

	// A pattern can legitimately expand to nothing -- e.g. "{narrator}" for a
	// book with no narrator. An empty stem would make the target a bare dotfile
	// (".m4b") that EVERY such book collides on, so fall back to the title and
	// only then to the configured placeholder.
	if strings.TrimSpace(stem) == "" {
		stem = SanitizePathComponent(strings.TrimSpace(v.Title))
	}
	if strings.TrimSpace(stem) == "" {
		stem = SanitizePathComponent(opts.TitleFallback)
	}

	if folder == "" {
		return stem, nil
	}
	return folder + "/" + stem, nil
}

// PathVars is the union of both former variable sets. A caller fills in what it
// knows; anything left zero is treated as absent and its pattern segment is
// dropped rather than left half-substituted.
type PathVars struct {
	// Identity
	Author       string
	Title        string
	Series       string
	SeriesNumber string
	Narrator     string

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

	raw := map[string]string{
		"{author}":          author,
		"{title}":           title,
		"{series}":          v.Series,
		"{series_number}":   v.SeriesNumber,
		"{series_position}": v.SeriesNumber,
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

	out := make(map[string]string, len(raw)+1)
	for k, val := range raw {
		out[k] = scrubVar(strings.TrimSpace(val))
	}

	// {series_prefix} is built AFTER the trim pass, from the already-scrubbed
	// series values, because its trailing " - " is pattern structure rather than
	// metadata. Building it alongside the others put it through TrimSpace, which
	// ate the trailing space and turned "MySeries - Book" into "MySeries -Book"
	// on every series book in the library.
	if series := out["{series}"]; series != "" {
		prefix := series
		if num := out["{series_number}"]; num != "" {
			prefix += " " + num
		}
		out["{series_prefix}"] = prefix + " - "
	} else {
		out["{series_prefix}"] = ""
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

	// Resolve {track:02d}-style specs first -- see expandFormatSpecs for why
	// this cannot move after the empty-segment pass.
	result, err := expandFormatSpecs(result, v)
	if err != nil {
		return "", err
	}

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
