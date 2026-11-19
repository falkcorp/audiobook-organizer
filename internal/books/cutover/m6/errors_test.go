// file: internal/books/cutover/m6/errors_test.go
// version: 1.0.0
// guid: 5e93b1a7-d204-4c68-b7f0-3a81c6d9e552
// last-edited: 2026-11-19

package m6_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/storage"
)

// TestNew_RefusesAnIncompleteConfig: the step is built by F7-09 from a Config,
// and a missing importer or argument is a build-time error, never a nil
// dereference in the middle of a boot.
func TestNew_RefusesAnIncompleteConfig(t *testing.T) {
	r := newRig(t, withRows(headRows(t)))
	good := m6.Config{
		Integrations: lazyInteg{r}, AI: lazyAI{r}, Notify: lazyNotify{r}, Config: r.cfg, Secrets: r.secrets,
		SecretNames: r.secrets.names, SecretValue: r.secrets.value, Legacy: r.legacy,
	}
	after := []storage.StepRef{{Version: 1}}
	if _, err := m6.New(good, "step", 3, after); err != nil {
		t.Fatalf("a complete config was refused: %v", err)
	}
	for name, mut := range map[string]func(c *m6.Config){
		"integrations": func(c *m6.Config) { c.Integrations = nil },
		"ai":           func(c *m6.Config) { c.AI = nil },
		"notify":       func(c *m6.Config) { c.Notify = nil },
		"config":       func(c *m6.Config) { c.Config = nil },
		"secrets":      func(c *m6.Config) { c.Secrets = nil },
		"secret names": func(c *m6.Config) { c.SecretNames = nil },
		"secret value": func(c *m6.Config) { c.SecretValue = nil },
		"legacy":       func(c *m6.Config) { c.Legacy = nil },
	} {
		c := good
		mut(&c)
		if _, err := m6.New(c, "step", 3, after); err == nil {
			t.Errorf("a config without %s was accepted", name)
		}
	}
	for name, call := range map[string]func() error{
		"no id":         func() error { _, err := m6.New(good, "", 3, after); return err },
		"level zero":    func() error { _, err := m6.New(good, "step", 0, after); return err },
		"negative":      func() error { _, err := m6.New(good, "step", -1, after); return err },
		"no level step": func() error { _, err := m6.New(good, "step", 3, nil); return err },
	} {
		if call() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestStep_ImporterErrorFailsAndResumes (I1): an importer that fails for a reason
// other than a conflict (a full disk, a closed store) fails the boot instead of
// being quarantined as if the row were bad, nothing is recorded done, and the
// next boot over the same store finishes the step with the same result as one
// that never failed.
func TestStep_ImporterErrorFailsAndResumes(t *testing.T) {
	clean := newRig(t, withRows(headRows(t)))
	want, err := clean.run(t)
	if err != nil {
		t.Fatalf("the clean run: %v", err)
	}

	for _, c := range []struct{ kind, id string }{{"job", "01JKNIGHTFALL"}, {"instance", "metadata/hardcover"}} {
		t.Run(c.kind, func(t *testing.T) {
			boom := errors.New("the store is closed")
			r := newRig(t, withRows(headRows(t)), func(r *rig) {
				r.refuse = func(k, i string) error {
					if k == c.kind && i == c.id {
						return boom
					}
					return nil
				}
			})
			fsys := r.seed(t)
			if _, err := r.runOn(t, fsys); !errors.Is(err, boom) {
				t.Fatalf("the boot returned %v, want the importer's error", err)
			} else if !strings.Contains(err.Error(), "m6:") {
				t.Errorf("the error does not say which step failed: %v", err)
			}
			r.mu.Lock()
			r.refuse = nil
			r.mu.Unlock()
			got, err := r.runOn(t, fsys)
			if err != nil {
				t.Fatalf("the resumed boot: %v\nlogs:\n%s", err, r.logs.String())
			}
			if len(got.Quarantine) != len(want.Quarantine) {
				t.Errorf("the failed boot left %d quarantined rows, a clean run has %d", len(got.Quarantine), len(want.Quarantine))
			}
			if d := diff(flatten(got), flatten(want)); d != "" {
				t.Errorf("the resumed store differs from a clean run's:\n%s", d)
			}
		})
	}
}

// TestStep_RunsWithoutALogger: Config.Logger is optional, and a quarantined row
// is the line that logs, so the run that reaches one is the one that proves it.
func TestStep_RunsWithoutALogger(t *testing.T) {
	r := newRig(t, withRows(headRows(t)), conflict("job", "01JKNIGHTFALL"), func(r *rig) { r.noLogger = true })
	s, err := r.run(t)
	if err != nil {
		t.Fatalf("M6: %v", err)
	}
	if len(s.Quarantine) == 0 {
		t.Fatal("no row was quarantined, so no log line was written")
	}
}
