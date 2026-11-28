// file: core/health/diagnostics/snapshot_required_test.go
// version: 0.1.0
// guid: 116ee4f4-7d9e-4096-9399-32cec26d5ec2
// last-edited: 2026-11-28

package diagnostics_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/tasks"
	"github.com/falkcorp/mediacore/tasks/taskstest"
)

// roundTrip is what a run's params are by the time it reads them: the executor
// stores them as JSON.
func roundTrip(t *testing.T, p diagnostics.ExportParams) diagnostics.ExportParams {
	t.Helper()
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var out diagnostics.ExportParams
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestExport_ARunWithoutASnapshotIsRefusedForEveryActor: a nil snapshot and an
// empty one (built by a JSON round trip, as the run reads them) are refused with
// ErrNoPermsSnapshot, by Build and by the kind's Run, for an administrator as for
// anyone, before ViewerFor is asked and with no file written. Neither is read as
// "unnarrowed". A snapshot that names a scope still runs.
func TestExport_ARunWithoutASnapshotIsRefusedForEveryActor(t *testing.T) {
	cases := []struct {
		name  string
		perms []string
	}{
		{"nil", nil},
		{"empty", []string{}},
	}
	viewers := map[string]func(context.Context, tasks.Actor) (diagnostics.Viewer, error){
		"administrator": viewerOf("core:admin", "core:system.manage"),
		"manager":       viewerOf("core:system.manage"),
		"nobody":        viewerOf(),
	}
	for _, c := range cases {
		for who, vf := range viewers {
			t.Run(c.name+"/"+who, func(t *testing.T) {
				f := newFixture(t)
				asked := 0
				e := f.exporter(func(ctx context.Context, a tasks.Actor) (diagnostics.Viewer, error) {
					asked++
					return vf(ctx, a)
				}, diagnostics.CoreSources{})
				p := roundTrip(t, diagnostics.ExportParams{ActorID: "bruce", Perms: c.perms})
				if len(p.Perms) != 0 {
					t.Fatalf("the case is a run with no snapshot, got %v", p.Perms)
				}

				actor := tasks.Actor{UserID: "bruce", AuthMethod: "session"}
				if _, err := e.Build(context.Background(), "run-a", actor, p, nil); !errors.Is(err, diagnostics.ErrNoPermsSnapshot) {
					t.Errorf("Build: err = %v, want ErrNoPermsSnapshot", err)
				}
				rc := tasks.NewRC(&actorRun{FakeRun: taskstest.NewFakeRun(context.Background(), diagnostics.ExportKind), id: "run-b", actor: actor})
				if _, err := e.Export(rc, p); !errors.Is(err, diagnostics.ErrNoPermsSnapshot) {
					t.Errorf("Export: err = %v, want ErrNoPermsSnapshot", err)
				}
				if asked != 0 {
					t.Errorf("ViewerFor was asked %d times for a run that is refused first", asked)
				}
				for _, name := range []string{"run-a.zip", "run-a.zip.tmp", "run-b.zip", "run-b.zip.tmp"} {
					if _, err := f.root.Stat(name); err == nil {
						t.Errorf("a refused run wrote %s", name)
					}
				}
			})
		}
	}

	f := newFixture(t)
	e := f.exporter(viewerOf("core:system.manage"), diagnostics.CoreSources{})
	p := roundTrip(t, diagnostics.ExportParams{ActorID: "bruce", Perms: []string{"core:system.manage"}})
	if _, err := e.Build(context.Background(), "run-ok", tasks.Actor{UserID: "bruce", AuthMethod: "session"}, p, nil); err != nil {
		t.Errorf("a run with a snapshot was refused: %v", err)
	}
}
