// file: internal/dedup/split_book_detector.go
// version: 1.7.2
// guid: 9c1f0a3e-b7d2-4e84-8c12-3fa8e1d6b9c0
// last-edited: 2026-10-21

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
//  5. DISTINCT-WORKS VETO. A shared author and series is what a SERIES of
//     separate books looks like too, and a run of numbers is what volumes
//     of that series look like. So a cluster that passes 1-4 is still
//     dropped when it carries evidence of being several works rather than
//     chapters of one (vetoDistinctWorks: V2, V2b, V4, V5, V8, V9, V3). The
//     detector FAILS CLOSED: DetectSplitBookCandidates never returns a vetoed
//     cluster; DetectSplitBookCandidatesWithVetoes returns them with their
//     reasons so a caller can show a held row instead of nothing.
//  6. A STORED candidate is only a snapshot of what a scan saw. The merge paths
//     that act on one (the bulk-merge op, POST /dedup/split-book-candidates/:id/merge)
//     re-read the members and re-apply these vetoes plus the no-files and
//     duration checks first: MergeSplitBookClusterRechecked (split_book_recheck.go).

package dedup

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/config"
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

	// Shape is "parent", "grandparent" or "parent-relaxed".
	Shape string `json:"shape"`

	// Vetoes is why the cluster is NOT a split book: one line per
	// vetoDistinctWorks rule that fired ("V2 ...", "V4 ..."). Always empty on
	// what DetectSplitBookCandidates returns (and so on stored candidates);
	// DetectSplitBookCandidatesWithVetoes fills it.
	Vetoes []string `json:"vetoes,omitempty"`

	// Evidence is context that is not a veto, shown beside the cluster (for
	// example members whose series positions differ).
	Evidence []string `json:"evidence,omitempty"`
}

// splitBookSlim is the projection used internally to keep the working set
// small while scanning the library.
type splitBookSlim struct {
	ID               string
	Title            string
	FilePath         string
	OriginalFilename string
	AuthorID         *int
	SeriesID         *int
	// Duration is the book's recorded length in seconds; nil is unknown.
	Duration *int
	// SeriesSequence and SeriesPositionRaw are the book's position in its
	// series. They are evidence only (see seriesPositionEvidence), never a
	// veto: the field is polluted on exactly the rows this detector looks at.
	SeriesSequence    *int
	SeriesPositionRaw string
}

// DetectSplitBookCandidates walks every book in the store and returns the
// list of split-book clusters. No DB writes — purely analytical.
// progress, if non-nil, is invoked with the running count of books loaded
// (every page of 1000) so long scans stay observable up to the op timeout.
//
// A cluster with any distinct-works veto (vetoDistinctWorks) is dropped, so
// every consumer of this function (the scan op that fills the Split Books
// tab, the bulk-merge op's candidates, the merge-split-books CLI) fails
// closed. Use DetectSplitBookCandidatesWithVetoes to see what was dropped.
func DetectSplitBookCandidates(ctx context.Context, store Store, progress func(loaded int)) ([]SplitBookCandidate, error) {
	all, err := DetectSplitBookCandidatesWithVetoes(ctx, store, progress)
	if err != nil {
		return nil, err
	}
	return withoutVetoed(all), nil
}

// DetectSplitBookCandidatesWithVetoes is DetectSplitBookCandidates plus the
// clusters a distinct-works veto dropped: those come back with Vetoes set, in
// the position the detector found them. A caller that lists rows (the
// Repairs fixer) shows them as held, with the reasons, instead of showing
// nothing; a caller that merges must skip every candidate with Vetoes.
func DetectSplitBookCandidatesWithVetoes(ctx context.Context, store Store, progress func(loaded int)) ([]SplitBookCandidate, error) {
	all, err := loadSlimBooks(ctx, store, progress)
	if err != nil {
		return nil, err
	}
	return detectFromSlimWithVetoes(all, func(authorID int) string {
		if a, err := store.GetAuthorByID(authorID); err == nil && a != nil {
			return a.Name
		}
		return ""
	}, RepairChapterMaxSec()), nil
}

// RepairChapterMaxSec is the longest a member may run and still be a chapter
// (repair_chapter_max_min, the limit the fragment fixer's duration gate
// uses), in seconds.
func RepairChapterMaxSec() int {
	return config.AppConfig.ResolveRepairChapterMaxMin() * 60
}

// withoutVetoed drops the clusters that carry a veto.
func withoutVetoed(all []SplitBookCandidate) []SplitBookCandidate {
	var out []SplitBookCandidate
	for _, c := range all {
		if len(c.Vetoes) == 0 {
			out = append(out, c)
		}
	}
	return out
}

// slimFromBook projects a book row (a Book goes through Book.Core) to the fields the detector reads. The scan
// (loadSlimBooks) and the merge-time recheck (RecheckSplitBookMembers) build
// their input through this one function, so they judge the same fields.
func slimFromBook(b *database.BookCore) splitBookSlim {
	originalFilename := ""
	if b.OriginalFilename != nil {
		originalFilename = *b.OriginalFilename
	}
	seriesRaw := ""
	if b.SeriesPositionRaw != nil {
		seriesRaw = *b.SeriesPositionRaw
	}
	return splitBookSlim{
		ID:                b.ID,
		Title:             b.Title,
		FilePath:          b.FilePath,
		OriginalFilename:  originalFilename,
		AuthorID:          b.AuthorID,
		SeriesID:          b.SeriesID,
		Duration:          b.Duration,
		SeriesSequence:    b.SeriesSequence,
		SeriesPositionRaw: seriesRaw,
	}
}

// loadSlimBooks paginates through every book and projects to splitBookSlim.
// progress, if non-nil, is called once per page with the total books loaded
// so far (pages are 1000 books).
func loadSlimBooks(ctx context.Context, store Store, progress func(loaded int)) ([]splitBookSlim, error) {
	const pageSize = 1000
	var all []splitBookSlim
	offset := 0
	loaded := 0
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
			if b.IsSoftDeleted() {
				continue
			}
			all = append(all, slimFromBook(b))
		}
		loaded += len(batch)
		if progress != nil {
			progress(loaded)
		}
		if len(batch) < pageSize {
			break
		}
		offset += len(batch)
	}
	return all, nil
}

// detectFromSlimWithVetoes is the pure detection core; isolated from store
// lookups so it can be unit-tested without a fake Store. chapterMaxSec is the
// duration limit (RepairChapterMaxSec). DetectSplitBookCandidates is this
// through withoutVetoed (fail closed). A vetoed cluster is
// returned (Vetoes set) AND claims its books exactly as a qualified one does,
// so a later pass cannot re-emit the same books under another shape: the
// veto is a property of the books, not of the pass that found them.
func detectFromSlimWithVetoes(all []splitBookSlim, resolveAuthorName func(int) string, chapterMaxSec int) []SplitBookCandidate {
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
		cand, ok := qualifyTypedGroup(group, key, "parent", resolveAuthorName, chapterMaxSec)
		if !ok {
			continue
		}
		for _, id := range cand.BookIDs {
			assigned[id] = true
		}
		out = append(out, cand)
	}

	// RELAXED PARENT pass — historical track fragments often have polluted
	// author/series IDs, so metadata equality is not evidence. Require a much
	// stronger filename signal instead: siblings must share a non-empty stem
	// after a leading sequence number. This only persists a preview candidate.
	for _, key := range sortedKeys(parentBuckets) {
		group := parentBuckets[key]
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
		cand, ok := qualifyRelaxedNumberedSiblingGroup(group, key, chapterMaxSec)
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
		cand, ok := qualifyTypedGroup(group, key, "grandparent", resolveAuthorName, chapterMaxSec)
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
// suggested merged title. stripChapterMarker keeps a trailing number that
// follows Book/Vol/# (bookVolTailRe): that is the work's series volume, not a
// chapter.
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

var numberedSiblingStemRe = regexp.MustCompile(`^\s*0*(\d{1,4})\s*(?:[-_.]\s*|\s+)(.+?)\s*$`)

// sortedKeys returns map keys sorted ascending for deterministic iteration.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bookVolTailRe matches a title ending in a series volume number: "Book 3",
// "Bk 03", "Vol. 2", "Volume 12", "#4".
var bookVolTailRe = regexp.MustCompile(`(?i)(?:\b(?:book|bk|vol(?:ume)?)\.?|#)\s*0*\d{1,3}\s*$`)

// stripChapterMarker removes a trailing chapter marker from a title. A title
// that ends in a Book/Vol number ("Galaxy Run Book 3") keeps it: stripping
// would name the work after the series and drop which one it is.
func stripChapterMarker(title string) string {
	t := strings.TrimSpace(title)
	if bookVolTailRe.MatchString(t) {
		return t
	}
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

// numberedSiblingStem extracts the leading track number, a normalized filename
// stem for comparison, and the corresponding human-readable stem for display.
// A bare number such as "1984" deliberately does not qualify: it has no
// separator and no shared title evidence with sibling tracks.
func numberedSiblingStem(book splitBookSlim) (int, string, string, bool) {
	name := book.OriginalFilename
	if name == "" {
		name = filepath.Base(book.FilePath)
	}
	name = strings.TrimSuffix(name, filepath.Ext(name))
	match := numberedSiblingStemRe.FindStringSubmatch(name)
	if len(match) != 3 {
		return 0, "", "", false
	}
	number := extractChapterNumber(match[1])
	displayStem := strings.Trim(strings.TrimSpace(match[2]), "-_. ")
	if number < 0 || displayStem == "" {
		return 0, "", "", false
	}
	return number, strings.ToLower(displayStem), displayStem, true
}

// yearFloor is the smallest number sequentialRun treats as a year (V5): a
// "run" of 2010, 2011, 2012 is a list of dated editions, not chapter numbers.
const yearFloor = 1900

// sequentialRun checks whether nums form a near-sequential run: ≥70%
// coverage of [min..max] across unique values, and no gap >2 between
// successive unique values. years reports that the run holds a year (V5, any
// number ≥ yearFloor): dated editions, not chapters. The qualifiers turn it
// into a V5 veto, so a year run comes back as a vetoed cluster with its reason
// instead of vanishing.
func sequentialRun(nums []int) (ok bool, pattern string, years bool) {
	if len(nums) < 3 {
		return false, "", false
	}
	uniq := make(map[int]bool, len(nums))
	for _, n := range nums {
		if n >= 0 {
			uniq[n] = true
		}
	}
	if len(uniq) < 3 {
		return false, "", false
	}
	sorted := make([]int, 0, len(uniq))
	for n := range uniq {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)
	lo, hi := sorted[0], sorted[len(sorted)-1]
	if hi-lo < 2 {
		return false, "", false
	}
	span := hi - lo + 1
	coverage := float64(len(sorted)) / float64(span)
	if coverage < 0.70 {
		return false, "", false
	}
	for i := 1; i < len(sorted); i++ {
		if sorted[i]-sorted[i-1] > 2 {
			return false, "", false
		}
	}
	return true, fmt.Sprintf("%d..%d (%d covered of %d)", lo, hi, len(sorted), span), hi >= yearFloor
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

// repeatedTitle returns the most frequent non-empty string in ss that occurs
// at least twice (ties broken lexically), or "" when every string is distinct.
// A title that only one member has is that member's own name (a chapter name),
// not the work's.
func repeatedTitle(ss []string) string {
	counts := make(map[string]int)
	for _, s := range ss {
		if s != "" {
			counts[s]++
		}
	}
	best, bestN := "", 0
	for s, n := range counts {
		if n < 2 {
			continue
		}
		if n > bestN || (n == bestN && s < best) {
			best, bestN = s, n
		}
	}
	return best
}

// numberSource names where a cluster's chapter numbers came from: the
// filename, the immediate parent folder (the fallback for a file whose name
// has no number), or both.
func numberSource(fromFilename, fromSubdir int) string {
	switch {
	case fromFilename > 0 && fromSubdir > 0:
		return "filename+subdir"
	case fromSubdir > 0:
		return "subdir"
	default:
		return "filename"
	}
}

// typedChapterNumbers extracts each member's chapter number: the filename
// basename first, the immediate parent dir name as the fallback (rogue-subdir
// case). It also counts which source each number really came from: the
// pattern label must say so.
func typedChapterNumbers(group []splitBookSlim) (nums []int, fromFilename, fromSubdir int) {
	nums = make([]int, 0, len(group))
	for _, b := range group {
		n := extractChapterNumber(splitFileStem(b.FilePath))
		if n >= 0 {
			fromFilename++
		} else {
			n = extractChapterNumber(filepath.Base(filepath.Dir(b.FilePath)))
			if n >= 0 {
				fromSubdir++
			}
		}
		nums = append(nums, n)
	}
	return nums, fromFilename, fromSubdir
}

// qualifyTypedGroup applies size/author/series/sequence checks, then the
// distinct-works vetoes. A cluster that passes the first but trips a veto is
// returned with Vetoes set (ok true): detectFromSlimWithVetoes sorts it.
func qualifyTypedGroup(group []splitBookSlim, parentKey, shape string, resolveAuthorName func(int) string, chapterMaxSec int) (SplitBookCandidate, bool) {
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
	// immediate parent dir name (rogue-subdir case). Count which source each
	// number really came from: the label below must say so.
	nums, fromFilename, fromSubdir := typedChapterNumbers(group)
	ok, pattern, yearRun := sequentialRun(nums)
	if !ok {
		return SplitBookCandidate{}, false
	}
	pattern = numberSource(fromFilename, fromSubdir) + ":" + pattern

	// Suggested title: the stripped Title the members share. When no stripped
	// title repeats (flat chapter files, each titled with its own chapter name)
	// no member's title names the work; the folder does.
	titles := make([]string, 0, len(group))
	for _, b := range group {
		titles = append(titles, stripChapterMarker(b.Title))
	}
	suggestedTitle := repeatedTitle(titles)
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
		Vetoes:            vetoDistinctWorks(group, shape, parentKey, chapterMaxSec, yearRun),
		Evidence:          seriesPositionEvidence(group),
	}, true
}

// qualifyRelaxedNumberedSiblingGroup detects a prior scanner failure shape:
// one book row per numbered track in one directory. It does not rely on the
// author/series fields because those rows are frequently what the failure
// corrupted. The full merge still requires later operator review.
func qualifyRelaxedNumberedSiblingGroup(group []splitBookSlim, parentKey string, chapterMaxSec int) (SplitBookCandidate, bool) {
	nums := make([]int, 0, len(group))
	stems := make([]string, 0, len(group))
	displayStems := make([]string, 0, len(group))
	for _, b := range group {
		n, stem, displayStem, ok := numberedSiblingStem(b)
		if !ok {
			return SplitBookCandidate{}, false
		}
		nums = append(nums, n)
		stems = append(stems, stem)
		displayStems = append(displayStems, displayStem)
	}
	if mostCommon(stems) == "" {
		return SplitBookCandidate{}, false
	}
	for _, stem := range stems {
		if stem != stems[0] {
			return SplitBookCandidate{}, false
		}
	}
	ok, pattern, yearRun := sequentialRun(nums)
	if !ok {
		return SplitBookCandidate{}, false
	}
	ids := make([]string, 0, len(group))
	for _, b := range group {
		ids = append(ids, b.ID)
	}
	return SplitBookCandidate{
		ParentFolder:      parentKey,
		BookIDs:           sortBookIDsULIDAsc(ids),
		SuggestedTitle:    displayStems[0],
		SequentialPattern: "filename-stem:" + pattern,
		Shape:             "parent-relaxed",
		Vetoes:            vetoDistinctWorks(group, "parent-relaxed", parentKey, chapterMaxSec, yearRun),
		Evidence:          seriesPositionEvidence(group),
	}, true
}

// ---- distinct-works vetoes ------------------------------------------------

var (
	// splitPartMarkerRe is a Part/Disc/(N of M) marker: a position inside ONE
	// work. Part is deliberately not a V2 volume marker.
	splitPartMarkerRe = regexp.MustCompile(`(?i)\b(?:part|pt|disc|disk|cd)\.?\s*0*\d{1,3}(?:\s*(?:of|/)\s*\d+)?\b|\(\s*\d{1,3}\s*(?:of|/)\s*\d{1,3}\s*\)`)

	// splitChapterWordRe is a chapter-style position ("Chapter 3", "Track #4",
	// "Part 2"). Removed before V2/V2b read a title or name, so a "#" or a
	// "Stem N: Subtitle" shape that is really a chapter heading is not
	// mistaken for a series volume.
	splitChapterWordRe = regexp.MustCompile(`(?i)\b(?:chapter|ch|track|tr|part|pt|disc|disk|cd|section|episode|ep)\.?\s*#?\s*\d{1,4}\b`)

	// splitVolMarkerRe is a series-volume marker: Book/Bk/Vol/Volume/# and its
	// number. NOT "part".
	splitVolMarkerRe = regexp.MustCompile(`(?i)(?:\bbook|\bbk|\bvol(?:ume)?\.?|#)\s*0*(\d{1,3})\b`)

	// splitNumSubtitleRe is "Stem N: Subtitle".
	splitNumSubtitleRe = regexp.MustCompile(`^(.*?\S)\s+0*(\d{1,3})\s*:\s*(\S.*)$`)

	// splitCopySuffixRe is a duplicate-file suffix: "_copy1", " dup 2".
	splitCopySuffixRe = regexp.MustCompile(`(?i)[\s_-]*(?:copy|dup(?:licate)?)\s*\d+$`)

	// splitFolderVetoRe is a folder that names several works: "Books 1-4",
	// "Books 1 thru 6", or a series, collection, omnibus or box set.
	splitFolderVetoRe = regexp.MustCompile(`(?i)\bbooks?\s*\d+\s*(?:-|–|to|thru|through|&|and)\s*\d+\b|\bseries\b|\bcollection\b|\bomnibus\b|\bbox(?:ed)?\s*set\b`)
)

// splitFileStem is a book path's file name without its extension.
func splitFileStem(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// volNumbers is the distinct, ascending Book/Vol numbers across texts.
func volNumbers(texts []string) []int {
	seen := map[int]bool{}
	for _, t := range texts {
		if m := splitVolMarkerRe.FindStringSubmatch(splitChapterWordRe.ReplaceAllString(t, " ")); m != nil {
			seen[extractChapterNumber(m[1])] = true
		}
	}
	out := make([]int, 0, len(seen))
	for n := range seen {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// subdirStem is a member subfolder's name with its position removed: the
// Part/(N of M) marker, then every digit, then punctuation and case.
func subdirStem(name string) string {
	name = splitPartMarkerRe.ReplaceAllString(name, "")
	name = numericTokenRe.ReplaceAllString(name, "")
	name = strings.Join(strings.Fields(name), " ")
	return strings.ToLower(strings.Trim(name, " -_.,:()[]"))
}

func joinInts(ns []int) string {
	if len(ns) > 6 {
		ns = ns[:6]
	}
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

// vetoDistinctWorks returns why a cluster that passed the shape tests is
// several works rather than chapters of one, one line per rule that fired
// (empty = no veto). Each rule alone vetoes:
//
//	V2   two or more distinct numbers in Book/Vol markers (book|bk|vol|volume|#
//	     + N; NOT part), read separately across the titles, the member
//	     subfolder names and the file-name stems
//	V2b  two or more titles of the shape "Stem N: Subtitle" whose subtitles
//	     differ
//	V4   the cluster folder is named for several works (books N-M, series,
//	     collection, omnibus, box set)
//	V5   the run's numbers include a year (yearRun, from sequentialRunShape)
//	V8   two or more file stems end in a copy/dup suffix
//	V9   grandparent shape whose member subfolders, with Part/(N of M) markers
//	     and digits removed, do not all name the same thing
//	V3   any member runs chapterMaxSec or longer (the fragment fixer's
//	     duration gate); an unknown duration (nil or 0) is not a veto here,
//	     the Repairs fixer holds those on the book_file rows
//
// series_sequence is NOT a veto: see seriesPositionEvidence.
func vetoDistinctWorks(group []splitBookSlim, shape, parentKey string, chapterMaxSec int, yearRun bool) []string {
	var vetoes []string
	grand := shape == "grandparent"
	titles := make([]string, 0, len(group))
	subdirs := make([]string, 0, len(group))
	files := make([]string, 0, len(group))
	for _, b := range group {
		titles = append(titles, b.Title)
		files = append(files, splitFileStem(b.FilePath))
		if grand {
			subdirs = append(subdirs, filepath.Base(filepath.Dir(b.FilePath)))
		}
	}

	// V2.
	for _, src := range []struct {
		name  string
		texts []string
	}{{"titles", titles}, {"member subfolder names", subdirs}, {"file names", files}} {
		if ns := volNumbers(src.texts); len(ns) >= 2 {
			vetoes = append(vetoes, fmt.Sprintf("V2 distinct Book/Vol numbers [%s] in %s", joinInts(ns), src.name))
		}
	}

	// V2b.
	subtitles := map[int]string{}
	for _, t := range titles {
		if m := splitNumSubtitleRe.FindStringSubmatch(splitChapterWordRe.ReplaceAllString(t, " ")); m != nil {
			subtitles[extractChapterNumber(m[2])] = strings.ToLower(strings.TrimSpace(m[3]))
		}
	}
	if len(subtitles) >= 2 {
		distinct := map[string]bool{}
		for _, st := range subtitles {
			distinct[st] = true
		}
		if len(distinct) >= 2 {
			vetoes = append(vetoes, fmt.Sprintf("V2b %d titles shaped \"Stem N: Subtitle\" with %d different subtitles", len(subtitles), len(distinct)))
		}
	}

	// V4.
	if base := filepath.Base(parentKey); splitFolderVetoRe.MatchString(base) {
		vetoes = append(vetoes, fmt.Sprintf("V4 the folder %q names several works (books N-M, series, collection, omnibus or box set)", base))
	}

	// V5.
	if yearRun {
		vetoes = append(vetoes, fmt.Sprintf("V5 the run holds a year (a number of %d or more): dated editions, not chapters", yearFloor))
	}

	// V8.
	copies := 0
	for _, f := range files {
		if splitCopySuffixRe.MatchString(f) {
			copies++
		}
	}
	if copies >= 2 {
		vetoes = append(vetoes, fmt.Sprintf("V8 %d file names end in a copy/dup suffix: the numbers count duplicates, not chapters", copies))
	}

	// V9.
	if grand {
		stems := map[string]bool{}
		for _, d := range subdirs {
			stems[subdirStem(d)] = true
		}
		if len(stems) >= 2 {
			vetoes = append(vetoes, fmt.Sprintf("V9 the member subfolders name %d different things once part numbers are removed", len(stems)))
		}
	}

	// V3.
	if chapterMaxSec > 0 {
		long, longest := 0, 0
		for _, b := range group {
			if b.Duration != nil && *b.Duration >= chapterMaxSec {
				long++
				longest = max(longest, *b.Duration)
			}
		}
		if long > 0 {
			vetoes = append(vetoes, fmt.Sprintf("V3 %d of %d members run %d min or longer (longest %d min): whole books, not chapters",
				long, len(group), chapterMaxSec/60, longest/60))
		}
	}
	return vetoes
}

// seriesPositionEvidence reports members that hold different positions in
// their series (series_sequence, or the raw position text). It is shown as
// evidence and never vetoes: the field is unreliable on the rows this
// detector looks at, and a chapter cluster legitimately shares one position.
func seriesPositionEvidence(group []splitBookSlim) []string {
	seqs := map[int]bool{}
	raws := map[string]bool{}
	for _, b := range group {
		if b.SeriesSequence != nil {
			seqs[*b.SeriesSequence] = true
		}
		if r := strings.TrimSpace(b.SeriesPositionRaw); r != "" {
			raws[r] = true
		}
	}
	var out []string
	if len(seqs) >= 2 {
		ns := make([]int, 0, len(seqs))
		for n := range seqs {
			ns = append(ns, n)
		}
		sort.Ints(ns)
		out = append(out, fmt.Sprintf("members hold %d different series_sequence values [%s] (evidence only: not a veto)", len(seqs), joinInts(ns)))
	}
	if len(raws) >= 2 {
		out = append(out, fmt.Sprintf("members hold %d different raw series positions (evidence only: not a veto)", len(raws)))
	}
	return out
}
