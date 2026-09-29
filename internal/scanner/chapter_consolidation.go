// file: internal/scanner/chapter_consolidation.go
// version: 2.3.0
// guid: f9a0b1c2-d3e4-5f60-a7b8-c9d0e1f2a3b4
// last-edited: 2026-09-28

package scanner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/mediainfo"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// chapterKeyKind and chapterGroupKey live in internal/metadata (moved
// 2026-09-28 so the fragment-consolidation repair shares the rule); these
// aliases keep the scanner's own names.
type chapterKeyKind = metadata.ChapterKeyKind

const (
	chapterKeyNone           = metadata.ChapterKeyNone
	chapterKeyLeading        = metadata.ChapterKeyLeading
	chapterKeyMarker         = metadata.ChapterKeyMarker
	chapterKeyOfTotal        = metadata.ChapterKeyOfTotal
	chapterKeyTrailingNumber = metadata.ChapterKeyTrailingNumber
)

func chapterGroupKey(stem string) (string, chapterKeyKind) { return metadata.ChapterGroupKey(stem) }

// chapterFileDurationSec reads one file's duration in seconds, 0 when it
// cannot be read. A package var so tests can supply durations without real
// audio; production never reassigns it.
var chapterFileDurationSec = func(path string) int {
	if mi, err := mediainfo.Extract(path); err == nil && mi != nil && mi.Duration > 0 {
		return mi.Duration
	}
	return 0
}

// defaultChapterConsolidationThresholdMin is config's documented default
// (chapter_consolidation_threshold_min: 10), used by the oversized-directory
// path when the setting is 0.
const defaultChapterConsolidationThresholdMin = 10

// durationProbeSem bounds duration probes across the WHOLE scan. The
// consolidation pass runs inside each discovery worker, so a per-call pool
// sized to NumCPU multiplied by the discovery workers (workers x NumCPU
// concurrent ffprobes); one package-wide semaphore keeps the total at NumCPU
// however many directories are being grouped at once.
var durationProbeSem = make(chan struct{}, runtime.NumCPU())

// groupFileDurationSec is the duration consolidation uses for one file: the
// duration an existing present book_file row already holds for this exact file
// (same path, same size) when there is one, else a probe under the scan-wide
// semaphore. Without the cached read, trailing-number keys made every rescan of
// a "Series 01" … "Series 41" author shelf pay one ffprobe per file.
func groupFileDurationSec(path string) int {
	if d := storedBookFileDurationSec(path); d > 0 {
		return d
	}
	durationProbeSem <- struct{}{}
	defer func() { <-durationProbeSem }()
	return chapterFileDurationSec(path)
}

// storedBookFileDurationSec returns the duration a present book_file row at
// path records, when the row's size still matches the file on disk; 0 when
// there is no such row, the store is unavailable, or the file changed.
func storedBookFileDurationSec(path string) int {
	store := getStore()
	if store == nil {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	rows, err := database.BookFileRowsAtPath(store, path)
	if err != nil {
		return 0
	}
	for _, r := range rows {
		// A legacy row whose "seconds" are really milliseconds would read as a
		// 1000x-long chapter and push the group into the long/shelf branch.
		if !r.Missing && r.Duration > 0 && r.FileSize == fi.Size() && !database.DurationLooksLikeMillis(r.FileSize, r.Duration) {
			return r.Duration
		}
	}
	return 0
}

// consolidateMode selects how consolidateChapterGroups treats a same-key group
// it cannot prove to be one book.
type consolidateMode int

const (
	// consolidateUntagged: files with no album tag. An unproven group stands
	// as one book per file, exactly as before.
	consolidateUntagged consolidateMode = iota
	// consolidateOversized: an album group too large to be one book. An
	// unproven group is REFUSED and counted, never shattered into per-file
	// books -- shattering is the defect this path exists to stop.
	consolidateOversized
)

// consolidateChapterGroups inspects a list of audio files (files with no album
// tag and no playlist claim) and detects chapter-naming patterns. Files are
// grouped by chapterGroupKey -- leading numbers, trailing numbers, "Chapter N",
// "Part N", "Disc N", "N of M". When a group has ≥ 3 files AND each file
// individually averages below config.AppConfig.ChapterConsolidationThresholdMin
// minutes (default 10 min), the whole group is emitted as a single multi-file
// Book with the total duration; otherwise each file becomes its own Book.
//
// Files with no chapter numbering are passed through unchanged. Groups that
// contain at least one file exceeding the threshold are not consolidated
// (mixed durations → likely separate books with similar names, e.g. an author
// shelf of "Mistborn 1", "Mistborn 2").
//
// A consolidated group has NO file ceiling. One key, three or more files and
// every file short is the chapter-sequence evidence maxDirectoryBookFiles
// stands in for when there is none: an author shelf is whole books, and a
// shelf of whole books cannot pass "every file short". So a 1,200-chapter
// single title becomes one book (marked chapterSequence, which exempts it from
// createBookFilesForBook's backstop too) instead of importing nothing.
func consolidateChapterGroups(ctx context.Context, files []string) []Book {
	return consolidateChapterGroupsMode(ctx, files, consolidateUntagged)
}

func consolidateChapterGroupsMode(ctx context.Context, files []string, mode consolidateMode) []Book {
	if len(files) == 0 {
		return nil
	}

	thresholdMin := config.AppConfig.ChapterConsolidationThresholdMin
	if thresholdMin <= 0 {
		if mode == consolidateUntagged {
			// Consolidation disabled for untagged files.
			return filesToBooks(files)
		}
		// The switch turns off merging of UNTAGGED files. It never licensed
		// shattering an oversized album into one book per file; that path
		// judges with the default threshold.
		thresholdMin = defaultChapterConsolidationThresholdMin
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

	perFile := func(books []Book, group []candidate) []Book {
		for _, c := range group {
			books = append(books, Book{
				FilePath: c.path,
				Format:   strings.ToLower(filepath.Ext(c.path)),
			})
		}
		return books
	}
	refuse := func(group []candidate, key, why string) {
		// Counted on the walk's own counters (scanRunCounters), reported once
		// per discovery pass by ScanDirectoryParallel.
		rc := scanRunCountersFrom(ctx)
		rc.oversizedGroups.Add(1)
		rc.oversizedFiles.Add(int64(len(group)))
		logging.Warn(ctx, "scanner oversized directory: same-title group refused; no book created for these files",
			"dir", filepath.Dir(group[0].path), "count", len(group), "key", key, "reason", why)
	}

	var books []Book
	unknownDurationGroups := 0
	for _, key := range groupOrder {
		group := groups[key]

		// Non-chapter files or groups too small to be a chapter sequence.
		if strings.HasPrefix(key, "\x00") || len(group) < 3 {
			books = perFile(books, group)
			continue
		}

		// Read duration for every file in the group (best-effort). The
		// goroutine count is bounded here; the probe count is bounded
		// scan-wide by durationProbeSem inside groupFileDurationSec.
		// readable counts how many durations are KNOWN -- distinct from
		// "short".
		durations := make([]int, len(group))
		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(runtime.NumCPU())
		for i := range group {
			g.Go(func() error {
				if gctx.Err() != nil {
					return gctx.Err()
				}
				durations[i] = groupFileDurationSec(group[i].path)
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
		// any of these files actually are.
		if readable == 0 {
			unknownDurationGroups++
			if mode == consolidateOversized {
				refuse(group, key, "no file's duration could be read")
				continue
			}
			logging.Warn(ctx, "scanner chapter consolidation skipped: all files in group unreadable, duration unknown",
				"count", len(group), "key", key)
			books = perFile(books, group)
			continue
		}

		totalSec := 0
		long := 0
		for _, c := range group {
			totalSec += c.duration
			if c.duration > thresholdSec {
				long++
			}
		}
		avgSec := totalSec / len(group)

		if long > 0 || avgSec >= thresholdSec {
			if mode == consolidateUntagged {
				// Files are individually long or mixed → don't consolidate.
				books = perFile(books, group)
				continue
			}
			// Oversized: only a group of whole books -- EVERY file readable
			// and long -- is a shelf, and a shelf is one book per file. A
			// mixed or partly unreadable group is neither a proven shelf nor
			// a proven book; shattering it is how a 1,188-chapter Bible became
			// 1,188 books, so it is refused and counted instead.
			if long == len(group) {
				books = perFile(books, group)
				continue
			}
			refuse(group, key, fmt.Sprintf("mixed durations: %d of %d files over %d min, %d unreadable",
				long, len(group), thresholdMin, len(group)-readable))
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
			FilePath:        group[0].path,
			Format:          strings.ToLower(filepath.Ext(group[0].path)),
			Duration:        totalSec,
			SegmentFiles:    paths,
			chapterSequence: true,
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
