// file: internal/books/cutover/kindrename/map_test.go
// version: 1.0.1
// guid: 2f9d5b30-71e8-4a6c-8c03-b4e1d7a95c28
// last-edited: 2026-11-19

package kindrename_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename/gen"
	"github.com/falkcorp/mediacore/tasks"
)

func root(t testing.TB) string {
	t.Helper()
	cwd, err := os.Getwd() // not runtime.Caller: CI builds with -trimpath
	if err != nil {
		t.Fatal(err)
	}
	r, err := gen.FindRoot(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestKindRenameMap_UpToDate: map_gen.go is what a fresh run of the generator
// writes (the brief's `go generate` then `git diff --exit-code`, as a test).
func TestKindRenameMap_UpToDate(t *testing.T) {
	r := root(t)
	want, err := gen.Generate(r)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(r, filepath.FromSlash(gen.OutputPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s is stale: run go generate ./internal/books/cutover/kindrename/ (and bump gen.HeaderVersion and gen.HeaderEdited when the map changed)", gen.OutputPath)
	}
}

// TestKindRenameMap_CoversEveryInput: every ledger ID, registered ID, alias
// and tombstone has an entry, so no HEAD ID falls through to rule 5 by
// accident. (F5-18's TestKindRenameMapCoversLedger adds the check against the
// registered kinds, which exist only on the flip build.)
func TestKindRenameMap_CoversEveryInput(t *testing.T) {
	in, err := gen.ReadInputs(root(t))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	ids = append(ids, in.Ledger...)
	ids = append(ids, in.Registered...)
	ids = append(ids, in.Tombstones...)
	for a := range in.Aliases {
		ids = append(ids, a)
	}
	if len(ids) < 200 {
		t.Fatalf("read only %d IDs: the inputs did not load", len(ids))
	}
	for _, id := range ids {
		tg, ok := kindrename.Lookup(id)
		if !ok {
			t.Errorf("%q has no entry", id)
			continue
		}
		if tg.Kind == "" && tg.Marker != kindrename.MarkerRepairsPlan && tg.Marker != kindrename.MarkerRepairsApply {
			t.Errorf("%q maps to no kind", id)
		}
		if tg.Kind != "" {
			if _, err := tasks.ParseKind(string(tg.Kind)); err != nil {
				t.Errorf("%q maps to %q, which fails the kind grammar: %v", id, tg.Kind, err)
			}
		}
	}
}

func params(t testing.TB, v any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestKindRenameMap_RulesTable: one case per rule 1-6, the library.scan
// run-level split, the scheduler-name rule with its library_scan_full
// exception, batch_poller and transcode.
func TestKindRenameMap_RulesTable(t *testing.T) {
	type c struct {
		name   string
		defID  string
		params json.RawMessage
		rule   kindrename.Rule
		want   tasks.Kind
		mode   tasks.Mode
		plan   tasks.RunID
		refuse bool
	}
	cases := []c{
		// Rule 1: the T4 table, every row.
		{name: "rule1 db-optimize", defID: "maintenance.db-optimize", rule: kindrename.RuleCore, want: "core.storage.compact", mode: tasks.ModeLive},
		{name: "rule1 db-census-exact", defID: "maintenance.db-census-exact", rule: kindrename.RuleCore, want: "core.storage.census", mode: tasks.ModeLive},
		{name: "rule1 purge-old-logs", defID: "maintenance.purge-old-logs", rule: kindrename.RuleCore, want: "core.log.journal.prune", mode: tasks.ModeLive},
		{name: "rule1 throttle-reset", defID: "maintenance.throttle-reset", rule: kindrename.RuleCore, want: "core.integrations.release-holds", mode: tasks.ModeLive},
		{name: "rule1 diagnostics.export", defID: "diagnostics.export", rule: kindrename.RuleCore, want: "core.diagnostics.export", mode: tasks.ModeLive},
		{name: "rule1 window", defID: "maintenance.window", rule: kindrename.RuleCore, want: "core.window.nightly", mode: tasks.ModeLive},
		// Rule 2: repairs, run-level.
		{name: "rule2 plan is preview", defID: "repairs.plan", params: params(t, map[string]any{"fixer_id": "junk-titles"}), rule: kindrename.RuleRepairs, want: "books.repairs.junk-titles", mode: tasks.ModePreview},
		{name: "rule2 apply is live with the plan ref", defID: "repairs.apply", params: params(t, map[string]any{"fixer_id": "junk-titles", "plan_op_id": "01PLANRUN", "row_ids": []string{"a"}}), rule: kindrename.RuleRepairs, want: "books.repairs.junk-titles", mode: tasks.ModeLive, plan: "01PLANRUN"},
		{name: "rule2 apply ignores dry_run", defID: "repairs.apply", params: params(t, map[string]any{"fixer_id": "junk-titles", "dry_run": true}), rule: kindrename.RuleRepairs, want: "books.repairs.junk-titles", mode: tasks.ModeLive},
		{name: "rule2 plan without fixer_id", defID: "repairs.plan", params: params(t, map[string]any{}), rule: kindrename.RuleRepairs, refuse: true},
		{name: "rule2 plan with undecodable params", defID: "repairs.plan", params: json.RawMessage(`{"fixer_id":`), rule: kindrename.RuleRepairs, refuse: true},
		{name: "rule2 fixer id fails the grammar", defID: "repairs.plan", params: params(t, map[string]any{"fixer_id": "Bad Id"}), rule: kindrename.RuleRepairs, refuse: true},
		// Rule 3: an alias goes where its canonical ID goes (a core kind, a books kind).
		{name: "rule3 alias of a core kind", defID: "scheduler.db-optimize", rule: kindrename.RuleAlias, want: "core.storage.compact", mode: tasks.ModeLive},
		{name: "rule3 alias of a books kind", defID: "maintenance.dedup-llm-review", rule: kindrename.RuleAlias, want: "books.dedup.llm-review", mode: tasks.ModeLive},
		// Rule 4: every other registered ID; the wrapped maintenance IDs included.
		{name: "rule4 plain", defID: "acoustid.backfill", rule: kindrename.RuleBooks, want: "books.acoustid.backfill", mode: tasks.ModeLive},
		{name: "rule4 wrapped maintenance job", defID: "maintenance.external-id-backfill", rule: kindrename.RuleBooks, want: "books.maintenance.external-id-backfill", mode: tasks.ModeLive},
		{name: "rule4 retention-and-hygiene stays books", defID: "maintenance.retention-and-hygiene", rule: kindrename.RuleBooks, want: "books.maintenance.retention-and-hygiene", mode: tasks.ModeLive},
		{name: "rule4 dry run is preview", defID: "acoustid.backfill", params: params(t, map[string]any{"dry_run": true}), rule: kindrename.RuleBooks, want: "books.acoustid.backfill", mode: tasks.ModePreview},
		{name: "rule4 camelCase dry run is preview", defID: "acoustid.backfill", params: params(t, map[string]any{"dryRun": true}), rule: kindrename.RuleBooks, want: "books.acoustid.backfill", mode: tasks.ModePreview},
		{name: "rule4 dry_run false is live", defID: "acoustid.backfill", params: params(t, map[string]any{"dry_run": false}), rule: kindrename.RuleBooks, want: "books.acoustid.backfill", mode: tasks.ModeLive},
		// The library.scan run-level split.
		{name: "scan full sweep params", defID: "library.scan", params: params(t, map[string]any{"force_update": true, "include_root_dir": true}), rule: kindrename.RuleBooks, want: "books.library.scan-full", mode: tasks.ModeLive},
		{name: "scan full sweep params with a folder", defID: "library.scan", params: params(t, map[string]any{"force_update": true, "include_root_dir": true, "folder_path": "/x"}), rule: kindrename.RuleBooks, want: "books.library.scan-full", mode: tasks.ModeLive},
		{name: "scan force only", defID: "library.scan", params: params(t, map[string]any{"force_update": true}), rule: kindrename.RuleBooks, want: "books.library.scan", mode: tasks.ModeLive},
		{name: "scan root dir only", defID: "library.scan", params: params(t, map[string]any{"include_root_dir": true, "force_update": false}), rule: kindrename.RuleBooks, want: "books.library.scan", mode: tasks.ModeLive},
		{name: "scan no params", defID: "library.scan", rule: kindrename.RuleBooks, want: "books.library.scan", mode: tasks.ModeLive},
		{name: "scan undecodable params", defID: "library.scan", params: json.RawMessage(`not json`), rule: kindrename.RuleBooks, want: "books.library.scan", mode: tasks.ModeLive},
		// Rule 5: tombstoned, deleted by the flip, and any other well-formed ID.
		{name: "rule5 tombstone", defID: "maintenance.batch-poller", rule: kindrename.RuleRetired, want: "books.retired.maintenance.batch-poller", mode: tasks.ModeLive},
		{name: "rule5 deleted by the flip", defID: "operations.backfill-legacy-status", rule: kindrename.RuleRetired, want: "books.retired.operations.backfill-legacy-status", mode: tasks.ModeLive},
		{name: "rule5 unknown well-formed ID", defID: "gotham.signal-test", rule: kindrename.RuleRetired, want: "books.retired.gotham.signal-test", mode: tasks.ModeLive},
		// Rule 6: fails the grammar.
		{name: "rule6 upper case", defID: "Maintenance.Job", rule: kindrename.RuleInvalid, refuse: true},
		{name: "rule6 colon", defID: "maint:job", rule: kindrename.RuleInvalid, refuse: true},
		{name: "rule6 empty", defID: "", rule: kindrename.RuleInvalid, refuse: true},
		{name: "rule6 empty segment", defID: "a..b", rule: kindrename.RuleInvalid, refuse: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tg, err := kindrename.Resolve(tc.defID)
			if tc.rule == kindrename.RuleInvalid {
				if err == nil || !errors.Is(err, kindrename.ErrInvalidKind) {
					t.Fatalf("Resolve(%q) = %+v, %v; want an ErrInvalidKind refusal", tc.defID, tg, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.defID, err)
			}
			if tg.Rule != tc.rule {
				t.Fatalf("Resolve(%q).Rule = %v, want %v", tc.defID, tg.Rule, tc.rule)
			}
			rt, err := tg.ForRun(tc.params)
			if tc.refuse {
				if err == nil || !errors.Is(err, kindrename.ErrInvalidKind) {
					t.Fatalf("ForRun = %+v, %v; want an ErrInvalidKind refusal", rt, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ForRun: %v", err)
			}
			if rt.Kind != tc.want || rt.Mode != tc.mode || rt.PlanRef != tc.plan {
				t.Fatalf("ForRun = {%s %v %q}, want {%s %v %q}", rt.Kind, rt.Mode, rt.PlanRef, tc.want, tc.mode, tc.plan)
			}
		})
	}
}

// TestKindRenameMap_SchedulerNames: the scheduler-name rule (taskV2DefIDs,
// then rule 1 or 4) with its three exceptions.
func TestKindRenameMap_SchedulerNames(t *testing.T) {
	for _, tc := range []struct {
		task    string
		want    tasks.Kind
		dropped bool
	}{
		{task: "library_scan", want: "books.library.scan"},
		{task: "library_scan_full", want: "books.library.scan-full"}, // taskV2DefIDs says library.scan
		{task: "db_optimize", want: "core.storage.compact"},          // rule 1
		{task: "purge_old_logs", want: "core.log.journal.prune"},     // rule 1
		{task: "temp_file_cleanup", want: "books.maintenance.temp-file-cleanup"},
		{task: "dedup_refresh", want: "books.dedup.author-scan"},
		{task: "batch_poller", want: "core.ai.batch-poll"},
		{task: "transcode", dropped: true},
	} {
		got, ok := kindrename.SchedulerKind(tc.task)
		if !ok {
			t.Errorf("SchedulerKind(%q): unknown", tc.task)
			continue
		}
		if got.Kind != tc.want || got.Dropped != tc.dropped {
			t.Errorf("SchedulerKind(%q) = %+v, want kind %q dropped %v", tc.task, got, tc.want, tc.dropped)
		}
	}
	if _, ok := kindrename.SchedulerKind("gotham_task"); ok {
		t.Error("SchedulerKind of a name the scheduler never had reported ok")
	}
}

// TestKindRenameMap_RuleTallies pins the map's size and its per-rule tally,
// the figures the PR description reports, so a change to the inputs that moves
// them is reviewed on purpose.
func TestKindRenameMap_RuleTallies(t *testing.T) {
	in, err := gen.ReadInputs(root(t))
	if err != nil {
		t.Fatal(err)
	}
	tb, err := gen.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	tally := map[kindrename.Rule]int{}
	for _, tg := range tb.Defs {
		tally[tg.Rule]++
	}
	want := map[kindrename.Rule]int{
		kindrename.RuleCore: 6, kindrename.RuleRepairs: 2, kindrename.RuleAlias: 19, kindrename.RuleBooks: 226, kindrename.RuleRetired: 17,
	}
	for r, n := range want {
		if tally[r] != n {
			t.Errorf("rule %v: %d entries, want %d", r, tally[r], n)
		}
	}
	if len(tb.Defs) != 270 {
		t.Errorf("%d entries, want 270 (the ledger's lines)", len(tb.Defs))
	}
}
