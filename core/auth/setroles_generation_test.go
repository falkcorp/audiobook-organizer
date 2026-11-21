// file: core/auth/setroles_generation_test.go
// version: 0.1.0
// guid: 8b3e6d10-c27f-4a95-9e41-0f7d2a5c8b36
// last-edited: 2026-11-21

package auth_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/audit"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/rbac"
)

// Batman names only: Harvey Dent loses his commissioner role.

// genLog records the generations rbac.Deps.OnChange reports: what the kernel
// would publish as core.rbac.changed.v1.
type genLog struct {
	mu   sync.Mutex
	gens []uint64
}

func (g *genLog) record(gen uint64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gens = append(g.gens, gen)
}

// take returns the generations since the last take and forgets them.
func (g *genLog) take() []uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := g.gens
	g.gens = nil
	return out
}

// TestSetRoles_ARemovedRoleReachesAnOpenStream: removing a role bumps rbac's
// generation and reports it through OnChange (the kernel publishes it as
// core.rbac.changed.v1, which wakes every open realtime stream), and the
// stream's next check, its Refresh of the principal it holds, no longer
// carries the removed role's permission. Before F4-08 A16 the generation did
// not move, so a stream kept the removed role until its heartbeat. A role set
// that does not change, and a refused change, report nothing.
func TestSetRoles_ARemovedRoleReachesAnOpenStream(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	harvey := f.user("harvey-dent", "gotham.detective", roleCommissioner)
	viewer := f.user("vicki-vale", rbac.RoleViewer)
	stream := f.signedIn(harvey) // the principal an open stream holds
	if !stream.Perms.Has("core:users.manage") {
		t.Fatal("the commissioner holds no core:users.manage; the test does not test what it says")
	}
	f.genLog.take()
	before := f.rb.Generation()

	if err := f.svc.SetRoles(ctx, f.signedIn(bruce), harvey.ID, []rbac.RoleName{"gotham.detective"}); err != nil {
		t.Fatal(err)
	}
	after := f.rb.Generation()
	if after <= before {
		t.Fatalf("rbac generation %d -> %d: the role change did not move it", before, after)
	}
	if got := f.genLog.take(); !slices.Equal(got, []uint64{after}) {
		t.Fatalf("OnChange reported %v, want exactly [%d]", got, after)
	}
	next, err := f.svc.Refresh(ctx, stream)
	if err != nil {
		t.Fatalf("the stream's next check: %v", err)
	}
	if next.Perms.Has("core:users.manage") {
		t.Fatal("the stream's next check still carries the removed role's core:users.manage")
	}
	if !next.Perms.Has("gotham:patrol") {
		t.Fatal("the stream's next check lost the role it kept")
	}

	if err := f.svc.SetRoles(ctx, f.signedIn(bruce), harvey.ID, []rbac.RoleName{"gotham.detective"}); err != nil {
		t.Fatal(err)
	}
	if got := f.genLog.take(); len(got) != 0 || f.rb.Generation() != after {
		t.Fatalf("an unchanged role set reported %v and moved the generation to %d", got, f.rb.Generation())
	}

	err = f.svc.SetRoles(ctx, f.session(viewer), harvey.ID, nil)
	wantErr(t, "a viewer changing roles", err, auth.ErrDenied)
	if got := f.genLog.take(); len(got) != 0 || f.rb.Generation() != after {
		t.Fatalf("a refused change reported %v and moved the generation to %d", got, f.rb.Generation())
	}
}

// TestSetRoles_ReportsOnlyACommittedChange: a write whose every attempt
// conflicts reports nothing, and neither does one whose retry finds that a
// concurrent write already made the change (the attempt that staged it did
// not commit). The concurrent writes go straight to the store, so they report
// nothing of their own.
func TestSetRoles_ReportsOnlyACommittedChange(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	bruce := f.user("bruce-wayne", rbac.RoleAdmin)
	admin := f.signedIn(bruce)
	harvey := f.user("harvey-dent", "gotham.detective", roleCommissioner)
	f.genLog.take()
	g := f.rb.Generation()

	f.audit.hold = func(e audit.Event) {
		if e.Target.ID == string(harvey.ID) {
			f.setRoles(harvey.ID, "gotham.detective", roleCommissioner)
		}
	}
	err := f.svc.SetRoles(ctx, admin, harvey.ID, []rbac.RoleName{"gotham.detective"})
	wantErr(t, "every attempt conflicting", err, auth.ErrContended)
	if got := f.genLog.take(); len(got) != 0 || f.rb.Generation() != g {
		t.Fatalf("a write that never committed reported %v, generation %d -> %d", got, g, f.rb.Generation())
	}

	once := true
	f.audit.hold = func(e audit.Event) {
		if e.Target.ID == string(harvey.ID) && once {
			once = false
			f.setRoles(harvey.ID, "gotham.detective")
		}
	}
	if err := f.svc.SetRoles(ctx, admin, harvey.ID, []rbac.RoleName{"gotham.detective"}); err != nil {
		t.Fatal(err)
	}
	f.audit.hold = nil
	if got := f.genLog.take(); len(got) != 0 || f.rb.Generation() != g {
		t.Fatalf("a retry that found the change made reported %v, generation %d -> %d", got, g, f.rb.Generation())
	}
}

// TestAssignmentsChanged_BumpsAndReports: rbac's AssignmentsChanged moves the
// generation by one and reports the new value through OnChange, once per call.
func TestAssignmentsChanged_BumpsAndReports(t *testing.T) {
	f := newFx(t)
	f.genLog.take()
	g := f.rb.Generation()
	f.rb.AssignmentsChanged()
	f.rb.AssignmentsChanged()
	if got := f.genLog.take(); !slices.Equal(got, []uint64{g + 1, g + 2}) || f.rb.Generation() != g+2 {
		t.Fatalf("two calls from %d reported %v, generation %d; want [%d %d], %d", g, got, f.rb.Generation(), g+1, g+2, g+2)
	}
}
