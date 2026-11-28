// file: core/kernel/diagnostics_wire_snapshot_test.go
// version: 1.0.0
// guid: 8d2f4b7e-6a31-4c95-b0e8-5f1a9c3d7e26
// last-edited: 2026-11-28

package kernel

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/health/diagnostics"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/tasks"
)

// dwProbe is a contributor whose sections record that they ran, so a test can
// say which sections a viewer was given.
type dwProbe struct {
	mu       sync.Mutex
	ran      map[string]bool
	sections []diagnostics.Section
}

func newDWProbe(perms map[string]string) *dwProbe {
	p := &dwProbe{ran: map[string]bool{}}
	for name, perm := range perms {
		p.sections = append(p.sections, diagnostics.Section{
			Name: name,
			Perm: rbac.Need(rbac.Permission(perm)),
			Write: func(_ context.Context, w diagnostics.Writer, _ diagnostics.Viewer) error {
				p.mu.Lock()
				p.ran[name] = true
				p.mu.Unlock()
				return w.JSON("probe.json", map[string]bool{"ran": true})
			},
		})
	}
	return p
}

func (p *dwProbe) Sections() []diagnostics.Section { return p.sections }

// TestExport_NonKeyMethodsAreCappedByTheRequestSnapshot is the Security MEDIUM
// cell: for every method the no-credential-id arm admits, a request-time
// principal whose permissions exclude a section does not get that section in its
// export, however wide the user's roles are now. The arm's viewer is the one
// Exporter.viewer intersects with ExportParams.Perms (the snapshot the start
// route takes from the request principal's checker), so it is not a second path
// around the intersection. A key-bound actor goes through the same intersection.
func TestExport_NonKeyMethodsAreCappedByTheRequestSnapshot(t *testing.T) {
	const (
		kept = "books.probe-metrics" // core:metrics.read, in the snapshot
		cut  = "books.probe-manage"  // core:system.manage, not in the snapshot
	)
	for _, a := range []tasks.Actor{
		{UserID: "bruce", AuthMethod: "ext:cfaccess"},
		{UserID: "bruce", AuthMethod: "books.abs"},
		{UserID: "bruce", AuthMethod: "session_delegated"},
		{UserID: "bruce", AuthMethod: "session"},
		{UserID: "bruce", AuthMethod: "token", CredentialID: "key-batphone"},
		{UserID: "bruce", AuthMethod: "books.abs", CredentialID: "key-batphone"},
	} {
		t.Run(a.AuthMethod+"/"+a.CredentialID, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			probe := newDWProbe(map[string]string{kept: "core:metrics.read", cut: "core:system.manage"})
			keys := &dwKeys{keys: []*auth.APIKey{dwKey("key-batphone", "bruce", "core:system.manage", "core:metrics.read")}}
			users := dwUsers{"bruce": dwUser("bruce", auth.StatusActive, "admin")}
			ex, err := diagnostics.New(diagnostics.Config{
				Root:         root,
				ViewerFor:    diagnosticsViewerFor(users, keys, newDWCompiler(t), dwNonKey()),
				Contributors: []diagnostics.Contributor{probe},
			})
			if err != nil {
				t.Fatalf("diagnostics.New: %v", err)
			}
			// The request-time principal held metrics.read only: its start route
			// stamped exactly that.
			params := diagnostics.ExportParams{ActorID: "bruce", Perms: []string{"core:metrics.read"}, Sections: []string{kept, cut}}
			if _, err := ex.Build(t.Context(), "run-batcave", a, params, nil); err != nil {
				t.Fatalf("Build: %v", err)
			}
			if !probe.ran[kept] {
				t.Errorf("%s: the section the request held was left out", kept)
			}
			if probe.ran[cut] {
				t.Errorf("%s ran: the export is wider than the request-time principal", cut)
			}
		})
	}
}
