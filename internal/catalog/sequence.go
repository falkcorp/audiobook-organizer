// file: internal/catalog/sequence.go
// version: 1.0.0
// guid: 8d3f1b62-4a7e-4c19-b5d8-2e6a9c0f7d35
// last-edited: 2026-10-01

package catalog

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
)

// seqRangeRe is a numeric range: "1-3", "1 – 3", "Books 1-3", "1.5-2".
// It is checked BEFORE seqnum.ParsePosition, whose trailing-number fallback
// would read "1-3" as 3.
var seqRangeRe = regexp.MustCompile(`(?i)^\s*(?:(?:books?|bks?|volumes?|vols?)\.?\s*)?(\d+(?:\.\d+)?)\s*(?:-|–|—|to|through)\s*(\d+(?:\.\d+)?)\s*$`)

// ParseSequence turns a provider sequence string into a numeric range (R7).
// A single position returns lo == hi. ok is false for anything that does not
// parse ("", "Prequel", "1-0"): an unparseable sequence must never produce a
// series gap, so callers treat !ok as "no position", not as 0.
//
//	"1"      -> 1, 1
//	"2.5"    -> 2.5, 2.5
//	"1-3"    -> 1, 3
//	"Book 2" -> 2, 2
//	""       -> !ok
func ParseSequence(s string) (lo, hi float64, ok bool) {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0, 0, false
	}
	if m := seqRangeRe.FindStringSubmatch(t); m != nil {
		a, errA := strconv.ParseFloat(m[1], 64)
		b, errB := strconv.ParseFloat(m[2], 64)
		if errA != nil || errB != nil || b < a {
			return 0, 0, false
		}
		return a, b, true
	}
	n, found := seqnum.ParsePosition(t)
	if !found {
		return 0, 0, false
	}
	return n.Value, n.Value, true
}
