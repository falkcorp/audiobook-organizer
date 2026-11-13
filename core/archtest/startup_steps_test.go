// file: core/archtest/startup_steps_test.go
// version: 1.0.0
// guid: 8d1f7a60-5b3c-4e29-a0c4-92e6b7d31f58
// last-edited: 2026-11-13

package archtest

import (
	"maps"
	"slices"
	"strings"
	"testing"
)

// startupTree is a fixture core module: health declaring Startup, and a
// kernel package running two steps through a parameter, a field and a
// NewStartup result. extra adds or replaces files.
func startupTree(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := map[string]string{
		"go.mod": "module github.com/falkcorp/mediacore\n\ngo 1.27.0\n",
		"health/health.go": `package health

import "context"

type Startup struct{}

func NewStartup() (*Startup, error) { return &Startup{}, nil }

func (s *Startup) Run(ctx context.Context, name string, fn func(context.Context) error) error {
	return fn(ctx)
}
`,
		"kernel/kernel.go": `package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/health"
)

type Kernel struct{ startup *health.Startup }

func (k *Kernel) boot(ctx context.Context) error {
	return k.startup.Run(ctx, "alfred.ready", func(context.Context) error { return nil })
}

func early(ctx context.Context, st *health.Startup) error {
	return st.Run(ctx, "gotham.signal", func(context.Context) error { return nil })
}

func fresh(ctx context.Context) error {
	s, _ := health.NewStartup()
	return s.Run(ctx, "arkham.gate", func(context.Context) error { return nil })
}
`,
	}
	maps.Copy(files, extra)
	return writeModule(t, files)
}

func TestStartupSteps_FixtureControls(t *testing.T) {
	table := []string{"alfred.ready", "gotham.signal", "arkham.gate"}

	t.Run("MatchingTableReportsNothing", func(t *testing.T) {
		got, err := StartupStepFindings(startupTree(t, nil), table, "kernel")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("a matching table reported:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("UnrunRowReported", func(t *testing.T) {
		got, err := StartupStepFindings(startupTree(t, nil), append(slices.Clone(table), "joker.laugh"), "kernel")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], `"joker.laugh" is never run`) {
			t.Fatalf("want the unrun row reported once, got:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("UnknownLiteralReported", func(t *testing.T) {
		root := startupTree(t, map[string]string{
			"kernel/extra.go": `package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/health"
)

func extra(ctx context.Context, st *health.Startup) error {
	return st.Run(ctx, "riddler.puzzle", func(context.Context) error { return nil })
}
`,
		})
		got, err := StartupStepFindings(root, table, "kernel")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], `Startup.Run("riddler.puzzle") is not a row`) {
			t.Fatalf("want the unknown literal reported once, got:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("NonLiteralNameReported", func(t *testing.T) {
		root := startupTree(t, map[string]string{
			"kernel/dyn.go": `package kernel

import (
	"context"

	"github.com/falkcorp/mediacore/health"
)

func dyn(ctx context.Context, st *health.Startup, name string) error {
	return st.Run(ctx, name, func(context.Context) error { return nil })
}
`,
		})
		got, err := StartupStepFindings(root, table, "kernel")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 1 || !strings.Contains(got[0], "not a string literal") {
			t.Fatalf("want the non-literal name reported once, got:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("OtherRunMethodIgnored", func(t *testing.T) {
		root := startupTree(t, map[string]string{
			"kernel/other.go": `package kernel

import "context"

type runner struct{}

func (runner) Run(ctx context.Context, name string, fn func(context.Context) error) error { return fn(ctx) }

func other(ctx context.Context, r runner) error {
	return r.Run(ctx, "penguin.umbrella", func(context.Context) error { return nil })
}
`,
		})
		got, err := StartupStepFindings(root, table, "kernel")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 0 {
			t.Fatalf("a Run on another type reported:\n  %s", strings.Join(got, "\n  "))
		}
	})

	t.Run("RefusesWhatItCannotSee", func(t *testing.T) {
		if _, err := StartupStepFindings(startupTree(t, nil), table, "nowhere"); err == nil {
			t.Fatal("a package that matches nothing was accepted")
		}
		if _, err := StartupStepFindings(startupTree(t, nil), table); err == nil {
			t.Fatal("an empty package list was accepted")
		}
		bare := startupTree(t, map[string]string{"kernel/kernel.go": "package kernel\n"})
		if _, err := StartupStepFindings(bare, table, "kernel"); err == nil {
			t.Fatal("a tree with no Run call was accepted")
		}
	})
}
