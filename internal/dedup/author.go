// file: internal/dedup/author.go
// version: 1.22.2
// guid: d4e5f6a7-b8c9-0d1e-2f3a-4b5c6d7e8f90
// last-edited: 2026-09-02

package dedup

import (
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"
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

// Compiled once, not per call. NormalizeAuthorName runs twice for every
// candidate pair inside the pairwise metadata-fuzzy scan, so a
// regexp.MustCompile in its body is two compilations per comparison over a
// whole-library candidate set. leadingConjunctionRe below was already
// package-level; these two were missed.
var (
	// \s in Go's regexp is ASCII-only ([\t\n\f\r ]), so it does NOT match U+00A0
	// or any other Unicode space separator. \p{Zs} adds them. Without it,
	// "John\u00a0Smith" survives normalization intact and is stored as an author
	// that can never compare equal to "John Smith" in any index -- and it now
	// REACHES that point, because LooksLikePersonName uses strings.Fields, which
	// does split on U+00A0, where main's `strings.Contains(outer, " ")` did not.
	authorSpaceRe    = regexp.MustCompile(`[\s\p{Zs}]+`)
	authorInitialsRe = regexp.MustCompile(`([A-Z]\.)([A-Z])`)
	// extractBaseAuthor's: called twice per author-name comparison
	// (authorNamesEquivalent, below) and once per author when the prefilter
	// index is built.
	authorParenSuffixRe = regexp.MustCompile(`\s*\([^)]*\)\s*$`)
	// SplitCompositeAuthorName's two. Not on the pairwise scan path -- these
	// are hoisted for consistency, so "compile once" is the rule in this file
	// rather than a thing three of five declarations happen to do.
	authorAkaRe          = regexp.MustCompile(`(?i)\(aka\s`)
	authorBracketSplitRe = regexp.MustCompile(`^(.+?)\s*[\(\[]\s*(.+?)\s*[\)\]]\s*$`)
)

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
	name = authorSpaceRe.ReplaceAllString(name, " ")

	// Strip a stranded leading conjunction, but never turn a name into nothing:
	// a bare "&" or "and" with no remainder is left as-is for the caller's
	// existing empty/short-part checks to reject.
	if stripped := strings.TrimSpace(leadingConjunctionRe.ReplaceAllString(name, "")); stripped != "" {
		name = stripped
	}

	// Expand collapsed initials: "S.A." → "S. A."
	for authorInitialsRe.MatchString(name) {
		name = authorInitialsRe.ReplaceAllString(name, "$1 $2")
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
	// authorParenSuffixRe: see the note on authorSpaceRe. extractBaseAuthor is
	// called twice per author-name comparison (author.go:538-539) and once per
	// author in the prefilter build (:1068).
	name = authorParenSuffixRe.ReplaceAllString(name, "")

	// If name contains "/" and isn't just initials, take the first part
	if idx := strings.Index(name, "/"); idx > 0 {
		name = strings.TrimSpace(name[:idx])
	}

	return strings.TrimSpace(name)
}

// IsDirtyAuthorName returns true if the name is obviously not a real author:
// publisher/production names, "A - B" separators, copyright fragments and
// HTML-entity shrapnel (leading "©" or "&#"), and strings that OPEN with a
// 4-digit year ("2013 by HarperCollinsPublishers") — those are rights lines
// from artist tags, never people. Exported so CREATION paths can reject these
// up front instead of minting rows that need repair later (C413; author rows
// 46583 "&#169" and 51870 "&#169;2013 by HarperCollinsPublishers").
func IsDirtyAuthorName(name string) bool {
	name = strings.TrimSpace(name)
	if strings.Contains(name, " - ") {
		return true
	}
	if strings.HasPrefix(name, "©") || strings.HasPrefix(name, "&#") {
		return true
	}
	if leadingYearRe.MatchString(name) {
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

// leadingYearRe matches names that BEGIN with a standalone 4-digit year —
// copyright lines, not people. Anchored so "1984 George Orwell" style titles
// are caught but "Agent 47" style names (year not leading) are not.
var leadingYearRe = regexp.MustCompile(`^\d{4}\b`)

// SplitCompositeAuthorName splits "Author1 / Author2" or "Author1, Author2" into parts.
// Returns nil or single-element slice if the name doesn't look composite.
func SplitCompositeAuthorName(name string) []string {
	// Don't split AKA patterns
	if authorAkaRe.MatchString(name) {
		return nil
	}

	// A source carrying subtitle punctuation is a title, not a credit list —
	// refuse every split rather than guessing at its clauses (C414).
	if strings.ContainsAny(name, ":!?") {
		return nil
	}

	// Try slash first: "Author1 / Author2" -- shape-gated like every other branch.
	//
	// This is the FIRST branch tried, and until 2026-09-01 its only test was
	// `len(p) > 2` -- weaker even than the "contains a space" test C414 removed
	// from the comma branch, since it does not require a space at all. It minted
	// exactly the strings this file claims to refuse:
	//
	//   "Book 3 / Ida Wells"          -> ["Book 3" "Ida Wells"]
	//   "the quick brown / Ida Wells" -> ["the quick brown" "Ida Wells"]
	//   "Ann Petry (DBY) / Ida Wells" -> ["Ann Petry (DBY)" "Ida Wells"]
	//   "Unabridged / Ida Wells"      -> ["Unabridged" "Ida Wells"]
	//
	// It was missed when the comma, bracket, semicolon and and/& branches were
	// gated, because it was found by READING the branches rather than by running
	// them: the consumer test's separator list had no "/" in it, so no input
	// reached this branch at all.
	if strings.Contains(name, "/") {
		parts := strings.Split(name, "/")
		var result []string
		for _, p := range parts {
			if n := NormalizeAuthorName(strings.TrimSpace(p)); personname.LooksLikePersonName(n) {
				result = append(result, n)
			}
		}
		if len(result) > 1 {
			return result
		}
	}

	// Try comma: "Author1, Author2" — but not "Last, First" format
	// "Last, First" has exactly 2 parts where the second is a single name without spaces
	// "Author1, Author2" has parts where both sides have spaces.
	//
	// C414: "contains a space" alone let TITLE clauses through — a comma-split
	// of "So Long, and Thanks for All the Fish" minted "and Thanks for All the
	// Fish" as an author (row 46595; also 46989 "and the Farm Boy (DBY)" and
	// 47193 "and Make Better Decisions"). Every part must be person-shaped or
	// the whole split is refused — refusing leaves the composite VISIBLY wrong
	// for repair rather than laundering a title fragment into a name.
	parts := strings.Split(name, ",")
	if len(parts) >= 2 {
		var result []string
		allLookLikeNames := true
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			// Shape-check the NORMALIZED part: credit lists legitimately read
			// "…, and Conrad Westmaas" and the normalizer strips that leading
			// conjunction; a title clause's remainder still fails the shape.
			normalized := NormalizeAuthorName(p)
			if !personname.LooksLikePersonName(normalized) {
				allLookLikeNames = false
				break
			}
			result = append(result, normalized)
		}
		if allLookLikeNames && len(result) > 1 {
			return result
		}
	}

	// Try parentheses or brackets: "Author (Author 2)" or "Author [Author 2]"
	//
	// C414 (cont.): the comma branch above refuses the WHOLE split when any part
	// fails the shape check, but it `break`s rather than returning -- so a refusal
	// falls through to here and to the semicolon branch below. Those two branches
	// used to ask only `len(p) > 2 && strings.Contains(p, " ")`, which is exactly
	// the "contains a space" test C414 removed from the comma branch for minting
	// "and the Farm Boy (DBY)". Measured 2026-09-01: they still minted
	// "Ann Petry (DBY), Ida Wells", "the quick brown, Ida Wells" and
	// "So Long, and Thanks for All the Fish" as author names. All five branches now
	// gate on the SAME personname.LooksLikePersonName, so the comment above is a
	// control and not just a description.
	//
	// "All four" was wrong when first written: there are FIVE split branches, and
	// the slash branch above -- the first one tried -- was still ungated. The
	// claim was made from reading the branches rather than running them. It now
	// holds for all five, and TestSplitCompositeNeverMintsANonPersonPart carries
	// "/" in its separator list so that a future ungating is observable.
	if m := authorBracketSplitRe.FindStringSubmatch(name); len(m) == 3 {
		outer := strings.TrimSpace(m[1])
		inner := strings.TrimSpace(m[2])
		if no, ni := NormalizeAuthorName(outer), NormalizeAuthorName(inner); personname.LooksLikePersonName(no) && personname.LooksLikePersonName(ni) {
			return []string{no, ni}
		}
	}

	// Try semicolon: "Author1; Author2" -- shape-gated like the comma branch.
	// Note this still admits "Smith, John; Doe, Jane": each semicolon part is
	// normalized first, and a last-first part is person-shaped after normalization.
	if strings.Contains(name, ";") {
		parts := strings.Split(name, ";")
		var result []string
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if n := NormalizeAuthorName(p); personname.LooksLikePersonName(n) {
				result = append(result, n)
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
					// Same person-shape gate as the comma branch (C414):
					// "So Long, and Thanks for All the Fish" reaches THIS
					// branch via its " and ", and a title clause here is just
					// as capable of minting a fake author.
					if norm := NormalizeAuthorName(p); len(p) > 2 && personname.LooksLikePersonName(norm) {
						result = append(result, norm)
					} else if len(p) > 2 {
						return nil // one non-name clause poisons the whole split
					}
					break
				}
				p := strings.TrimSpace(remaining[:idx])
				if norm := NormalizeAuthorName(p); len(p) > 2 && personname.LooksLikePersonName(norm) {
					result = append(result, norm)
				} else if len(p) > 2 {
					return nil // one non-name clause poisons the whole split
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
	// A comma-bearing name already had its chance in the comma branch above;
	// reaching here means its clauses FAILED the person-shape gate, and
	// re-splitting them on spaces would launder the same title fragments the
	// gate just refused (C414).
	words := strings.Fields(name)
	if len(words) >= 4 && !strings.Contains(name, ",") {
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
	// This was a FIFTH copy of the person-name heuristic, and it carried the
	// ASCII byte test (r < 'A' || r > 'Z') that this package's unification exists
	// to delete -- so trySplitConcatenatedAuthors silently refused to split any
	// name whose first letter is non-ASCII (Émile Zola, Åsa Larsson, 村上 春樹)
	// while accepting title fragments the shared predicate rejects. Found by
	// TestSplitCompositeNeverMintsANonPersonPart, which caught
	// "One Two Three Four Five (Bob Jones)" being split into
	// ["One Two" "Three Four" "Five (Bob Jones)"].
	//
	// It is composed rather than replaced: the surname rule below is a genuine
	// extra constraint this branch needs and the shared predicate does not have.
	if !personname.LooksLikePersonName(s) {
		return false
	}
	// The ORIGINAL carried TWO constraints past the ASCII test, and composing it
	// from LooksLikePersonName preserved only the length one. The dropped
	// constraint was "the last word must start with an uppercase letter", and
	// losing it is not cosmetic: LooksLikePersonName deliberately PERMITS
	// interior lowercase name particles, so every particle of >=3 runes (van,
	// von, del, della, dos, den, ter, bin, ibn, mac) began qualifying as a
	// SURNAME. That admits "Ludwig van" as a name, which unlocks the 3-way split
	// below and -- through scoreAuthorSplit -- makes it OUTSCORE the right answer:
	//
	//   "Ludwig van Beethoven Wolfgang Amadeus Mozart"
	//     was  ["Ludwig van Beethoven" "Wolfgang Amadeus Mozart"]   (score 22)
	//     became ["Ludwig van" "Beethoven Wolfgang" "Amadeus Mozart"] (score 36)
	//
	// The <=2-rune particles (de, di, du, da, la, le, al, el) were shielded by the
	// length rule, which is why "Simone de Beauvoir Jean Paul Sartre" survived and
	// hid this. Restored below in the unicode-correct form -- "must not be
	// LOWERCASE", never "must be uppercase", since unicode.IsUpper is false for
	// every caseless script.
	parts := strings.Fields(s)
	lastWord := parts[len(parts)-1]
	// A name particle is never a surname, in any casing: "Ludwig van" and
	// "Volker Le" are both non-names, and admitting either unlocks a 3-way split
	// that scoreAuthorSplit then ranks ABOVE the correct 2-way one.
	//
	// This was written as `unicode.IsLower(first rune) || IsNameParticle(...)`
	// with a comment claiming both halves were needed. The IsLower half was DEAD:
	// LooksLikePersonName has already run above, and it rejects any word starting
	// lowercase UNLESS it is a listed particle -- so by this line a lowercase last
	// word is necessarily a particle that IsNameParticle catches. Mutation-verified
	// (neutralizing that half leaves the suite green). The claim was asserted, not
	// controlled.
	//
	// KNOWN WEAKNESS, stated plainly rather than implied: this is a CLOSED LIST.
	// Der, Ten, Abu, Ben, Bint, Op, Zu, Zur, Dem, Af, Av, Nic and Ap are all real
	// particles that are not in it. The length rule below is what currently keeps
	// most of them out, which means the two rules are entangled -- relaxing the
	// length rule re-exposes exactly this gap.
	if personname.IsNameParticle(lastWord) {
		return false
	}
	// And it must not be a bare INITIAL: "A." and "B" are initials. This is what
	// keeps "R.A. Mejia Charles Dean" splitting at the right boundary instead of
	// stranding "R.A." as a surname.
	//
	// The original said `len(lastTrimmed) < 3` -- a BYTE count standing in for the
	// question "is this an abbreviation?", and the proxy is wrong in both
	// directions once non-ASCII is admitted:
	//   - as BYTES it is meaningless for CJK ("春樹" is 6 bytes, so it passed by
	//     accident and only the ASCII test above was rejecting it);
	//   - rewritten as a flat RUNE count >= 2 it admits two-letter LATIN tokens,
	//     which re-opens the particle gap above: "Jane St Clair Wolfgang Amadeus
	//     Mozart" split as ["Jane St" "Clair Wolfgang" "Amadeus Mozart"], and the
	//     same for "Klaus Zu Guttenberg" and "Jane Ph D". St/Zu/Ph are not in the
	//     particle list and at >= 3 could never reach it.
	//
	// The discriminator is SCRIPT, not length: a two-character surname is ordinary
	// in Han/Hiragana/Katakana/Hangul and is almost always an abbreviation
	// elsewhere. So the threshold is script-conditional, and expressed as an
	// allow-list so an unenumerated script fails CLOSED -- see below.
	//
	// COST, accepted deliberately: romanized two-letter surnames written in Latin
	// (Wang Li, Chen Yu, Ng, Wu, Ho) are refused, so "Wang Li Chen Yu" does not
	// split. That is a MISS. The alternative is the three silent WRONG splits
	// above, and this file's own C414 rule is that refusing beats laundering:
	// a missed split leaves the composite visibly wrong for repair, a wrong split
	// does not.
	trimmed := []rune(strings.TrimRight(lastWord, "."))
	if len(trimmed) == 0 {
		return false
	}
	if isSyllabicOrLogographic(trimmed[0]) {
		// No length floor at all. "Is this a bare initial?" is a LATIN
		// orthographic question -- "J.", "R.A." -- and Han/Hiragana/Katakana/
		// Hangul have no initial form, so the rule simply does not apply. A
		// single character is an ordinary given name there: 田中 翼 (Tanaka
		// Tsubasa), 山田 誠. Holding them to a Latin minimum is the same category
		// error as the byte count this rule replaced.
		return len(trimmed) >= 1
	}
	return len(trimmed) >= 3
}

// isSyllabicOrLogographic reports whether r belongs to a script where a
// TWO-CHARACTER word is an ordinary whole word rather than an abbreviation, so
// the surname threshold may safely drop to 2.
//
// This is an ALLOW-LIST, and the direction is the point. It was first written as
// its complement -- a deny-list naming Latin, Cyrillic and Greek, with every
// other script falling through to the permissive >= 2 branch. That is fail-OPEN
// for every script nobody enumerated, and the scripts nobody enumerated had the
// exact bug the threshold exists to prevent:
//
//	"محمد بن سلمان أحمد"      -> ["محمد بن" "سلمان أحمد"]     Arabic bin, 2 letters
//	"דוד בן גוריון משה"       -> ["דוד בן" "גוריון משה"]      Hebrew ben -- that is
//	                                                          David Ben-Gurion, split
//	                                                          with "ben" as the surname
//	"عبد ال فهد محمد"         -> ["عبد ال" "فهد محمد"]        Arabic article al
//	"Արամ Բա Սարգսյան Պետրոս" -> ["Արամ Բա" "Սարգսյան Պետրոս"] Armenian
//	"राम बा शर्मा विष्णु"      -> ["राम बा" "शर्मा विष्णु"]     Devanagari
//
// personname.IsNameParticle can never catch those -- it is a romanized ASCII
// list. Inverted, the same four scripts in the corpus behave identically and the
// next script nobody thought of lands on the STRICT side, which is what this
// file's refuse-beats-launder rule requires of an unknown case.
func isSyllabicOrLogographic(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r)
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
	if IsDirtyAuthorName(name1) || IsDirtyAuthorName(name2) {
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

// jaroWinklerBelowThreshold reports whether jaroWinklerSimilarity(s1, s2) is
// guaranteed to come out below threshold based on string LENGTH alone, so a
// caller screening pairs can skip the real comparison. It never returns true
// for a pair that would have met the threshold, so using it cannot change which
// pairs a scan accepts -- only how many it has to compute.
//
// Why this is sound, against the implementation directly above:
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
// Two details are load-bearing. Lengths are RUNE counts because the function
// above counts runes -- measuring bytes would over-skip on any non-ASCII name.
// And the ratio is biased down by an epsilon, so floating-point error in 5t-4
// can only ever cost a few wasted comparisons, never wrongly discard a pair
// sitting on the boundary. See jaroWinklerMinLengthRatio: at the 0.95 gate this
// package actually uses, 5t-4 IS exactly 0.75, so the epsilon is defensive
// there rather than load-bearing; it earns its place at other thresholds.
//
// For t <= 0.8 the bound is non-positive -- length proves nothing -- and this
// correctly declines to skip anything.
func jaroWinklerBelowThreshold(s1, s2 string, threshold float64) bool {
	return lengthRatioBelowThreshold(
		utf8.RuneCountInString(s1),
		utf8.RuneCountInString(s2),
		jaroWinklerMinLengthRatio(threshold),
	)
}

// jaroWinklerMinLengthRatio returns the smallest shorter/longer rune-length
// ratio a pair can have and still reach threshold, per the derivation on
// jaroWinklerBelowThreshold. A result <= 0 means no pair can be excluded on
// length alone (every ratio clears the bar), which is the case for thresholds
// at or below 0.8.
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
		if isMultiAuthorString(a.Name) || isCompositeAuthorName(a.Name) || IsDirtyAuthorName(a.Name) {
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
					// is provably conservative (see jaroWinklerBelowThreshold's
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
