// file: core/kernel/cutover_blockers_test.go
// version: 1.0.0
// guid: 0f9a6d13-2c78-4b95-a3e0-61d4b8c7e259
// last-edited: 2026-11-12

package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	cpebble "github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// F4-06's `storage cutover-blockers` over a fake cutover step. The step's
// Preflight reports a case file whose value holds "Joker venom" (Fix: fix or
// delete), one whose value holds "Scarecrow toxin" (Fix: delete only), a
// "case:merge/" row (a merge in progress: finish or undo it on the
// pre-freeze release) and a "case:idx/<n>" row whose file is gone (an
// orphan). Its FixBlocker sets the row; its DeleteBlocker deletes the file
// and its idx row and returns the id. Every value is a Batman fixture.

const blockerStepID = "gcpd.case-files"

// blockerFixtureStep is cutoverMigration with the preflight, fix and delete
// hooks. leaveIdx makes DeleteBlocker forget the idx row, a defect the
// recheck must catch.
func blockerFixtureStep(leaveIdx bool) datamigrate.DataMigration {
	m := cutoverMigration(&cutoverHandles{})
	m.Preflight = func(ctx context.Context, r storage.Reader) ([]datamigrate.Blocker, error) {
		var out []datamigrate.Blocker
		files := map[string]bool{}
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte("case:file/"), End: []byte("case:file0")}) {
			if err != nil {
				return nil, err
			}
			files[strings.TrimPrefix(string(kv.Key), "case:file/")] = true
			switch {
			case bytes.Contains(kv.Value, []byte("Joker venom")):
				out = append(out, datamigrate.Blocker{Key: bytes.Clone(kv.Key), Family: "gcpd.file", Reason: "the case file holds Joker venom",
					Fix: "repair it or remove it: storage cutover-blockers fix|delete"})
			case bytes.Contains(kv.Value, []byte("Scarecrow toxin")):
				out = append(out, datamigrate.Blocker{Key: bytes.Clone(kv.Key), Family: "gcpd.file", Reason: "the case file holds Scarecrow toxin",
					Fix: "remove it: storage cutover-blockers delete"})
			}
		}
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte("case:merge/"), End: []byte("case:merge0")}) {
			if err != nil {
				return nil, err
			}
			out = append(out, datamigrate.Blocker{Key: bytes.Clone(kv.Key), Family: "gcpd.merge", Reason: "merge in progress",
				Fix: "finish or undo the merge on the pre-freeze release"})
		}
		for kv, err := range r.Scan(ctx, storage.ScanOptions{Start: []byte("case:idx/"), End: []byte("case:idx0")}) {
			if err != nil {
				return nil, err
			}
			if !files[strings.TrimPrefix(string(kv.Key), "case:idx/")] {
				out = append(out, datamigrate.Blocker{Key: bytes.Clone(kv.Key), Family: "gcpd.idx", Reason: "the index row has no case file",
					Fix: "storage cutover-blockers delete"})
			}
		}
		slices.SortFunc(out, func(a, b datamigrate.Blocker) int { return bytes.Compare(a.Key, b.Key) })
		return out, nil
	}
	m.FixBlocker = func(_ context.Context, b *storage.Batch, key, value []byte) error { return b.Set(key, value) }
	m.DeleteBlocker = func(_ context.Context, b *storage.Batch, key []byte) ([]string, error) {
		if err := b.Delete(key); err != nil {
			return nil, err
		}
		id := strings.TrimPrefix(string(key), "case:file/")
		if !leaveIdx && strings.HasPrefix(string(key), "case:file/") {
			if err := b.Delete([]byte("case:idx/" + id)); err != nil {
				return nil, err
			}
		}
		return []string{"case-" + id}, nil
	}
	return m
}

// blockerRows: file 001 is clean; 002 blocks (fix or delete) and has an idx
// row; 003 blocks (delete only); a merge is in progress; idx/009 is orphaned.
func blockerRows() map[string]string {
	return map[string]string{
		"case:file/001":  "The Long Halloween, part 1",
		"case:file/002":  "Joker venom: The Killing Joke",
		"case:file/003":  "Scarecrow toxin: Fear Itself",
		"case:file/004":  "Year One",
		"case:idx/002":   "bruce.wayne",
		"case:idx/004":   "alfred.pennyworth",
		"case:idx/009":   "selina.kyle",
		"case:merge/001": "applying",
	}
}

// blockersHost seeds rows under the pre-cutover build and returns the
// cutover build's Config (claim retired, the fixture step in its manifest).
func blockersHost(t *testing.T, rows map[string]string, leaveIdx bool) (*swapHost, Config) {
	t.Helper()
	h, _ := dryRunHost(t, rows)
	c := testConfig(t, gcpdMod(storage.ClaimRetired).with(func(m *module.Manifest) {
		m.DataMigrations = []datamigrate.DataMigration{blockerFixtureStep(leaveIdx)}
	}))
	c.StoragePath, c.StateDir = h.db, h.state
	return h, c
}

// dumpDir is every key and value of the (closed) store at path.
func dumpDir(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := cpebble.Open(path, &cpebble.Options{ReadOnly: true, Logger: quietPebble{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	it, err := db.NewIter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = it.Close() }()
	out := map[string]string{}
	for ok := it.First(); ok; ok = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	return out
}

func openBlockers(t *testing.T, h *swapHost, c Config, readOnly bool) *CutoverBlockers {
	t.Helper()
	cb, err := OpenCutoverBlockers(h.ctx, c, readOnly, CutoverBlockersOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cb.Close() })
	return cb
}

func blockerKeys(bs []storage.CutoverBlocker) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = string(b.Key)
	}
	return out
}

// diffKeys is the keys whose value differs between two dumps.
func diffKeys(before, after map[string]string) []string {
	var out []string
	for k, v := range before {
		if got, ok := after[k]; !ok || got != v {
			out = append(out, k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

// TestCutoverBlockers_List: zero, one and several blockers; the report is
// the step's, in key order, with the step ID filled in; the raw rows come out
// of Rows, and only there. The store opens although its cutover step is
// pending, which the ordinary offline open refuses.
func TestCutoverBlockers_List(t *testing.T) {
	clean := map[string]string{"case:file/001": "The Long Halloween, part 1", "case:idx/001": "bruce.wayne"}
	one := map[string]string{"case:file/001": "The Long Halloween, part 1", "case:file/002": "Joker venom: The Killing Joke"}

	for name, tc := range map[string]struct {
		rows map[string]string
		want []string
	}{
		"none":    {clean, nil},
		"one":     {one, []string{"case:file/002"}},
		"several": {blockerRows(), []string{"case:file/002", "case:file/003", "case:idx/009", "case:merge/001"}},
	} {
		t.Run(name, func(t *testing.T) {
			h, c := blockersHost(t, tc.rows, false)
			cb := openBlockers(t, h, c, true)
			got, err := cb.List(h.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(blockerKeys(got), tc.want) {
				t.Fatalf("List = %v, want %v", blockerKeys(got), tc.want)
			}
			for _, b := range got {
				if b.Step != blockerStepID {
					t.Errorf("blocker %s has step %q, want the migration's ID", b.Key, b.Step)
				}
			}
			rows, err := cb.Rows(h.ctx, got)
			if err != nil || len(rows) != len(got) {
				t.Fatalf("Rows = %d rows, %v", len(rows), err)
			}
			for i, r := range rows {
				if !bytes.Equal(r.Key, got[i].Key) || string(r.Value) != tc.rows[string(r.Key)] {
					t.Errorf("row %d = %q=%q, want the stored value", i, r.Key, r.Value)
				}
			}
		})
	}

	h, c := blockersHost(t, one, false)
	if _, err := OpenOffline(h.ctx, c, true); !errors.Is(err, storage.ErrOfflinePendingSteps) {
		t.Fatalf("the ordinary offline open of a store with a pending cutover step = %v, want ErrOfflinePendingSteps", err)
	}
	cb := openBlockers(t, h, c, true)
	if _, err := cb.Fix(h.ctx, []byte("case:file/002"), []byte("Year One"), "alfred"); err == nil || !strings.Contains(err.Error(), "read-write") {
		t.Errorf("Fix on a read-only open = %v, want a refusal", err)
	}
	if _, err := cb.Delete(h.ctx, []byte("case:file/002"), "alfred"); err == nil || !strings.Contains(err.Error(), "read-write") {
		t.Errorf("Delete on a read-only open = %v, want a refusal", err)
	}
}

// TestCutoverBlockers_Fix: a repaired value replaces the row through the
// step's FixBlocker; the batch holds the row, the record at
// c/storage/cutover/blockers/<step>/1 and the audit event, and nothing else
// of the store changes; the preflight then no longer names the row; the
// value is in neither the record nor the audit log.
func TestCutoverBlockers_Fix(t *testing.T) {
	h, c := blockersHost(t, blockerRows(), false)
	before := dumpDir(t, h.db)
	cb := openBlockers(t, h, c, false)
	res, err := cb.Fix(h.ctx, []byte("case:file/002"), []byte("The Killing Joke, repaired"), "alfred.pennyworth")
	if err != nil || res.N != 1 || res.Step != blockerStepID {
		t.Fatalf("Fix = %+v, %v", res, err)
	}
	left, err := cb.List(h.ctx)
	if err != nil || !slices.Equal(blockerKeys(left), []string{"case:file/003", "case:idx/009", "case:merge/001"}) {
		t.Fatalf("after the fix the report is %v, %v", blockerKeys(left), err)
	}
	if err := cb.Close(); err != nil {
		t.Fatal(err)
	}
	after := dumpDir(t, h.db)

	if got := after["case:file/002"]; got != "The Killing Joke, repaired" {
		t.Errorf("the row is %q after the fix", got)
	}
	rec := "c/storage/" + string(storage.CutoverBlockerKey(blockerStepID, 1))
	var r storage.CutoverBlockerRecord
	if err := json.Unmarshal([]byte(after[rec]), &r); err != nil {
		t.Fatalf("the record at %q: %v", rec, err)
	}
	if r.Key != `"case:file/002"` || r.Action != "fix" || r.Build != swapBuild || r.At == "" || r.IDs == nil || len(r.IDs) != 0 || !strings.Contains(r.Reason, "Joker venom") {
		t.Errorf("record = %+v", r)
	}
	if strings.Contains(after[rec], "repaired") || strings.Contains(after[rec], "Killing Joke") {
		t.Error("the record carries the row's value")
	}
	// Nothing else changed: the row, the record, and the audit keyspace.
	for _, k := range diffKeys(before, after) {
		if k != "case:file/002" && k != rec && !strings.HasPrefix(k, "c/audit/") {
			t.Errorf("the fix changed %q", k)
		}
	}
	var audited int
	for k, v := range after {
		if strings.HasPrefix(k, "c/audit/") && strings.Contains(v, "core.storage.cutover_blocker_fix") {
			audited++
			if strings.Contains(v, "repaired") || strings.Contains(v, "Killing Joke") {
				t.Error("the audit record carries the row's value")
			}
			if !strings.Contains(v, "alfred.pennyworth") {
				t.Error("the audit record does not name the operator")
			}
		}
	}
	if audited != 1 {
		t.Errorf("%d audit records of the fix, want 1", audited)
	}
	cb2 := openBlockers(t, h, c, true)
	as, err := buildAudit(h.ctx, cb2.db, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var events int
	for rec, err := range as.Query(h.ctx, audit.Filter{ActionPrefix: "core.storage.cutover_blocker_"}) {
		if err != nil {
			t.Fatal(err)
		}
		if rec.Action == "core.storage.cutover_blocker_fix" && rec.Actor.Username == "alfred.pennyworth" && rec.Details["step"] == blockerStepID {
			events++
		}
	}
	if events != 1 {
		t.Errorf("fix audit events = %d, want 1", events)
	}
}

// TestCutoverBlockers_FixRefused: a repaired value that still blocks, and a
// fix of a row that offers delete only, are refused with the
// store byte-identical (a full dump before and after, the audit sequence's
// lease row included).
func TestCutoverBlockers_FixRefused(t *testing.T) {
	h, c := blockersHost(t, blockerRows(), false)
	before := dumpDir(t, h.db)
	cb := openBlockers(t, h, c, false)
	_, err := cb.Fix(h.ctx, []byte("case:file/002"), []byte("Joker venom, again"), "alfred")
	if err == nil || !strings.Contains(err.Error(), "still blocks") {
		t.Fatalf("a fix that still blocks = %v, want a refusal", err)
	}
	if strings.Contains(err.Error(), "again") {
		t.Error("the refusal echoes the value")
	}
	// An orphan idx row offers delete only: no value repairs it.
	if _, err := cb.Fix(h.ctx, []byte("case:idx/009"), []byte("selina.kyle"), "alfred"); err == nil || !strings.Contains(err.Error(), "does not offer fix") {
		t.Errorf("a fix of an orphan = %v, want a refusal", err)
	}
	// A repair that swaps one blocking value for another still blocks.
	if _, err := cb.Fix(h.ctx, []byte("case:file/002"), []byte("Scarecrow toxin"), "alfred"); err == nil || !strings.Contains(err.Error(), "still blocks") {
		t.Errorf("a fix to another blocking value = %v, want a refusal", err)
	}
	if err := cb.Close(); err != nil {
		t.Fatal(err)
	}
	if d := diffKeys(before, dumpDir(t, h.db)); len(d) != 0 {
		t.Errorf("a refused fix changed the store at %q", d)
	}
}

// TestCutoverBlockers_Delete: the row and the rows the step says go with it
// are deleted through DeleteBlocker; the result carries its ids; the record
// holds them; the audit event is the delete's.
func TestCutoverBlockers_Delete(t *testing.T) {
	h, c := blockersHost(t, blockerRows(), false)
	before := dumpDir(t, h.db)
	cb := openBlockers(t, h, c, false)
	res, err := cb.Delete(h.ctx, []byte("case:file/002"), "alfred")
	if err != nil || res.N != 1 || !slices.Equal(res.IDs, []string{"case-002"}) {
		t.Fatalf("Delete = %+v, %v", res, err)
	}
	// A second edit takes the next record number, and the delete-only row
	// is deletable here.
	if res, err := cb.Delete(h.ctx, []byte("case:file/003"), "alfred"); err != nil || res.N != 2 {
		t.Fatalf("second Delete = %+v, %v", res, err)
	}
	if err := cb.Close(); err != nil {
		t.Fatal(err)
	}
	after := dumpDir(t, h.db)
	for _, k := range []string{"case:file/002", "case:idx/002", "case:file/003"} {
		if _, ok := after[k]; ok {
			t.Errorf("%s survived the delete", k)
		}
	}
	var r storage.CutoverBlockerRecord
	if err := json.Unmarshal([]byte(after["c/storage/"+string(storage.CutoverBlockerKey(blockerStepID, 1))]), &r); err != nil {
		t.Fatal(err)
	}
	if r.Action != "delete" || !slices.Equal(r.IDs, []string{"case-002"}) || r.Key != `"case:file/002"` {
		t.Errorf("record 1 = %+v", r)
	}
	for k := range after {
		if strings.HasPrefix(k, "c/audit/") && strings.Contains(after[k], "cutover_blocker_fix") {
			t.Error("a delete audited as a fix")
		}
	}
	for _, k := range diffKeys(before, after) {
		if !slices.Contains([]string{"case:file/002", "case:idx/002", "case:file/003"}, k) &&
			!strings.HasPrefix(k, "c/audit/") && !strings.HasPrefix(k, "c/storage/cutover") {
			t.Errorf("the deletes changed %q", k)
		}
	}
}

// TestCutoverBlockers_DeleteRecheckCatchesAnIncompleteDelete: a DeleteBlocker
// that leaves a row behind that then blocks (an idx row with no file) is
// refused, nothing written.
func TestCutoverBlockers_DeleteRecheckCatchesAnIncompleteDelete(t *testing.T) {
	h, c := blockersHost(t, blockerRows(), true)
	before := dumpDir(t, h.db)
	cb := openBlockers(t, h, c, false)
	_, err := cb.Delete(h.ctx, []byte("case:file/002"), "alfred")
	if err == nil || !strings.Contains(err.Error(), "would make the row") {
		t.Fatalf("a delete that orphans an index row = %v, want a refusal naming the new blocker", err)
	}
	if err := cb.Close(); err != nil {
		t.Fatal(err)
	}
	if d := diffKeys(before, dumpDir(t, h.db)); len(d) != 0 {
		t.Errorf("a refused delete changed the store at %q", d)
	}
}

// TestCutoverBlockers_RefusesWhatTheReportDoesNotOffer: fix and delete refuse
// a key the current report does not name (a healthy row, a key that does not
// exist, a key of another family), a merge in progress (its Fix text does not
// offer the command) and an action the Fix text does not offer; nothing is
// written.
func TestCutoverBlockers_RefusesWhatTheReportDoesNotOffer(t *testing.T) {
	h, c := blockersHost(t, blockerRows(), false)
	before := dumpDir(t, h.db)
	cb := openBlockers(t, h, c, false)
	for _, key := range []string{"case:file/001", "case:file/999", "case:idx/002", "case:file/00", "case:file/0022"} {
		if _, err := cb.Fix(h.ctx, []byte(key), []byte("Year One"), "alfred"); err == nil || !strings.Contains(err.Error(), "not named by the current blocker report") {
			t.Errorf("Fix of %s = %v, want a refusal", key, err)
		}
		if _, err := cb.Delete(h.ctx, []byte(key), "alfred"); err == nil || !strings.Contains(err.Error(), "not named by the current blocker report") {
			t.Errorf("Delete of %s = %v, want a refusal", key, err)
		}
	}
	merge := []byte("case:merge/001")
	if _, err := cb.Fix(h.ctx, merge, []byte("done"), "alfred"); err == nil || !strings.Contains(err.Error(), "pre-freeze release") {
		t.Errorf("Fix of a merge in progress = %v, want a refusal naming the way out", err)
	}
	if _, err := cb.Delete(h.ctx, merge, "alfred"); err == nil || !strings.Contains(err.Error(), "pre-freeze release") {
		t.Errorf("Delete of a merge in progress = %v, want a refusal naming the way out", err)
	}
	if _, err := cb.Fix(h.ctx, []byte("case:file/003"), []byte("Year One"), "alfred"); err == nil || !strings.Contains(err.Error(), "does not offer fix") {
		t.Errorf("Fix of a delete-only row = %v, want a refusal", err)
	}
	if err := cb.Close(); err != nil {
		t.Fatal(err)
	}
	if d := diffKeys(before, dumpDir(t, h.db)); len(d) != 0 {
		t.Errorf("a refused edit changed the store at %q", d)
	}
}

// TestBlockerOffers: the Fix text names the command and the actions it
// offers.
func TestBlockerOffers(t *testing.T) {
	for _, tc := range []struct {
		fix, action string
		want        bool
	}{
		{"storage cutover-blockers fix|delete", "fix", true},
		{"storage cutover-blockers fix|delete", "delete", true},
		{"storage cutover-blockers delete", "delete", true},
		{"storage cutover-blockers delete", "fix", false},
		{"run `storage cutover-blockers fix`, then upgrade", "fix", true},
		{"repair it, or storage cutover-blockers delete it", "delete", true},
		{"finish or undo the merge on the pre-freeze release", "fix", false},
		{"finish or undo the merge, then run storage cutover-blockers list", "fix", false},
		{"", "delete", false},
		{"storage cutover-blockers", "fix", false},
	} {
		if got := blockerOffers(tc.fix, tc.action); got != tc.want {
			t.Errorf("blockerOffers(%q, %q) = %t, want %t", tc.fix, tc.action, got, tc.want)
		}
	}
}

// TestCutoverBlockers_RefusesWhileTheStoreIsHeld: a process that holds the
// store's lock refuses the open, as every offline storage command does.
func TestCutoverBlockers_RefusesWhileTheStoreIsHeld(t *testing.T) {
	h, c := blockersHost(t, blockerRows(), false)
	held, err := cpebble.Open(h.db, &cpebble.Options{Logger: quietPebble{}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	for _, ro := range []bool{true, false} {
		if cb, err := OpenCutoverBlockers(h.ctx, c, ro, CutoverBlockersOptions{}); err == nil {
			_ = cb.Close()
			t.Errorf("OpenCutoverBlockers(readOnly=%t) opened a store another process holds", ro)
		}
	}
}

// TestCutoverBlockers_RefusesAStartedCutover: a store whose cutover step has
// its c/storage/dm/ record is refused naming make rollback-restore, for list
// as for fix and delete. (The level-at-target cell is storage's own test.)
func TestCutoverBlockers_RefusesAStartedCutover(t *testing.T) {
	t.Run("dm record", func(t *testing.T) {
		rows := cutoverRows(10, 2)
		h, c := blockersHost(t, rows, false)
		liveCutover(t, h.ctx, c)
		for _, ro := range []bool{true, false} {
			cb, err := OpenCutoverBlockers(h.ctx, c, ro, CutoverBlockersOptions{})
			if err == nil {
				_ = cb.Close()
				t.Fatalf("readOnly=%t: opened a store whose cutover started", ro)
			}
			if !errors.Is(err, storage.ErrCutoverStarted) || !strings.Contains(err.Error(), "make rollback-restore") {
				t.Errorf("readOnly=%t: %v, want ErrCutoverStarted naming make rollback-restore", ro, err)
			}
		}
	})
}
