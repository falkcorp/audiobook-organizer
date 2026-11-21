// file: internal/books/cutover/m4/activity_internal_test.go
// version: 1.3.0
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
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"

	"github.com/falkcorp/audiobook-organizer/internal/books/store"
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
	for l := range strings.SplitSeq(string(raw), "\n") {
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

// opTagsOf is n op: tags, OP00..OP<n-1>, as a JSON array fragment.
func opTagsOf(n int) string {
	tags := make([]string, n)
	for i := range tags {
		tags[i] = fmt.Sprintf(`"op:OP%02d"`, i)
	}
	return strings.Join(tags, ",")
}

// TestRenameDigestItems_SummarizeSampleIsHeld: a Summarize digest item keeps at
// most digestOpSampleMax op: tags, so an item with that many may be missing the
// operations of another fixer. It stays as stored (a rename cannot be undone);
// one with fewer tags has its whole list and is renamed.
func TestRenameDigestItems_SummarizeSampleIsHeld(t *testing.T) {
	runs := runsOf{}
	for i := range 40 {
		runs[fmt.Sprintf("OP%02d", i)] = "books.repairs.two-face-asins"
	}
	for _, c := range []struct {
		name string
		ops  int
		held []string
	}{
		{"one op under the cap is renamed", digestOpSampleMax - 1, nil},
		{"the cap's worth of ops is held", digestOpSampleMax, []string{reasonActivityItemSampled}},
		{"more than the cap is held", digestOpSampleMax + 1, []string{reasonActivityItemSampled}},
	} {
		t.Run(c.name, func(t *testing.T) {
			tags := `"def:repairs.plan",` + opTagsOf(c.ops)
			det := map[string]json.RawMessage{"items": json.RawMessage(`[{"type":"repairs.plan","tags":[` + tags + `]},{"type":"scan_complete","tags":[` + opTagsOf(c.ops) + `]}]`)}
			ks := &kinds{ctx: context.Background(), runs: runs, cache: map[string]string{}}
			changed, ok, err := renameDigestItems(det, ks)
			if err != nil || !ok {
				t.Fatalf("ok=%v err=%v", ok, err)
			}
			if !reflect.DeepEqual(ks.heldItems, c.held) {
				t.Errorf("held items = %q, want %q", ks.heldItems, c.held)
			}
			var items []struct {
				Type string `json:"type"`
				Tags []string
			}
			if err := json.Unmarshal(det["items"], &items); err != nil {
				t.Fatal(err)
			}
			wantType, wantChanged := "books.repairs.two-face-asins", true
			if c.held != nil {
				wantType, wantChanged = "repairs.plan", false
			}
			if items[0].Type != wantType || changed != wantChanged {
				t.Errorf("the repairs item is %q (changed %v), want %q (changed %v)", items[0].Type, changed, wantType, wantChanged)
			}
			if items[1].Type != "scan_complete" {
				t.Errorf("an item of no repairs def changed: %q", items[1].Type)
			}
		})
	}
	t.Run("a tagless item with an operation_id is never sampled", func(t *testing.T) {
		det := map[string]json.RawMessage{"items": json.RawMessage(`[{"type":"repairs.plan","operation_id":"OP00"}]`)}
		ks := &kinds{ctx: context.Background(), runs: runs, cache: map[string]string{}}
		if changed, _, err := renameDigestItems(det, ks); err != nil || !changed || len(ks.heldItems) != 0 {
			t.Errorf("changed=%v err=%v held=%q", changed, err, ks.heldItems)
		}
	})
}

// TestDigestOpSampleMax_MatchesSummarize: digestOpSampleMax is a pinned copy of
// the store's unexported summarizeOpSampleMax (m4 cannot import the store), so
// run a real Summarize over more operations than any cap and count the op: tags
// of the row it writes. A store that changed its cap fails here.
func TestDigestOpSampleMax_MatchesSummarize(t *testing.T) {
	db, err := pebble.Open("test.pebble", &pebble.Options{FS: vfs.NewMem()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := store.NewPebbleActivityStore(store.NewGatedDB(db))
	day := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	const ops = 200
	for i := range ops {
		if _, err := s.Record(store.ActivityEntry{Tier: "change", Type: "repairs.plan", Level: "info", Source: "maintenance",
			OperationID: fmt.Sprintf("OP%03d", i), Summary: "planned", Timestamp: day.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Summarize(context.Background(), day.Add(48*time.Hour), "change"); err != nil {
		t.Fatal(err)
	}
	rows, _, err := s.Query(context.Background(), store.ActivityFilter{Tier: "change", Limit: 10})
	if err != nil || len(rows) != 1 {
		t.Fatalf("summary rows = %d, err %v; want 1", len(rows), err)
	}
	n := 0
	for _, tag := range rows[0].Tags {
		if strings.HasPrefix(tag, "op:") {
			n++
		}
	}
	if n != digestOpSampleMax {
		t.Errorf("Summarize kept %d op: tags, digestOpSampleMax = %d: update the pinned copy", n, digestOpSampleMax)
	}
}

// recSink is a datamigrate.Sink that records what Step does with a row.
type recSink struct {
	sets   int
	holds  map[string]string
	counts map[string]int
}

func (r *recSink) Set(_, _ []byte) error               { r.sets++; return nil }
func (r *recSink) Delete([]byte) error                 { return nil }
func (r *recSink) SetIfAbsent(_, _ []byte) error       { return nil }
func (r *recSink) SetIfUnchanged(_, _, _ []byte) error { return nil }
func (r *recSink) DeleteIfUnchanged(_, _ []byte) error { return nil }
func (r *recSink) Hold(key []byte, reason string)      { r.holds[string(key)] = reason }
func (r *recSink) Count(outcome string)                { r.counts[outcome]++ }

// TestActivityStep_ItemsHeldAreCountedAndRecorded: a digest row with
// unresolvable items is written (or kept) like any other, counts one
// activity-items-held per item, is recorded once through Hold with its reasons,
// and is one row for Read however many items it holds.
func TestActivityStep_ItemsHeldAreCountedAndRecorded(t *testing.T) {
	runs := runsOf{"R1": "books.repairs.two-face-asins"}
	dm, err := NewActivity(ActivityConfig{Runs: func() RunReader { return runs }}, "activity-test", 1, []storage.StepRef{{}})
	if err != nil {
		t.Fatal(err)
	}
	step := func(key, items string) *recSink {
		sink := &recSink{holds: map[string]string{}, counts: map[string]int{}}
		row := `{"type":"daily_digest","details":{"items":` + items + `}}`
		err := dm.Step(context.Background(), storage.KV{Key: []byte(key), Value: []byte(row)}, datamigrate.NewStepWriter(sink, nil))
		if err != nil {
			t.Fatal(err)
		}
		return sink
	}
	const k1 = "act:digest:00000000000000000001:01HBATROW1"
	const k2 = "act:digest:00000000000000000002:01HBATROW2"
	const k3 = "act:digest:00000000000000000003:01HBATROW3"
	// Written: one item resolves, two cannot (no operation, absent run).
	w := step(k1, `[{"type":"repairs.plan","operation_id":"R1"},{"type":"repairs.plan"},{"type":"repairs.apply","operation_id":"GONE"}]`)
	if w.sets != 1 || w.counts[OutcomeActivityRewritten] != 1 || w.counts[OutcomeActivityItemsHeld] != 2 {
		t.Errorf("written row: sets %d, counts %v; want 1 write, rewritten 1, items-held 2", w.sets, w.counts)
	}
	if got := w.holds[k1]; got != "2 digest item(s) left as stored: "+reasonActivityItem {
		t.Errorf("hold reason = %q", got)
	}
	// Kept: its only item cannot resolve, so nothing changes.
	w = step(k2, `[{"type":"repairs.plan"}]`)
	if w.sets != 0 || w.counts[OutcomeActivityKept] != 1 || w.counts[OutcomeActivityItemsHeld] != 1 || w.holds[k2] == "" {
		t.Errorf("kept row: sets %d, counts %v, holds %v", w.sets, w.counts, w.holds)
	}
	// A row whose items all resolve is not held.
	w = step(k3, `[{"type":"repairs.plan","operation_id":"R1"}]`)
	if len(w.holds) != 0 || w.counts[OutcomeActivityItemsHeld] != 0 {
		t.Errorf("a fully resolved row was held: %v %v", w.holds, w.counts)
	}
	// Two rows held for items, one row clean: Read counts the three rows once
	// each, and Verify reports the two as held.
	rep, err := dm.Verify(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Read != 3 || rep.Written != 2 || rep.Held != 2 {
		t.Errorf("verify = %+v; want Read 3, Written 2, Held 2", rep)
	}
}
