// file: core/kernel/journal_prune_parity_test.go
// version: 0.1.0
// guid: e73acc3d-d676-47ee-bbaf-60c5e857f446
// last-edited: 2026-11-21

package kernel

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/logjournal"
	"github.com/falkcorp/mediacore/log"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest/schedule"
)

// journalPruneGolden is testdata/journal_prune_parity.json: what the app's
// pre-core scheduler did for its log purge, the task whose kind is
// core.log.journal.prune after the cutover, on six rows, and the core values
// the app's cutover converts each step's config into. The app's test writes
// it from that scheduler (core cannot run it, and core holds no app name: the
// fixture carries core kinds, paths and instants only).
type journalPruneGolden struct {
	Kind                 tasks.Kind `json:"kind"`
	Every                string     `json:"every"`
	Window               string     `json:"window"`
	Order                int        `json:"order"`
	DefaultPruneInWindow bool       `json:"default_prune_in_window"`
	ZoneOffset           int        `json:"zone_offset"`
	Rows                 []struct {
		Name  string    `json:"name"`
		Clock time.Time `json:"clock"`
		Steps []struct {
			At            time.Time `json:"at"`
			Boot          bool      `json:"boot"`
			RetentionDays int       `json:"retention_days"`
			PruneInWindow bool      `json:"prune_in_window"`
			WindowStart   int       `json:"window_start"`
			WindowEnd     int       `json:"window_end"`
			WindowEnabled bool      `json:"window_enabled"`
			Head          []string  `json:"head"`
		} `json:"steps"`
	} `json:"rows"`
}

// nopJournal is a journal store the scheduler never reaches: the harness
// starts runs, it does not execute them. A non-nil store gives the
// definition its Run, so it registers.
type nopJournal struct{}

func (nopJournal) Append(context.Context, []log.Record) error { return nil }
func (nopJournal) Query(context.Context, log.JournalQuery) (log.JournalPage, error) {
	return log.JournalPage{}, nil
}
func (nopJournal) Prune(context.Context, time.Time, int) (int, bool, error) { return 0, false, nil }

// TestJournalPruneParity: core.log.journal.prune's schedule matches the
// pre-core log purge. The kernel's default for core.log.schedules.journal_prune
// is the purge's interval, in its window at its position, and
// core.log.journal.prune_in_window's default is its shipped window toggle;
// then each row of the fixture, played through the scheduler harness with
// the real PruneDefinition (its retention and window gates over the step's
// converted settings), the kernel's default schedule and the converted
// window, enqueues the kind on exactly the steps the old scheduler ran it,
// by the same trigger.
func TestJournalPruneParity(t *testing.T) {
	raw, err := os.ReadFile("testdata/journal_prune_parity.json")
	if err != nil {
		t.Fatal(err)
	}
	var g journalPruneGolden
	if err := json.Unmarshal(raw, &g); err != nil {
		t.Fatal(err)
	}
	if g.Kind != logjournal.KindPrune {
		t.Fatalf("the fixture's kind is %s, want %s", g.Kind, logjournal.KindPrune)
	}
	if len(g.Rows) == 0 {
		t.Fatal("the fixture has no rows")
	}

	// The kernel's defaults, read back through the config store.
	st, err := config.NewStore(config.Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	if _, err := config.RegisterSections(st.Registry(), coreSections()...); err != nil {
		t.Fatalf("register the core sections: %v", err)
	}
	if err := st.Load(context.Background()); err != nil {
		t.Fatalf("load: %v", err)
	}
	read := func(p string, out any) {
		t.Helper()
		ks, ok := st.Lookup(config.Path(p))
		if !ok {
			t.Fatalf("config has no key %s", p)
		}
		if err := json.Unmarshal(ks.Effective, out); err != nil {
			t.Fatalf("%s = %s: %v", p, ks.Effective, err)
		}
	}
	var sched tasks.ScheduleConfig
	read(logjournal.PruneScheduleKey, &sched)
	every, err := time.ParseDuration(g.Every)
	if err != nil {
		t.Fatalf("the fixture's every %q: %v", g.Every, err)
	}
	if want := (tasks.ScheduleConfig{Enabled: true, Every: config.Duration(every), Window: g.Window, Order: g.Order}); sched != want {
		t.Errorf("%s default = %+v, want %+v (the old purge's interval, window and order)", logjournal.PruneScheduleKey, sched, want)
	}
	var inWindow bool
	read("core.log.journal.prune_in_window", &inWindow)
	if inWindow != g.DefaultPruneInWindow {
		t.Errorf("core.log.journal.prune_in_window default = %v, want the old toggle's %v", inWindow, g.DefaultPruneInWindow)
	}

	zone := time.FixedZone("fixture", g.ZoneOffset)
	for _, row := range g.Rows {
		t.Run(row.Name, func(t *testing.T) {
			var cur logjournal.Settings
			setTo := func(i int) {
				s := row.Steps[i]
				cur = logjournal.Settings{Enabled: true, MinLevel: "info", RetentionDays: s.RetentionDays, QueueSize: 4096, PruneInWindow: s.PruneInWindow}
			}
			window := func(i int) map[string]schedule.Window {
				s := row.Steps[i]
				return map[string]schedule.Window{g.Window: {Start: s.WindowStart, End: s.WindowEnd, Enabled: s.WindowEnabled}}
			}
			setTo(0)
			def := logjournal.PruneDefinition(nopJournal{}, func() logjournal.Settings { return cur }, nil)
			cfg := schedule.Config{
				Schedules: map[string]tasks.ScheduleConfig{logjournal.PruneScheduleKey: sched},
				Windows:   window(0),
				Zone:      zone,
			}
			steps := make([]schedule.Step, len(row.Steps))
			for i, s := range row.Steps {
				// Every run ends within its hour, the window's own run too:
				// an unfinished one would fold the next fire, which the old
				// scheduler (no fold, no window run) never did.
				steps[i] = schedule.Step{At: s.At, Boot: s.Boot && i > 0, Finish: []tasks.Kind{g.Kind, tasks.WindowKind(g.Window)}}
				if i == 0 {
					steps[i].Clocks = map[tasks.Kind]tasks.Clock{g.Kind: {LastDue: row.Clock}}
					continue
				}
				if s.Boot {
					steps[i].Edit = func(c *schedule.Config) {
						setTo(i)
						c.Windows = window(i)
					}
				}
			}
			evs := schedule.Run(t, []tasks.Definition{def}, cfg, steps)
			got := make([][]string, len(row.Steps))
			for i := range got {
				got[i] = []string{}
			}
			stepOf := func(at time.Time) int {
				for i, s := range row.Steps {
					if s.At.Equal(at) {
						return i
					}
				}
				t.Fatalf("an event at %s, which is no step", at)
				return -1
			}
			for _, e := range evs {
				switch {
				case e.Type == schedule.EventEnqueue && e.Kind == g.Kind && e.Trigger != tasks.TriggerWindow:
					i := stepOf(e.At)
					got[i] = append(got[i], "enqueue "+string(e.Trigger))
				case e.Type == schedule.EventWindow && slices.Contains(e.Members, g.Kind):
					i := stepOf(e.At)
					got[i] = append(got[i], "window")
				}
			}
			for i, s := range row.Steps {
				slices.Sort(got[i])
				if !slices.Equal(got[i], s.Head) {
					t.Errorf("%s: core %v, the old scheduler %v", s.At.Format(time.RFC3339), got[i], s.Head)
				}
			}
		})
	}
}
