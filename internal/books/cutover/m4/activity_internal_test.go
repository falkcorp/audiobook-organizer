// file: internal/books/cutover/m4/activity_internal_test.go
// version: 1.0.0
// guid: c20d6f94-1b7a-4e85-a3f0-7d92e51b4c68
// last-edited: 2026-11-18

package m4

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestActivityRewrite_SecondPassChangesNothing: a row the rewrite already
// renamed names a kind, which is no def ID, so a second pass (a resumed boot
// that re-steps the row, or a later run of the step) finds nothing to change.
func TestActivityRewrite_SecondPassChangesNothing(t *testing.T) {
	for _, tier := range []string{"info", "digest"} {
		var row map[string]json.RawMessage
		body := `{"type":"dedup.book-scan","tags":["def:dedup.book-scan"],"details":{"def_id":"dedup.book-scan","items":[{"type":"dedup.book-scan","tags":["def:dedup.book-scan"]}]}}`
		if tier == "digest" {
			body = `{"type":"daily_digest","tags":["def:dedup.book-scan"],"details":{"items":[{"type":"dedup.book-scan","tags":["def:dedup.book-scan"]}]}}`
		}
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			t.Fatal(err)
		}
		if _, _, changed, ok := rewriteEntry(tier, row); !ok || !changed {
			t.Fatalf("%s: the first pass changed=%v ok=%v", tier, changed, ok)
		}
		out, err := encode(row)
		if err != nil {
			t.Fatal(err)
		}
		var again map[string]json.RawMessage
		if err := json.Unmarshal(out, &again); err != nil {
			t.Fatal(err)
		}
		if _, _, changed, ok := rewriteEntry(tier, again); !ok || changed {
			t.Errorf("%s: the second pass changed=%v ok=%v over %s", tier, changed, ok, out)
		}
	}
}

// TestActivityKinds_NoKindIsADefID: the type index key is written without the
// store's ':' and '%' escape (see typeIndexKey), which is sound only while
// neither a def ID the map knows nor a kind it maps to contains either.
func TestActivityKinds_NoKindIsADefID(t *testing.T) {
	raw, err := os.ReadFile("../kindrename/testdata/head_registry.golden")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range strings.Split(string(raw), "\n") {
		f := strings.Fields(l)
		switch {
		case strings.HasPrefix(l, "#") || len(f) < 2:
		case f[0] == "id":
			ids = append(ids, f[1])
		case f[0] == "alias":
			ids = append(ids, f[1], f[2])
		}
	}
	if len(ids) < 200 {
		t.Fatalf("read %d ids from the registry dump, want the whole registry", len(ids))
	}
	for _, id := range ids {
		k, ok := kindOf(id)
		if !ok {
			continue
		}
		for _, s := range []string{id, k} {
			for _, c := range s {
				if c == ':' || c == '%' {
					t.Errorf("%q (%q) contains %q: typeIndexKey would need the store's escape", s, id, c)
				}
			}
		}
		if _, again := kindOf(k); again {
			t.Errorf("kind %q is itself a def ID: a second pass would rename it again", k)
		}
	}
}
