// file: internal/scanner/chapter_consolidation.go
// version: 2.0.0
// guid: f9a0b1c2-d3e4-5f60-a7b8-c9d0e1f2a3b4
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// chapterKeyKind says which numbering shape chapterGroupKey recognised.
type chapterKeyKind int

const (
	chapterKeyNone           chapterKeyKind = iota // no chapter numbering: a standalone file
	chapterKeyLeading                              // "01 - Title", "01. Title", bare "98"
	chapterKeyMarker                               // "Title - Chapter 12", "Title Part 3", "Title Disc 2", "Chapter 01 - Title"
	chapterKeyOfTotal                              // "Title 3 of 12", "Title (03 of 12)"
	chapterKeyTrailingNumber                       // "Book Title 01"
)

var (
	// chapterLeadingNumRe: a leading track/chapter number followed by a
	// separator, or a stem that is nothing but a number ("98").
	chapterLeadingNumRe = regexp.MustCompile(`^\d+(?:[\s\-–._]+|$)`)
	// chapterLeadingMarkerRe: "Chapter 01 - Title", "Disc 2 - Title". The
	// marker word must be followed by a number, so "Discworld - Mort" is not
	// a disc marker.
	chapterLeadingMarkerRe = regexp.MustCompile(`(?i)^(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_]*\d+(?:\s*of\s*\d+)?(?:[\s\-–._:]+|$)`)
	// chapterTrailingMarkerRe: "Title - Chapter 12", "Title Part 3",
	// "Title_Disc2", "Title (CD 1)", "Title Part 3 of 12". The marker must
	// start a word (start of stem or after a separator) and be followed by a
	// number, so "Discworld 5" is NOT a disc marker.
	chapterTrailingMarkerRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[]+)(?:chapter|chap|ch|part|pt|disc|disk|cd|track)\.?[\s_\-]*\d+(?:\s*of\s*\d+)?[)\]]?\s*$`)
	// chapterTrailingOfRe: "Title 3 of 12", "Title (03 of 12)". Captures the
	// total, which is part of the grouping key.
	chapterTrailingOfRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[]+)\d+\s*of\s*(\d+)[)\]]?\s*$`)
	// chapterTrailingNumRe: "Book Title 01", "Book Title - 01", "Title_01".
	chapterTrailingNumRe = regexp.MustCompile(`(?:^|[\s\-–_.,(\[]+)\d+[)\]]?\s*$`)
	// chapterSeriesWordRe: a trailing number after one of these is a series
	// entry ("Mistborn Book 2", "Vol. 3", "#4"), a separate book, not a
	// chapter.
	chapterSeriesWordRe = regexp.MustCompile(`(?i)(?:^|[\s\-–_.,(\[])(?:book|bk|volume|vol|no|#)\.?$`)
)

// chapterGroupKey reduces a filename stem to the key its chapter siblings
// share, by stripping ONE leading and ONE trailing chapter number:
//
//	"01 - My Book"          -> "my book"   (leading)
//	"98"                    -> ""          (leading; bare chapter number)
//	"My Book - Chapter 12"  -> "my book"   (marker)
//	"My Book Disc 2"        -> "my book"   (marker)
//	"My Book 3 of 12"       -> "my book|of 12" (of-total; the total is kept so
//	                           two differently-sized sets do not merge)
//	"My Book 01"            -> "my book"   (trailing number)
//	"01 Genesis 001"        -> "genesis"   (both ends)
//
// kind is chapterKeyNone when the stem carries no chapter numbering at all; the
// file then stands alone. A trailing number after "Book", "Vol" or "#" is a
// series position, not a chapter, and is not stripped. "Discworld" is not a
// disc marker: every marker must be followed by a number.
//
// Recognising a key is not a decision to merge: consolidateChapterGroups still
// requires ≥3 files and short durations before it groups anything.
func chapterGroupKey(stem string) (key string, kind chapterKeyKind) {
	s := strings.TrimSpace(stem)

	if loc := chapterLeadingMarkerRe.FindStringIndex(s); loc != nil {
		s, kind = s[loc[1]:], chapterKeyMarker
	} else if loc := chapterLeadingNumRe.FindStringIndex(s); loc != nil {
		s, kind = s[loc[1]:], chapterKeyLeading
	}

	suffix := ""
	switch {
	case chapterTrailingMarkerRe.MatchString(s):
		s = chapterTrailingMarkerRe.ReplaceAllString(s, "")
		kind = chapterKeyMarker
	case chapterTrailingOfRe.MatchString(s):
		suffix = "|of " + chapterTrailingOfRe.FindStringSubmatch(s)[1]
		s = chapterTrailingOfRe.ReplaceAllString(s, "")
		if kind == chapterKeyNone {
			kind = chapterKeyOfTotal
		}
	case chapterTrailingNumRe.MatchString(s):
		rest := chapterTrailingNumRe.ReplaceAllString(s, "")
		if rest != "" && !chapterSeriesWordRe.MatchString(strings.TrimSpace(rest)) {
			s = rest
			if kind == chapterKeyNone {
				kind = chapterKeyTrailingNumber
			}
		}
	}
	if kind == chapterKeyNone {
		return "", chapterKeyNone
	}

	s = strings.Trim(strings.ToLower(s), " -–_.,:")
	s = strings.Join(strings.Fields(s), " ")
	return s + suffix, kind
}

// chapterFileDurationSec reads one file's duration in seconds, 0 when it
// cannot be read. A package var so tests can supply durations without real
// audio; production never reassigns it.
var chapterFileDurationSec = func(path string) int {
	if mi, err := mediainfo.Extract(path); err == nil && mi != nil && mi.Duration > 0 {
		return mi.Duration
	}
	return 0
}

// consolidateChapterGroups inspects a list of audio files (files with no album
// tag and no playlist claim, or an album group too large to be one book) and
// detects chapter-naming patterns. Files are grouped by chapterGroupKey --
// leading numbers, trailing numbers, "Chapter N", "Part N", "Disc N", "N of M".
// When a group has ≥ 3 files AND each file individually averages below
// config.AppConfig.ChapterConsolidationThresholdMin minutes (default 10 min),
// the whole group is emitted as a single multi-file Book with the total
// duration; otherwise each file becomes its own Book.
//
// Files with no chapter numbering are passed through unchanged. Groups that
// contain at least one file exceeding the threshold are not consolidated
// (mixed durations → likely separate books with similar names, e.g. an author
// shelf of "Mistborn 1", "Mistborn 2").
//
// A group that qualifies but holds more than maxDirectoryBookFiles files is
// REFUSED, not split and not shattered: no Book is emitted for it and a
// warning names it. It is the same ceiling createBookFilesForBook enforces,
// applied before saveBook creates a row the backstop would then leave with no
// files at all.
func consolidateChapterGroups(ctx context.Context, files []string) []Book {
	if len(files) == 0 {
		return nil
	}

	thresholdMin := config.AppConfig.ChapterConsolidationThresholdMin
	if thresholdMin <= 0 {
		// Consolidation disabled.
		return filesToBooks(files)
	}
	thresholdSec := thresholdMin * 60

	type candidate struct {
		path     string
		duration int // seconds; 0 = unknown / unreadable
	}

	// Group by chapter key. A null-byte prefix makes a per-file unique key for
	// files with no chapter numbering (cannot be chapters).
	var groupOrder []string
	groups := make(map[string][]candidate)

	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		key, kind := chapterGroupKey(stem)
		if kind == chapterKeyNone {
			key = "\x00" + f
		} else {
			key = "k:" + key
		}
		if _, seen := groups[key]; !seen {
			groupOrder = append(groupOrder, key)
		}
		groups[key] = append(groups[key], candidate{path: f})
	}

	var books []Book
	unknownDurationGroups := 0
	for _, key := range groupOrder {
		group := groups[key]

		// Non-chapter files or groups too small to be a chapter sequence.
		if strings.HasPrefix(key, "\x00") || len(group) < 3 {
			for _, c := range group {
				books = append(books, Book{
					FilePath: c.path,
					Format:   strings.ToLower(filepath.Ext(c.path)),
				})
			}
			continue
		}

		// Read duration for every file in the group (best-effort), on a
		// bounded pool: an oversized flat directory reaches here with hundreds
		// of files, and each read opens the file. readable counts how many
		// durations are KNOWN -- distinct from "short".
		durations := make([]int, len(group))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(runtime.NumCPU())
		for i := range group {
			g.Go(func() error {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				durations[i] = chapterFileDurationSec(group[i].path)
				return nil
			})
		}
		if err := g.Wait(); err != nil {
			return nil // canceled: the caller discards a partial result on ctx error
		}
		readable := 0
		for i := range group {
			group[i].duration = durations[i]
			if durations[i] > 0 {
				readable++
			}
		}

		// R-8: when mediainfo failed for every file in the group, duration
		// is UNKNOWN for the whole group, not "short". Without this guard,
		// totalSec/avgSec below are computed from all-zero durations, so
		// avgSec == 0 < thresholdSec always looks like "short" and the group
		// gets silently consolidated even though we have no idea how long
		// any of these files actually are. Skip consolidation for this group
		// (each file stands alone, same as the mixed/long-group path) and
		// count + warn so an operator can see unreadable groups accumulating.
		if readable == 0 {
			unknownDurationGroups++
			logging.Warn(ctx, "scanner chapter consolidation skipped: all files in group unreadable, duration unknown",
				"count", len(group), "key", key)
			for _, c := range group {
				books = append(books, Book{
					FilePath: c.path,
					Format:   strings.ToLower(filepath.Ext(c.path)),
				})
			}
			continue
		}

		// Check for mixed durations (at least one file above the threshold).
		totalSec := 0
		hasLong := false
		for _, c := range group {
			totalSec += c.duration
			if c.duration > thresholdSec {
				hasLong = true
			}
		}
		avgSec := totalSec / len(group)

		if hasLong || avgSec >= thresholdSec {
			// Files are individually long or mixed → don't consolidate.
			for _, c := range group {
				books = append(books, Book{
					FilePath: c.path,
					Format:   strings.ToLower(filepath.Ext(c.path)),
				})
			}
			continue
		}

		if len(group) > maxDirectoryBookFiles {
			logging.Warn(ctx, "scanner chapter consolidation refused: group too large for one book; no book created for these files",
				"dir", filepath.Dir(group[0].path), "count", len(group), "limit", maxDirectoryBookFiles, "key", key)
			continue
		}

		// All files are short and share a chapter key → consolidate. The
		// segments go in natural order ("Chapter 2" before "Chapter 10"):
		// SegmentFiles is the positional order the book falls back to when tag
		// numbers are refused. FilePath stays the first file in arrival order,
		// as before, because it is the book's lookup key on the next scan.
		paths := make([]string, len(group))
		for i, c := range group {
			paths[i] = c.path
		}
		slices.SortFunc(paths, util.CompareNatural)
		logging.Info(ctx, "scanner chapter consolidation merging files", "count", len(group), "key", key, "avg_sec_per_file", avgSec, "total_sec", totalSec)
		books = append(books, Book{
			FilePath:     group[0].path,
			Format:       strings.ToLower(filepath.Ext(group[0].path)),
			Duration:     totalSec,
			SegmentFiles: paths,
		})
	}

	if unknownDurationGroups > 0 {
		logging.Warn(ctx, "scanner chapter consolidation: groups skipped due to unknown duration",
			"unknown_duration_groups", unknownDurationGroups)
	}

	return books
}

// filesToBooks converts a flat slice of file paths into individual Book records.
func filesToBooks(files []string) []Book {
	books := make([]Book, 0, len(files))
	for _, f := range files {
		books = append(books, Book{
			FilePath: f,
			Format:   strings.ToLower(filepath.Ext(f)),
		})
	}
	return books
}
