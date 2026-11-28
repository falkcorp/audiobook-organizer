// file: core/internal/executor/actor_parity_test.go
// version: 1.0.0
// guid: 6b2d9f40-1c83-4e75-a9d6-3f0e8a7c5b12
// last-edited: 2026-11-28

package executor

import (
	"context"
	"fmt"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 M4 parity: what an imported run's actor lets a live caller do, held
// against what HEAD let the same caller do, per auth method and per operation.
//
// HEAD's rules (the model below):
//
//   - cancel: the run's own user, by user id alone (internal/server/handlers/
//     opsv3/runs.go startedBy, :370-373; used at :376-379), or the cancel
//     permission. The method and the key a request carried are not compared.
//   - read: library.view, or the run's own user (same startedBy, used at :330).
//   - discard: settings.manage only. DELETE /api/v1/operations/v2/:id/record
//     (internal/server/legacy_manifest.go:797) has no own-run arm.
//
// Core compares the whole tasks.Actor ({UserID, AuthMethod, System};
// core/tasks/rc.go:87, no key id) in mayCancel (control.go) and moduleMayAct
// (module_client.go) for cancel and discard, and has no own-run arm for reads.
// So "the same user through a different API key" is, to core, "the same user
// through the method token": one cell, and HEAD answered it by user id alone.
//
// m4's TestCutover_ImportedActorMethodIsTheLiveSpelling pins the other half:
// the literals in headMethods below are the actors m4 stamps (m4 is in the
// root module and cannot import this internal package, so the two halves share
// literals, not code).

// ownRunArm is whether the build under test still has the own-run arm in
// mayCancel. It is true on this branch; PR #5077 (cancel/discard need
// Perms.Manage AND the operation's permission, no own-run arm) makes it
// false, and the same cells then pin the other column. Flip it when #5077 is
// in the tree.
const ownRunArm = true

// headMethods is HEAD's auth method and the spelling m4 imports it as, which
// is the one a live principal of that method is stamped with (taskhttp
// actorOf).
var headMethods = []struct{ head, native string }{
	{"session", "session"},
	{"session_delegated", "session_delegated"},
	{"api_key", "token"},
	{"cf_access", "ext:cfaccess"},
	{"abs", "books.abs"},
}

type parityOp string

const (
	opCancel  parityOp = "cancel"
	opDiscard parityOp = "discard"
	opView    parityOp = "view"
)

// headAllows is HEAD's answer. holdsView: the caller has library.view.
func headAllows(op parityOp, callerUser, runUser string, holdsView bool) bool {
	same := callerUser == runUser
	switch op {
	case opCancel:
		return same
	case opDiscard:
		return false
	case opView:
		return holdsView || same
	}
	panic(op)
}

type rights int

const (
	equal rights = iota
	narrower
	wider
)

func (r rights) String() string { return [...]string{"equal", "narrower", "wider"}[r] }

func compare(head, core bool) rights {
	switch {
	case head == core:
		return equal
	case head:
		return narrower
	}
	return wider
}

// parityCtx is a request of actor holding only the named scopes.
func parityCtx(actor tasks.Actor, viewer bool) context.Context {
	g := grants{}
	if viewer {
		g[scopeTasksView] = true
	}
	return context.WithValue(context.Background(), callerKey{}, callerID{actor, g})
}

// TestImportedActor_RightsAgainstHEAD is the parity table: for each HEAD
// method, each import spelling (m4's, or HEAD's verbatim, which is what the
// import did before the mapping), each caller and each operation, the live
// answer is compared with HEAD's and the direction of any difference is
// pinned. Callers hold at most core:tasks.view, so the only way to cancel or
// discard is the own-run arm, which is the thing under test.
func TestImportedActor_RightsAgainstHEAD(t *testing.T) {
	const (
		bruce  = "bruce.wayne"
		selina = "selina.kyle"
	)
	h := reachHarness(t, nil)
	const retired = tasks.Kind("gotham.retired")
	putSnapshot(t, h, retired, snapshotOfDef(t, taskDef("gotham.retired", okBody)))
	kinds := []struct {
		name string
		kind tasks.Kind
	}{{"kind this build does not register", retired}, {"registered kind", "gotham.patrol"}}

	next := 0
	for _, m := range headMethods {
		for _, imported := range []struct{ name, method string }{{"m4 spelling", m.native}, {"HEAD verbatim", m.head}} {
			callers := []struct {
				name   string
				actor  tasks.Actor
				viewer bool
			}{
				{"same user, same method", tasks.Actor{UserID: bruce, AuthMethod: m.native}, false},
				{"same user, same method, holds library.view", tasks.Actor{UserID: bruce, AuthMethod: m.native}, true},
				{"another user, same method", tasks.Actor{UserID: selina, AuthMethod: m.native}, false},
				{"another user, same method, holds library.view", tasks.Actor{UserID: selina, AuthMethod: m.native}, true},
			}
			for _, other := range headMethods {
				if other.native != m.native {
					callers = append(callers, struct {
						name   string
						actor  tasks.Actor
						viewer bool
					}{"same user, method " + other.native, tasks.Actor{UserID: bruce, AuthMethod: other.native}, false})
				}
			}
			for _, k := range kinds {
				for _, c := range callers {
					for _, op := range []parityOp{opCancel, opDiscard, opView} {
						name := fmt.Sprintf("%s/%s/%s/%s/%s", m.head, imported.name, k.name, c.name, op)
						t.Run(name, func(t *testing.T) {
							next++
							id := fmt.Sprintf("parity-%04d", next)
							rec := doneRun(id, k.kind, tasks.StateSucceeded)
							rec.Actor = tasks.Actor{UserID: bruce, AuthMethod: imported.method}
							importRuns(t, h, []tasks.RunRecord{rec})
							ctx := parityCtx(c.actor, c.viewer)
							var core bool
							switch op {
							case opCancel:
								core = !isErr(h.x.Cancel(ctx, tasks.RunID(id)), ErrNotPermitted)
							case opDiscard:
								core = !isErr(h.x.Discard(ctx, tasks.RunID(id)), ErrNotPermitted)
							case opView:
								_, err := h.x.Get(ctx, tasks.RunID(id))
								core = err == nil
							}
							head := headAllows(op, c.actor.UserID, bruce, c.viewer)
							got := compare(head, core)
							t.Logf("PARITY method=%s import=%s op=%s caller=%s head=%v core=%v: %v", m.head, imported.name, op, c.name, head, core, got)
							if want := expectedRights(op, imported.method == m.native, c.actor, bruce, m.native, c.viewer); got != want {
								t.Errorf("%s: core %v, HEAD %v: %v than HEAD, pinned %v", op, core, head, got, want)
							}
						})
					}
				}
			}
		}
	}
}

// expectedRights is the pinned direction per cell, from the rules above, for
// the build with or without the own-run arm (ownRunArm). matches: the run's
// actor is spelt as a live caller of its own method is stamped.
func expectedRights(op parityOp, matchesLive bool, caller tasks.Actor, runUser, runNative string, viewer bool) rights {
	sameWholeActor := caller.UserID == runUser && caller.AuthMethod == runNative && matchesLive
	switch op {
	case opCancel:
		coreAllows := ownRunArm && sameWholeActor
		return compare(headAllows(op, caller.UserID, runUser, viewer), coreAllows)
	case opDiscard:
		return compare(false, ownRunArm && sameWholeActor)
	case opView:
		// Core has no own-run read arm: a run is readable by holders of
		// core:tasks.view (or the kind's View) only.
		return compare(headAllows(op, caller.UserID, runUser, viewer), viewer)
	}
	panic(op)
}
