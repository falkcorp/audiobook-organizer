// file: internal/dedup/author.go
// version: 1.26.3
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-10-21

package dedup

import (
	"fmt"
	"math"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/personname"
)

// AuthorDedupGroup represents a group of potentially duplicate authors.
type AuthorDedupGroup struct {
	Canonical           database.Author   `json:"canonical"`
	Variants            []database.Author `json:"variants"`
	BookCount           int               `json:"book_count"`
	SuggestedName       string            `json:"suggested_name,omitempty"`
	SplitNames          []string          `json:"split_names,omitempty"`           // for composite authors like "A / B"
	IsProductionCompany bool              `json:"is_production_company,omitempty"` // true if canonical is a production company
}

// Compiled once, not per call — "compile once" is the rule in this file rather
// than a thing some declarations happen to do.
//
// The normalization regexes that used to sit here moved to
// internal/personname with NormalizeAuthorName on 2026-09-03; what remains is
// used by extractBaseAuthor. SplitCompositeAuthorName moved to
// internal/personname on 2026-10-04 (see authorname_reexport.go).
var (
	// extractBaseAuthor's: called once per author, in newAuthorPrecomputed,
	// when the prefilter index is built.
	authorParenSuffixRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
)

// splitAuthorParts splits "First Middle Last" into (first+middle, last).
// Handles "Last, First" format too.
func splitAuthorParts(name string) (first, last string) {
	name = strings.TrimSpace(name)

	// Handle "Last, First" format
	if idx := strings.Index(name, ","); idx > 0 {
		return strings.TrimSpace(name[idx+1:]), strings.TrimSpace(name[:idx])
	}

	parts := strings.Fields(name)
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return "", parts[0]
	}
	return strings.Join(parts[:len(parts)-1], " "), parts[len(parts)-1]
}

// extractBaseAuthor strips narrator/co-author suffixes like "Author/Narrator"
// or "Author (Narrator Name)" and returns the base author name.
func extractBaseAuthor(name string) string {
	// Strip " (anything)" parenthetical that looks like a role
	// authorParenSuffixRe: see the note on authorSpaceRe. extractBaseAuthor is
	// called once per author, in newAuthorPrecomputed, when the prefilter
	// index is built.
	name = authorParenSuffixRe.ReplaceAllString(name, "")

	// If name contains "/" and isn't just initials, take the first part
	if idx := strings.Index(name, "/"); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}

	return strings.TrimSpace(name)
}

// isCompositeAuthorName returns true if the name contains multiple real authors
func isCompositeAuthorName(name string) bool {
	return len(SplitCompositeAuthorName(name)) > 1
}

// isInitialMatch checks if one name is an initial form of the other.
// "J." matches "James", "J. K." matches "J. K.", "J.K." matches "J. K."
func isInitialMatch(a, b string) bool {
	aParts := strings.Fields(a)
	bParts := strings.Fields(b)

	// Must have same number of name parts
	if len(aParts) != len(bParts) {
		return false
	}

	for i := range aParts {
		ap := strings.TrimRight(aParts[i], ".")
		bp := strings.TrimRight(bParts[i], ".")

		// If both are single char (initials), they must match
		if len(ap) == 1 && len(bp) == 1 {
			if !strings.EqualFold(ap, bp) {
				return false
			}
			continue
		}

		// If one is an initial and the other is a full name, initial must match first letter
		if len(ap) == 1 {
			if !strings.HasPrefix(strings.ToLower(bp), strings.ToLower(ap)) {
				return false
			}
			continue
		}
		if len(bp) == 1 {
			if !strings.HasPrefix(strings.ToLower(ap), strings.ToLower(bp)) {
				return false
			}
			continue
		}

		// Both are full names — must be very similar
		if jaroWinklerSimilarity(ap, bp) < 0.92 {
			return false
		}
	}
	return true
}

// jaroWinklerSimilarity computes the Jaro-Winkler similarity between two strings.
func jaroWinklerSimilarity(s1, s2 string) float64 {
	s1 = strings.ToLower(s1)
	s2 = strings.ToLower(s2)

	if s1 == s2 {
		return 1.0
	}

	len1 := utf8.RuneCountInString(s1)
	len2 := utf8.RuneCountInString(s2)

	if len1 == 0 || len2 == 0 {
		return 0.0
	}

	r1 := []rune(s1)
	r2 := []rune(s2)

	matchDistance := max(int(math.Max(float64(len1), float64(len2)))/2-1, 0)

	s1Matches := make([]bool, len1)
	s2Matches := make([]bool, len2)

	matches := 0
	transpositions := 0

	for i := range len1 {
		start := int(math.Max(0, float64(i-matchDistance)))
		end := int(math.Min(float64(len2-1), float64(i+matchDistance)))

		for j := start; j <= end; j++ {
			if s2Matches[j] || r1[i] != r2[j] {
				continue
			}
			s1Matches[i] = true
			s2Matches[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0.0
	}

	k := 0
	for i := range len1 {
		if !s1Matches[i] {
			continue
		}
		for !s2Matches[k] {
			k++
		}
		if r1[i] != r2[k] {
			transpositions++
		}
		k++
	}

	jaro := (float64(matches)/float64(len1) +
		float64(matches)/float64(len2) +
		float64(matches-transpositions/2)/float64(matches)) / 3.0

	// Winkler modification: boost for common prefix (up to 4 chars)
	prefixLen := 0
	for i := 0; i < int(math.Min(4, math.Min(float64(len1), float64(len2)))); i++ {
		if r1[i] == r2[i] {
			prefixLen++
		} else {
			break
		}
	}

	return jaro + float64(prefixLen)*0.1*(1-jaro)
}

// authorLastNameSimilarity is the Jaro-Winkler score two DIFFERENT last names
// must reach before the authors behind them are compared in full. It is
// deliberately much stricter than the caller-supplied author threshold: this
// gate only decides whether two surname buckets are worth opening.
const authorLastNameSimilarity = 0.95

// scanWorkerCount is how many goroutines phase 3's similarity scan uses.
// Production always gets runtime.NumCPU(); it is a variable only so tests can
// pin a specific shard count, because otherwise every test observes whatever
// core count the machine happens to have -- and on a single-core CI runner the
// parallel path would never be exercised at all while the suite stayed green.
// Tests that set it must restore it and must not call t.Parallel.
var scanWorkerCount = runtime.NumCPU()

// jaroWinklerMinLengthRatio returns the smallest shorter/longer rune-length
// ratio a pair can have and still reach threshold. A pair whose ratio is below
// it is guaranteed to score under threshold on jaroWinklerSimilarity, so a scan
// can skip the real comparison without changing which pairs it accepts. A
// result <= 0 means no pair can be excluded on length alone (every ratio clears
// the bar), which is the case for thresholds at or below 0.8.
//
// Why the bound is sound, against jaroWinklerSimilarity:
//
//	Jaro is (m/len1 + m/len2 + (m-t/2)/m) / 3, and (m-t/2)/m <= 1 because
//	t >= 0. Matches cannot exceed the shorter string, so m <= L where L and M
//	are the shorter and longer rune counts. That gives
//
//	    J <= (L/L + L/M + 1) / 3 = (2 + L/M) / 3
//
//	The Winkler boost is jaro + prefixLen*0.1*(1-jaro) with prefixLen capped at
//	4, so JW <= J + 0.4*(1-J) = 0.4 + 0.6*J. Requiring JW >= t therefore
//	requires J >= (t-0.4)/0.6, and substituting the bound above:
//
//	    (2 + L/M)/3 >= (t-0.4)/0.6   <=>   L/M >= 5t - 4
//
//	At the 0.95 threshold this is L/M >= 0.75: a name can only be a near-match
//	for another within 4/3 of its length.
//
// Lengths are RUNE counts because jaroWinklerSimilarity counts runes;
// measuring bytes would over-skip on any non-ASCII name.
//
// The epsilon biases the ratio DOWN, which makes skipping strictly harder. 5t-4
// is not exactly representable for most t, so without it a pair sitting exactly
// on the bound could be skipped by a rounding error. Erring this way costs a
// wasted comparison; erring the other way would drop a real duplicate.
func jaroWinklerMinLengthRatio(threshold float64) float64 {
	const epsilon = 1e-9
	return 5*threshold - 4 - epsilon
}

// lengthRatioBelowThreshold is the arithmetic half of the prefilter, split out
// so callers that compare one string against many can hoist the rune counting
// out of their inner loop. minRatio must come from jaroWinklerMinLengthRatio.
func lengthRatioBelowThreshold(len1, len2 int, minRatio float64) bool {
	if minRatio <= 0 {
		return false
	}
	shorter, longer := len1, len2
	if shorter > longer {
		shorter, longer = longer, shorter
	}
	if longer == 0 {
		return false
	}
	return float64(shorter) < minRatio*float64(longer)
}

// isMultiAuthorString returns true if the name looks like multiple authors
// (more than 2 comma-separated parts, suggesting "Author1, Author2, Author3").
func isMultiAuthorString(name string) bool {
	parts := strings.Split(name, ",")
	return len(parts) > 3
}

// authorNameScore returns a penalty score for a name. Lower = cleaner/better.
// Prefers full, properly-cased names: "James S. A. Corey" scores better than "J.S.A. Corey".
func authorNameScore(name string) int {
	score := 0

	// Penalize garbage characters
	if strings.Contains(name, "/") {
		score += 25 // slash usually means "Author/Narrator" composite — not a clean author name
	}
	if strings.Contains(name, "(") {
		score += 30 // heavy penalty for parenthetical content like "(Author)" or "(Narrator)"
	}
	if strings.Contains(name, " - ") {
		score += 20
	}
	if strings.HasSuffix(name, "_") {
		score += 20 // trailing underscore garbage
	}

	// Penalize ALL CAPS names (e.g. "JAMES COREY") — unlikely to be the canonical form
	if len(name) > 3 && name == strings.ToUpper(name) {
		score += 20
	}

	// Penalize names that are mostly initials — prefer full names
	parts := strings.Fields(name)
	initialCount := 0
	for _, p := range parts {
		trimmed := strings.TrimRight(p, ".")
		if len(trimmed) == 1 && trimmed >= "A" && trimmed <= "Z" {
			initialCount++
		}
	}
	// Heavy penalty per initial (we want "James" over "J.")
	score += initialCount * 15

	// Penalize bare initials missing their period: "John F Kennedy" vs "John F. Kennedy"
	// A single uppercase letter NOT followed by a period is an unpunctuated initial.
	for _, p := range parts {
		if len(p) == 1 && p >= "A" && p <= "Z" {
			score += 20 // strongly prefer "F." over "F"
		}
	}

	// REWARD longer names (more complete) — invert the length bonus.
	// Max reasonable author name is ~40 chars; subtract length from that max so
	// longer names get a lower (better) score.
	score += max(0, 40-len(name))

	return score
}

// buildSuggestedName picks the best version of each name part across all variants.
// E.g., given "J. S. A. Corey" and "James S.A. Corey" → "James S. A. Corey"
func buildSuggestedName(authors []database.Author) string {
	if len(authors) == 0 {
		return ""
	}
	if len(authors) == 1 {
		return NormalizeAuthorName(authors[0].Name)
	}

	// Split each name into parts, pick the longest version for each position
	type nameParts struct {
		first string
		last  string
		parts []string // all first/middle parts
	}

	var all []nameParts
	maxParts := 0
	for _, a := range authors {
		norm := NormalizeAuthorName(a.Name)
		first, last := splitAuthorParts(norm)
		fp := strings.Fields(first)
		all = append(all, nameParts{first: first, last: last, parts: fp})
		if len(fp) > maxParts {
			maxParts = len(fp)
		}
	}

	// For each position, pick the longest (most expanded) version
	bestParts := make([]string, maxParts)
	for pos := 0; pos < maxParts; pos++ {
		best := ""
		for _, np := range all {
			if pos < len(np.parts) {
				candidate := np.parts[pos]
				// Prefer non-initial over initial
				candidateTrimmed := strings.TrimRight(candidate, ".")
				bestTrimmed := strings.TrimRight(best, ".")
				if best == "" {
					best = candidate
				} else if len(candidateTrimmed) > 1 && len(bestTrimmed) <= 1 {
					// candidate is a full name, best is initial — use candidate
					best = candidate
				} else if len(candidateTrimmed) > len(bestTrimmed) && strings.HasPrefix(strings.ToLower(candidateTrimmed), strings.ToLower(bestTrimmed)) {
					best = candidate
				}
			}
		}
		// Ensure initials have trailing dot
		trimmed := strings.TrimRight(best, ".")
		if len(trimmed) == 1 && trimmed >= "A" && trimmed <= "Z" {
			best = trimmed + "."
		}
		bestParts[pos] = best
	}

	// Pick longest last name
	bestLast := ""
	for _, np := range all {
		if len(np.last) > len(bestLast) {
			bestLast = np.last
		}
	}

	result := strings.Join(bestParts, " ")
	if bestLast != "" {
		if result != "" {
			result += " "
		}
		result += bestLast
	}
	return result
}

// pickCanonicalAuthor selects the cleanest author name from a group.
func pickCanonicalAuthor(authors []database.Author, bookCountFn func(int) int) database.Author {
	if len(authors) == 0 {
		return database.Author{}
	}
	best := 0
	bestScore := authorNameScore(authors[0].Name)
	bestBooks := bookCountFn(authors[0].ID)

	for i := 1; i < len(authors); i++ {
		score := authorNameScore(authors[i].Name)
		books := bookCountFn(authors[i].ID)
		if score < bestScore || (score == bestScore && books > bestBooks) {
			best = i
			bestScore = score
			bestBooks = books
		}
	}
	return authors[best]
}

// ProgressCallback is called periodically during long-running dedup operations.
// current is the number of items processed, total is the total number of items.
type ProgressCallback func(current, total int, message string)

// authorPrecomputed holds pre-computed data for an author to avoid redundant
// string operations during O(n²) comparison.
type authorPrecomputed struct {
	index     int
	author    database.Author
	skip      bool   // dirty, multi-author, or composite
	norm      string // lowercased normalized name
	base      string // lowercased base author (before slash etc.)
	first     string // first name part
	last      string // last name part
	lastLower string // lowercased last name for bucketing
}

// newAuthorPrecomputed builds the comparison form of one author: dirty,
// multi-author and composite names are marked skip and carry no name parts;
// every other name gets its normalized, base, first/last parts filled in.
func newAuthorPrecomputed(index int, a database.Author) authorPrecomputed {
	pre := authorPrecomputed{
		index:  index,
		author: a,
	}
	if isMultiAuthorString(a.Name) || isCompositeAuthorName(a.Name) || IsDirtyAuthorName(a.Name) || personname.HasPartTrackMarker(a.Name) {
		pre.skip = true
	} else {
		pre.norm = strings.ToLower(NormalizeAuthorName(a.Name))
		pre.base = strings.ToLower(extractBaseAuthor(NormalizeAuthorName(a.Name)))
		pre.first, pre.last = splitAuthorParts(pre.base)
		if pre.last != "" {
			pre.lastLower = strings.ToLower(pre.last)
		}
	}
	return pre
}

// FindDuplicateAuthors groups authors by similarity using structured name comparison.
// The threshold parameter is kept for API compatibility but the actual matching
// uses areAuthorsDuplicatePrecomputed, which compares first/last names
// separately.
//
// Performance: Pre-computes normalized names and buckets by last name to reduce
// comparisons from O(n²) to O(n × avg_bucket_size). For 5,000 authors with
// ~2,000 unique last names, this reduces comparisons by ~60-80%.
func FindDuplicateAuthors(authors []database.Author, threshold float64, bookCountFn func(int) int, progressFn ...ProgressCallback) []AuthorDedupGroup {
	var reportProgress ProgressCallback
	if len(progressFn) > 0 && progressFn[0] != nil {
		reportProgress = progressFn[0]
	}

	// Phase 1: Pre-compute normalized names and filter skippable authors
	precomputed := make([]authorPrecomputed, len(authors))
	lastNameBuckets := make(map[string][]int) // lastLower → indices into precomputed
	for i, a := range authors {
		pre := newAuthorPrecomputed(i, a)
		precomputed[i] = pre

		// Bucket by last name for faster comparison
		if !pre.skip && pre.lastLower != "" {
			lastNameBuckets[pre.lastLower] = append(lastNameBuckets[pre.lastLower], i)
		}
	}

	if reportProgress != nil {
		reportProgress(0, len(authors), fmt.Sprintf("Pre-computed %d authors into %d last-name buckets", len(authors), len(lastNameBuckets)))
	}

	used := make(map[int]bool)
	var groups []AuthorDedupGroup

	// Phase 2: Compare within last-name buckets (exact last name match)
	// Plus check similar last names via Jaro-Winkler >= 0.95
	processed := 0
	// Iterate buckets in a stable order. Bucket CONTENTS are order-independent
	// (buckets partition authors by exact last name, so no author is reachable
	// from two of them), but the order groups are appended in is user-visible,
	// and ranging the map directly reshuffled the result set on every run.
	bucketKeys := make([]string, 0, len(lastNameBuckets))
	for ln := range lastNameBuckets {
		bucketKeys = append(bucketKeys, ln)
	}
	sort.Strings(bucketKeys)

	for _, bucketKey := range bucketKeys {
		bucket := lastNameBuckets[bucketKey]
		for bi := range bucket {
			i := bucket[bi]
			pi := &precomputed[i]
			if used[pi.author.ID] || pi.skip {
				continue
			}

			group := AuthorDedupGroup{Canonical: pi.author, Variants: []database.Author{}}

			// Compare against rest of same bucket
			for bj := bi + 1; bj < len(bucket); bj++ {
				j := bucket[bj]
				pj := &precomputed[j]
				if used[pj.author.ID] || pj.skip {
					continue
				}
				if areAuthorsDuplicatePrecomputed(pi, pj) {
					group.Variants = append(group.Variants, pj.author)
					used[pj.author.ID] = true
				}
			}

			if len(group.Variants) > 0 {
				allInGroup := make([]database.Author, 0, 1+len(group.Variants))
				allInGroup = append(allInGroup, pi.author)
				allInGroup = append(allInGroup, group.Variants...)
				canonical := pickCanonicalAuthor(allInGroup, bookCountFn)
				group.Canonical = canonical
				var variants []database.Author
				for _, a := range allInGroup {
					if a.ID != canonical.ID {
						variants = append(variants, a)
					}
				}
				group.Variants = variants

				suggested := buildSuggestedName(allInGroup)
				if suggested != "" && suggested != canonical.Name {
					group.SuggestedName = suggested
				}

				used[pi.author.ID] = true
				totalBooks := bookCountFn(canonical.ID)
				for _, v := range group.Variants {
					totalBooks += bookCountFn(v.ID)
				}
				group.BookCount = totalBooks
				groups = append(groups, group)
			}
		}
		processed += len(bucket)
		if reportProgress != nil && processed%200 == 0 {
			reportProgress(processed, len(authors), fmt.Sprintf("Comparing authors... (%d/%d, %d groups found)", processed, len(authors), len(groups)))
		}
	}

	// Phase 3: Cross-bucket comparison for similar (not exact) last names.
	// Build list of unique last names, compare pairs with JW >= 0.95.
	//
	// Sorted for the same reason phase 2's iteration is: the grouping below is
	// greedy over `used`, so the ORDER in which pairs are visited decides which
	// author becomes a group's anchor and which get absorbed into it. Ranging a
	// map here made the group contents themselves vary run to run.
	lastNames := make([]string, 0, len(lastNameBuckets))
	for ln := range lastNameBuckets {
		lastNames = append(lastNames, ln)
	}
	sort.Strings(lastNames)
	// Finding which last names are similar is the most expensive loop in author
	// dedup -- every pair of distinct names, 26,357,430 of them on the
	// production library's 7,261 -- and it is PURE: it reads only lastNames and
	// writes nothing shared. The grouping it feeds is the opposite, being greedy
	// over `used` and order-dependent. So the two are separated here: the scan
	// runs across all cores, then the handful of surviving pairs are grouped
	// serially.
	//
	// No lock is needed because each worker writes only similarPairs[li], and
	// walking li in ascending order afterwards visits pairs in exactly the order
	// the original nested loop did, so the result is unchanged.
	//
	// Workers pull outer indices from a shared counter instead of taking a
	// contiguous range. The inner loop runs len(lastNames)-li times, so a static
	// split would hand the worker holding li=0 thousands of times the work of
	// the one holding the tail, and the whole phase would finish no sooner than
	// that one worker.
	//
	// Rune lengths are counted once here rather than inside the scan. Every
	// length is read len(lastNames)-1 times across the pass, so counting them
	// per-comparison meant ~52.7M RuneCountInString calls to learn 7,261 facts.
	//
	// The scan also used to open with `if lastNames[li] == lastNames[lj]
	// { continue }`, commented as "already handled in same-bucket phase". That
	// could never fire: lastNames holds the KEYS of lastNameBuckets, so its
	// entries are distinct by construction. It was a string compare on all
	// 26.4M pairs guarding a case the data model rules out.
	lastNameRunes := make([]int, len(lastNames))
	for i, ln := range lastNames {
		lastNameRunes[i] = utf8.RuneCountInString(ln)
	}
	minRatio := jaroWinklerMinLengthRatio(authorLastNameSimilarity)

	similarPairs := make([][]int, len(lastNames))
	nextLi := int64(-1)
	workers := min(scanWorkerCount, len(lastNames))
	var scanWG sync.WaitGroup
	for range workers {
		scanWG.Go(func() {
			for {
				li := int(atomic.AddInt64(&nextLi, 1))
				if li >= len(lastNames) {
					return
				}
				var matches []int
				for lj := li + 1; lj < len(lastNames); lj++ {
					// Screen on length before paying for the real
					// comparison. jaroWinklerSimilarity allocates two rune
					// slices and two match bitmaps per call. The length test
					// is provably conservative (see jaroWinklerMinLengthRatio's
					// doc comment), so this changes only the cost, not the
					// outcome; it discards ~61% of pairs on the real corpus.
					if lengthRatioBelowThreshold(lastNameRunes[li], lastNameRunes[lj], minRatio) {
						continue
					}
					if jaroWinklerSimilarity(lastNames[li], lastNames[lj]) < authorLastNameSimilarity {
						continue
					}
					matches = append(matches, lj)
				}
				similarPairs[li] = matches
			}
		})
	}
	scanWG.Wait()

	for li := 0; li < len(lastNames); li++ {
		for _, lj := range similarPairs[li] {
			// Similar last names — compare all pairs across these two buckets
			bucketI := lastNameBuckets[lastNames[li]]
			bucketJ := lastNameBuckets[lastNames[lj]]
			for _, i := range bucketI {
				pi := &precomputed[i]
				if used[pi.author.ID] || pi.skip {
					continue
				}
				for _, j := range bucketJ {
					pj := &precomputed[j]
					if used[pj.author.ID] || pj.skip {
						continue
					}
					if areAuthorsDuplicatePrecomputed(pi, pj) {
						// Add to existing group if pi already has one, else create new
						found := false
						for gi := range groups {
							if groups[gi].Canonical.ID == pi.author.ID {
								groups[gi].Variants = append(groups[gi].Variants, pj.author)
								used[pj.author.ID] = true
								groups[gi].BookCount += bookCountFn(pj.author.ID)
								found = true
								break
							}
						}
						if !found {
							used[pi.author.ID] = true
							used[pj.author.ID] = true
							allInGroup := []database.Author{pi.author, pj.author}
							canonical := pickCanonicalAuthor(allInGroup, bookCountFn)
							var variants []database.Author
							for _, a := range allInGroup {
								if a.ID != canonical.ID {
									variants = append(variants, a)
								}
							}
							groups = append(groups, AuthorDedupGroup{
								Canonical: canonical,
								Variants:  variants,
								BookCount: bookCountFn(pi.author.ID) + bookCountFn(pj.author.ID),
							})
						}
					}
				}
			}
		}
	}

	if reportProgress != nil {
		reportProgress(len(authors), len(authors), fmt.Sprintf("Comparison complete: %d groups found", len(groups)))
	}

	// Phase 4: Add composite/multi-author entries as separate groups with split info
	for i := range authors {
		if used[authors[i].ID] {
			continue
		}
		splitNames := SplitCompositeAuthorName(authors[i].Name)
		if len(splitNames) > 1 {
			groups = append(groups, AuthorDedupGroup{
				Canonical:  authors[i],
				Variants:   []database.Author{},
				BookCount:  bookCountFn(authors[i].ID),
				SplitNames: splitNames,
			})
			used[authors[i].ID] = true
		}
	}

	// Phase 5: Surface standalone production company authors as their own groups
	for i := range authors {
		if used[authors[i].ID] {
			continue
		}
		if IsProductionCompany(authors[i].Name) {
			bc := bookCountFn(authors[i].ID)
			if bc > 0 {
				groups = append(groups, AuthorDedupGroup{
					Canonical:           authors[i],
					Variants:            []database.Author{},
					BookCount:           bc,
					IsProductionCompany: true,
				})
				used[authors[i].ID] = true
			}
		}
	}

	return groups
}

// areAuthorsDuplicatePrecomputed is an author-pair predicate over pre-computed
// normalized names, which avoids redundant string operations in the O(n^2)
// scan.
func areAuthorsDuplicatePrecomputed(a, b *authorPrecomputed) bool {
	// Exact normalized match
	if a.norm == b.norm {
		return true
	}

	// Base author match (handles "Author / Narrator" style)
	if a.base == b.base {
		return true
	}

	// Both must have a last name
	if a.last == "" || b.last == "" {
		return false
	}

	// Last names must be very similar
	lastSim := jaroWinklerSimilarity(a.last, b.last)
	if lastSim < 0.95 {
		return false
	}

	// If one has no first name, only match if last names are identical
	if a.first == "" || b.first == "" {
		return a.last == b.last
	}

	// First names/initials must also be similar
	if isInitialMatch(a.first, b.first) {
		return true
	}

	firstSim := jaroWinklerSimilarity(a.first, b.first)
	return firstSim >= 0.85
}
