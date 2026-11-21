// file: internal/config/install_test.go
// version: 1.1.0
// guid: 5a1e3c7d-9b04-4f62-8d3a-2e6b0c9f1a48
// last-edited: 2026-11-19

package config

import (
	"context"
	"errors"
	"testing"
)

// stubFacade is a Facade that records what the package-level names hand it.
type stubFacade struct {
	mutateErr error
	mutated   int
	locks     map[string]string
	updates   []Update
	updateAs  []Actor
	updateErr error
}

func (s *stubFacade) Snapshot() Config   { return Config{} }
func (s *stubFacade) Generation() uint64 { return 0 }
func (s *stubFacade) Mutate(fn func(*Config)) error {
	s.mutated++
	var c Config
	fn(&c)
	return s.mutateErr
}
func (s *stubFacade) Locks() map[string]string { return s.locks }
func (s *stubFacade) View() View               { return View{Keys: []KeyView{}} }
func (s *stubFacade) Update(_ context.Context, a Actor, u Update) (UpdateResult, error) {
	s.updates, s.updateAs = append(s.updates, u), append(s.updateAs, a)
	return UpdateResult{}, s.updateErr
}

// installForTest installs f and restores whatever was installed.
func installForTest(t *testing.T, f Facade) {
	t.Helper()
	prev := Install(f)
	t.Cleanup(func() { Install(prev) })
}

// TestInstall_ReturnsThePreviousAndCurrentSeesIt: Install returns the Facade it
// replaced, Current is the installed one, and Install(nil) unbinds.
func TestInstall_ReturnsThePreviousAndCurrentSeesIt(t *testing.T) {
	installForTest(t, nil)
	if Current() != nil {
		t.Fatal("Current is not nil with nothing installed")
	}
	a, b := &stubFacade{}, &stubFacade{}
	if prev := Install(a); prev != nil {
		t.Fatalf("first Install returned %v, want nil", prev)
	}
	if Current() != Facade(a) {
		t.Fatal("Current is not the installed Facade")
	}
	if prev := Install(b); prev != Facade(a) {
		t.Fatalf("second Install returned %v, want the first", prev)
	}
	if prev := Install(nil); prev != Facade(b) || Current() != nil {
		t.Fatalf("Install(nil) returned %v, Current %v", prev, Current())
	}
}

// TestInstall_MutateDelegatesAndLogsARefusalOnce: with a Facade installed,
// Mutate hands fn to it and does not touch AppConfig; a refusal is reported
// exactly once and leaves AppConfig as it was; a success reports nothing.
func TestInstall_MutateDelegatesAndLogsARefusalOnce(t *testing.T) {
	prev := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = prev }) })
	var reported []error
	old := mutateRefused
	mutateRefused = func(err error) { reported = append(reported, err) }
	t.Cleanup(func() { mutateRefused = old })

	f := &stubFacade{}
	installForTest(t, f)
	gen := Generation()
	Mutate(func(c *Config) { c.FilenameParseModel = "alfred-pennyworth" })
	if f.mutated != 1 || len(reported) != 0 {
		t.Fatalf("mutated=%d reported=%v after an accepted Mutate", f.mutated, reported)
	}
	if got := Snapshot().FilenameParseModel; got == "alfred-pennyworth" {
		t.Fatal("Mutate with a Facade wrote AppConfig itself; only the Facade's projection may")
	}
	if Generation() != gen {
		t.Fatal("Generation moved with no projection")
	}

	refusal := errors.New("key is set by the operator layers")
	f.mutateErr = refusal
	before := Snapshot()
	Mutate(func(c *Config) { c.FilenameParseModel = "harvey-dent" })
	if len(reported) != 1 || !errors.Is(reported[0], refusal) {
		t.Fatalf("reported %v, want exactly the refusal", reported)
	}
	if Snapshot().FilenameParseModel != before.FilenameParseModel {
		t.Fatal("a refused Mutate changed AppConfig")
	}
}

// TestInstall_ProjectReplacesAppConfigAndMovesGeneration is the Facade's
// publish step.
func TestInstall_ProjectReplacesAppConfigAndMovesGeneration(t *testing.T) {
	prev := Snapshot()
	t.Cleanup(func() { Mutate(func(c *Config) { *c = prev }) })
	installForTest(t, &stubFacade{})
	gen := Generation()
	next := prev
	next.FilenameParseModel = "selina-kyle"
	Project(next)
	if Snapshot().FilenameParseModel != "selina-kyle" || AppConfig.FilenameParseModel != "selina-kyle" {
		t.Fatal("Project did not replace AppConfig")
	}
	if Generation() == gen {
		t.Fatal("Project did not move Generation")
	}
}
