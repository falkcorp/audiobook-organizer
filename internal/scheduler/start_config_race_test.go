// file: internal/scheduler/start_config_race_test.go
// version: 1.0.0
// guid: 6e2a9d41-3b8c-4d70-a5f1-7c0e8b2d4a63
// last-edited: 2026-11-10

package scheduler

import (
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestStart_ReadsMaintenanceFromDepsNotTheGlobalConfig: Start runs on the
// server's post-listen start, which a stop can abandon while a caller edits the
// config. Start must decide the maintenance-window checker from the settings
// its deps carry. Under `-race` this fails if Start reads
// config.AppConfig.Maintenance.{Enabled,WindowStart,WindowEnd}: a writer
// goroutine stores to exactly those fields, with nothing ordering it against
// Start.
func TestStart_ReadsMaintenanceFromDepsNotTheGlobalConfig(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })
	config.AppConfig.Maintenance.Enabled = false

	ts := &TaskScheduler{
		deps: SchedulerDeps{
			Store:       func() SchedulerStore { return nil },
			Maintenance: MaintenanceSettings{Enabled: true, WindowStart: 2, WindowEnd: 5},
		},
		tasks:   make(map[string]*TaskDefinition),
		lastRun: make(map[string]time.Time),
	}

	var writer sync.WaitGroup
	writer.Go(func() {
		for i := range 100 {
			config.AppConfig.Maintenance.Enabled = i%2 == 0
			config.AppConfig.Maintenance.WindowStart = i
			config.AppConfig.Maintenance.WindowEnd = i + 1
		}
	})

	shutdown := make(chan struct{})
	var wg sync.WaitGroup
	ts.Start(shutdown, &wg)
	close(shutdown)
	wg.Wait()
	writer.Wait()
}

// TestStart_RunsTheMaintenanceCheckerOnlyWhenTheDepsEnableIt: the gate, and the
// window Start announces, are the deps' settings, whatever the global config
// says.
func TestStart_RunsTheMaintenanceCheckerOnlyWhenTheDepsEnableIt(t *testing.T) {
	restore := config.Snapshot()
	t.Cleanup(func() { config.AppConfig = restore })

	for _, tc := range []struct {
		name         string
		deps, global bool
		want         bool
	}{
		{"deps on, config off", true, false, true},
		{"deps off, config on", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config.AppConfig.Maintenance.Enabled = tc.global
			config.AppConfig.Maintenance.WindowStart = 11
			config.AppConfig.Maintenance.WindowEnd = 12
			buf := captureLogs(t, slog.LevelInfo)
			ts := &TaskScheduler{
				deps: SchedulerDeps{
					Store:       func() SchedulerStore { return nil },
					Maintenance: MaintenanceSettings{Enabled: tc.deps, WindowStart: 2, WindowEnd: 5},
				},
				tasks:   make(map[string]*TaskDefinition),
				lastRun: make(map[string]time.Time),
			}
			shutdown := make(chan struct{})
			var wg sync.WaitGroup
			ts.Start(shutdown, &wg)
			close(shutdown)
			wg.Wait()

			out := buf.String()
			if got := strings.Contains(out, "Maintenance window enabled"); got != tc.want {
				t.Fatalf("checker announced = %v, want %v\n%s", got, tc.want, out)
			}
			if tc.want && !strings.Contains(out, "windowStart=2") {
				t.Fatalf("Start announced the live config's window, not the deps':\n%s", out)
			}
		})
	}
}
