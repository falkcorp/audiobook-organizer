// file: internal/plugins/maintenance/plugin_test.go
// version: 2.0.0
// guid: a3b4c5d6-e7f8-9012-6789-234567890123
// last-edited: 2026-08-20

package maintenance_test

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/plugins/maintenance"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// captureRegistry collects every OperationDef the plugin registers.
//
// 🔴 These three tests were t.Skip'd from 2026-05-12 to 2026-08-20 on the grounds
// that they "require a full ServerDeps stub". That was never true: sdk.Registry is a
// two-method interface, and the def constructors only capture the plugin pointer for
// their Run closure — none of them touches deps at construction time. So the whole
// def table can be enumerated with a nil-deps plugin and a ten-line fake.
//
// The cost of the skip was real. maintenance.missing-file-repoint shipped without a
// ResumePolicy on 2026-08-20; the package suite passed, and the failure surfaced only
// when the SERVER REFUSED TO START ("registration failed for 1 op(s)") — in the
// binary smoke test and again in E2E. This test is exactly the thing that should have
// caught it one second after the def was written.
type captureRegistry struct {
	defs []sdk.OperationDef
	errs []error
}

func (c *captureRegistry) RegisterOp(def sdk.OperationDef) error {
	c.defs = append(c.defs, def)
	return nil
}

func (c *captureRegistry) EnqueueOp(context.Context, string, any, ...sdk.EnqueueOption) (string, error) {
	return "", nil
}

// allDefs registers with a nil-deps plugin and returns the def table. It fails the
// test rather than returning an empty slice, because "zero defs" would make every
// assertion below vacuously pass — the classic always-green instrument.
func allDefs(t *testing.T) []sdk.OperationDef {
	t.Helper()
	reg := &captureRegistry{}
	if err := maintenance.New(nil).Register(reg); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if len(reg.defs) < 20 {
		t.Fatalf("captured only %d defs — the instrument is broken, not the plugin", len(reg.defs))
	}
	return reg.defs
}

// Every op must state a ResumePolicy. The registry REJECTS ResumeUnspecified at
// startup and the whole server refuses to boot, so a missing policy is not a lint
// nit — it is a total outage that ships green from `go test ./...`.
func TestMaintenancePlugin_Register_AllOpsHaveExplicitResumePolicy(t *testing.T) {
	for _, def := range allDefs(t) {
		if def.ResumePolicy == sdk.ResumeUnspecified {
			t.Errorf("op %q has no ResumePolicy — the server will refuse to start", def.ID)
		}
	}
}

// Every op must declare what it touches. An op with no capabilities cannot be
// permission-checked meaningfully.
func TestMaintenancePlugin_AllOpsHaveCapabilities(t *testing.T) {
	for _, def := range allDefs(t) {
		if len(def.Capabilities) == 0 {
			t.Errorf("op %q declares no Capabilities", def.ID)
		}
	}
}

// Structural rules that make a def usable at all: a unique, namespaced ID, something
// to show a human, and something to run.
func TestMaintenancePlugin_HardRules(t *testing.T) {
	seen := map[string]bool{}
	for _, def := range allDefs(t) {
		switch {
		case def.ID == "":
			t.Error("a def has an empty ID")
			continue
		case seen[def.ID]:
			t.Errorf("duplicate op ID %q — the later registration silently wins", def.ID)
		case !strings.Contains(def.ID, "."):
			t.Errorf("op %q is not namespaced (want <plugin>.<op>)", def.ID)
		}
		seen[def.ID] = true

		if strings.TrimSpace(def.DisplayName) == "" {
			t.Errorf("op %q has no DisplayName", def.ID)
		}
		if strings.TrimSpace(def.Description) == "" {
			t.Errorf("op %q has no Description", def.ID)
		}
		if def.Run == nil {
			t.Errorf("op %q has a nil Run", def.ID)
		}
	}
}
