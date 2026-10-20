// file: internal/util/email.go
// version: 1.0.0
// guid: a11d6c27-5ca8-4c72-b3cc-a9e585bc7dc8
// last-edited: 2026-10-20

package util

import (
	"strings"

	"golang.org/x/text/unicode/norm"
)

// NormalizeEmail returns the form every email comparison and storage key
// uses: trimmed, Unicode NFKC-normalized, lowercased, and normalized again
// (lowercasing can leave a string that is no longer in NFKC).
//
// strings.ToLower alone is not enough. Compatibility characters such as the
// Kelvin sign U+212A, the long s U+017F, or fullwidth letters render like
// ASCII and match under strings.EqualFold (or a mail server's own folding)
// but not under ToLower, so a key built with ToLower and a check built with
// EqualFold could disagree about whether two addresses are the same: a user
// could hold "\u212aate@example.com" (Kelvin sign) next to a verified
// "kate@example.com" and be treated as the same address by one check and as
// a different one by another. NFKC maps every such
// character to its ordinary form first.
//
// Compare two addresses with SameEmail, not strings.EqualFold.
func NormalizeEmail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return strings.TrimSpace(norm.NFKC.String(strings.ToLower(norm.NFKC.String(s))))
}

// SameEmail reports whether a and b are the same address after
// NormalizeEmail. Two empty addresses are not the same address.
func SameEmail(a, b string) bool {
	na := NormalizeEmail(a)
	return na != "" && na == NormalizeEmail(b)
}
