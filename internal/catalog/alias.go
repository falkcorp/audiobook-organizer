// file: internal/catalog/alias.go
// version: 1.0.0
// guid: 0f6b2d84-9a3c-4e17-8c5d-b1e7a2f40d69
// last-edited: 2026-10-12

package catalog

import (
	"strings"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// minAliasFold is the shortest alias fold worth filing: "jlee" would name
// half the catalog's Lees.
const minAliasFold = 5

// nameSuffixes are generational and academic suffixes a library credit often
// drops ("Jane Doe Jr." / "Jane Doe").
var nameSuffixes = map[string]bool{"jr": true, "sr": true, "ii": true, "iii": true, "iv": true, "phd": true, "md": true}

// nameTokens splits a provider author name into its words: "Last, First" (one
// comma) reads as "First Last", dots separate initials ("J.N." -> J N), and
// everything but letters and digits is a separator.
func nameTokens(name string) []string {
	if last, first, ok := strings.Cut(name, ","); ok && !strings.Contains(first, ",") {
		name = strings.TrimSpace(first) + " " + strings.TrimSpace(last)
	}
	return strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' && r != '’' })
}

// AuthorAliasFolds returns the authorjunk.FoldKey folds of the spelling
// variants a library credit may use for the author a provider spells name:
// without middle names, "Last First" reordered (the "Last, First" tag shape,
// whose comma folds away), initialled, and without a generational suffix. The
// name's own fold is not among them (its block is found by the exact key). A
// name of fewer than two words has no variants: one word has nothing to drop,
// reorder or abbreviate.
//
// The variants are guesses at how another source writes the same person, so
// they are filed as aliases, not blocks: PutAuthorAliases demotes an alias two
// different authors claim to ambiguous, and MatchBook only follows an alias
// when the exact key holds nothing.
func AuthorAliasFolds(name string) []string {
	toks := nameTokens(name)
	for len(toks) > 2 && nameSuffixes[strings.ToLower(toks[len(toks)-1])] {
		toks = toks[:len(toks)-1]
	}
	own := authorjunk.FoldKey(name)
	if len(toks) < 2 {
		return nil
	}
	first, last := toks[0], toks[len(toks)-1]
	mids := toks[1 : len(toks)-1]
	initial := func(w string) string {
		for _, r := range w {
			return string(r)
		}
		return ""
	}
	initials := func(ws []string) string {
		var b strings.Builder
		for _, w := range ws {
			b.WriteString(initial(w))
		}
		return b.String()
	}
	join := func(parts ...string) string { return authorjunk.FoldKey(strings.Join(parts, " ")) }

	var out []string
	seen := map[string]bool{own: true}
	add := func(fold string) {
		if len(fold) < minAliasFold || seen[fold] {
			return
		}
		seen[fold] = true
		out = append(out, fold)
	}
	add(join(first, last))                                              // without middle names (and without a suffix)
	add(join(append(append([]string{first}, mids...), last)...))        // with a dropped suffix
	add(join(append([]string{last, first}, mids...)...))                // "Last First M"
	add(join(last, first))                                              // "Last First"
	add(join(initials(append([]string{first}, mids...)), last))         // "J P Last"
	add(join(initial(first), last))                                     // "J Last"
	add(join(append(append([]string{first}, initials(mids)), last)...)) // "First P Last"
	return out
}

// entryAliasPairs are the alias rows a stored entry earns: for each credited
// author, every spelling variant of the credit mapped to the credit's own
// block. Pairs are de-duplicated (an author credited on many entries of one
// call is read once).
func entryAliasPairs(entries ...database.CatalogEntry) []database.CatalogAuthorAlias {
	var names []string
	for i := range entries {
		for _, a := range entries[i].Authors {
			names = append(names, a.Name)
		}
	}
	return aliasPairsForNames(names)
}

// aliasPairsForNames maps every spelling variant of each name to the name's
// own block, de-duplicated and in first-seen order.
func aliasPairsForNames(names []string) []database.CatalogAuthorAlias {
	var out []database.CatalogAuthorAlias
	seen := map[database.CatalogAuthorAlias]bool{}
	for _, name := range names {
		target := authorjunk.FoldKey(name)
		if target == "" {
			continue
		}
		for _, v := range AuthorAliasFolds(name) {
			p := database.CatalogAuthorAlias{Alias: v, Target: target}
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}
