// file: internal/books/journal_prune_parity_test.go
// version: 0.1.0
// guid: b94aa726-dc07-42ec-b8f6-3b354843e730
// last-edited: 2026-11-21

package books

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/kindrename"
	hconfig "github.com/falkcorp/audiobook-organizer/internal/config"
	mlog "github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/tasks"
)

var updateJournalPrune = flag.Bool("update-journal-prune", false,
	"rewrite core/kernel/testdata/journal_prune_parity.json from HEAD's scheduler")

// journalPruneGoldenPath is the parity golden of HEAD's log purge (task
// purge_old_logs), whose kind after the cutover is core's
// core.log.journal.prune. Books cannot import core's logjournal (Go's internal
// rule) and core cannot run HEAD's scheduler, so the parity check is split
// across one fixture, the way rename_core_targets.golden splits the rename
// table's core contract: this test writes what HEAD's own scheduler and the
// real M2 and M4 produce, and core/kernel's TestJournalPruneParity replays it
// through the scheduler harness with the real PruneDefinition and the kernel's
// default. Core holds no app name (03 R1), so the fixture carries core paths,
// kinds and instants only.
const journalPruneGoldenPath = "../../core/kernel/testdata/journal_prune_parity.json"

type journalPruneGolden struct {
	Header map[string]string `json:"_header"`
	// Kind is the kind HEAD's task becomes (kindrename).
	Kind tasks.Kind `json:"kind"`
	// Every is HEAD's fixed interval (TaskDefinition.GetInterval).
	Every string `json:"every"`
	// Window and Order are HEAD's maintenance window and the task's position
	// in its order (cutover.HeadMaintenanceWindow, HeadMaintenanceOrder).
	Window string `json:"window"`
	Order  int    `json:"order"`
	// DefaultPruneInWindow is HEAD's shipped maintenance.purge_old_logs.
	DefaultPruneInWindow bool `json:"default_prune_in_window"`
	// ZoneOffset is the scheduler's zone, seconds east of UTC.
	ZoneOffset int               `json:"zone_offset"`
	Rows       []journalPruneRow `json:"rows"`
}

type journalPruneRow struct {
	Name string `json:"name"`
	// Clock is what M4 imported for Kind from HEAD's settings rows.
	Clock time.Time          `json:"clock"`
	Steps []journalPruneStep `json:"steps"`
}

type journalPruneStep struct {
	At   time.Time `json:"at"`
	Boot bool      `json:"boot,omitempty"` // a restart before the step (every step 0, and each edit)
	// The core values M2 converts the step's HEAD config into.
	RetentionDays int  `json:"retention_days"`  // core.log.journal.retention_days
	PruneInWindow bool `json:"prune_in_window"` // core.log.journal.prune_in_window
	WindowStart   int  `json:"window_start"`    // core.tasks.windows.nightly.start
	WindowEnd     int  `json:"window_end"`      // core.tasks.windows.nightly.end
	WindowEnabled bool `json:"window_enabled"`  // core.tasks.windows.nightly.enabled
	// Head is what HEAD's scheduler did for the task at the step, sorted:
	// "enqueue <trigger>" for a TriggerFn run and "window" for a maintenance
	// window opening whose members include it.
	Head []string `json:"head"`
}

// m2Converted is the core value M2 writes at target for HEAD path head of c
// (cutover.Convert over the rename-table row's target), decoded into out.
// It refuses a log_retention_days of 0: M2's effective-tree step drops a
// stored 0 (HEAD's own load turns it into the default, log_retention.go), so
// Convert alone would report a 0 that M2 never writes. A config HEAD loaded
// never holds one; a row sets the resolved value instead.
func m2Converted(t *testing.T, c hconfig.Config, head, target string, out any) {
	t.Helper()
	if head == "log_retention_days" && c.LogRetentionDays == 0 {
		t.Fatalf("log_retention_days 0 is no loaded HEAD value (HEAD resolves it to %d); set that instead", mlog.DefaultLogRetentionDays)
	}
	for _, r := range cutover.Table().Rows {
		if r.Head != head {
			continue
		}
		for _, tg := range r.Targets {
			if tg.Kind != cutover.TargetKey || tg.Path != target {
				continue
			}
			hv, ok := treeAt(headTree(t, c), head)
			if !ok {
				t.Fatalf("%s is not a HEAD config path", head)
			}
			in, err := json.Marshal(hv)
			if err != nil {
				t.Fatal(err)
			}
			v, _, err := cutover.Convert(tg, in)
			if err != nil {
				t.Fatalf("convert %s -> %s: %v", head, target, err)
			}
			if err := json.Unmarshal(v, out); err != nil {
				t.Fatalf("%s -> %s: %s: %v", head, target, v, err)
			}
			return
		}
	}
	t.Fatalf("no rename-table row %s with key target %s", head, target)
}

// TestJournalPruneParity_HeadGolden: HEAD's purge_old_logs on the parity rig
// (a weekly timer whose clock M4 imports, and a nightly window run gated by
// maintenance.purge_old_logs; both gated by log_retention_days resolving
// above 0) across six rows, rendered into the fixture core replays. With
// -update-journal-prune it rewrites the fixture; otherwise the fixture must
// equal what HEAD produces now.
func TestJournalPruneParity_HeadGolden(t *testing.T) {
	const task = "purge_old_logs"
	target, ok := kindrename.SchedulerKind(task)
	if !ok || target.Dropped {
		t.Fatalf("%s has no kind after the cutover", task)
	}
	order, ok := cutover.HeadMaintenanceOrder(task)
	if !ok {
		t.Fatalf("%s is not in HEAD's maintenance order", task)
	}
	shipped := loadOracleGolden(t, "defaults").Config
	_, offset := parityStart.Zone()
	g := journalPruneGolden{
		Header: map[string]string{
			"file":        "core/kernel/testdata/journal_prune_parity.json",
			"version":     "1.0.0",
			"guid":        "aa45f737-95e4-4577-a4dc-3033e53b5730",
			"last-edited": "2026-11-21",
			"generated":   "go test ./internal/books -run TestJournalPruneParity_HeadGolden -update-journal-prune",
		},
		Kind: target.Kind, Window: cutover.HeadMaintenanceWindow, Order: order,
		DefaultPruneInWindow: shipped.Maintenance.PurgeOldLogs, ZoneOffset: offset,
	}

	// The timer's clock: 160 hours before the first step, so it falls due at
	// 20:00 on the first evening, between the two nightly windows.
	clock := parityStart.Add(-160 * time.Hour)
	at := func(i int) time.Time { return parityStart.Add(time.Duration(i) * time.Hour) }
	const n = 40 // 2030-03-04 12:00 to 2030-03-06 03:00: two windows (01:00-05:00)
	type edit struct {
		step int
		set  map[string]any
	}
	rows := []struct {
		name string
		set  map[string]any
		edit *edit
	}{
		{name: "defaults"},
		{name: "window toggle off", set: map[string]any{"maintenance.purge_old_logs": false}},
		{name: "retention off", set: map[string]any{"log_retention_days": mlog.LogRetentionKeepForever}},
		{name: "window off", set: map[string]any{"maintenance.enabled": false}},
		// Closed past the timer's due instant, then open: one catch-up run at
		// the reopening, then the next a full interval later (not in range).
		{name: "retention off then on at 22:00", set: map[string]any{"log_retention_days": mlog.LogRetentionKeepForever},
			edit: &edit{step: 10, set: map[string]any{"log_retention_days": mlog.DefaultLogRetentionDays}}},
		// Off through the first window, on before the second.
		{name: "window toggle off then on at noon", set: map[string]any{"maintenance.purge_old_logs": false},
			edit: &edit{step: 24, set: map[string]any{"maintenance.purge_old_logs": true}}},
	}

	saved := hconfig.Snapshot()
	t.Cleanup(func() { setHeadConfig(t, saved) })
	for _, rw := range rows {
		head := parityBase(t)
		if rw.set != nil {
			head = withHead(t, head, rw.set)
		}
		settings := map[string]string{"scheduled_task_last_tick." + task: clock.UTC().Format(time.RFC3339)}
		imported := m4Import(t, settings, parityZone)
		c, ok := imported[target.Kind]
		if !ok {
			t.Fatalf("%s: M4 imported no clock for %s", rw.name, target.Kind)
		}
		row := journalPruneRow{Name: rw.name, Clock: c.LastDue.In(parityZone)}

		rig := newHeadRig(t, paritySetup{})
		for k, v := range settings {
			rig.settings[k] = v
		}
		for i := range n {
			boot := i == 0
			if rw.edit != nil && rw.edit.step == i {
				head, boot = withHead(t, head, rw.edit.set), true
			}
			setHeadConfig(t, head)
			if boot {
				rig.boot(at(i))
				if i == 0 && g.Every == "" {
					d, ok := rig.ts.Tasks()[task]
					if !ok || d.GetInterval == nil {
						t.Fatalf("HEAD has no %s timer", task)
					}
					g.Every = d.GetInterval().String()
				}
			}
			rig.step(at(i))
			st := journalPruneStep{At: at(i), Boot: boot,
				WindowStart: head.Maintenance.WindowStart, WindowEnd: head.Maintenance.WindowEnd, WindowEnabled: head.Maintenance.Enabled}
			m2Converted(t, head, "log_retention_days", "core.log.journal.retention_days", &st.RetentionDays)
			m2Converted(t, head, "maintenance.purge_old_logs", "core.log.journal.prune_in_window", &st.PruneInWindow)
			st.Head = []string{}
			for _, rec := range rig.take() {
				switch {
				case rec.task == task:
					st.Head = append(st.Head, fmt.Sprintf("enqueue %s", rec.trigger))
				case rec.trigger == tasks.TriggerWindow && slices.Contains(rec.members, task):
					st.Head = append(st.Head, "window")
				}
			}
			slices.Sort(st.Head)
			row.Steps = append(row.Steps, st)
		}
		rig.stop()
		g.Rows = append(g.Rows, row)
	}

	// What the rows must show for the fixture to test anything: the timer
	// once and both windows in "defaults", only the timer with the window
	// toggle or the window off, nothing with retention off, the catch-up run
	// and both windows after retention reopens, and only the second window
	// after the toggle is back on at noon.
	count := func(row journalPruneRow, line string) int {
		k := 0
		for _, st := range row.Steps {
			for _, l := range st.Head {
				if l == line {
					k++
				}
			}
		}
		return k
	}
	for _, chk := range []struct {
		row            int
		timer, windows int
	}{{0, 1, 2}, {1, 1, 0}, {2, 0, 0}, {3, 1, 0}, {4, 1, 2}, {5, 1, 1}} {
		row := g.Rows[chk.row]
		if got := count(row, "enqueue schedule"); got != chk.timer {
			t.Errorf("%s: HEAD's timer ran %d times, want %d", row.Name, got, chk.timer)
		}
		if got := count(row, "window"); got != chk.windows {
			t.Errorf("%s: HEAD's window ran it %d times, want %d", row.Name, got, chk.windows)
		}
	}

	got, err := json.MarshalIndent(g, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.FromSlash(journalPruneGoldenPath)
	if *updateJournalPrune {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate with -update-journal-prune)", path, err)
	}
	if string(want) != string(got) {
		t.Fatalf("%s differs from what HEAD's scheduler produces now (regenerate with -update-journal-prune, then run core/kernel's TestJournalPruneParity)", path)
	}
}
