// file: internal/seriesdenumber/oracle_test.go
// version: 1.0.0
// guid: 7d2e9b14-3a60-4c85-8f1d-b5e07a926c43
// last-edited: 2026-11-17

package seriesdenumber

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// ─── THE ORACLE ─────────────────────────────────────────────────────────────
//
// An independent, deliberately simple statement of the rule, written from the
// round-3 brief and the grammar's own documentation, not from the code under
// test. It calls NONE of ParseName, Demote, OwnNameKeys, AuthorKeys, TitleKey,
// CheckRun, Decide or the production word lists: a bug shared with them would be
// invisible to a test that borrowed them (the round-2 property test took its
// base and position from the same parser it was checking, so the two could not
// disagree).
//
// It answers one question for a library as a whole: which books move, to which
// position, and for a series that stays, the first reason it stays.

type oracleWords map[string]bool

func ow(list ...string) oracleWords {
	m := oracleWords{}
	for _, w := range list {
		m[w] = true
	}
	return m
}

var (
	oPartWords = ow("part", "parts", "pt", "unabridged", "abridged", "disc", "discs", "disk", "disks",
		"cd", "cds", "track", "tracks", "file", "mp3", "m4b", "audio", "side", "sides", "reel", "reels",
		"hour", "hours", "edition", "version", "episode", "episodes", "ep", "chapter", "chap", "ch", "section")
	oVolumeWords = ow("tome", "tomo", "tom", "band", "libro", "livre", "teil", "buch", "deel", "volumen",
		"kniha", "bok", "bog", "kirja", "volym", "numero", "number", "book", "bk", "vol", "vols",
		"volume", "volumes", "no", "nr", "bd", "v", "t")
	oConnectors = ow("of", "to", "and", "or", "in", "on", "at", "for", "from", "with", "by", "vs", "the", "a", "an")
	oStructural = ow("chap", "chapter", "chapters", "ch", "ep", "episode", "episodes", "part", "parts", "pt",
		"track", "tracks", "disc", "disk", "cd", "file", "side", "section", "volume", "vol", "book", "bk",
		"no", "number", "audio", "unabridged", "abridged")
	oNumberWords = ow("one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve")
	oMarkers     = ow("book", "bk", "vol", "volume", "no")
)

// oNameRe is "<base> <separator> <marker?> <number>": a space, or a space-padded
// "-", ":", ",", en dash or em dash, then an optional marker word with an optional
// ".", or "#", then one to three digits and the end.
var oNameRe = regexp.MustCompile(`(?i)^(.+?)(?:\s*[-:,\x{2013}\x{2014}]\s*|\s+)(?:(?:book|bk|vol|volume|no)\.?\s*|#\s*)?(\d{1,3})$`)

var oWordRe = regexp.MustCompile(`[\p{L}\p{N}']+`)

// oFold is the one normalization the oracle applies to text: composed Unicode,
// lower case, straight apostrophes. Accents are NOT stripped: "Renée" and
// "Renee" are different words.
func oFold(s string) string {
	s = norm.NFC.String(strings.ToLower(s))
	return strings.NewReplacer("’", "'", "‘", "'").Replace(s)
}

func oWords(s string) []string {
	var out []string
	for _, w := range oWordRe.FindAllString(oFold(s), -1) {
		if w = strings.Trim(w, "'"); w != "" {
			out = append(out, w)
		}
	}
	return out
}

// oLetters is the comparison key of a person's name or a base: letters and
// digits of the composed, lower-cased text.
func oLetters(s string) string {
	var b strings.Builder
	for _, r := range oFold(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func oLetterCount(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// oParse reads a series name. ok false carries the reason the name is not a
// numbered series this op may touch.
func oParse(name string) (base string, n int, reason string) {
	s := norm.NFC.String(strings.Join(strings.Fields(name), " "))
	whole := oWords(s)
	for _, w := range whole {
		if oPartWords[w] {
			return "", 0, "part_word"
		}
	}
	m := oNameRe.FindStringSubmatch(s)
	if m == nil {
		return "", 0, "shape"
	}
	n, _ = strconv.Atoi(m[2])
	if n < 1 || n > 99 {
		return "", 0, "range"
	}
	base = strings.TrimSpace(m[1])
	switch {
	case base == "":
		return "", 0, "shape"
	case strings.ContainsFunc(base, unicode.IsDigit):
		return "", 0, "digit_in_base"
	}
	if r := []rune(base); !unicode.IsLetter(r[len(r)-1]) {
		return "", 0, "base_not_clean"
	}
	bw := oWords(base)
	if len(bw) == 0 || oConnectors[bw[len(bw)-1]] {
		return "", 0, "connector"
	}
	for _, w := range bw {
		if oVolumeWords[w] {
			return "", 0, "volume_word"
		}
	}
	if oLetterCount(base) < 3 {
		return "", 0, "tiny"
	}
	allStructural := true
	for _, w := range bw {
		if !oStructural[w] {
			allStructural = false
		}
	}
	if allStructural {
		return "", 0, "structural"
	}
	return base, n, ""
}

var oNumRe = regexp.MustCompile(`^\d+$`)

// oTitleKey reduces a title to what names the book: the folded words with every
// number, number word, marker, volume word, part or format word and structural
// word removed, and trailing connectors trimmed.
func oTitleKey(title string) string {
	var kept []string
	for _, w := range oWords(title) {
		if oNumRe.MatchString(w) || oNumberWords[w] || oMarkers[w] || oVolumeWords[w] || oPartWords[w] || oStructural[w] {
			continue
		}
		kept = append(kept, w)
	}
	for len(kept) > 0 && oConnectors[kept[len(kept)-1]] {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, " ")
}

func oASIN(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

func oISBN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= '0' && r <= '9') || r == 'X' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ─── the generated library, as plain data ───────────────────────────────────

type pBook struct {
	ID     string
	Group  string // version group, "" when the book stands alone
	Title  string
	ASIN   string
	ISBN   string
	Seq    int // 0 = none
	Locked bool
}

func (b pBook) identity() string {
	if b.Group != "" {
		return "g:" + b.Group
	}
	return "b:" + b.ID
}

type pSeries struct {
	ID     int
	Name   string
	Author int
	Books  []pBook
	// ClaimBase and ClaimN are what the planner is claimed to have read: the
	// generator's own base and number, taken from how the name was BUILT, not
	// parsed back from it.
	ClaimBase string
	ClaimN    int
}

// pAuthor is a library author as the generator built it, so the oracle reads
// the parts of the name from the construction and never parses a string.
type pAuthor struct {
	ID int
	// Name is the credit as stored.
	Name string
	// PartKeys are the keys of every part of the name that could stand alone as a
	// series base: given name, surname, given and surname without any initial,
	// the full name; one set per credited person.
	PartKeys map[string]bool
	// FullKeys are the keys of each credited person's whole name in the spellings
	// a catalogue uses ("Given Surname" and "Surname Given").
	FullKeys map[string]bool
}

type pTarget struct {
	Positions  []int // positions held by the base series' members
	Unseen     int   // members the decision cannot see
	Elsewhere  bool  // a base of this name exists under another author row
	Exists     bool
	TargetID   int
	BaseSeries string
}

type pLib struct {
	Authors []pAuthor
	Series  []pSeries
	Targets map[string]*pTarget // key: author id + "|" + folded base
}

func targetKey(author int, base string) string {
	return strconv.Itoa(author) + "|" + oLetters(base)
}

type pMove struct {
	NewSeq int
}

// oracleDecide returns the books the rule lets move, with the position each
// takes, and the reason every non-moving series stays (for coverage counting).
func oracleDecide(lib pLib) (moves map[string]pMove, why map[int]string) {
	moves = map[string]pMove{}
	why = map[int]string{}
	author := map[int]pAuthor{}
	for _, a := range lib.Authors {
		author[a.ID] = a
	}

	read := map[int]oParsed{}
	for _, s := range lib.Series {
		base, n, reason := oParse(s.Name)
		if reason != "" {
			why[s.ID] = "grammar:" + reason
			continue
		}
		read[s.ID] = oParsed{base, n}
	}

	// A numbered series holding no book never takes part: it is not a sibling and
	// it moves nothing.
	siblingsOf := func(s pSeries, p oParsed) []pSeries {
		var out []pSeries
		for _, o := range lib.Series {
			po, ok := read[o.ID]
			if !ok || o.ID == s.ID || o.Author != s.Author || len(o.Books) == 0 {
				continue
			}
			if oLetters(po.base) == oLetters(p.base) {
				out = append(out, o)
			}
		}
		return out
	}

	for _, s := range lib.Series {
		p, ok := read[s.ID]
		if !ok {
			continue
		}
		a, hasAuthor := author[s.Author]
		switch {
		case !hasAuthor || s.Author == 0:
			why[s.ID] = "author:none"
			continue
		case oLetters(s.ClaimBase) != oLetters(p.base) || s.ClaimN != p.n:
			why[s.ID] = "planner_disagrees"
			continue
		}
		if k := oLetters(p.base); a.PartKeys[k] || a.FullKeys[k] {
			why[s.ID] = "author:own_name_part"
			continue
		}
		other := false
		for _, o := range lib.Authors {
			if o.ID != s.Author && o.FullKeys[oLetters(p.base)] {
				other = true
			}
		}
		if other {
			why[s.ID] = "author:other_full_name"
			continue
		}

		tgt := lib.Targets[targetKey(s.Author, p.base)]
		if tgt != nil && tgt.Exists && tgt.Unseen > 0 {
			why[s.ID] = "target_unseen"
			continue
		}
		if tgt != nil && !tgt.Exists && tgt.Elsewhere {
			why[s.ID] = "base_elsewhere"
			continue
		}
		if len(s.Books) == 0 {
			why[s.ID] = "empty"
			continue
		}

		identities := map[string]bool{}
		for _, b := range s.Books {
			identities[b.identity()] = true
		}
		if len(identities) > 1 {
			why[s.ID] = "multi_book_source"
			continue
		}
		locked, differs := false, false
		for _, b := range s.Books {
			if b.Locked {
				locked = true
			}
			if b.Seq != 0 && b.Seq != p.n {
				differs = true
			}
		}
		if locked {
			why[s.ID] = "locked"
			continue
		}
		if differs {
			why[s.ID] = "own_position_differs"
			continue
		}

		if reason := oRun(s, p, siblingsOf(s, p), read); reason != "" {
			why[s.ID] = reason
			continue
		}
		if tgt != nil {
			taken := false
			for _, pos := range tgt.Positions {
				if pos == p.n {
					taken = true
				}
			}
			if taken {
				why[s.ID] = "collision"
				continue
			}
		}
		why[s.ID] = "moves"
		for _, b := range s.Books {
			moves[b.ID] = pMove{NewSeq: p.n}
		}
	}
	return moves, why
}

// oParsed is the base and number the oracle read from a series name.
type oParsed struct {
	base string
	n    int
}

// oRun applies the run rule to series s: it returns "" when the maximal run of
// consecutive numbers around its number has at least three members and every
// member is one book with its own title, else the reason the run does not
// corroborate. sibs are the other same-author series with this base that hold a
// book; read gives every series' parsed number.
func oRun(s pSeries, p oParsed, sibs []pSeries, read map[int]oParsed) string {
	byN := map[int][]pSeries{p.n: {s}}
	for _, o := range sibs {
		n := read[o.ID].n
		byN[n] = append(byN[n], o)
	}
	lo, hi := p.n, p.n
	for len(byN[lo-1]) > 0 {
		lo--
	}
	for len(byN[hi+1]) > 0 {
		hi++
	}
	if hi-lo+1 < 3 {
		return "run_short"
	}
	baseKey := oTitleKey(p.base)
	seenTitle := map[string]bool{}
	seenID := map[string]bool{}
	for n := lo; n <= hi; n++ {
		if len(byN[n]) > 1 {
			return "run_rejected:two_series_one_number"
		}
		m := byN[n][0]
		identities := map[string]bool{}
		titles := map[string]bool{}
		ids := map[string]bool{}
		for _, b := range m.Books {
			identities[b.identity()] = true
			if k := oTitleKey(b.Title); k != "" {
				titles[k] = true
			}
			for _, id := range []string{oASIN(b.ASIN), oISBN(b.ISBN)} {
				if id != "" {
					ids[id] = true
				}
			}
		}
		if len(identities) != 1 {
			return "run_rejected:member_holds_several_books"
		}
		if len(titles) == 0 {
			return "run_rejected:member_has_no_title"
		}
		for t := range titles {
			if t == baseKey {
				return "run_rejected:title_is_base"
			}
			if seenTitle[t] {
				return "run_rejected:title_repeats"
			}
		}
		for t := range titles {
			seenTitle[t] = true
		}
		for id := range ids {
			if seenID[id] {
				return "run_rejected:shared_identifier"
			}
		}
		for id := range ids {
			seenID[id] = true
		}
	}
	return ""
}
