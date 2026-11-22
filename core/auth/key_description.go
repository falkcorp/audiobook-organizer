// file: core/auth/key_description.go
// version: 0.1.0
// guid: e0a87903-82de-4cac-8579-840b631b1554
// last-edited: 2026-11-22

package auth

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxKeyDescriptionRunes is the longest API key description core stores, in
// characters. HEAD set no limit (handlers.CreateAPIKeyRequest passes the text
// to the store unchecked), so this one is core's: it matches the role
// description bound of the RBAC routes (rbachttp maxDescriptionRunes), enough
// for a sentence or two telling a phone app's key from the Claude workspace's,
// and short enough that a key list row can show it.
const MaxKeyDescriptionRunes = 1024

// The fixed reasons CheckKeyDescription gives. They name no value: a caller
// that wraps one (a blocker, a field error) never carries the text.
var (
	errKeyDescriptionLong    = errors.New("the description is too long")
	errKeyDescriptionInvalid = errors.New("the description is not valid text (invalid UTF-8 or a control character, line breaks included)")
)

// CheckKeyDescription is the one rule for an API key's optional description:
// valid UTF-8, no control character (a line break and a tab are controls) and
// at most MaxKeyDescriptionRunes characters. The empty string is valid. The
// text is judged as given: it is never trimmed or shortened, so a description
// that breaks the rule is refused, never altered. CreateAPIKey, the import
// rule (CheckImportAPIKey) and the HTTP layer all call it.
func CheckKeyDescription(s string) error {
	if !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
		return errKeyDescriptionInvalid
	}
	if utf8.RuneCountInString(s) > MaxKeyDescriptionRunes {
		return errKeyDescriptionLong
	}
	return nil
}
