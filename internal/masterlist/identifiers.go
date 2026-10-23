// file: internal/masterlist/identifiers.go
// version: 1.0.0
// guid: 9f55d631-2d6d-4b9e-80f6-2ff098e1419e
// last-edited: 2026-10-23

package masterlist

import "strings"

// Identifier validation and normalisation for the master-list import. Every
// function here is pure: it reads one string and says what it is.

// cleanIdentifier strips the characters an export puts inside an identifier
// (a UTF-8 byte order mark, spaces, hyphens, non-breaking spaces) and
// upper-cases it. It never drops a letter or a digit.
func cleanIdentifier(s string) string {
	s = strings.TrimPrefix(s, "\ufeff")
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '-', '\t', '\u00a0', '\u2010', '\u2011', '\u2013', '\ufeff':
			continue
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// ValidISBN10 reports whether s (after cleaning) is an ISBN-10: nine digits
// and a check character that is a digit or X, with sum(weight*digit) divisible
// by 11 for weights 10 down to 1. X (the value 10) is allowed only last. It
// returns the cleaned form.
func ValidISBN10(s string) (string, bool) {
	c := cleanIdentifier(s)
	if len(c) != 10 {
		return c, false
	}
	sum := 0
	for i := 0; i < 10; i++ {
		ch := c[i]
		v := 0
		switch {
		case ch >= '0' && ch <= '9':
			v = int(ch - '0')
		case ch == 'X' && i == 9:
			v = 10
		default:
			return c, false
		}
		sum += (10 - i) * v
	}
	return c, sum%11 == 0
}

// ValidISBN13 reports whether s (after cleaning) is an ISBN-13: thirteen
// digits starting 978 or 979 whose EAN-13 check digit (weights 1 and 3,
// alternating) is right. It returns the cleaned form.
func ValidISBN13(s string) (string, bool) {
	c := cleanIdentifier(s)
	if len(c) != 13 || !allDigits(c) {
		return c, false
	}
	if !strings.HasPrefix(c, "978") && !strings.HasPrefix(c, "979") {
		return c, false
	}
	return c, ean13Check(c[:12]) == c[12]
}

// ISBN10To13 converts a VALID ISBN-10 to its ISBN-13: 978, the first nine
// digits, and a new check digit. ok is false when s is not a valid ISBN-10.
func ISBN10To13(s string) (string, bool) {
	c, ok := ValidISBN10(s)
	if !ok {
		return "", false
	}
	body := "978" + c[:9]
	return body + string(ean13Check(body)), true
}

// NormalizeISBN13 is the ISBN-13 an ISBN-10 or ISBN-13 stands for, or "" when
// s is neither. It is what two spellings of one edition share.
func NormalizeISBN13(s string) string {
	if c, ok := ValidISBN13(s); ok {
		return c
	}
	if c, ok := ISBN10To13(s); ok {
		return c
	}
	return ""
}

// ean13Check is the EAN-13 check digit of the first twelve digits.
func ean13Check(twelve string) byte {
	sum := 0
	for i := 0; i < 12; i++ {
		d := int(twelve[i] - '0')
		if i%2 == 1 {
			d *= 3
		}
		sum += d
	}
	return byte('0' + (10-sum%10)%10)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ValidASIN reports whether s (after cleaning) has the shape of an ASIN: ten
// upper-case letters or digits. It returns the cleaned form. The shape is all
// that can be checked; an ASIN carries no checksum. An all-digit value that
// passes the ISBN-10 check is an ISBN-10, not an ASIN (see ClassifyASIN).
func ValidASIN(s string) (string, bool) {
	c := cleanIdentifier(s)
	if len(c) != 10 {
		return c, false
	}
	for i := 0; i < len(c); i++ {
		ch := c[i]
		if (ch < '0' || ch > '9') && (ch < 'A' || ch > 'Z') {
			return c, false
		}
	}
	return c, true
}

// ClassifyASIN splits an "ASIN" field value: an all-digit value that passes
// the ISBN-10 check is an ISBN-10 (isbn10 is returned, asin is empty); any
// other value of ASIN shape is an ASIN. ok is false when it is neither.
func ClassifyASIN(s string) (asin, isbn10 string, ok bool) {
	c, shape := ValidASIN(s)
	if !shape {
		return "", "", false
	}
	if allDigits(c) {
		if v, isISBN := ValidISBN10(c); isISBN {
			return "", v, true
		}
	}
	return c, "", true
}
