// file: internal/applygate/booknum.go
// version: 1.0.0
// guid: 9d4f2c83-6a1e-4b57-8c09-e3b7a5d16f42
// last-edited: 2026-09-13

package applygate

import (
	"regexp"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// ReasonSeriesNumberLost: the apply replaces a title that carries a volume
// number with one that drops it. On the 2026-09-13 prod preview
// "Empire of Man 04 - We Few" -> "We Few", "Into the Void: Sentenced to War,
// Book 14" -> "Into the Void" and "00.5 Tin Man" -> "Tin Man" all passed.
const ReasonSeriesNumberLost = "series_number_lost"

// KeepNumberViaSeries, when true, lets a title lose its number if the result
// still files the book under a series whose name matches the words the number
// was given with, at the same position ("Dungeon Crawler Carl Book 4 - The
// Gate of the Feral Gods" -> "The Gate of the Feral Gods", series "Dungeon
// Crawler Carl" #4).
//
// It is OFF. Measured on the 2026-09-13 preview (2,637 passing rows, 35 the
// owner flagged): without the exception the check catches 8 of the 35 and
// newly blocks 298 others; with it, 1 of 35 and 2 others. The exception
// cannot tell the owner's examples from the rows it would spare:
// "Into the Void: Sentenced to War, Book 14" -> series "Sentenced to War" #14
// has exactly the shape of "Candy Bomb: Starship for Sale, Book 4" ->
// "Starship for Sale" #4. Every one of the 298 is a row whose stored title is
// REPLACED (the check runs only then), so they go to manual review rather
// than being written. Flip this to spare them; that is the owner's call.
const KeepNumberViaSeries = false

// checkSeriesNumberLost runs only when the apply replaces the title (title in
// overwrites). It blocks when the stored title carries a volume number and
// neither the candidate title nor title+subtitle carries the same number.
// It returns only block or neutral, never agree.
func checkSeriesNumberLost(book *database.Book, c *metafetch.MetadataCandidate, overwrites []string) CheckResult {
	r := CheckResult{Name: "series_number", Outcome: OutcomeNeutral}
	if !contains(overwrites, "title") {
		return r
	}
	n, ok := seqnum.ParseTitle(book.Title)
	if !ok {
		return r
	}
	candText := c.Title + " " + c.Subtitle
	for _, t := range []string{c.Title, candText} {
		if m, ok := seqnum.ParseTitle(t); ok && seqnum.Equal(m, n) {
			r.Detail = "candidate title keeps #" + n.Text
			return r
		}
	}
	series, pos, hasPos := resultSeries(book, c)
	if KeepNumberViaSeries && series != "" && hasPos && seqnum.Equal(pos, n) {
		ctx := numberContext(book.Title, n)
		for t := range tokens(candText, true) {
			delete(ctx, t)
		}
		if seriesCovers(ctx, series) {
			r.Detail = "title's #" + n.Text + " kept as " + series + " #" + pos.Text
			return r
		}
	}
	r.Outcome, r.Reason = OutcomeBlock, ReasonSeriesNumberLost
	r.Detail = "title " + quote(book.Title) + " carries #" + n.Text + "; candidate title " + quote(c.Title) + " drops it"
	if series != "" && hasPos {
		r.Detail += " (result series " + quote(series) + " #" + pos.Text + ")"
	}
	return r
}

// resultSeries is the series and position the book would have after the
// apply: the candidate's where it gives one, else the book's.
func resultSeries(book *database.Book, c *metafetch.MetadataCandidate) (string, seqnum.Number, bool) {
	series := strings.TrimSpace(c.Series)
	if series == "" {
		series = strings.TrimSpace(seriesName(book))
	}
	if strings.TrimSpace(c.SeriesPosition) != "" {
		n, ok := seqnum.ParsePosition(c.SeriesPosition)
		return series, n, ok
	}
	if book.SeriesPositionRaw != nil && strings.TrimSpace(*book.SeriesPositionRaw) != "" {
		n, ok := seqnum.ParsePosition(*book.SeriesPositionRaw)
		return series, n, ok
	}
	if book.SeriesSequence != nil && *book.SeriesSequence > 0 {
		n, ok := seqnum.ParsePosition(itoa(*book.SeriesSequence))
		return series, n, ok
	}
	return series, seqnum.Number{}, false
}

// numTok is a token that is a number or a number marker, not a name word.
var numTok = regexp.MustCompile(`^(?:\d+(?:\.\d+)?|[ivxlc]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|bk|no)$`)

// numberContext is the name words of the title segment that carries n (the
// series the number was given in), or of the segment before it when the
// number stands alone ("Legends of the First Empire - 3 - Age of War").
func numberContext(title string, n seqnum.Number) map[string]bool {
	var segs []string
	for _, s := range segSplitRe.Split(title, -1) {
		if strings.TrimSpace(s) != "" {
			segs = append(segs, s)
		}
	}
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for t := range tokens(s, true) {
			if !numTok.MatchString(t) {
				out[t] = true
			}
		}
		return out
	}
	for i, s := range segs {
		m, ok := seqnum.ParseTitle(s)
		if !ok || !seqnum.Equal(m, n) {
			if m, ok = seqnum.ParsePosition(s); !ok || !seqnum.Equal(m, n) {
				continue
			}
		}
		w := words(s)
		if len(w) == 0 && i > 0 {
			w = words(segs[i-1])
		}
		return w
	}
	return map[string]bool{}
}

// seriesCovers reports whether ctx is empty or shares at least half of the
// smaller of ctx and the series' words (a trailing plural "s" ignored).
func seriesCovers(ctx map[string]bool, series string) bool {
	if len(ctx) == 0 {
		return true
	}
	st := map[string]bool{}
	for t := range tokens(series, true) {
		st[strings.TrimSuffix(t, "s")] = true
	}
	cx := map[string]bool{}
	for t := range ctx {
		cx[strings.TrimSuffix(t, "s")] = true
	}
	inter := 0
	for t := range cx {
		if st[t] {
			inter++
		}
	}
	return inter > 0 && inter*2 >= min(len(cx), len(st))
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func quote(s string) string { return "\"" + s + "\"" }
