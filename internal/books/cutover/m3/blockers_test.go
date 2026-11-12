// file: internal/books/cutover/m3/blockers_test.go
// version: 1.0.0
// guid: d1915583-6095-4a27-b052-af70657b0a42
// last-edited: 2026-11-12

package m3

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestDecodeRows: a row that does not decode is named by a fixed class, never
// by the text of a JSON error (which can quote the row), and an ID that is not
// its key's is refused.
func TestDecodeRows(t *testing.T) {
	type decode func(key string, raw []byte) error
	user := func(k string, r []byte) error { _, err := decodeUser(k, r); return err }
	key := func(k string, r []byte) error { _, err := decodeKey(k, r); return err }
	ident := func(k string, r []byte) error { _, err := decodeIdentity(k, r); return err }
	role := func(k string, r []byte) error { _, err := decodeRole(k, r); return err }
	for _, c := range []struct {
		name   string
		fn     decode
		key    string
		raw    string
		want   error
		secret string
	}{
		{"user ok", user, "u:A", `{"id":"A"}`, nil, ""},
		{"user: empty key", user, "u:", `{"id":"A"}`, errEmptyKey, ""},
		{"user: not JSON", user, "u:A", "{harvey-dent", errNotJSON, "harvey-dent"},
		{"user: null", user, "u:A", "null", errNotJSON, ""},
		{"user: an array", user, "u:A", `["two-face"]`, errNotJSON, "two-face"},
		{"user: empty", user, "u:A", "  ", errNotJSON, ""},
		{"user: wrong type", user, "u:A", `{"id":"A","roles":"two-face"}`, errBadShape, "two-face"},
		{"user: no ID", user, "u:A", `{"username":"two-face"}`, errNoID, "two-face"},
		{"user: another's ID", user, "u:A", `{"id":"B"}`, errIDKey, ""},
		{"key ok", key, "apikey:A", `{"id":"A"}`, nil, ""},
		{"key: another's ID", key, "apikey:A", `{"id":"B"}`, errIDKey, ""},
		{"key: not JSON", key, "apikey:A", "oops", errNotJSON, ""},
		{"identity ok", ident, "oauthid:A", `{"id":"A"}`, nil, ""},
		{"identity: another's ID", ident, "oauthid:A", `{"id":"B"}`, errIDKey, ""},
		{"identity: empty key", ident, "oauthid:", `{"id":"A"}`, errEmptyKey, ""},
		{"role ok", role, "role:A", `{"id":"A"}`, nil, ""},
		{"role: no ID", role, "role:A", `{}`, errNoID, ""},
		{"role: another's ID", role, "role:A", `{"id":"B"}`, errIDKey, ""},
	} {
		err := c.fn(c.key, []byte(c.raw))
		if !errors.Is(err, c.want) && err != c.want {
			t.Errorf("%s: %v, want %v", c.name, err, c.want)
			continue
		}
		if err != nil && c.secret != "" && strings.Contains(err.Error(), c.secret) {
			t.Errorf("%s: the error quotes %q", c.name, c.secret)
		}
	}
}

// TestFixAndDeleteRefusals: fix and delete act only on a user, a key or an
// identity row; a replacement that is not a row of that family naming that
// key is refused before anything is staged (the batch is never reached).
func TestFixAndDeleteRefusals(t *testing.T) {
	m := testMigration(t)
	ctx := context.Background()
	for _, c := range []struct {
		name, key, value string
	}{
		{"a journal", JournalPrefix + "j", `{"id":"j"}`},
		{"a role", "role:A", `{"id":"A"}`},
		{"a session", "sess:A", "x"},
		{"the bare prefix", "u:", `{"id":"A"}`},
		{"an index row", "idx:user:username:a", "A"},
		{"a user with another's ID", "u:A", `{"id":"B"}`},
		{"a user with a value that is not JSON", "u:A", "not json"},
		{"a key with another's ID", "apikey:A", `{"id":"B"}`},
		{"an identity with no ID", "oauthid:A", `{}`},
	} {
		if err := m.FixBlocker(ctx, nil, []byte(c.key), []byte(c.value)); err == nil {
			t.Errorf("fix of %s was accepted", c.name)
		} else if strings.Contains(err.Error(), "not json") {
			t.Errorf("fix of %s: the refusal quotes the value: %v", c.name, err)
		}
	}
	for _, key := range []string{JournalPrefix + "j", "role:A", "sess:A", "u:", "apikey:", "oauthid:", "idx:user:username:a", "setting:bootstrap_token_hash"} {
		if _, err := m.DeleteBlocker(ctx, nil, []byte(key)); err == nil {
			t.Errorf("delete of %q was accepted", key)
		}
	}
}

func TestPrimaryFamily(t *testing.T) {
	for key, want := range map[string]string{
		"u:A": "u:", "apikey:A": "apikey:", "oauthid:A": "oauthid:",
		"u:": "", "apikey:": "", "oauthid:": "", "role:A": "", "invite:A": "", "idx:user:username:a": "", "": "",
	} {
		if got := primaryFamily([]byte(key)); got != want {
			t.Errorf("primaryFamily(%q) = %q, want %q", key, got, want)
		}
	}
}
