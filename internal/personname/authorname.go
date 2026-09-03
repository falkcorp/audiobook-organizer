// file: internal/personname/authorname.go
// version: 1.0.0
// guid: 0576b543-80ab-44eb-a083-015d3bff37fc
// last-edited: 2026-09-03

package personname

import (
	"regexp"
	"strings"
)

// Author-name normalization and the "this is not a person" predicates.
//
// These lived in internal/dedup until 2026-09-03. They moved here because
// internal/merge needs the creation gate and CANNOT import internal/dedup:
// six dedup files import merge, so the predicates sat on the wrong side of an
// import cycle. This package imports nothing internal, which is what makes it
// reachable from every layer -- the same reason the three diverged
// person-name detectors were folded in here on 2026-09-01.
//
// internal/dedup re-exports every exported name below, so its existing call
// sites are unchanged.

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

// Compiled once, not per call: NormalizeAuthorName runs twice for every
// candidate pair inside the pairwise metadata-fuzzy scan, so a
// regexp.MustCompile in its body would be two compilations per comparison
// over a whole-library candidate set.
var (
	// \s in Go's regexp is ASCII-only ([\t\n\f\r ]), so it does NOT match U+00A0
	// or any other Unicode space separator. \p{Zs} adds them. Without it,
	// "John\u00a0Smith" survives normalization intact and is stored as an author
	// that can never compare equal to "John Smith" in any index -- and it now
	// REACHES that point, because LooksLikePersonName uses strings.Fields, which
	// does split on U+00A0, where main's `strings.Contains(outer, " ")` did not.
	authorSpaceRe    = regexp.MustCompile(`[\s\p{Zs}]+`)
	authorInitialsRe = regexp.MustCompile(`([A-Z]\.)([A-Z])`)
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
