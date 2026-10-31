// file: internal/merge/warning.go
// version: 1.0.0
// guid: 3f6b1c52-9a47-4e08-b2d5-6c81e0a47d93
// last-edited: 2026-10-30

package merge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
)

// Warning is one note an undo leaves for its caller: something it left as it
// is. UserIDs names the accounts whose own data the note carries (a user's
// listening progress, with the account id in the text); empty means the note is
// library data. A caller who may not manage users withholds a note by this
// field, not by what its text says: a note can use the word "user" for another
// reason, and a note can name an account without it.
//
// The producer sets UserIDs where it writes the account into the text, so the
// two cannot drift apart at a distance.
type Warning struct {
	Text    string
	UserIDs []string
}

// PlainWarning is a note that carries no user's data.
func PlainWarning(text string) Warning { return Warning{Text: text} }

// UserWarning is a note whose text names the given accounts.
func UserWarning(text string, userIDs ...string) Warning {
	return Warning{Text: text, UserIDs: slices.Clone(userIDs)}
}

// PlainWarnings wraps texts that carry no user's data.
func PlainWarnings(texts ...string) []Warning {
	if len(texts) == 0 {
		return nil
	}
	out := make([]Warning, len(texts))
	for i, t := range texts {
		out[i] = PlainWarning(t)
	}
	return out
}

// WarningTexts is the text of each warning, in order.
func WarningTexts(ws []Warning) []string {
	if len(ws) == 0 {
		return nil
	}
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Text
	}
	return out
}

// NamesUser reports whether the note carries a user's data.
func (w Warning) NamesUser() bool { return len(w.UserIDs) > 0 }

// String is the note's text.
func (w Warning) String() string { return w.Text }

// warningWire is the stored form of a note that names a user.
type warningWire struct {
	Text    string   `json:"text"`
	UserIDs []string `json:"user_ids"`
}

// MarshalJSON writes a note that names no user as a bare string, the shape
// these lists always had, and one that names a user as an object so the
// account ids survive a store round trip (the undo's applied record).
func (w Warning) MarshalJSON() ([]byte, error) {
	if len(w.UserIDs) == 0 {
		return json.Marshal(w.Text)
	}
	return json.Marshal(warningWire{Text: w.Text, UserIDs: w.UserIDs})
}

// UnmarshalJSON reads either form. A bare string (every record written before
// the field existed) is a note with no recorded user.
func (w *Warning) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*w = Warning{Text: s}
		return nil
	}
	var wire warningWire
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decode warning: %w", err)
	}
	*w = Warning{Text: wire.Text, UserIDs: wire.UserIDs}
	return nil
}
