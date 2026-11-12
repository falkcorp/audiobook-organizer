// file: internal/books/cutover/m3/bound.go
// version: 1.0.0
// guid: e143bbff-4f2e-42fd-bbf2-a332fc5a943e
// last-edited: 2026-11-12

package m3

import (
	"fmt"
	"unicode/utf8"
)

// Values the step takes from HEAD's rows and puts in a log line or a report
// are bounded: a row can carry a status or an atom name of any length, and a
// log field must not grow with it.
const (
	// maxStatusLen is the longest status a log line or a report names.
	maxStatusLen = 32
	// maxLoggedAtoms is how many unknown atom names one log line lists.
	maxLoggedAtoms = 8
	// maxAtomLen is the longest atom name a log line carries.
	maxAtomLen = 64
)

// clip is s cut to at most n bytes, at a rune boundary: a cut through the
// middle of a multi-byte rune would leave invalid UTF-8 in a log field.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// boundedAtoms is the atom names of one log field: the first maxLoggedAtoms,
// each clipped to maxAtomLen bytes, and a last entry counting what was left
// out. The caller still counts every name through its own outcome.
func boundedAtoms(atoms []string) []string {
	out := make([]string, 0, min(len(atoms), maxLoggedAtoms)+1)
	for i, a := range atoms {
		if i == maxLoggedAtoms {
			out = append(out, fmt.Sprintf("(+%d more)", len(atoms)-maxLoggedAtoms))
			break
		}
		out = append(out, clip(a, maxAtomLen))
	}
	return out
}
