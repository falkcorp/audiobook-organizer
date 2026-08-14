// file: internal/dedup/author.go
// version: 1.12.0
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-08-14

package dedup

import (
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/falkcorp/audiobook-organizer/internal/database"
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

// knownProductionCompanies maps lowercased names of audiobook production companies.
var knownProductionCompanies = map[string]bool{
	"soundbooth theater":     true,
	"graphic audio":          true,
	"podium audio":           true,
	"tantor media":           true,
	"tantor audio":           true,
	"blackstone audio":       true,
	"blackstone publishing":  true,
	"recorded books":         true,
	"brilliance audio":       true,
	"marvel":                 true,
	"dc comics":              true,
	"audible studios":        true,
	"audible originals":      true,
	"macmillan audio":        true,
	"random house audio":     true,
	"harpercollins":          true,
	"simon & schuster audio": true,
}

// IsProductionCompany returns true if the name matches a known audiobook production company.
func IsProductionCompany(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if knownProductionCompanies[lower] {
		return true
	}
	// Check keyword suffixes
	for _, suffix := range []string{" theater", " theatre"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// leadingConjunctionRe matches a coordinating conjunction stranded at the start
// of an author name, e.g. "& Conrad Westmaas" or "and Sadie Miller".
//
// The trailing \s+ is load-bearing and must not be relaxed to \s*:
//   - "&#169" and "&#169;2013 by HarperCollinsPublishers" are real rows in the
//     author table — decapitated HTML entities for © from a copyright string that
//     leaked into an artist tag. They are a SEPARATE defect. A bare "^&" strip
//     rewrites them to "#169", which is strictly worse than leaving them alone.
//   - Requiring whitespace also stops "and" from eating the first syllable of
//     real names like "Anders Bergman" or "Andrea Cremer".
var leadingConjunctionRe = regexp.MustCompile(`(?i)^(?:&|and)\s+`)

// NormalizeAuthorName normalizes whitespace around initials and trims.
// "James S. A. Corey" and "James S.A. Corey" both become "James S. A. Corey"
//
// It also strips a leading conjunction. Every delimiter branch of
// SplitCompositeAuthorName funnels through here, and each of them validated a
// candidate part only by asking whether it contained a space — which
// "& Conrad Westmaas" satisfies. An Oxford comma before the ampersand
// ("A, B, & C") makes the comma branch fire before the " & " branch further
// down, so the ampersand is stranded on the final name and stored verbatim.
// That produced 48 "& Name" author rows in one import run. Normalizing here
// rather than in the comma branch closes the same hole in the slash, semicolon
// and bracket branches, which all share it.
func NormalizeAuthorName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return name
	}

	// Normalize multiple spaces to single
	spaceRe := regexp.MustCompile(`\s+`)
	name = spaceRe.ReplaceAllString(name, " ")

	// Strip a stranded leading conjunction, but never turn a name into nothing:
	// a bare "&" or "and" with no remainder is left as-is for the caller's
	// existing empty/short-part checks to reject.
	if stripped := strings.TrimSpace(leadingConjunctionRe.ReplaceAllString(name, "")); stripped != "" {
		name = stripped
	}

	// Expand collapsed initials: "S.A." → "S. A."
	initialsRe := regexp.MustCompile(`([A-Z]\.)([A-Z])`)
	for initialsRe.MatchString(name) {
		name = initialsRe.ReplaceAllString(name, "$1 $2")
	}

	return strings.TrimSpace(name)
}

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
	parenRe := regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	name = parenRe.ReplaceAllString(name, "")

	// If name contains "/" and isn't just initials, take the first part
	if idx := strings.Index(name, "/"); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}

	return strings.TrimSpace(name)
}

// isDirtyAuthorName returns true if the name is obviously not a real author
func isDirtyAuthorName(name string) bool {
	if strings.Contains(name, " - ") {
		return true
	}

	lower := strings.ToLower(name)
	publisherSuffixes := []string{"production", "productions", "publishing", "publishers",
		"press", "studios", "studio", "media", "entertainment", "books", "audio",
		"house", "group", "company", "records", "recordings"}
	for _, suffix := range publisherSuffixes {
		if strings.HasSuffix(lower, " "+suffix) {
			return true
		}
	}

	publisherPrefixes := []string{"bbc ", "penguin ", "harpercollins", "hachette", "simon & schuster"}
	for _, prefix := range publisherPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}

	return IsProductionCompany(name)
}

// SplitCompositeAuthorName splits "Author1 / Author2" or "Author1, Author2" into parts.
// Returns nil or single-element slice if the name doesn't look composite.
func SplitCompositeAuthorName(name string) []string {
	// Don't split AKA patterns
	if regexp.MustCompile(`(?i)\(aka\s`).MatchString(name) {
		return nil
	}

	// Try slash first: "Author1 / Author2"
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		var result []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if len(p) > 2 {
				result = append(result, NormalizeAuthorName(p))
			}
		}
		if len(result) > 1 {
			return result
		}
	}

	// Try comma: "Author1, Author2" — but not "Last, First" format
	// "Last, First" has exactly 2 parts where the second is a single name without spaces
	// "Author1, Author2" has parts where both sides have spaces
	parts := strings.Split(name, ",")
	if len(parts) >= 2 {
		var result []string
		allLookLikeNames := true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// A name part should contain a space (first + last) to be a separate author
			if !strings.Contains(p, " ") {
				allLookLikeNames = false
				break
			}
			result = append(result, NormalizeAuthorName(p))
		}
		if allLookLikeNames && len(result) > 1 {
			return result
		}
	}

	// Try parentheses or brackets: "Author (Author 2)" or "Author [Author 2]"
	if m := regexp.MustCompile(`^(.+?)\s*[\(\[]\s*(.+?)\s*[\)\]]\s*$`).FindStringSubmatch(name); len(m) == 3 {
		outer := strings.TrimSpace(m[1])
		inner := strings.TrimSpace(m[2])
		// Both parts must look like author names (contain a space for first+last)
		if len(outer) > 2 && len(inner) > 2 && strings.Contains(outer, " ") && strings.Contains(inner, " ") {
			return []string{NormalizeAuthorName(outer), NormalizeAuthorName(inner)}
		}
	}

	// Try semicolon: "Author1; Author2"
	if strings.Contains(name, ";") {
		parts := strings.Split(name, ";")
		var result []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if len(p) > 2 && strings.Contains(p, " ") {
				result = append(result, NormalizeAuthorName(p))
			}
		}
		if len(result) > 1 {
			return result
		}
	}

	// Try " and " or " & ": "Author1 and Author2"
	for _, sep := range []string{" and ", " & "} {
		if strings.Contains(strings.ToLower(name), sep) {
			parts := strings.SplitN(strings.ToLower(name), sep, -1)
			// Use original casing by finding separator positions
			var result []string
			remaining := name
			for {
				idx := -1
				for _, s := range []string{" and ", " And ", " AND ", " & "} {
					if i := strings.Index(remaining, s); i >= 0 && (idx < 0 || i < idx) {
						idx = i
					}
				}
				if idx < 0 {
					p := strings.TrimSpace(remaining)
					if len(p) > 2 && strings.Contains(p, " ") {
						result = append(result, NormalizeAuthorName(p))
					}
					break
				}
				p := strings.TrimSpace(remaining[:idx])
				if len(p) > 2 && strings.Contains(p, " ") {
					result = append(result, NormalizeAuthorName(p))
				}
				// Skip past separator
				for _, s := range []string{" and ", " And ", " AND ", " & "} {
					if strings.HasPrefix(remaining[idx:], s) {
						remaining = remaining[idx+len(s):]
						break
					}
				}
			}
			_ = parts // used for detection
			if len(result) > 1 {
				return result
			}
		}
	}

	// Try space-concatenated full names: "R.A. Mejia Charles Dean"
	// Heuristic: try splitting at each word boundary and check if both halves
	// look like valid author names (each has at least first+last).
	// Only attempt this for names with 4+ words (minimum for two "First Last" names).
	words := strings.Fields(name)
	if len(words) >= 4 {
		result := trySplitConcatenatedAuthors(name, words)
		if len(result) > 1 {
			return result
		}
	}

	return nil
}

// trySplitConcatenatedAuthors tries to find a split point in a space-concatenated
// string of author names like "R.A. Mejia Charles Dean" → ["R.A. Mejia", "Charles Dean"].
// It tries each possible split point and checks if both halves look like valid names.
func trySplitConcatenatedAuthors(name string, words []string) []string {
	type candidate struct {
		parts []string
		score int
	}
	var candidates []candidate

	// Try splitting into 2 authors at each word boundary
	for i := 2; i <= len(words)-2; i++ {
		left := strings.Join(words[:i], " ")
		right := strings.Join(words[i:], " ")
		if looksLikeAuthorName(left) && looksLikeAuthorName(right) {
			score := scoreAuthorSplit(left, right)
			candidates = append(candidates, candidate{
				parts: []string{NormalizeAuthorName(left), NormalizeAuthorName(right)},
				score: score,
			})
		}
	}

	// Try splitting into 3 authors (for 6+ words)
	if len(words) >= 6 {
		for i := 2; i <= len(words)-4; i++ {
			for j := i + 2; j <= len(words)-2; j++ {
				left := strings.Join(words[:i], " ")
				mid := strings.Join(words[i:j], " ")
				right := strings.Join(words[j:], " ")
				if looksLikeAuthorName(left) && looksLikeAuthorName(mid) && looksLikeAuthorName(right) {
					score := scoreAuthorSplit(left, mid, right)
					candidates = append(candidates, candidate{
						parts: []string{NormalizeAuthorName(left), NormalizeAuthorName(mid), NormalizeAuthorName(right)},
						score: score,
					})
				}
			}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	// Pick highest-scoring split
	best := candidates[0]
	for _, c := range candidates[1:] {
		if c.score > best.score {
			best = c
		}
	}
	return best.parts
}

// looksLikeAuthorName returns true if the string looks like a plausible author name.
// Must have at least 2 parts (first+last), start with uppercase, and have a
// real surname (not just an initial like "A." which is likely a middle initial).
func looksLikeAuthorName(s string) bool {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) < 2 {
		return false
	}
	// First part should start with uppercase letter
	first := parts[0]
	if len(first) == 0 {
		return false
	}
	r := rune(first[0])
	if r < 'A' || r > 'Z' {
		return false
	}
	// Last part (surname) must be a real name, not an initial
	// "A." or "B" are initials, not surnames
	last := parts[len(parts)-1]
	lastTrimmed := strings.TrimRight(last, ".")
	if len(lastTrimmed) < 3 {
		return false // too short to be a surname — likely an initial
	}
	r = rune(last[0])
	return r >= 'A' && r <= 'Z'
}

// scoreAuthorSplit scores a split of names. Higher = more likely correct.
// Prefers splits where each part has a typical name structure.
func scoreAuthorSplit(parts ...string) int {
	score := 0
	for _, p := range parts {
		words := strings.Fields(p)
		// Prefer 2-3 word names (First Last, First Middle Last)
		if len(words) == 2 {
			score += 10
		} else if len(words) == 3 {
			score += 8
		} else {
			score += 3
		}
		// Bonus for initials (common in author names like "R.A.")
		for _, w := range words[:len(words)-1] { // skip last name
			if len(strings.TrimRight(w, ".")) <= 2 {
				score += 2 // initial like "R." or "J.K."
			}
		}
		// Bonus if last word (surname) has >3 chars
		if len(words[len(words)-1]) > 3 {
			score += 3
		}
	}
	return score
}

// isCompositeAuthorName returns true if the name contains multiple real authors
func isCompositeAuthorName(name string) bool {
	return len(SplitCompositeAuthorName(name)) > 1
}

// areAuthorsDuplicate determines if two author names refer to the same person.
// Much stricter than raw Jaro-Winkler — requires last name match.
func areAuthorsDuplicate(name1, name2 string) bool {
	// Skip dirty names (book titles, publishers)
	if isDirtyAuthorName(name1) || isDirtyAuthorName(name2) {
		return false
	}

	norm1 := strings.ToLower(NormalizeAuthorName(name1))
	norm2 := strings.ToLower(NormalizeAuthorName(name2))

	// Exact normalized match
	if norm1 == norm2 {
		return true
	}

	// Check if one contains the other (e.g. "David Kushner" vs "David Kushner/Wil Wheaton")
	base1 := strings.ToLower(extractBaseAuthor(NormalizeAuthorName(name1)))
	base2 := strings.ToLower(extractBaseAuthor(NormalizeAuthorName(name2)))
	if base1 == base2 {
		return true
	}

	// If after base extraction they still differ, compare parts
	first1, last1 := splitAuthorParts(base1)
	first2, last2 := splitAuthorParts(base2)

	// Both must have a last name
	if last1 == "" || last2 == "" {
		return false
	}

	// Last names must be very similar (>= 0.95) or exact match
	lastSim := jaroWinklerSimilarity(last1, last2)
	if lastSim < 0.95 {
		return false
	}

	// If one has no first name, only match if last names are identical
	if first1 == "" || first2 == "" {
		return last1 == last2
	}

	// First names/initials must also be similar
	// Handle initial vs full name: "J." matches "James", "J. K." matches "Joanne K."
	if isInitialMatch(first1, first2) {
		return true
	}

	firstSim := jaroWinklerSimilarity(first1, first2)
	return firstSim >= 0.85
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

	matchDistance := int(math.Max(float64(len1), float64(len2)))/2 - 1
	if matchDistance < 0 {
		matchDistance = 0
	}

	s1Matches := make([]bool, len1)
	s2Matches := make([]bool, len2)

	matches := 0
	transpositions := 0

	for i := 0; i < len1; i++ {
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
	for i := 0; i < len1; i++ {
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

// BuildAuthorSeriesMap pre-loads series names for all authors from the store.
// Returns a map of authorID → slice of normalized series names (lowercased, trimmed).
// This is an optional input to FindDuplicateAuthors for series cross-referencing.
func BuildAuthorSeriesMap(store interface {
	GetBooksByAuthorIDCore(authorID int) ([]database.BookCore, error)
	GetSeriesByID(id int) (*database.Series, error)
}, authors []database.Author) map[int][]string {
	result := make(map[int][]string, len(authors))
	// H2 (2026-07 error-correction sweep): both store errors below used to be
	// bare `continue`s. A failure here doesn't fail the map build — it just
	// silently degrades author-dedup's series cross-referencing for the
	// affected author (fewer/no series names → sharesSeries() never fires
	// for them). Count both kinds and emit one summary Warn so a systemic
	// store problem shows up instead of manifesting only as "series
	// cross-referencing isn't catching duplicates".
	booksLoadErrs := 0
	seriesLoadErrs := 0
	for _, a := range authors {
		books, err := store.GetBooksByAuthorIDCore(a.ID)
		if err != nil {
			booksLoadErrs++
			continue
		}
		seen := make(map[int]bool)
		for _, b := range books {
			if b.SeriesID == nil || seen[*b.SeriesID] {
				continue
			}
			seen[*b.SeriesID] = true
			series, err := store.GetSeriesByID(*b.SeriesID)
			if err != nil {
				seriesLoadErrs++
				continue
			}
			if series == nil {
				continue
			}
			normalized := strings.ToLower(strings.TrimSpace(series.Name))
			if normalized != "" {
				result[a.ID] = append(result[a.ID], normalized)
			}
		}
	}
	if booksLoadErrs > 0 || seriesLoadErrs > 0 {
		slog.Warn("dedup author-series map: store errors degraded series cross-referencing",
			"authors", len(authors),
			"books_load_errors", booksLoadErrs,
			"series_load_errors", seriesLoadErrs,
		)
	}
	return result
}

// sharesSeries returns true if the two sets of normalized series names have any overlap.
func sharesSeries(seriesA, seriesB []string) bool {
	if len(seriesA) == 0 || len(seriesB) == 0 {
		return false
	}
	setA := make(map[string]bool, len(seriesA))
	for _, s := range seriesA {
		setA[s] = true
	}
	for _, s := range seriesB {
		if setA[s] {
			return true
		}
	}
	return false
}

// FindDuplicateAuthors groups authors by similarity using structured name comparison.
// The threshold parameter is kept for API compatibility but the actual matching
// uses areAuthorsDuplicate which compares first/last names separately.
//
// The optional seriesMap parameter (from BuildAuthorSeriesMap) enables series
// cross-referencing: two authors with different but close names who share a series
// are treated as duplicates.
//
// Performance: Pre-computes normalized names and buckets by last name to reduce
// comparisons from O(n²) to O(n × avg_bucket_size). For 5,000 authors with
// ~2,000 unique last names, this reduces comparisons by ~60-80%.
func FindDuplicateAuthors(authors []database.Author, threshold float64, bookCountFn func(int) int, progressFn ...ProgressCallback) []AuthorDedupGroup {
	return findDuplicateAuthorsInternal(authors, threshold, bookCountFn, nil, progressFn...)
}

// FindDuplicateAuthorsWithSeries is like FindDuplicateAuthors but also uses series
// overlap as a tiebreaker when author names are close but below the strict match threshold.
// Use BuildAuthorSeriesMap to build the seriesMap from a store.
func FindDuplicateAuthorsWithSeries(authors []database.Author, threshold float64, bookCountFn func(int) int, seriesMap map[int][]string, progressFn ...ProgressCallback) []AuthorDedupGroup {
	return findDuplicateAuthorsInternal(authors, threshold, bookCountFn, seriesMap, progressFn...)
}

func findDuplicateAuthorsInternal(authors []database.Author, threshold float64, bookCountFn func(int) int, seriesMap map[int][]string, progressFn ...ProgressCallback) []AuthorDedupGroup {
	var reportProgress ProgressCallback
	if len(progressFn) > 0 && progressFn[0] != nil {
		reportProgress = progressFn[0]
	}

	// Phase 1: Pre-compute normalized names and filter skippable authors
	precomputed := make([]authorPrecomputed, len(authors))
	lastNameBuckets := make(map[string][]int) // lastLower → indices into precomputed
	for i, a := range authors {
		pre := authorPrecomputed{
			index:  i,
			author: a,
		}
		if isMultiAuthorString(a.Name) || isCompositeAuthorName(a.Name) || isDirtyAuthorName(a.Name) {
			pre.skip = true
		} else {
			pre.norm = strings.ToLower(NormalizeAuthorName(a.Name))
			pre.base = strings.ToLower(extractBaseAuthor(NormalizeAuthorName(a.Name)))
			pre.first, pre.last = splitAuthorParts(pre.base)
			if pre.last != "" {
				pre.lastLower = strings.ToLower(pre.last)
			}
		}
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
	for _, bucket := range lastNameBuckets {
		for bi := 0; bi < len(bucket); bi++ {
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
	lastNames := make([]string, 0, len(lastNameBuckets))
	for ln := range lastNameBuckets {
		lastNames = append(lastNames, ln)
	}
	for li := 0; li < len(lastNames); li++ {
		for lj := li + 1; lj < len(lastNames); lj++ {
			if lastNames[li] == lastNames[lj] {
				continue // already handled in same-bucket phase
			}
			if jaroWinklerSimilarity(lastNames[li], lastNames[lj]) < 0.95 {
				continue
			}
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

	// Phase 3.5: Series cross-reference — if two authors share a series and their
	// names are close (JW >= 0.80 on last name), treat them as duplicates.
	// This catches cases like "James S. A. Corey" vs "J. S. A. Corey" when the
	// name similarity alone falls below the strict threshold.
	if len(seriesMap) > 0 {
		for li := 0; li < len(lastNames); li++ {
			bucketI := lastNameBuckets[lastNames[li]]
			for lj := li; lj < len(lastNames); lj++ {
				lastSim := jaroWinklerSimilarity(lastNames[li], lastNames[lj])
				if lastSim < 0.80 {
					continue // last names too different even with series signal
				}
				bucketJ := lastNameBuckets[lastNames[lj]]
				for _, i := range bucketI {
					pi := &precomputed[i]
					if used[pi.author.ID] || pi.skip {
						continue
					}
					startJ := 0
					if li == lj {
						// Same bucket — avoid double-counting (only compare forward pairs)
						startJ = i + 1
					}
					_ = startJ
					for _, j := range bucketJ {
						if li == lj && j <= i {
							continue // same bucket, skip already-checked pairs
						}
						pj := &precomputed[j]
						if used[pj.author.ID] || pj.skip || pj.author.ID == pi.author.ID {
							continue
						}
						// Only apply series signal when name similarity alone is borderline
						if areAuthorsDuplicatePrecomputed(pi, pj) {
							continue // already would have been caught in phases 2/3
						}
						if !sharesSeries(seriesMap[pi.author.ID], seriesMap[pj.author.ID]) {
							continue
						}
						// First-name compatibility check — series match alone isn't enough
						// if first names are clearly different
						if pi.first != "" && pj.first != "" {
							firstSim := jaroWinklerSimilarity(pi.first, pj.first)
							if firstSim < 0.60 && !isInitialMatch(pi.first, pj.first) {
								continue // first names are clearly different people
							}
						}
						// Merge as duplicates
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
							suggested := buildSuggestedName(allInGroup)
							g := AuthorDedupGroup{
								Canonical: canonical,
								Variants:  variants,
								BookCount: bookCountFn(pi.author.ID) + bookCountFn(pj.author.ID),
							}
							if suggested != "" && suggested != canonical.Name {
								g.SuggestedName = suggested
							}
							groups = append(groups, g)
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
	for i := 0; i < len(authors); i++ {
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
	for i := 0; i < len(authors); i++ {
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

// areAuthorsDuplicatePrecomputed is a faster version of areAuthorsDuplicate
// that uses pre-computed normalized names to avoid redundant string operations.
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
