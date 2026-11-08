// file: internal/serviceregistry/registry_test.go
// version: 1.1.0
// last-edited: 2026-11-08

package serviceregistry

import (
	"errors"
	"strings"
	"testing"
)

func okBuild(c *Container) (any, error) { return nil, nil }

func TestNewContainerFrom_DuplicatePanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on duplicate name")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "duplicate") {
			t.Errorf("panic msg = %v, expected duplicate", r)
		}
	}()
	NewContainerFrom(
		ServiceDef{Name: "x", Build: okBuild},
		ServiceDef{Name: "x", Build: okBuild},
	)
}

func TestNewContainerFrom_RequiresName(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on missing Name")
		}
	}()
	NewContainerFrom(ServiceDef{Build: okBuild})
}

func TestNewContainerFrom_RequiresBuild(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on missing Build")
		}
	}()
	NewContainerFrom(ServiceDef{Name: "x"})
}

func TestNewContainerFrom_NoDefsIsEmpty(t *testing.T) {
	c := NewContainerFrom()
	if err := c.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := c.Names(); len(got) != 0 {
		t.Fatalf("names = %v, want none", got)
	}
}

// TestNewContainerFrom_TwoContainersIsolated is the isolation the package-level
// factory map broke: two containers built from different lists in one process
// see only their own defs.
func TestNewContainerFrom_TwoContainersIsolated(t *testing.T) {
	a := NewContainerFrom(ServiceDef{Name: "robin", Build: func(c *Container) (any, error) { return "robin-a", nil }})
	b := NewContainerFrom(
		ServiceDef{Name: "robin", Build: func(c *Container) (any, error) { return "robin-b", nil }},
		ServiceDef{Name: "batgirl", Build: func(c *Container) (any, error) { return "batgirl-b", nil }},
	)
	for _, c := range []*Container{a, b} {
		if err := c.Build(t.Context()); err != nil {
			t.Fatalf("build: %v", err)
		}
	}
	if got := Get[string](a, "robin"); got != "robin-a" {
		t.Errorf("a robin = %q, want robin-a", got)
	}
	if got := Get[string](b, "robin"); got != "robin-b" {
		t.Errorf("b robin = %q, want robin-b", got)
	}
	if _, ok := TryGet[string](a, "batgirl"); ok {
		t.Error("container a sees batgirl, which only b's list defines")
	}
	if got := Get[string](b, "batgirl"); got != "batgirl-b" {
		t.Errorf("b batgirl = %q, want batgirl-b", got)
	}
}

// Include still names a service; one with no def and no override is unknown.
func TestNewContainerFrom_IncludeUnknownName(t *testing.T) {
	c := NewContainerFrom(ServiceDef{Name: "robin", Build: okBuild}).Include("joker")
	if err := c.Resolve(); !errors.Is(err, ErrUnknownService) {
		t.Fatalf("expected ErrUnknownService, got %v", err)
	}
}
