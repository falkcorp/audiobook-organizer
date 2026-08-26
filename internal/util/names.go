// file: internal/util/names.go
// version: 1.1.0
// guid: 3a91c4e7-52bd-4f16-8c03-9e7d1b5a2f48
// last-edited: 2026-08-17

package util

import "strings"

// creditSeparators are the delimiters that split a credit string into
// individual people, longest-first so " and " is consumed before the bare "&"
// inside it.
//
// This list is deliberately EXPLICIT rather than a general tokenizer. A comma is
// included because comma-separated credits ("Kate Reading, Michael Kramer") are
// the common case in real tags — but that is also what makes a naive split
// dangerous, since "Smith, John" is one person written surname-first.
// SplitCreditNames guards that case; do not widen this list without extending
// the guard.
var creditSeparators = []string{" & ", " and ", "; ", ";", ", ", " with ", " + ", "/", "&"}

// creditPieceCutset is the punctuation stripped from the ENDS of a split piece.
//
// 🔴 SEPARATOR CHARACTERS ONLY — deliberately NOT a general punctuation strip.
// A period and a hyphen are part of real names ("Sammy Davis Jr.",
// "Alex Hill-Knight", "E. E. Knight"); trimming those would mint a new phantom
// duplicate next to the correctly-spelled entry, which is exactly the defect this
// trimming exists to remove. Every character here already appears in
// creditSeparators, so removing it can only ever undo a split artifact.
const creditPieceCutset = " \t,;&+/"

// trimCreditPiece removes leading and trailing separator punctuation left behind
// when one separator's split strands another separator's character.
//
// 🔴 WHY THIS IS NEEDED. Separators are applied in sequence, so an earlier one can
// cut a string at a point that leaves a later one's character at an end, where it
// no longer matches. The live case is the Oxford comma: " & " splits
// "…, Alan Barnes, & Jonathan Morris" into "…, Alan Barnes," first, and the later
// ", " pass cannot reach that final comma because nothing follows it.
//
// Measured against the live narrator list 2026-08-17 (3,289 entries): this cutset
// changes 11 names and loses none. 8 of the 11 merge into an entry for the SAME
// person that already existed alongside them — "Alan Barnes" had 14 books while
// "Alan Barnes," had 1 — and the other 3 simply become clean.
func trimCreditPiece(s string) string {
	return strings.Trim(s, creditPieceCutset)
}

// looksLikeSurnameFirst reports whether a two-part comma split looks like one
// person written "Surname, Given" rather than two people.
//
// The signal is that the RIGHT side is a lone word: "Smith, John" and
// "Le Guin, Ursula" both are, while a real list has a surname on the right
// ("Kate Reading, Michael Kramer"). Testing the left side too was the first
// attempt and it broke on multi-word surnames like "Le Guin".
//
// This deliberately errs toward NOT splitting. A genuine list ending in a
// mononym ("Kate Reading, Cher") is left compound — rare, and merging two names
// is far easier to spot and undo than shredding one person into two phantom
// narrators that then pollute every narrator list and filter.
func looksLikeSurnameFirst(left, right string) bool {
	return left != "" && len(strings.Fields(right)) == 1
}

// SplitCreditNames splits an author/narrator credit string into individual
// names, de-duplicated, order preserved.
//
// This is the SINGLE source of truth for name splitting. It previously existed
// as two independent copies — internal/audiobooks/service_filtering.go and
// internal/server/handlers/operations/handler.go — each of which was
// strings.Split(name, " & ") and nothing else. Every comma-separated,
// "and"-joined or semicolon-joined credit therefore stayed one "person", which
// is how the narrator list fills with entries that are not people and why
// multi-narrator books drop out of narrator filters entirely.
//
// Returns the input as a single element when nothing splits, so callers can
// treat the result uniformly.
func SplitCreditNames(name string) []string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return []string{name}
	}

	// "Smith, John" is one person, not two. Guard only the simple two-part
	// case: more parts, or multi-word sides, is a real list.
	if parts := strings.Split(trimmed, ","); len(parts) == 2 {
		l, r := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if l != "" && r != "" && looksLikeSurnameFirst(l, r) {
			return []string{trimmed}
		}
	}

	work := []string{trimmed}
	for _, sep := range creditSeparators {
		var next []string
		for _, chunk := range work {
			for piece := range strings.SplitSeq(chunk, sep) {
				// Trimmed rather than merely TrimSpace'd so a stranded separator
				// character cannot become part of a person's name, and so the
				// emptiness check below sees a piece that is only punctuation
				// (an "A & & B" credit) for what it is.
				if p := trimCreditPiece(piece); p != "" {
					next = append(next, p)
				}
			}
		}
		work = next
	}

	// Drop duplicates, preserving order: "A & A" and repeated tag credits must
	// not create two identical narrator rows.
	seen := make(map[string]struct{}, len(work))
	var result []string
	for _, p := range work {
		key := strings.ToLower(p)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, p)
	}

	if len(result) == 0 {
		return []string{name}
	}
	return result
}

// IsCompoundCreditName reports whether a credit string names more than one
// person. Use this instead of strings.Contains(name, " & ") when gating a split:
// the old gate meant the splitter never even ran for comma-separated credits.
func IsCompoundCreditName(name string) bool {
	return len(SplitCreditNames(name)) > 1
}
