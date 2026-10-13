// file: internal/catalog/match_title.go
// version: 1.1.0
// guid: 91c4e7a3-6b08-4d52-b3f9-2a7d5e1c8046
// last-edited: 2026-10-12

package catalog

import (
	"cmp"
	"slices"
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// titleKey is a title in the form B2 compares: edition markers stripped and
// folded (NormalizeTitle), its character trigrams, and its digit runs.
type titleKey struct {
	folded string
	runes  []rune
	grams  map[string]struct{}
	digits string
}

func newTitleKey(title string) (titleKey, bool) {
	f := NormalizeTitle(title)
	if f == "" {
		return titleKey{}, false
	}
	k := titleKey{folded: f, runes: []rune(f), grams: map[string]struct{}{}}
	for i := 0; i+3 <= len(k.runes); i++ {
		k.grams[string(k.runes[i:i+3])] = struct{}{}
	}
	if len(k.runes) < 3 {
		k.grams[f] = struct{}{}
	}
	// The digit runs, each closed by a separator, so "x 12" and "x 12 y" have
	// the same signature for the same run and "1 2" differs from "12".
	var digits []rune
	inRun := false
	for _, r := range k.runes {
		if unicode.IsDigit(r) {
			digits = append(digits, r)
			inRun = true
		} else if inRun {
			digits = append(digits, ' ')
			inRun = false
		}
	}
	if inRun {
		digits = append(digits, ' ')
	}
	k.digits = string(digits)
	return k, true
}

// newTitleKeys keys every distinct, non-empty title the book is known by.
func newTitleKeys(titles []string) []titleKey {
	var out []titleKey
	seen := map[string]bool{}
	for _, t := range titles {
		k, ok := newTitleKey(t)
		if !ok || seen[k.folded] {
			continue
		}
		seen[k.folded] = true
		out = append(out, k)
	}
	return out
}

func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	small, big := a, b
	if len(small) > len(big) {
		small, big = big, small
	}
	inter := 0
	for g := range small {
		if _, ok := big[g]; ok {
			inter++
		}
	}
	return float64(inter) / float64(len(a)+len(b)-inter)
}

// editCap is the largest edit distance a title of n folded runes may be from
// its match: 3 under 30 runes, 6 from there up, tightened to one edit per 6
// runes so a short title must match (nearly) exactly.
func editCap(n int) int {
	base := maxEditShort
	if n >= longTitleRunes {
		base = maxEditLong
	}
	return min(base, n/runesPerEdit)
}

// boundedEditDistance is the Levenshtein distance of a and b, or -1 when it
// exceeds limit. It stops as soon as every cell of a row exceeds limit.
func boundedEditDistance(a, b []rune, limit int) int {
	if d := len(a) - len(b); d > limit || -d > limit {
		return -1
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		rowMin := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			rowMin = min(rowMin, cur[j])
		}
		if rowMin > limit {
			return -1
		}
		prev, cur = cur, prev
	}
	if prev[len(b)] > limit {
		return -1
	}
	return prev[len(b)]
}

// scoredEntry is an entry that matched one of the book's titles.
type scoredEntry struct {
	e           *database.CatalogEntry
	score       float64
	seriesMatch bool
	pos         int
}

// entryTitleKeys are the keys an entry can be matched by: its title, and its
// title with its subtitle (a library title is often "Title: Subtitle").
func entryTitleKeys(e *database.CatalogEntry) []titleKey {
	var out []titleKey
	if k, ok := newTitleKey(e.Title); ok {
		out = append(out, k)
	}
	if e.Subtitle != "" {
		if k, ok := newTitleKey(e.Title + " " + e.Subtitle); ok {
			out = append(out, k)
		}
	}
	return out
}

// scoreBlock scores a block's entries against every title of the question: a
// folded exact match scores 1; otherwise the trigram Jaccard (floor
// minTitleJaccard, digit runs equal, so "Title 12" never meets "Title 13")
// ranks the entries, and the top few (topForEditDistance) are accepted only
// within the capped edit distance. An entry that is the same series at a
// different position than the question's is dropped.
func scoreBlock(live []*database.CatalogEntry, titles []titleKey, q BookQuestion) []scoredEntry {
	type cand struct {
		e   *database.CatalogEntry
		jac float64
		qk  titleKey
		ek  titleKey
	}
	var exact []scoredEntry
	var ranked []cand
	qSeries := SeriesKey(q.Series)
	qLo, qHi, qOK := ParseSequence(q.Position)
	for _, e := range live {
		if qSeries != "" && qOK && wrongPosition(e, qSeries, qLo, qHi) {
			continue
		}
		eks := entryTitleKeys(e)
		best := cand{e: e}
		isExact := false
		for _, qk := range titles {
			for _, ek := range eks {
				if qk.folded == ek.folded {
					isExact = true
					break
				}
				if qk.digits != ek.digits {
					continue
				}
				if j := jaccard(qk.grams, ek.grams); j > best.jac {
					best.jac, best.qk, best.ek = j, qk, ek
				}
			}
			if isExact {
				break
			}
		}
		switch {
		case isExact:
			exact = append(exact, scoredEntry{e: e, score: 1, seriesMatch: seriesMatches(e, qSeries)})
		case best.jac >= minTitleJaccard:
			ranked = append(ranked, best)
		}
	}
	slices.SortStableFunc(ranked, func(a, b cand) int {
		return cmp.Or(cmp.Compare(b.jac, a.jac), cmp.Compare(a.e.ProviderID, b.e.ProviderID))
	})
	if len(ranked) > topForEditDistance {
		ranked = ranked[:topForEditDistance]
	}
	out := exact
	for _, c := range ranked {
		limit := editCap(len(c.qk.runes))
		d := boundedEditDistance(c.qk.runes, c.ek.runes, limit)
		if d < 0 {
			continue
		}
		longest := max(len(c.qk.runes), len(c.ek.runes))
		out = append(out, scoredEntry{e: c.e, score: 1 - float64(d)/float64(longest), seriesMatch: seriesMatches(c.e, qSeries)})
	}
	return out
}

func seriesMatches(e *database.CatalogEntry, qSeries string) bool {
	if qSeries == "" {
		return false
	}
	for _, s := range e.Series {
		if SeriesKey(s.Name) == qSeries {
			return true
		}
	}
	return false
}

// wrongPosition reports whether e belongs to the question's series at a
// position that does not overlap the question's.
func wrongPosition(e *database.CatalogEntry, qSeries string, qLo, qHi float64) bool {
	for _, s := range e.Series {
		if SeriesKey(s.Name) != qSeries || s.SeqLo == nil || s.SeqHi == nil {
			continue
		}
		if *s.SeqHi < qLo || qHi < *s.SeqLo {
			return true
		}
	}
	return false
}
