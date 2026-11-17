// file: internal/metafetch/collected_edition.go
// version: 1.0.0
// guid: 6c1f8e24-9b07-4d53-a8e1-3f7d2b50c486
// last-edited: 2026-11-16

package metafetch

import (
	"regexp"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// The compilation penalty (isCompilation, config
// metadata_scoring.compilation_penalty) exists so a search for ONE book does
// not rank a box set that merely contains it. It is the wrong answer when the
// book itself IS that box set: an exact omnibus-to-omnibus title match scored
// base x 0.15 and fell under every apply floor.
//
// SameCollectedEdition is the one question that lifts it, and it defaults to
// "penalise": it says yes only when the stored book is itself a collected
// edition and the candidate is demonstrably that same one.
//
//   - both state a range (seqnum.ParseRange on a title or subtitle, a series
//     position field): the same range, nothing else;
//   - exactly one states a range: no ("Books 1-3" is not "Omnibus");
//   - neither does: both carry the same collected-edition wording (an omnibus
//     is an omnibus, a box set a box set) and neither carries a volume number,
//     or both carry the same one ("Publisher's Pack 2" is not "Publisher's
//     Pack 3").
//
// The wording is matched on word boundaries, never as a substring, so
// "recollection" is not "collection". The book side is the STORED title, not a
// search variant or a transcribed title: a variant can be a different
// string than the book's own and must not decide a penalty.
var collectedWording = []struct {
	class string
	re    *regexp.Regexp
}{
	{"omnibus", regexp.MustCompile(`\bomnibus\b`)},
	{"box set", regexp.MustCompile(`\b(?:box(?:ed)?[\s-]*set|books?\s+set|series\s+set)\b`)},
	{"complete", regexp.MustCompile(`\bcomplete\s+(?:series|collection)\b|\bseries\s+collection\b`)},
	{"collection", regexp.MustCompile(`\bcollection\b`)},
	{"anthology", regexp.MustCompile(`\banthology\b`)},
	{"compendium", regexp.MustCompile(`\bcompendium\b`)},
	{"publisher's pack", regexp.MustCompile(`\bpublisher.?s\s+pack\b`)},
	{"n books", regexp.MustCompile(`\b\d+\s+books\b`)},
}

// collectedWordingClasses is the sorted set of collected-edition wording
// classes in s.
func collectedWordingClasses(s string) []string {
	lower := strings.ToLower(s)
	var out []string
	for _, w := range collectedWording {
		if m := w.re.FindString(lower); m != "" {
			c := w.class
			if c == "n books" {
				c = m // "3 books" is not "5 books"
			}
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

// collectedEdition is what a set of texts says about being a collected
// edition.
type collectedEdition struct {
	span   *seqnum.Number // the stated range, nil for none
	single *seqnum.Number // a volume or pack number outside any range, nil for none
	words  []string
}

// describeCollected reads title, subtitle and series position (position may
// be "").
func describeCollected(title, subtitle, position string) collectedEdition {
	var e collectedEdition
	for _, t := range []string{title, subtitle} {
		if n, ok := seqnum.ParseRange(t); ok && e.span == nil {
			e.span = &n
		}
	}
	if p := strings.TrimSpace(position); p != "" {
		if n, ok := seqnum.ParsePosition(p); ok {
			if n.IsRange() {
				if e.span == nil {
					e.span = &n
				}
			} else if e.single == nil {
				e.single = &n
			}
		}
	}
	if e.span == nil {
		for _, t := range []string{title, subtitle} {
			if n, ok := seqnum.Parse(t); ok && !n.IsRange() && e.single == nil {
				e.single = &n
			}
		}
	}
	e.words = collectedWordingClasses(title + " " + subtitle)
	return e
}

// SameCollectedEdition reports whether the stored book is itself a collected
// edition and the candidate is that same one, so the compilation penalty does
// not apply. See the rules above; every other case, including an empty book
// title, is false.
func SameCollectedEdition(bookTitle, candTitle, candSubtitle, candSeriesPosition string) bool {
	if strings.TrimSpace(bookTitle) == "" {
		return false
	}
	b := describeCollected(bookTitle, "", "")
	c := describeCollected(candTitle, candSubtitle, candSeriesPosition)
	if b.span != nil || c.span != nil {
		return b.span != nil && c.span != nil && seqnum.Equal(*b.span, *c.span)
	}
	if len(b.words) == 0 || !slices.Equal(b.words, c.words) {
		return false
	}
	switch {
	case b.single == nil && c.single == nil:
		return true
	case b.single != nil && c.single != nil:
		return seqnum.Equal(*b.single, *c.single)
	}
	return false
}
