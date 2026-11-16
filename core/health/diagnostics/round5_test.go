// file: core/health/diagnostics/round5_test.go
// version: 1.2.0
// guid: 5b1f8c36-92d4-4e07-a3b8-6d70c2e19f54
// last-edited: 2026-11-16

package diagnostics_test

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/tasks"
)

// TestDiagnostics_BuildRefusesAMismatchedActor (round 5, N-7): Build is
// exported, so a caller other than Export can reach it with an actor that is
// not the person the params name. Only Export used to check; Build now refuses
// by itself, and writes no file.
func TestDiagnostics_BuildRefusesAMismatchedActor(t *testing.T) {
	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	for name, actor := range map[string]tasks.Actor{
		"run-other":  {UserID: "robin", AuthMethod: "session"},
		"run-system": {System: "core"},
		"run-blank":  {},
	} {
		_, err := e.Build(context.Background(), name, actor, diagnostics.ExportParams{ActorID: "bruce"}, nil)
		if !errors.Is(err, diagnostics.ErrActorMismatch) {
			t.Errorf("%s: err = %v, want ErrActorMismatch", name, err)
		}
		if _, serr := f.root.Stat(name + ".zip"); serr == nil {
			t.Errorf("%s: a refused build wrote a bundle", name)
		}
		if _, serr := f.root.Stat(name + ".zip.tmp"); serr == nil {
			t.Errorf("%s: a refused build left a temp file", name)
		}
	}
	if _, err := e.Build(context.Background(), "run-same", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, diagnostics.ExportParams{ActorID: "bruce"}, nil); err != nil {
		t.Errorf("the matching actor was refused: %v", err)
	}
}
