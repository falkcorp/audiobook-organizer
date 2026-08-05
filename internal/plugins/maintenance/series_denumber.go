// file: internal/plugins/maintenance/series_denumber.go
// version: 1.0.0
// guid: dee834d3-1f7e-453e-9303-85d37479e79d
// last-edited: 2026-08-04

package maintenance

import (
	"regexp"
	"strconv"
	"strings"
)

// A series name should name the SERIES. These carry the book's position instead,
// which splits one series into as many one-book series as it has volumes:
// production holds 37 separate "Discworld NN" series, plus Safehold 01/02,
// Mistborn 3/6, Frontiers Saga 07 and 2,251 others.
//
// maintenance.series-normalize cannot see this — metadata.StripSeriesContamination
// returns every one of these unchanged with pos="" — so the merge machinery
// downstream is never told they belong together.

// explicitPositionSuffix matches an UNAMBIGUOUS position marker: a keyword that
// only ever introduces a number. "Schooled in Magic: Book 11", "Reclaiming Honor
// bk 6", "The Long Winter Trilogy Book 3".
var explicitPositionSuffix = regexp.MustCompile(
	`(?i)[\s:,\-_]*\b(?:book|bk|vol|volume|part|pt|no|num|#)\s*[.:#]?\s*(\d{1,3})\s*$`)

// barePositionSuffix matches a trailing number with no keyword: "Discworld 05".
// Far riskier — "Fahrenheit 451" and "Blake's 7" are real names — so the caller
// decides whether to trust it (see SeriesDenumber).
var barePositionSuffix = regexp.MustCompile(`^(.*?)[\s\-_.]+(\d{1,3})$`)

// junkSeriesBases are base names that are not series at all. They come from disc
// and chapter tags being written into the series field, and production holds
// hundreds: "Chapter 12", "Disc 3". Merging these would create one giant bogus
// "Chapter" series, which is worse than leaving them split.
var junkSeriesBases = map[string]struct{}{
	"chapter": {}, "chapters": {}, "disc": {}, "disk": {}, "cd": {},
	"track": {}, "part": {}, "pt": {}, "book": {}, "vol": {}, "volume": {},
	"side": {}, "tape": {}, "file": {}, "section": {},
}

// trailingJunkWord catches a base that ENDS in a tag keyword rather than being
// one outright: "The Tower of Nero - Chapter", "The Darkling Child: ... , Chapter".
// Stripping "Chapter 12" leaves the keyword stranded on the end, and merging on
// that base manufactures a series named after the word "chapter".
var trailingJunkWord = regexp.MustCompile(
	`(?i)[\s:,\-–—_]+(chapter|chapters|disc|disk|cd|track|part|pt|book|vol|volume|side|tape|section)\s*$`)

// hasLetter reports whether s contains any letter at all. A base of "01" or "—"
// is not a name.
func hasLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127 {
			return true
		}
	}
	return false
}

// IsJunkSeriesBase reports whether a stripped base name is a tag artefact rather
// than a series.
//
// The dry run against production is what taught this its shape: an
// equality-only check let through 76 merges into
// "The Darkling Child: ... (Unabridged), Chapter", 39 into
// "The Tower of Nero - Chapter", and 11 each into bases of "01".."04".
func IsJunkSeriesBase(base string) bool {
	b := strings.TrimSpace(base)
	if b == "" {
		return true
	}
	if _, ok := junkSeriesBases[strings.ToLower(b)]; ok {
		return true
	}
	if trailingJunkWord.MatchString(b) {
		return true
	}
	// Purely numeric or punctuation-only: "01", "—", "3.".
	if !hasLetter(b) {
		return true
	}
	// A base left dangling on a separator ("Drew Hayes – Bones of the Past –")
	// means the split landed mid-name rather than at a real boundary.
	if strings.HasSuffix(b, "-") || strings.HasSuffix(b, "–") || strings.HasSuffix(b, "—") ||
		strings.HasSuffix(b, ":") || strings.HasSuffix(b, ",") {
		return true
	}
	return false
}

// SeriesSplit is the outcome of parsing one series name.
type SeriesSplit struct {
	Base     string // the series name with the position removed
	Position int    // the parsed position, 0 when none
	Explicit bool   // true when a keyword marked the position ("Book 3")
	Padded   bool   // true when the number was zero-padded ("05")
}

// SplitSeriesPosition separates a trailing book position from a series name.
//
// ok=false means the name carries no position and must be left exactly as it is.
//
// 🔑 Explicit and Padded are reported rather than acted on, because a bare
// trailing number is genuinely ambiguous: "Discworld 5" is a position and
// "Fahrenheit 451" is not. The caller decides, using evidence this function
// cannot see (see SeriesDenumber).
func SplitSeriesPosition(name string) (SeriesSplit, bool) {
	n := strings.TrimSpace(name)
	if n == "" {
		return SeriesSplit{}, false
	}

	if m := explicitPositionSuffix.FindStringSubmatch(n); m != nil {
		base := strings.TrimSpace(strings.TrimRight(n[:len(n)-len(m[0])], " :,-_"))
		pos, _ := strconv.Atoi(m[1])
		if base == "" || pos <= 0 {
			return SeriesSplit{}, false
		}
		return SeriesSplit{Base: base, Position: pos, Explicit: true,
			Padded: strings.HasPrefix(m[1], "0")}, true
	}

	if m := barePositionSuffix.FindStringSubmatch(n); m != nil {
		base := strings.TrimSpace(m[1])
		pos, _ := strconv.Atoi(m[2])
		if base == "" || pos <= 0 {
			return SeriesSplit{}, false
		}
		return SeriesSplit{Base: base, Position: pos, Explicit: false,
			Padded: strings.HasPrefix(m[2], "0")}, true
	}

	return SeriesSplit{}, false
}

// SeriesInput is one series row, reduced to what the planner needs.
type SeriesInput struct {
	ID       int
	Name     string
	AuthorID int // 0 when unset
	Books    int
}

// SeriesMergePlan is one series that should fold into another.
type SeriesMergePlan struct {
	FromID   int
	FromName string
	IntoName string // canonical base name
	IntoID   int    // existing series with the base name, 0 when one must be created
	Position int
	Reason   string
	Books    int
}

// SeriesDenumber plans the merges that collapse "<Series> <N>" rows into one
// series per base name, per author.
//
// A bare trailing number is only trusted when the library itself corroborates it:
//
//   - the number is zero-padded ("Discworld 05") — nobody titles a work "Blake's 07"; or
//   - another series shares the same base ("Mistborn 3" alongside "Mistborn 6").
//
// A lone, unpadded "Blake's 7" therefore stays untouched, which is the whole
// point: the cost of wrongly merging a real name is a corrupted series, while the
// cost of skipping one is that it stays as it is.
//
// Explicit markers ("Book 3") need no corroboration — the keyword is the evidence.
func SeriesDenumber(in []SeriesInput) []SeriesMergePlan {
	type key struct {
		base   string
		author int
	}

	// Pass 1 — parse every name and count how many series share each base.
	splits := make(map[int]SeriesSplit, len(in))
	baseCount := map[key]int{}
	for _, s := range in {
		sp, ok := SplitSeriesPosition(s.Name)
		if !ok || IsJunkSeriesBase(sp.Base) {
			continue
		}
		splits[s.ID] = sp
		baseCount[key{strings.ToLower(sp.Base), s.AuthorID}]++
	}

	// Pass 2 — an existing series already named exactly the base is the target.
	canonical := map[key]int{}
	for _, s := range in {
		if _, numbered := splits[s.ID]; numbered {
			continue
		}
		canonical[key{strings.ToLower(strings.TrimSpace(s.Name)), s.AuthorID}] = s.ID
	}

	var plans []SeriesMergePlan
	for _, s := range in {
		sp, ok := splits[s.ID]
		if !ok {
			continue
		}
		k := key{strings.ToLower(sp.Base), s.AuthorID}

		reason := ""
		switch {
		case sp.Explicit:
			reason = "explicit position keyword"
		case sp.Padded:
			reason = "zero-padded position"
		case baseCount[k] > 1:
			reason = "another series shares this base"
		default:
			continue // a lone unpadded number is not evidence — leave it alone
		}

		plans = append(plans, SeriesMergePlan{
			FromID: s.ID, FromName: s.Name, IntoName: sp.Base,
			IntoID: canonical[k], Position: sp.Position, Reason: reason, Books: s.Books,
		})
	}
	return plans
}
