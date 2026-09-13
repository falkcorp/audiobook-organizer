// file: internal/applygate/booknum.go
// version: 1.1.0
// guid: 9d4f2c83-6a1e-4b57-8c09-e3b7a5d16f42
// last-edited: 2026-09-13

package applygate

import (
	"regexp"
	"sort"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// ReasonSeriesNumberLost: the apply replaces a title that carries a volume
// number with one that drops it, and the result keeps that number nowhere,
// neither in the new title nor as the series position. On the 2026-09-13
// prod preview "The Legends of the First Empire - 3 - Age of War" ->
// "Age of War" passed with no series.
const ReasonSeriesNumberLost = "series_number_lost"

// ReasonSeriesRenamed: the number survives as the series position, but under
// a series whose name does not match the words the title gave it with:
// "Empire of Man 04 - We Few" -> "We Few", series "Prince Roger" #4.
const ReasonSeriesRenamed = "series_renamed"

// checkSeriesNumberLost runs only when the apply replaces the title (title in
// overwrites) and the stored title carries a volume number. Then:
//
//   - the candidate title, or title plus subtitle, carries the same number:
//     neutral, the number is kept;
//   - the result's series position equals the number ("04" == 4, "0.5" ==
//     .5) and the series name matches the words the title numbered (or the
//     title gave no words beyond the new title's own): neutral, kept as the
//     series position ("Into the Void: Sentenced to War, Book 14" ->
//     "Into the Void", series "Sentenced to War" #14);
//   - same position under a series of other words: block, series_renamed;
//   - otherwise: block, series_number_lost.
//
// A TRAILING number ("Title - 01", "Catch-22") is often a track number in a
// file-derived title or part of a name, not a volume. It counts only when a
// series position, the stored one or the result's, equals it; otherwise the
// check is neutral.
//
// The result series is the candidate's where it gives one, else the book's
// own (an apply does not clear a series the candidate leaves blank). It
// returns only block or neutral, never agree.
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
	kept := hasPos && seqnum.Equal(pos, n)
	if n.Form == "trailing" && !kept {
		if sp, ok := storedPosition(book); !ok || !seqnum.Equal(sp, n) {
			r.Detail = "trailing " + n.Text + " in " + quote(book.Title) + " has no series position behind it: read as a track or name number, not a volume"
			return r
		}
	}
	if kept && series != "" {
		ctx := numberContext(book.Title, n)
		for t := range tokens(candText, true) {
			delete(ctx, t)
		}
		if seriesCovers(ctx, series) {
			r.Detail = "title's #" + n.Text + " kept as " + quote(series) + " #" + pos.Text
			return r
		}
		r.Outcome, r.Reason = OutcomeBlock, ReasonSeriesRenamed
		r.Detail = "title " + quote(book.Title) + " numbers " + quote(strings.Join(sortedKeys(ctx), " ")) + " #" + n.Text +
			"; the result keeps #" + pos.Text + " but under series " + quote(series)
		return r
	}
	r.Outcome, r.Reason = OutcomeBlock, ReasonSeriesNumberLost
	r.Detail = "title " + quote(book.Title) + " carries #" + n.Text + "; the candidate title " + quote(c.Title) + " drops it"
	switch {
	case series != "" && hasPos:
		r.Detail += " and the result series is " + quote(series) + " #" + pos.Text
	case series != "":
		r.Detail += " and the result series " + quote(series) + " has no position"
	default:
		r.Detail += " and the result has no series"
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
	n, ok := storedPosition(book)
	return series, n, ok
}

// storedPosition is the book's own series position, if it has one.
func storedPosition(book *database.Book) (seqnum.Number, bool) {
	if book.SeriesPositionRaw != nil && strings.TrimSpace(*book.SeriesPositionRaw) != "" {
		return seqnum.ParsePosition(*book.SeriesPositionRaw)
	}
	if book.SeriesSequence != nil && *book.SeriesSequence > 0 {
		return seqnum.ParsePosition(itoa(*book.SeriesSequence))
	}
	return seqnum.Number{}, false
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

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
