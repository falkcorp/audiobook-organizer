// file: internal/personname/composite.go
// version: 1.0.0
// guid: 51de0a2a-f4d4-4af3-ab21-5d89e6b4db19
// last-edited: 2026-10-04

package personname

import (
	"regexp"
	"strings"
)

// SplitCompositeAuthorName moved here from internal/dedup on 2026-10-04 so the
// author-creation paths (scanner, metadata, metafetch, importer) can share one
// splitter without importing internal/dedup and its dependency tree. Behaviour
// is unchanged; dedup.SplitCompositeAuthorName is a wrapper.

var (
	authorAkaRe          = regexp.MustCompile(`(?i)\(aka\s`)
	authorBracketSplitRe = regexp.MustCompile(`^(.+?)\s*[\(\[]\s*(.+?)\s*[\)\]]\s*$`)
)

// SplitCompositeAuthorName splits "Author1 / Author2" or "Author1, Author2" into parts.
// Returns nil or single-element slice if the name doesn't look composite.
//
// Every part it returns passes IsPlausibleAuthorName, the shared
// author creation gate. The callers create an author row per part and then
// delete the composite, so a part that fails the gate would be a junk row the
// split itself minted; when any part fails, the whole split is refused and the
// composite stays visibly wrong for repair (the C414 rule below).
func SplitCompositeAuthorName(name string) []string {
	parts := splitCompositeAuthorName(name)
	if len(parts) < 2 {
		return parts
	}
	for _, p := range parts {
		if ok, _ := IsPlausibleAuthorName(p); !ok {
			return nil
		}
	}
	return parts
}

func splitCompositeAuthorName(name string) []string {
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
			if n := NormalizeAuthorName(strings.TrimSpace(p)); LooksLikePersonName(n) {
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
			if !LooksLikePersonName(normalized) {
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
	// gate on the SAME LooksLikePersonName, so the comment above is a
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
		if no, ni := NormalizeAuthorName(outer), NormalizeAuthorName(inner); LooksLikePersonName(no) && LooksLikePersonName(ni) {
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
			if n := NormalizeAuthorName(p); LooksLikePersonName(n) {
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
					if norm := NormalizeAuthorName(p); len(p) > 2 && LooksLikePersonName(norm) {
						result = append(result, norm)
					} else if len(p) > 2 {
						return nil // one non-name clause poisons the whole split
					}
					break
				}
				p := strings.TrimSpace(remaining[:idx])
				if norm := NormalizeAuthorName(p); len(p) > 2 && LooksLikePersonName(norm) {
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

	// There is deliberately NO space-concatenation branch. Until 2026-09-25 a
	// word-run heuristic (trySplitConcatenatedAuthors) split any 4+ word string
	// whose halves were each person-SHAPED: "R.A. Mejia Charles Dean" ->
	// ["R. A. Mejia" "Charles Dean"]. Person shape cannot tell a name from a
	// title-case phrase, and run over all 15,055 production author names it
	// split 349 strings, of which about 8 were real two-person credits. The rest
	// were titles and fragments ("Wraith Knight Three Worlds" -> ["Wraith
	// Knight" "Three Worlds"], "A Memory Called Empire" -> ["A Memory" "Called
	// Empire"], "Ch02 The Kasari Nexus"), and it split one real person:
	// "Joseph Sheridan Le Fanu" -> ["Joseph Sheridan" "Le Fanu"]. The split ops
	// create a row per part and delete the composite, so each wrong split minted
	// junk authors. No gate on the parts can fix this -- "Wraith Knight" is
	// shaped exactly like "Charles Dean" -- so the heuristic was removed rather
	// than tuned. A space-joined credit list now stays one row, visibly wrong,
	// for the manual split endpoint.

	return nil
}
