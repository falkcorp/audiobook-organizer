// file: internal/matcher/prepared.go
// version: 1.0.0
// guid: 786dca59-41da-4f49-b735-a0d61851f1a6
// last-edited: 2026-10-30

package matcher

import (
	"cmp"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

// preparedWord is one whitespace-separated word of a Prepared string: its
// normalized text (for the word-start prefix test) and its lower-cased runes
// (for Levenshtein).
type preparedWord struct {
	text  string
	runes []rune
}

// Prepared is a string run through the per-string work of title scoring
// exactly once: normalization (lower-case, drop everything but letters, digits
// and spaces), lower-cased rune decoding, word splitting and the sorted unique
// token list the token-set signal needs. A caller that scores one string
// against many others (a bucket of candidate duplicates, say) prepares each
// string once up front and then calls SymmetricScore per pair, instead of
// paying for all of it again, on both sides, on every comparison.
//
// A Prepared is immutable after Prepare returns, so it may be shared by any
// number of goroutines scoring concurrently (each with its own Scratch).
type Prepared struct {
	norm   string         // normalize(s)
	runes  []rune         // runes of strings.ToLower(norm); what Levenshtein compares
	words  []preparedWord // strings.Fields(norm), in order
	tokens []preparedWord // words, sorted by text, duplicates removed
}

// Prepare computes the Prepared form of s.
func Prepare(s string) Prepared {
	norm := normalize(s)
	var p Prepared
	p.norm = norm
	if norm == "" {
		return p
	}
	p.runes = appendLowerRunes(make([]rune, 0, len(norm)), norm)

	// Fields(norm) and a split of p.runes on ' ' agree: normalize keeps no
	// whitespace but ' ', and lower-casing never turns a letter into a space.
	// Slicing p.runes avoids a second allocation per word.
	fields := strings.Fields(norm)
	p.words = make([]preparedWord, 0, len(fields))
	start := 0
	for _, f := range fields {
		for start < len(p.runes) && p.runes[start] == ' ' {
			start++
		}
		n := utf8.RuneCountInString(f)
		p.words = append(p.words, preparedWord{text: f, runes: p.runes[start : start+n : start+n]})
		start += n
	}

	p.tokens = slices.Clone(p.words)
	slices.SortFunc(p.tokens, func(a, b preparedWord) int { return cmp.Compare(a.text, b.text) })
	p.tokens = slices.CompactFunc(p.tokens, func(a, b preparedWord) bool { return a.text == b.text })
	return p
}

// Scratch is the reusable working memory SymmetricScore needs. It is not safe
// for concurrent use: give each goroutine its own (GetScratch / PutScratch, or
// the zero value).
type Scratch struct {
	lev    levScratch
	t1, t2 []rune
}

var scratchPool = sync.Pool{New: func() any { return new(Scratch) }}

// GetScratch borrows a Scratch from a pool; return it with PutScratch.
func GetScratch() *Scratch { return scratchPool.Get().(*Scratch) }

// PutScratch returns a Scratch borrowed with GetScratch.
func PutScratch(s *Scratch) { scratchPool.Put(s) }

// SymmetricScore returns a 0-100 similarity between the two strings a and b
// were prepared from. It is symmetric: SymmetricScore(a, b) == SymmetricScore(b, a).
//
// It is the larger of the two directional scores a "how well does query match
// target" search metric gives, taking the best of these terms (both
// directions where the term is directional):
//
//   - 100 for equal normalized strings; 0 if either normalizes to empty;
//   - 90 if one normalized string is a prefix of the other;
//   - 60 + 25*(shorter/longer rune length) if one contains the other;
//   - 80 if one is a prefix of a word of the other;
//   - 50 * the whole-string Levenshtein similarity;
//   - 70 * the best Levenshtein similarity of one string against a single
//     word of the other;
//   - 70 * the token-set ratio (fuzzywuzzy's token_set_ratio construction:
//     the best Levenshtein similarity among the sorted shared tokens and the
//     shared tokens plus each side's remainder), which is order-insensitive.
//
// The scores are produced by exactly the arithmetic the previous string-based
// ScoreMatch used, called in both directions and maxed: the property test in
// fuzzy_equivalence_test.go keeps a verbatim copy of that implementation as the
// oracle and compares on random strings. What this form saves:
//
//   - normalization, rune decoding, word splitting and token sorting happen in
//     Prepare, once per string, not four times per pair;
//   - the whole-string edit distance and the token-set ratio are symmetric, so
//     each is computed once rather than once per direction;
//   - a term whose ceiling cannot exceed the score already reached is skipped
//     (the max of a set is unchanged by terms below it): per-word Levenshtein
//     and the token-set signal both cap at 70, the whole-string term at 50.
func SymmetricScore(a, b *Prepared, s *Scratch) int {
	if a.norm == "" || b.norm == "" {
		return 0
	}
	if a.norm == b.norm {
		return 100
	}

	score := max(directedHeuristic(a, b), directedHeuristic(b, a))

	// Fuzzy distance on the whole string. Symmetric in its arguments.
	maxLen := max(len(a.runes), len(b.runes))
	if maxLen > 0 {
		dist := levenshteinRunes(a.runes, b.runes, &s.lev)
		similarity := 1.0 - float64(dist)/float64(maxLen)
		score = max(score, max(int(similarity*50), 0))
	}

	// Fuzzy distance on individual words, in both directions.
	if score < 70 {
		score = max(score, bestWordScore(a, b, s))
		score = max(score, bestWordScore(b, a, s))
	}

	// Order-insensitive token-set signal (symmetric).
	if score < tokenSetScale {
		if tokenScore := int(tokenSetRatioPrepared(a, b, s) * tokenSetScale); tokenScore > score {
			score = tokenScore
		}
	}
	return score
}

// directedHeuristic is the prefix / substring / word-start tier with q as the
// query and t as the target.
func directedHeuristic(q, t *Prepared) int {
	score := 0
	if strings.HasPrefix(t.norm, q.norm) {
		score = max(score, 90)
	}
	if strings.Contains(t.norm, q.norm) {
		ratio := float64(len(q.runes)) / float64(len(t.runes))
		score = max(score, 60+int(ratio*25))
	}
	for _, w := range t.words {
		if strings.HasPrefix(w.text, q.norm) {
			score = max(score, 80)
			break
		}
	}
	return score
}

// bestWordScore is the per-word fuzzy term with q as the query and t as the
// target: the best Levenshtein similarity of q against any word of t.
func bestWordScore(q, t *Prepared, s *Scratch) int {
	best := 0
	for _, w := range t.words {
		dist := levenshteinRunes(q.runes, w.runes, &s.lev)
		wLen := max(len(q.runes), len(w.runes))
		if wLen > 0 {
			similarity := 1.0 - float64(dist)/float64(wLen)
			best = max(best, max(int(similarity*70), 0))
		}
	}
	return best
}

// tokenSetRatioPrepared is the token-set ratio over two Prepared strings. Both
// token lists are already sorted and unique, so the intersection and the two
// remainders fall out of one merge each, already in sort.Strings order, and
// the joined strings are assembled directly as runes in the Scratch buffers.
func tokenSetRatioPrepared(a, b *Prepared, s *Scratch) float64 {
	if len(a.tokens) == 0 || len(b.tokens) == 0 {
		return 0
	}

	// t0 = shared tokens; t1 = t0 + a-only; t2 = t0 + b-only. t0 is a prefix
	// of both, so build t1 and copy the prefix into t2.
	buf1 := s.t1[:0]
	i, j := 0, 0
	for i < len(a.tokens) && j < len(b.tokens) {
		switch c := cmp.Compare(a.tokens[i].text, b.tokens[j].text); {
		case c == 0:
			buf1 = appendToken(buf1, a.tokens[i].runes)
			i++
			j++
		case c < 0:
			i++
		default:
			j++
		}
	}
	n0 := len(buf1)
	buf2 := append(s.t2[:0], buf1...)

	i, j = 0, 0
	for i < len(a.tokens) && j < len(b.tokens) {
		switch c := cmp.Compare(a.tokens[i].text, b.tokens[j].text); {
		case c == 0:
			i++
			j++
		case c < 0:
			buf1 = appendToken(buf1, a.tokens[i].runes)
			i++
		default:
			buf2 = appendToken(buf2, b.tokens[j].runes)
			j++
		}
	}
	for ; i < len(a.tokens); i++ {
		buf1 = appendToken(buf1, a.tokens[i].runes)
	}
	for ; j < len(b.tokens); j++ {
		buf2 = appendToken(buf2, b.tokens[j].runes)
	}
	s.t1, s.t2 = buf1, buf2 // keep any growth for the next call

	t0 := buf1[:n0]
	return max(
		tokenSimilarityRunes(t0, buf1, &s.lev),
		tokenSimilarityRunes(t0, buf2, &s.lev),
		tokenSimilarityRunes(buf1, buf2, &s.lev),
	)
}

// appendToken appends tok to buf, space-separated from what is already there.
func appendToken(buf, tok []rune) []rune {
	if len(buf) > 0 {
		buf = append(buf, ' ')
	}
	return append(buf, tok...)
}

// tokenSimilarityRunes returns a Levenshtein-based similarity in [0,1] for two
// already lower-cased token strings (1.0 == identical). Two empty strings are
// identical; anything against empty is 0.
//
// There is no clamp to 0: the only way the result goes negative is the
// numerator and denominator disagreeing on their unit, and flooring that to 0
// would turn a detectable defect into a plausible-looking score. Both are runes.
func tokenSimilarityRunes(a, b []rune, s *levScratch) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1.0
	}
	maxLen := max(len(a), len(b))
	if maxLen == 0 {
		return 0
	}
	return 1.0 - float64(levenshteinRunes(a, b, s))/float64(maxLen)
}
