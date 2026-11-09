// file: core/kernel/fakemigration_hook_test.go
// version: 1.0.0
// guid: ebd1db59-235a-4047-b159-32d036235e10
// last-edited: 2026-11-09

//go:build unitlifecycle

package kernel

import (
	"context"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// extension is one start-timeout extension the kernel asked the service
// manager for, and when (on the bubble's clock, from the test's start).
type extension struct {
	step string
	at   time.Duration
}

// extensionLog is Config.ExtendStart: the service-manager half of
// Deps.ExtendStart, recorded with the time it was made.
type extensionLog struct {
	mu    sync.Mutex
	begin time.Time
	got   []extension
}

func (l *extensionLog) extend(step string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, extension{step, time.Since(l.begin)})
}

func (l *extensionLog) list() []extension {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]extension(nil), l.got...)
}

// TestFakeMigration_OneExtendPerMinuteStep: a 12 minute fake migration is
// twelve steps, each asking for more start time as it begins and then
// sleeping its minute: twelve extensions, one per minute, none from a timer,
// and the boot takes exactly twelve minutes (05-health §5H.3.4).
func TestFakeMigration_OneExtendPerMinuteStep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv(fakeMigrationEnv, "12m")
		rec := &extensionLog{begin: time.Now()}
		c := testConfig(t)
		c.ExtendStart = rec.extend
		mustNew(t, c)
		if took := time.Since(rec.begin); took != 12*time.Minute {
			t.Fatalf("boot took %s, want 12m", took)
		}
		got := rec.list()
		if len(got) != 12 {
			t.Fatalf("%d extensions, want 12 (one per step, none from a timer): %v", len(got), got)
		}
		for i, e := range got {
			if want := time.Duration(i) * time.Minute; e.at != want {
				t.Errorf("extension %d at %s, want %s: a step extends immediately before it sleeps", i+1, e.at, want)
			}
			if want := "fake migration step "; !strings.HasPrefix(e.step, want) {
				t.Errorf("extension %d names step %q, want a %q name", i+1, e.step, want)
			}
		}
	})
}

// TestFakeMigration_RoundsUpToWholeSteps: a duration that is not a whole
// number of minutes still runs whole one-minute steps.
func TestFakeMigration_RoundsUpToWholeSteps(t *testing.T) {
	t.Setenv(fakeMigrationEnv, "90s")
	if got := len(fakeMigrationSteps()); got != 2 {
		t.Fatalf("90s is %d steps, want 2", got)
	}
}

// TestFakeMigration_UnsetIsNoSteps: with the tag but no duration there is no
// fake step, so the binary this job builds boots normally for every other use.
func TestFakeMigration_UnsetIsNoSteps(t *testing.T) {
	t.Setenv(fakeMigrationEnv, "")
	if got := fakeMigrationSteps(); len(got) != 0 {
		t.Fatalf("unset duration made %d steps, want none", len(got))
	}
}

// TestFakeMigration_BadDurationFailsTheBoot: a value that is not a positive
// duration refuses the boot naming the variable, never a boot without the
// migration the cell was meant to test.
func TestFakeMigration_BadDurationFailsTheBoot(t *testing.T) {
	for _, raw := range []string{"twelve", "12", "0s", "-1m"} {
		t.Run(raw, func(t *testing.T) {
			t.Setenv(fakeMigrationEnv, raw)
			_, err := New(t.Context(), testConfig(t))
			if err == nil || !strings.Contains(err.Error(), fakeMigrationEnv) {
				t.Fatalf("New with %s=%q = %v, want an error naming the variable", fakeMigrationEnv, raw, err)
			}
		})
	}
}

// TestFakeMigration_StopsWithTheBootContext: cancelling the boot's context
// ends the step that is sleeping, so a stop signal during the fake migration
// is not held up for a minute.
func TestFakeMigration_StopsWithTheBootContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		t.Setenv(fakeMigrationEnv, "12m")
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			_, err := New(ctx, testConfig(t))
			done <- err
		}()
		time.Sleep(90 * time.Second)
		cancel()
		synctest.Wait()
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("New succeeded after its context was cancelled mid-migration")
			}
		default:
			t.Fatal("New is still sleeping after its context was cancelled")
		}
	})
}
