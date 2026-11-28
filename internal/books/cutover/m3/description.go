// file: internal/books/cutover/m3/description.go
// version: 0.1.0
// guid: 40132076-73af-4d88-a916-d5b5bd55bce6
// last-edited: 2026-11-22

package m3

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/falkcorp/mediacore/auth"
)

// What normalizeDescription changed, as the fixed words a report carries. A
// report names a key and these, never the description: the text can hold
// anything an owner typed.
const (
	descInvalidUTF8 = "invalid UTF-8 replaced"
	descLineBreaks  = "line breaks or tabs turned into spaces"
	descControls    = "control characters dropped"
	descTrimmed     = "surrounding spaces trimmed"
	descTruncated   = "truncated to the 1024-character limit"
)

// normalizeDescription is HEAD's API key description made to fit core's rule
// (auth.CheckKeyDescription), deterministically, and what it changed. HEAD
// never limited the text and its create form was a multi-line box, and HEAD has
// no way to edit a key's description afterwards: refusing a description would
// leave its owner no repair short of editing the store by hand or reissuing the
// key, which breaks the promise that existing abk_ keys keep working. A
// description is a display label, so the cutover tidies it instead:
//
//  1. each invalid UTF-8 byte becomes U+FFFD;
//  2. each run of CR, LF and tab becomes one space;
//  3. every other control character is dropped;
//  4. leading and trailing spaces are trimmed;
//  5. the text is cut to auth.MaxKeyDescriptionRunes characters.
//
// A description core's rule already accepts, with no surrounding space, comes
// back byte for byte and changes is empty. Core's own write rule stays strict:
// this runs only on the import path.
func normalizeDescription(s string) (out string, changes []string) {
	var b strings.Builder
	var invalid, breaks, controls bool
	inBreak := false
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
		switch {
		case r == utf8.RuneError && n == 1:
			invalid, inBreak = true, false
			b.WriteRune(unicode.ReplacementChar)
		case r == '\r' || r == '\n' || r == '\t':
			breaks = true
			if !inBreak {
				b.WriteByte(' ')
			}
			inBreak = true
		case unicode.IsControl(r):
			controls, inBreak = true, false
		default:
			inBreak = false
			b.WriteRune(r)
		}
	}
	out = b.String()
	trimmed, truncated := false, false
	if t := strings.Trim(out, " "); t != out {
		out, trimmed = t, true
	}
	if utf8.RuneCountInString(out) > auth.MaxKeyDescriptionRunes {
		out, truncated = string([]rune(out)[:auth.MaxKeyDescriptionRunes]), true
	}
	// A fixed order, so a report reads the same for the same input.
	for _, c := range []struct {
		did  bool
		what string
	}{{invalid, descInvalidUTF8}, {breaks, descLineBreaks}, {controls, descControls}, {trimmed, descTrimmed}, {truncated, descTruncated}} {
		if c.did {
			changes = append(changes, c.what)
		}
	}
	return out, changes
}
