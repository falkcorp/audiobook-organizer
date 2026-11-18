// file: internal/books/cutover/kindrename/gen/gen_test.go
// version: 1.0.0
// guid: 91c7e0a4-3b58-4d2f-a6e9-07d4b8f1c352
// last-edited: 2026-11-18

package gen_test

import (
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename/gen"
)

// base is the smallest input set that classifies: every T4 and flip ID
// registered, the scan, the repairs pair, one alias, one tombstone and the
// scheduler's full-sweep task.
func base() gen.Inputs {
	reg := []string{
		"maintenance.db-optimize", "maintenance.db-census-exact", "maintenance.purge-old-logs",
		"maintenance.throttle-reset", "diagnostics.export", "maintenance.window",
		"operations.backfill-legacy-status", "library.scan", "repairs.plan", "repairs.apply", "dedup.llm-review",
	}
	return gen.Inputs{
		Ledger:     append(append([]string{}, reg...), "maintenance.dedup-llm-review", "itunes.sync"),
		Registered: reg,
		Aliases:    map[string]string{"maintenance.dedup-llm-review": "dedup.llm-review"},
		Tombstones: []string{"itunes.sync"},
		TaskDefIDs: map[string]string{"library_scan": "library.scan", "library_scan_full": "library.scan"},
	}
}

func TestBuild_BaseClassifies(t *testing.T) {
	if _, err := gen.Build(base()); err != nil {
		t.Fatal(err)
	}
}

// TestBuild_Refuses: the generator stops on an input set the rules cannot
// classify, instead of guessing a kind for it.
func TestBuild_Refuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gen.Inputs)
		want   string
	}{
		{"a ledger ID nobody decided", func(in *gen.Inputs) { in.Ledger = append(in.Ledger, "gotham.unknown") }, "neither registered, an alias nor tombstoned"},
		{"an alias of an unregistered ID", func(in *gen.Inputs) { in.Aliases["old.id"] = "nowhere.id" }, "not registered"},
		{"an alias that shadows a registered ID", func(in *gen.Inputs) { in.Aliases["library.scan"] = "dedup.llm-review" }, "shadows"},
		{"a tombstone that is registered", func(in *gen.Inputs) { in.Tombstones = append(in.Tombstones, "library.scan") }, "tombstoned ID"},
		{"a T4 ID the registry lost", func(in *gen.Inputs) {
			in.Registered = in.Registered[1:]
			in.Ledger = in.Ledger[1:]
		}, "T4 table names"},
		{"a registered ID that fails the grammar", func(in *gen.Inputs) {
			in.Registered = append(in.Registered, "Bad.Id")
			in.Ledger = append(in.Ledger, "Bad.Id")
		}, "Bad.Id"},
		{"a scheduler task firing an unknown def", func(in *gen.Inputs) { in.TaskDefIDs["gotham_task"] = "gotham.none" }, "not a known def ID"},
		{"a scheduler task firing a retired def", func(in *gen.Inputs) { in.TaskDefIDs["old_task"] = "itunes.sync" }, "retired kind"},
		{"a scheduler task firing repairs", func(in *gen.Inputs) { in.TaskDefIDs["fixer_task"] = "repairs.plan" }, "fixer_id"},
		{"taskV2DefIDs naming batch_poller", func(in *gen.Inputs) { in.TaskDefIDs["batch_poller"] = "dedup.llm-review" }, "fires no def"},
		{"the full-sweep task gone", func(in *gen.Inputs) { delete(in.TaskDefIDs, "library_scan_full") }, "full-sweep exception"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			_, err := gen.Build(in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Build error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}
