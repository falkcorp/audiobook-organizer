// file: internal/books/cutover/m4/actor_test.go
// version: 1.0.0
// guid: 9c3b5e18-4d72-4a06-8f91-6e0a2d7c1b53
// last-edited: 2026-11-28

package m4_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m4"
	"github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/tasks"
)

// F5-18 M4: the auth method an imported run carries is the one a live
// principal of the same user and method is stamped with, so the executor's
// own-run arm (a whole-Actor compare) matches it exactly as often as HEAD's
// user-id compare did or less. The control-side cells that pin what that match
// grants are core/internal/executor/actor_parity_test.go, over these same
// literal actors.

// headMethods is HEAD's auth.Method vocabulary (internal/auth/context.go:39-56)
// and the native spelling of each (core/internal/taskhttp/principal.go
// actorOf; core/auth/method.go; the extension and compat methods' own
// registrations).
var headMethods = []struct{ head, native string }{
	{"session", "session"},
	{"session_delegated", "session_delegated"},
	{"api_key", "token"},
	{"cf_access", "ext:cfaccess"},
	{"abs", "books.abs"},
	{"system", "system"},
	{"", ""},
}

// TestNormalizeAuthMethod: one row per input. A method the mapping does not
// list comes back verbatim and unlisted, including a native spelling, which
// HEAD never wrote.
func TestNormalizeAuthMethod(t *testing.T) {
	for _, c := range headMethods {
		t.Run("head "+c.head, func(t *testing.T) {
			got, listed := m4.NormalizeAuthMethod(c.head)
			if got != c.native || !listed {
				t.Fatalf("NormalizeAuthMethod(%q) = %q, %v; want %q, true", c.head, got, listed, c.native)
			}
		})
	}
	for _, unlisted := range []string{"oidc_token", "token", "ext:cfaccess", "books.abs", "Session", "auth_disabled"} {
		t.Run("unlisted "+unlisted, func(t *testing.T) {
			got, listed := m4.NormalizeAuthMethod(unlisted)
			if got != unlisted || listed {
				t.Fatalf("NormalizeAuthMethod(%q) = %q, %v; want it back verbatim and unlisted", unlisted, got, listed)
			}
		})
	}
}

// TestCutover_ImportedActorMethodIsTheLiveSpelling is the end-to-end cell: a
// run and its trail, written by HEAD's row types with each method, are imported
// through the rig with the native spelling, the user id kept. An unlisted
// method stays verbatim and is reported once per actor (run, trail entry).
func TestCutover_ImportedActorMethodIsTheLiveSpelling(t *testing.T) {
	done := epoch.Add(time.Hour)
	var specs []runSpec
	for i, c := range headMethods {
		head := c.head
		specs = append(specs, runSpec{
			id: runID(i + 1), def: "dedup.book-scan", st: "succeeded", completed: new(done), evts: 1,
			mutateRec: func(r *store.RunRecord) { r.Actor = store.Actor{UserID: "bruce.wayne", AuthMethod: head} },
			evtActor:  &store.Actor{UserID: "selina.kyle", AuthMethod: head},
		})
	}
	odd := len(headMethods) + 1
	specs = append(specs, runSpec{
		id: runID(odd), def: "dedup.book-scan", st: "succeeded", completed: new(done), evts: 1,
		mutateRec: func(r *store.RunRecord) { r.Actor = store.Actor{UserID: "bruce.wayne", AuthMethod: "oidc_token"} },
		evtActor:  &store.Actor{UserID: "selina.kyle", AuthMethod: "oidc_token"},
	})
	r := newRig(t, withRuns(nil, specs...))
	r.runObserve(t, func(ctx context.Context, s *snap) {
		for i, c := range headMethods {
			rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(i+1)))
			if err != nil {
				t.Fatalf("method %q: %v", c.head, err)
			}
			if rec.Actor.UserID != "bruce.wayne" || rec.Actor.AuthMethod != c.native {
				t.Errorf("run of HEAD method %q imported with actor %+v, want bruce.wayne via %q", c.head, rec.Actor, c.native)
			}
			var evActors []tasks.Actor
			for p, err := range s.Stores.Runs.Pages(ctx, rec.ID, tasks.PageQuery{Page: tasks.PageEvents}) {
				if err != nil {
					t.Fatal(err)
				}
				if p.Event.Actor != nil {
					evActors = append(evActors, *p.Event.Actor)
				}
			}
			if len(evActors) != 1 || evActors[0].UserID != "selina.kyle" || evActors[0].AuthMethod != c.native {
				t.Errorf("trail of HEAD method %q imported with actors %+v, want selina.kyle via %q", c.head, evActors, c.native)
			}
		}
		rec, err := s.Stores.Runs.Get(ctx, tasks.RunID(runID(odd)))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Actor.AuthMethod != "oidc_token" {
			t.Errorf("an unlisted method was changed to %q, want it kept verbatim", rec.Actor.AuthMethod)
		}
	})
	if got := strings.Count(r.logs.String(), "an actor's auth method is not in the mapping"); got != 2 {
		t.Errorf("the log names %d unmapped actors, want 2 (the run's and its trail entry's):\n%s", got, r.logs.String())
	}
	if !strings.Contains(r.logs.String(), "method=oidc_token") {
		t.Errorf("the log does not carry the unmapped method:\n%s", r.logs.String())
	}
}
