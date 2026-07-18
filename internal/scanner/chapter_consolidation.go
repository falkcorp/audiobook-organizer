// file: internal/scanner/chapter_consolidation.go
// version: 1.3.0
// guid: f9a0b1c2-d3e4-5f60-a7b8-c9d0e1f2a3b4
// last-edited: 2026-07-18

package scanner

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
)

// chapterPrefixRe matches leading track/chapter numbers like "01 - ", "02. ",
// "003 - ", "1 " etc. at the beginning of a filename stem.
var chapterPrefixRe = regexp.MustCompile(`^[\d]+[\s\-\.]+`)

// normalizeChapterTitle strips a leading chapter/track number from a filename
// stem, lowercases the result, and collapses extra whitespace. This produces
// the grouping key used to detect chapter-file sequences.
func normalizeChapterTitle(stem string) string {
	norm := chapterPrefixRe.ReplaceAllString(stem, "")
	norm = strings.ToLower(strings.TrimSpace(norm))
	norm = strings.Join(strings.Fields(norm), " ")
	return norm
}

// consolidateChapterGroups inspects a list of audio files (typically files with
// no album tag and no playlist claim) and detects chapter-naming patterns.
// Files whose base name starts with a numeric prefix (e.g. "01 - My Book.mp3",
// "02 - My Book.mp3") are grouped by their normalized base title.  When a group
// has ≥ 3 files AND each file individually averages below
// config.AppConfig.ChapterConsolidationThresholdMin minutes (default 10 min),
// the whole group is emitted as a single multi-file Book with the total
// duration; otherwise each file becomes its own Book.
//
// Files that show no numeric prefix are passed through unchanged.
// Groups that contain at least one file exceeding the threshold are not
// consolidated (mixed durations → likely separate books with similar names).
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

	// Group by normalized title. Use a null-byte prefix as a per-file unique
	// key for files that have no numeric prefix (cannot be chapters).
	var groupOrder []string
	groups := make(map[string][]candidate)

	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), filepath.Ext(f))
		norm := normalizeChapterTitle(stem)
		stemLower := strings.ToLower(strings.TrimSpace(stem))

		var key string
		if norm != stemLower {
			// Had a numeric prefix → potential chapter file.
			key = norm
		} else {
			// No numeric prefix → treat as standalone file.
			key = "\x00" + f
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

		// Read duration for every file in the group (best-effort). readable
		// tracks how many files mediainfo actually returned a duration for —
		// distinct from "duration is short", it means "duration is KNOWN".
		readable := 0
		for i := range group {
			if mi, err := mediainfo.Extract(group[i].path); err == nil && mi != nil && mi.Duration > 0 {
				group[i].duration = mi.Duration
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

		// All files are short and share a base title → consolidate.
		paths := make([]string, len(group))
		for i, c := range group {
			paths[i] = c.path
		}
		logging.Info(ctx, "scanner chapter consolidation merging files", "count", len(group), "key", key, "avg_sec_per_file", avgSec, "total_sec", totalSec)
		books = append(books, Book{
			FilePath:     paths[0],
			Format:       strings.ToLower(filepath.Ext(paths[0])),
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
