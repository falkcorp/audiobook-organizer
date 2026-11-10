// file: core/audit/redact.go
// version: 1.0.0
// guid: ea6e37e7-8f3b-43c2-8d07-962dc40143f4
// last-edited: 2026-11-10

package audit

import (
	"bytes"
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// redacted replaces a value the redaction hides (05-rbac §7.7).
const redacted = "[redacted]"

// credentialPrefixes are the prefixes of core's credentials (05-rbac §7.7):
// session (mcs_), API key (mck_), bootstrap (mcb_), invite (mci_), temporary
// login (mct_), and HEAD's abk_ and abbs_, which imported keys and old
// archives still carry. A string value that starts with one is redacted
// whatever its key is called.
func credentialPrefixes() []string {
	return []string{"mcs_", "mck_", "mcb_", "mci_", "mct_", "abk_", "abbs_"}
}

// truncatedKey marks a Details map that lost keys to the size cap; its value
// is the number of keys dropped.
const truncatedKey = "_truncated"

// unserializableKey marks Details that JSON cannot encode (a channel, a
// function); the record keeps the marker, never the value.
const unserializableKey = "_unserializable"

// redactor applies §7.7 to Details. The key pattern is compiled once per
// store, not held in a package variable (R5).
type redactor struct {
	key *regexp.Regexp
	max int // DetailsMaxBytes
}

// newRedactor compiles the §7.7 key pattern; max is the JSON byte cap.
func newRedactor(max int) redactor {
	return redactor{key: regexp.MustCompile(`(?i)(password|secret|token|key|cookie|authorization)`), max: max}
}

// details returns a redacted, size-capped copy of in; in is never changed
// (a caller may keep and reuse its map after Emit returns). Keys that match
// the §7.7 pattern at any depth have their value replaced by "[redacted]";
// a string value carrying a credential prefix is replaced the same way. If
// the JSON of the result is over the cap, keys are dropped (in key order,
// whole values only) until it fits and truncatedKey counts the drops.
func (r redactor) details(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return map[string]any{unserializableKey: true}
	}
	// Decode with UseNumber so integers keep their digits.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree map[string]any
	if err := dec.Decode(&tree); err != nil {
		return map[string]any{unserializableKey: true}
	}
	for k, v := range tree {
		tree[k] = r.value(k, v)
	}
	return r.capped(tree)
}

// value redacts one decoded JSON value under key.
func (r redactor) value(key string, v any) any {
	if r.key.MatchString(key) {
		return redacted
	}
	switch v := v.(type) {
	case map[string]any:
		for k, e := range v {
			v[k] = r.value(k, e)
		}
		return v
	case []any:
		for i, e := range v {
			v[i] = r.value("", e)
		}
		return v
	case string:
		if hasCredential(v) {
			return redacted
		}
	}
	return v
}

// hasCredential reports a credential prefix at the start of s or of any word
// of it: a note that reads "Bearer mck_..." is as much a leak as the bare
// token.
func hasCredential(s string) bool {
	prefixes := credentialPrefixes()
	for _, w := range strings.FieldsFunc(s, func(c rune) bool {
		return !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' && c != '-'
	}) {
		for _, p := range prefixes {
			if strings.HasPrefix(w, p) {
				return true
			}
		}
	}
	return false
}

// capped keeps tree's JSON within r.max bytes.
func (r redactor) capped(tree map[string]any) map[string]any {
	if raw, err := json.Marshal(tree); err == nil && len(raw) <= r.max {
		return tree
	}
	// Reserve room for the marker: {"_truncated":99999}.
	budget := r.max - len(`,"`+truncatedKey+`":99999`)
	out := map[string]any{}
	size := 2 // the braces
	dropped := 0
	for _, k := range slices.Sorted(maps.Keys(tree)) {
		kb, kerr := json.Marshal(k)
		vb, verr := json.Marshal(tree[k])
		if kerr != nil || verr != nil {
			dropped++
			continue
		}
		n := len(kb) + 1 + len(vb) // "key":value
		if len(out) > 0 {
			n++ // the comma
		}
		if size+n > budget {
			dropped++
			continue
		}
		out[k] = tree[k]
		size += n
	}
	out[truncatedKey] = dropped
	return out
}
