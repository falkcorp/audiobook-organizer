// file: internal/dedup/split_book_detector.go
// version: 1.1.0
// guid: 9c1f0a3e-b7d2-4e84-8c12-3fa8e1d6b9c0
// last-edited: 2026-07-07

// Package dedup — split-book backfill detector.
//
// Finds Book rows that look like CHAPTERS of a single physical audiobook
// but have been stored as separate Books (one per chapter file). This is
// the same shape that the path_format scrub (PR #1158) blocks at scan
// time; this detector cleans up the existing mess.
//
// Two shapes are handled:
//
//	PARENT (flat):  .../Author/Tarkin/01.mp3, .../Author/Tarkin/02.mp3, ...
//	GRANDPARENT:    .../Author/Tarkin/1/01.mp3, .../Author/Tarkin/2/02.mp3, ...
//
// The grandparent shape is the rogue '/' bug from the same scrub: each
// chapter file lives in its own immediate parent dir, so grouping by
// parent yields size-1 groups but grouping by grandparent recovers the
// real cluster.
//
// HEURISTIC NOTE: G1 (sibling PR) implements the same detection at scan
// time. When both land, refactor to share. Decisions made here:
//
//  1. Build BOTH parent and grandparent groupings in a single pass.
//  2. Emit a grandparent candidate ONLY when every child parent-group has
//     size 1 (the rogue subdir case). Otherwise emit from parent.
//  3. A book is assigned to at most one candidate (parent wins).
//  4. Qualifying group: ≥3 books, same AuthorID (allow nil-on-all-sides),
//     same SeriesID (allow nil-on-all-sides), AND filename or parent-dir-name
//     yields integers forming a near-sequential run: ≥70% coverage of
//     [min..max] and no gap >2.

package dedup

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// SplitBookCandidate is one detected cluster of Books that look like
// chapters of a single audiobook.
type SplitBookCandidate struct {
	// ID is a stable identifier (assigned by storage on persist).
	ID string `json:"id"`

	// ParentFolder is the directory the cluster was grouped by — either
	// the immediate parent dir of all files (parent shape) or the
	// grandparent (rogue-subdir shape).
	ParentFolder string `json:"parent_folder"`

	// BookIDs are the IDs of every book in the cluster, sorted ULID-asc
	// so the lowest (earliest-created) is BookIDs[0] — the suggested
	// keep-ID.
	BookIDs []string `json:"book_ids"`

	// SuggestedTitle is the most common Title across the group, stripped
	// of any trailing "(N/M)" or " - NN" chapter marker.
	SuggestedTitle string `json:"suggested_title"`

	// SuggestedAuthor is the author name if all books share one author,
	// or "" if all books have nil AuthorID.
	SuggestedAuthor string `json:"suggested_author"`

	// SequentialPattern describes the integer run found, e.g.
	// "filename:1..85 (85 covered of 85)".
	SequentialPattern string `json:"sequential_pattern"`

	// Shape is "parent" or "grandparent".
	Shape string `json:"shape"`
}

// splitBookSlim is the projection used internally to keep the working set
// small while scanning the library.
type splitBookSlim struct {
	ID       string
	Title    string
	FilePath string
	AuthorID *int
	SeriesID *int
}

// DetectSplitBookCandidates walks every book in the store and returns the
// list of split-book clusters. No DB writes — purely analytical.
func DetectSplitBookCandidates(ctx context.Context, store database.Store) ([]SplitBookCandidate, error) {
	all, err := loadSlimBooks(ctx, store)
	if err != nil {
		return nil, err
	}
	return detectFromSlim(all, func(authorID int) string {
		if a, err := store.GetAuthorByID(authorID); err == nil && a != nil {
			return a.Name
		}
		return ""
	}), nil
}

// loadSlimBooks paginates through every book and projects to splitBookSlim.
func loadSlimBooks(ctx context.Context, store database.Store) ([]splitBookSlim, error) {
	const pageSize = 1000
	var all []splitBookSlim
	offset := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return nil, fmt.Errorf("split-book detector: GetAllBooksCore offset=%d: %w", offset, err)
		}
		if len(batch) == 0 {
			break
		}
		for i := range batch {
			b := &batch[i]
			if b.FilePath == "" {
				continue
			}
			if b.MarkedForDeletion != nil && *b.MarkedForDeletion {
				continue
			}
			all = append(all, splitBookSlim{
				ID:       b.ID,
				Title:    b.Title,
				FilePath: b.FilePath,
				AuthorID: b.AuthorID,
				SeriesID: b.SeriesID,
			})
		}
		if len(batch) < pageSize {
			break
		}
		offset += len(batch)
	}
	return all, nil
}

// detectFromSlim is the pure detection core; isolated from store lookups
// so it can be unit-tested without a fake Store.
func detectFromSlim(all []splitBookSlim, resolveAuthorName func(int) string) []SplitBookCandidate {
	parentBuckets := make(map[string][]splitBookSlim)
	grandparentBuckets := make(map[string][]splitBookSlim)
	for _, b := range all {
		p := filepath.Dir(b.FilePath)
		gp := filepath.Dir(p)
		parentBuckets[p] = append(parentBuckets[p], b)
		grandparentBuckets[gp] = append(grandparentBuckets[gp], b)
	}

	assigned := make(map[string]bool, len(all))
	var out []SplitBookCandidate

	// PARENT pass — emit clusters from flat parent groups.
	for _, key := range sortedKeys(parentBuckets) {
		group := parentBuckets[key]
		if len(group) < 3 {
			continue
		}
		cand, ok := qualifyTypedGroup(group, key, "parent", resolveAuthorName)
		if !ok {
			continue
		}
		for _, id := range cand.BookIDs {
			assigned[id] = true
		}
		out = append(out, cand)
	}

	// GRANDPARENT pass — emit only when every child parent-group is size 1.
	for _, key := range sortedKeys(grandparentBuckets) {
		group := grandparentBuckets[key]
		if len(group) < 3 {
			continue
		}
		anyAssigned := false
		for _, b := range group {
			if assigned[b.ID] {
				anyAssigned = true
				break
			}
		}
		if anyAssigned {
			continue
		}
		parentSizes := make(map[string]int)
		for _, b := range group {
			parentSizes[filepath.Dir(b.FilePath)]++
		}
		allSizeOne := true
		for _, n := range parentSizes {
			if n != 1 {
				allSizeOne = false
				break
			}
		}
		if !allSizeOne {
			continue
		}
		cand, ok := qualifyTypedGroup(group, key, "grandparent", resolveAuthorName)
		if !ok {
			continue
		}
		for _, id := range cand.BookIDs {
			assigned[id] = true
		}
		out = append(out, cand)
	}

	return out
}

// chapterMarkerRe matches a trailing chapter marker like " (3/85)",
// " - Chapter 1", or a bare trailing number. Stripped to derive the
// suggested merged title.
var chapterMarkerRe = regexp.MustCompile(
	`(?i)\s*(?:[-—:]\s*)?` +
		`(?:` +
		`\(\s*\d+\s*(?:/|of)\s*\d+\s*\)` + // (3/85), (3 of 85)
		`|\(\s*(?:chapter|track|part|ch|tr|pt)\s*\d+(?:\.\d+)?\s*\)` + // (Chapter 1)
		`|\d+\s*(?:/|of)\s*\d+` + // 3/85, 3 of 85
		`|(?:chapter|track|part|ch|tr|pt)\s*\d+(?:\.\d+)?` +
		`|\d{1,3}` + // bare trailing number
		`)\s*$`,
)

var numericTokenRe = regexp.MustCompile(`\d+`)

// sortedKeys returns map keys sorted ascending for deterministic iteration.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// stripChapterMarker removes a trailing chapter marker from a title.
func stripChapterMarker(title string) string {
	t := strings.TrimSpace(title)
	t = chapterMarkerRe.ReplaceAllString(t, "")
	return strings.TrimSpace(t)
}

// extractChapterNumber returns the first integer found in s, or -1 if none.
func extractChapterNumber(s string) int {
	m := numericTokenRe.FindString(s)
	if m == "" {
		return -1
	}
	n := 0
	for _, r := range m {
		n = n*10 + int(r-'0')
		if n > 100000 {
			break
		}
	}
	return n
}

// sequentialRun checks whether nums form a near-sequential run: ≥70%
// coverage of [min..max] across unique values, and no gap >2 between
// successive unique values.
func sequentialRun(nums []int) (bool, string) {
	if len(nums) < 3 {
		return false, ""
	}
	uniq := make(map[int]bool, len(nums))
	for _, n := range nums {
		if n >= 0 {
			uniq[n] = true
		}
	}
	if len(uniq) < 3 {
		return false, ""
	}
	sorted := make([]int, 0, len(uniq))
	for n := range uniq {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)
	lo, hi := sorted[0], sorted[len(sorted)-1]
	if hi-lo < 2 {
		return false, ""
	}
	span := hi - lo + 1
	coverage := float64(len(sorted)) / float64(span)
	if coverage < 0.70 {
		return false, ""
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i]-sorted[i-1] > 2 {
			return false, ""
		}
	}
	return true, fmt.Sprintf("%d..%d (%d covered of %d)", lo, hi, len(sorted), span)
}

// mostCommon returns the most frequent non-empty string in ss, ties
// broken lexically.
func mostCommon(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, s := range ss {
		if s == "" {
			continue
		}
		counts[s]++
	}
	best := ""
	bestN := 0
	for s, n := range counts {
		if n > bestN || (n == bestN && (best == "" || s < best)) {
			best = s
			bestN = n
		}
	}
	return best
}

// sortBookIDsULIDAsc sorts ULID-format IDs ascending. ULIDs sort
// lexicographically equal to chronologically; the first element is the
// earliest-created book — our suggested keep-ID.
func sortBookIDsULIDAsc(ids []string) []string {
	out := make([]string, len(ids))
	copy(out, ids)
	sort.Strings(out)
	return out
}

// qualifyTypedGroup applies size/author/series/sequence checks.
func qualifyTypedGroup(group []splitBookSlim, parentKey, shape string, resolveAuthorName func(int) string) (SplitBookCandidate, bool) {
	if len(group) < 3 {
		return SplitBookCandidate{}, false
	}

	// Author: all-or-nothing on AuthorID equality.
	var firstAuthor *int
	for i, b := range group {
		if i == 0 {
			firstAuthor = b.AuthorID
			continue
		}
		if (firstAuthor == nil) != (b.AuthorID == nil) {
			return SplitBookCandidate{}, false
		}
		if firstAuthor != nil && *firstAuthor != *b.AuthorID {
			return SplitBookCandidate{}, false
		}
	}

	// Series: same shape.
	var firstSeries *int
	for i, b := range group {
		if i == 0 {
			firstSeries = b.SeriesID
			continue
		}
		if (firstSeries == nil) != (b.SeriesID == nil) {
			return SplitBookCandidate{}, false
		}
		if firstSeries != nil && *firstSeries != *b.SeriesID {
			return SplitBookCandidate{}, false
		}
	}

	// Extract chapter numbers — filename basename first, fall back to
	// immediate parent dir name (rogue-subdir case).
	nums := make([]int, 0, len(group))
	for _, b := range group {
		base := strings.TrimSuffix(filepath.Base(b.FilePath), filepath.Ext(b.FilePath))
		n := extractChapterNumber(base)
		if n < 0 {
			n = extractChapterNumber(filepath.Base(filepath.Dir(b.FilePath)))
		}
		nums = append(nums, n)
	}
	ok, pattern := sequentialRun(nums)
	if !ok {
		return SplitBookCandidate{}, false
	}
	source := "filename"
	if shape == "grandparent" {
		source = "subdir"
	}
	pattern = source + ":" + pattern

	// Suggested title: most common stripped Title across the group.
	titles := make([]string, 0, len(group))
	for _, b := range group {
		titles = append(titles, stripChapterMarker(b.Title))
	}
	suggestedTitle := mostCommon(titles)
	if suggestedTitle == "" {
		suggestedTitle = filepath.Base(parentKey)
	}

	suggestedAuthor := ""
	if firstAuthor != nil && resolveAuthorName != nil {
		suggestedAuthor = resolveAuthorName(*firstAuthor)
	}

	ids := make([]string, 0, len(group))
	for _, b := range group {
		ids = append(ids, b.ID)
	}
	ids = sortBookIDsULIDAsc(ids)

	return SplitBookCandidate{
		ParentFolder:      parentKey,
		BookIDs:           ids,
		SuggestedTitle:    suggestedTitle,
		SuggestedAuthor:   suggestedAuthor,
		SequentialPattern: pattern,
		Shape:             shape,
	}, true
}
