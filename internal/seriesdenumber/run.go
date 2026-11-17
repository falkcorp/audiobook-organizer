// file: internal/seriesdenumber/run.go
// version: 1.0.0
// guid: 4a71c9e2-0d35-4b87-9c16-e2f8a05d3b71
// last-edited: 2026-11-17

package seriesdenumber

import (
	"fmt"
	"sort"
	"strings"
)

// ─── THE RUN ────────────────────────────────────────────────────────────────
//
// A position a book does not carry itself has one corroboration: the library
// holds a RUN of series with the same author and the same base, numbered
// consecutively, each a different book ("Court of Owls 2", "Court of Owls 3",
// "Court of Owls 4"). Three kinds of wrong input make a run by construction, and
// each one is refused here:
//
//   - the parts of ONE shattered book ("Hush Part 1/2/3", "Hush 01/02/03"): same
//     author, same base, consecutive, one file each. They differ from a real run
//     only in what the books are: one title, a title that is the base, or one
//     ASIN or ISBN under several rows.
//   - a series that holds a whole comic run inside one numbered row ("Nightwing
//     Vol. 3" with four issues): the row is a series, not one book.
//   - a title that says the book is part N of itself.
//
// So a run counts only when EVERY member of it is one book with its own title.
// The reading is strict on purpose: one failing member holds the whole run, not
// just itself, because a run with one shattered book in it is evidence about the
// run, and the members that look fine are the ones that make it look real.
//
// The pure package decides; the caller supplies each member's books, read from
// the store in one bulk read.

// RunMin is the fewest series of a run of consecutive numbers, the series being
// decided included, that corroborates a position by its siblings.
const RunMin = 3

// Facts is one book of a run member, one row per version of it: what the run
// check compares.
type Facts struct {
	ID string
	// Identity is the version group id, "" when the book stands for itself.
	// Versions of one book share it and count as one book.
	Identity string
	Title    string
	ASIN     string
	ISBN10   string
	ISBN13   string
}

func (f Facts) identity() string {
	if f.Identity != "" {
		return "g:" + f.Identity
	}
	return "b:" + f.ID
}

// RunSeries is a numbered series as the run check reads it: the number its name
// carries and the books it holds (every version).
type RunSeries struct {
	ID    int
	N     int
	Books []Facts
}

// RunState is what the run says about a series' position.
type RunState int

const (
	// RunNone: no run of RunMin consecutive numbers contains the series. Nothing
	// but its name says the position.
	RunNone RunState = iota
	// RunConfirmed: a run contains the series and every member passed.
	RunConfirmed
	// RunRejected: a run contains the series and a member failed. The run is
	// evidence of a shattered book or a mixed series, not of positions.
	RunRejected
)

// RunVerdict is the result of CheckRun. Reason is set for RunRejected.
type RunVerdict struct {
	State  RunState
	Reason string
}

// CheckRun reads the run that contains self among its siblings. base is the
// series' base name; self and the siblings are series of ONE author with that
// base (the caller filters), numbered by what their names carry.
func CheckRun(base string, self RunSeries, siblings []RunSeries) RunVerdict {
	byN := map[int][]RunSeries{self.N: {self}}
	for _, s := range siblings {
		if s.ID == self.ID {
			continue
		}
		byN[s.N] = append(byN[s.N], s)
	}
	lo, hi := self.N, self.N
	for byN[lo-1] != nil {
		lo--
	}
	for byN[hi+1] != nil {
		hi++
	}
	if hi-lo+1 < RunMin {
		return RunVerdict{State: RunNone}
	}

	var members []RunSeries
	for n := lo; n <= hi; n++ {
		if len(byN[n]) > 1 {
			return reject("%d series carry the number %d", len(byN[n]), n)
		}
		members = append(members, byN[n]...)
	}
	return checkMembers(base, members)
}

func reject(format string, args ...any) RunVerdict {
	return RunVerdict{State: RunRejected, Reason: fmt.Sprintf(format, args...)}
}

func checkMembers(base string, members []RunSeries) RunVerdict {
	baseKey := TitleKey(base)
	seenTitle := map[string]int{} // normalized title -> series id
	seenID := map[string]int{}    // ASIN or ISBN -> series id

	for _, m := range members {
		if len(m.Books) == 0 {
			return reject("series %d (number %d) holds no book", m.ID, m.N)
		}
		identities := map[string]bool{}
		titles := map[string]bool{}
		ids := map[string]bool{}
		for _, b := range m.Books {
			identities[b.identity()] = true
			if k := TitleKey(b.Title); k != "" {
				titles[k] = true
			}
			for _, id := range []string{normalizeASIN(b.ASIN), normalizeISBN(b.ISBN10), normalizeISBN(b.ISBN13)} {
				if id != "" {
					ids[id] = true
				}
			}
		}
		if len(identities) != 1 {
			return reject("series %d (number %d) holds %d distinct books, so it is a series of its own, not one book of this one", m.ID, m.N, len(identities))
		}
		if len(titles) == 0 {
			return reject("the book of series %d (number %d) has no title once its numbers and part words are taken off", m.ID, m.N)
		}
		for _, t := range sortedKeys(titles) {
			if t == baseKey {
				return reject("the title of the book of series %d (number %d) is the base name itself, so it numbers the parts of one book", m.ID, m.N)
			}
			if other, dup := seenTitle[t]; dup {
				return reject("series %d (number %d) and series %d hold books with the same title, so they are parts of one book", other, m.N, m.ID)
			}
		}
		for _, t := range sortedKeys(titles) {
			seenTitle[t] = m.ID
		}
		for _, id := range sortedKeys(ids) {
			if other, dup := seenID[id]; dup {
				return reject("series %d and series %d share the identifier %s, so they are one book", other, m.ID, id)
			}
		}
		for _, id := range sortedKeys(ids) {
			seenID[id] = m.ID
		}
	}
	return RunVerdict{State: RunConfirmed}
}

// numberWords are spelled-out numbers a title may carry where a digit would be.
var numberWords = map[string]bool{
	"one": true, "two": true, "three": true, "four": true, "five": true, "six": true,
	"seven": true, "eight": true, "nine": true, "ten": true, "eleven": true, "twelve": true,
}

// TitleKey is a title reduced to what names the book: lower-case words with every
// number, number word, marker, volume word and part or format word taken out. The
// part numbers of one shattered book differ only in those words, so they reduce
// to one key ("Hush Part 1", "Hush Part 2" and "Hush 03" are all "hush"). A title
// that is nothing but those words reduces to "".
func TitleKey(title string) string {
	var kept []string
	for _, w := range words(title) {
		if isNumeric(w) || numberWords[w] || partFormatWords[w] || VolumeWords[w] || markers[w] || structuralWords[w] {
			continue
		}
		kept = append(kept, w)
	}
	// "Hush, Disc 2 of 3" leaves the "of" of "2 of 3" behind.
	for len(kept) > 0 && connectorWords[kept[len(kept)-1]] {
		kept = kept[:len(kept)-1]
	}
	return strings.Join(kept, " ")
}

func isNumeric(w string) bool {
	for _, r := range w {
		if r < '0' || r > '9' {
			return false
		}
	}
	return w != ""
}

func normalizeASIN(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// normalizeISBN drops hyphens and spaces and upper-cases the check digit.
func normalizeISBN(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= '0' && r <= '9') || r == 'X' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
