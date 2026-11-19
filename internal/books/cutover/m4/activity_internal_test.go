// file: internal/books/cutover/m4/activity_internal_test.go
// version: 1.2.0
// guid: c20d6f94-1b7a-4e85-a3f0-7d92e51b4c68
// last-edited: 2026-11-18

package m4

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// noRuns is a resolver over no runs: every repairs reference fails.
func noRuns(t *testing.T) *kinds {
	return &kinds{ctx: context.Background(), runs: runsOf(nil), cache: map[string]string{}}
}

// runsOf is a RunReader over a map of run ID to kind.
type runsOf map[string]tasks.Kind

func (m runsOf) Get(_ context.Context, id tasks.RunID) (tasks.RunRecord, error) {
	k, ok := m[string(id)]
	if !ok {
		return tasks.RunRecord{}, fmt.Errorf("%w: %s", tasks.ErrRunNotFound, id)
	}
	return tasks.RunRecord{ID: id, Kind: k}, nil
}

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
		if _, _, changed, ok, err := rewriteEntry(tier, row, noRuns(t)); err != nil || !ok || !changed {
			t.Fatalf("%s: the first pass changed=%v ok=%v err=%v", tier, changed, ok, err)
		}
		out, err := encode(row)
		if err != nil {
			t.Fatal(err)
		}
		var again map[string]json.RawMessage
		if err := json.Unmarshal(out, &again); err != nil {
			t.Fatal(err)
		}
		if _, _, changed, ok, err := rewriteEntry(tier, again, noRuns(t)); err != nil || !ok || changed {
			t.Errorf("%s: the second pass changed=%v ok=%v err=%v over %s", tier, changed, ok, err, out)
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
		k, _, ok := kindOf(id)
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
		if _, _, again := kindOf(k); again {
			t.Errorf("kind %q is itself a def ID: a second pass would rename it again", k)
		}
	}
}

// TestRenameDigestItems_ResolvesPerItem: a digest item takes its operations from
// its op: tags, else from its own operation_id; an item naming several runs
// resolves when they share one kind; and an item that cannot resolve stays as
// stored, whole, while the digest's other items are renamed (the row is not held).
func TestRenameDigestItems_ResolvesPerItem(t *testing.T) {
	runs := runsOf{
		"R1": "books.repairs.two-face-asins", "R2": "books.repairs.two-face-asins", "R3": "books.repairs.hush-credits",
		"SCAN": "books.dedup.book-scan",
	}
	const (
		planNo  = `[{"type":"repairs.plan","tags":["def:repairs.plan"]}]`
		heldAmb = reasonActivityItemAmbiguous
		heldRun = reasonActivityItem
	)
	cases := []struct {
		name  string
		items string // the digest's items
		want  string // the items afterwards
		held  []string
	}{
		{"tagless item takes its operation_id",
			`[{"type":"repairs.plan","operation_id":"R1"},{"type":"repairs.apply","operation_id":"R3","tags":["def:repairs.apply"]}]`,
			`[{"type":"books.repairs.two-face-asins","operation_id":"R1"},{"type":"books.repairs.hush-credits","operation_id":"R3","tags":["def:books.repairs.hush-credits"]}]`, nil},
		{"the op: tag wins over operation_id",
			`[{"type":"repairs.plan","operation_id":"R1","tags":["op:R3"]}]`,
			`[{"type":"books.repairs.hush-credits","operation_id":"R1","tags":["op:R3"]}]`, nil},
		{"several ops of one kind resolve",
			`[{"type":"repairs.plan","tags":["def:repairs.plan","op:R1","op:R2"]}]`,
			`[{"type":"books.repairs.two-face-asins","tags":["def:books.repairs.two-face-asins","op:R1","op:R2"]}]`, nil},
		{"a repeated op: tag is one op",
			`[{"type":"repairs.plan","tags":["op:R1","op:R1"]}]`,
			`[{"type":"books.repairs.two-face-asins","tags":["op:R1","op:R1"]}]`, nil},
		{"several ops of different kinds stay as stored",
			`[{"type":"repairs.plan","tags":["def:repairs.plan","op:R1","op:R3"]}]`,
			`[{"type":"repairs.plan","tags":["def:repairs.plan","op:R1","op:R3"]}]`, []string{heldAmb}},
		{"several ops, one of them absent, stay as stored",
			`[{"type":"repairs.plan","tags":["op:R1","op:GONE"]}]`,
			`[{"type":"repairs.plan","tags":["op:R1","op:GONE"]}]`, []string{heldRun}},
		{"several ops, the first absent, stay as stored",
			`[{"type":"repairs.plan","tags":["op:GONE","op:R1"]}]`,
			`[{"type":"repairs.plan","tags":["op:GONE","op:R1"]}]`, []string{heldRun}},
		{"a def-less item with several ops is untouched and not held",
			`[{"type":"scan_complete","tags":["op:R1","op:R3"]}]`,
			`[{"type":"scan_complete","tags":["op:R1","op:R3"]}]`, nil},
		{"no operation at all stays as stored",
			planNo, planNo, []string{heldRun}},
		{"a non-string operation_id names no run",
			`[{"type":"repairs.plan","operation_id":7}]`,
			`[{"type":"repairs.plan","operation_id":7}]`, []string{heldRun}},
		{"a run of no repairs kind stays as stored",
			`[{"type":"repairs.plan","operation_id":"SCAN"}]`,
			`[{"type":"repairs.plan","operation_id":"SCAN"}]`, []string{heldRun}},
		{"an item is renamed whole or not at all",
			`[{"type":"dedup.book-scan","tags":["def:dedup.book-scan","def:repairs.plan"]}]`,
			`[{"type":"dedup.book-scan","tags":["def:dedup.book-scan","def:repairs.plan"]}]`, []string{heldRun}},
		{"a mixed row renames what resolves and holds only the rest",
			`[{"type":"dedup.book-scan","tags":["def:dedup.book-scan"]},{"type":"repairs.plan","operation_id":"R1"},{"type":"repairs.apply","tags":["def:repairs.apply","op:GONE"]},{"type":"scan_complete"},{"type":"repairs.plan","tags":["op:R1","op:R3"]}]`,
			`[{"type":"books.dedup.book-scan","tags":["def:books.dedup.book-scan"]},{"type":"books.repairs.two-face-asins","operation_id":"R1"},{"type":"repairs.apply","tags":["def:repairs.apply","op:GONE"]},{"type":"scan_complete"},{"type":"repairs.plan","tags":["op:R1","op:R3"]}]`,
			[]string{heldRun, heldAmb}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var items json.RawMessage = json.RawMessage(c.items)
			det := map[string]json.RawMessage{"items": items}
			ks := &kinds{ctx: context.Background(), runs: runs, cache: map[string]string{}}
			changed, ok, err := renameDigestItems(det, ks)
			if err != nil || !ok {
				t.Fatalf("ok=%v err=%v; an unresolvable item is not an error", ok, err)
			}
			if !reflect.DeepEqual(ks.heldItems, c.held) {
				t.Errorf("held items = %q, want %q", ks.heldItems, c.held)
			}
			var got, want any
			if err := json.Unmarshal(det["items"], &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("items = %s\nwant    %s", det["items"], c.want)
			}
			var before any
			_ = json.Unmarshal([]byte(c.items), &before)
			if changed != !reflect.DeepEqual(before, want) {
				t.Errorf("changed = %v, but the items %s", changed, map[bool]string{true: "differ", false: "are the same"}[!reflect.DeepEqual(before, want)])
			}
		})
	}
}
