// file: internal/books/cutover/m6/read_test.go
// version: 1.0.0
// guid: 5e8a21c7-0d34-4f96-b1a2-7c3d9e04f518
// last-edited: 2026-11-19

package m6_test

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
)

// The native stores' rows, decoded for the tests. A row's key is its segments
// joined by the keys package's separator; a value is a one-byte codec tag and
// JSON. These helpers read the stores' bytes directly rather than through the
// stores, so a test fails if M6 writes a row the stores could not decode.

func decodeRow(t testing.TB, raw []byte) map[string]any {
	t.Helper()
	if len(raw) < 2 || raw[0] != 'j' {
		t.Fatalf("row %q is not a json row", raw)
	}
	var m map[string]any
	if err := json.Unmarshal(raw[1:], &m); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return m
}

// instances are core/integrations' rows by "kind/instance".
func instances(t testing.TB, s *snap) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for k, v := range s.Rows[string(ownerInteg)] {
		if !strings.HasPrefix(k, "instances\x00\x01") {
			t.Fatalf("unexpected integrations row %q", k)
		}
		row := decodeRow(t, v)
		out[row["kind"].(string)+"/"+row["instance"].(string)] = row
	}
	return out
}

func settingsOf(row map[string]any) map[string]any {
	m, _ := row["settings"].(map[string]any)
	return m
}

// refName is the secret a settings entry references, or "".
func refName(settings map[string]any, key string) string {
	m, ok := settings[key].(map[string]any)
	if !ok {
		return ""
	}
	n, _ := m["secret"].(string)
	return n
}

// jobs are core/ai's job rows by ID.
func jobs(t testing.TB, s *snap) map[string]map[string]any {
	t.Helper()
	out := map[string]map[string]any{}
	for k, v := range s.Rows[string(ownerAI)] {
		if strings.HasPrefix(k, "jobs\x00\x01") {
			row := decodeRow(t, v)
			out[row["id"].(string)] = row
		}
	}
	return out
}

// keysWithPrefix are the sorted keys of one owner's keyspace under prefix.
func keysWithPrefix(s *snap, owner, prefix string) []string {
	var out []string
	for k := range s.Rows[owner] {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func audits(s *snap) []string {
	return slices.Sorted(maps.Keys(s.Rows[string(ownerAudit)]))
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}
