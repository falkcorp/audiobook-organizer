// file: core/internal/executor/actor_credential_test.go
// version: 1.0.0
// guid: 2e7c9a15-4d03-48b6-9f8e-c1a5d3b70e92
// last-edited: 2026-11-28

package executor

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/tasks"
)

// TestActorRecord_CredentialIDIsStoredWhole: the executor persists the actor a
// start was stamped with, CredentialID included, and reads it back unchanged.
func TestActorRecord_CredentialIDIsStoredWhole(t *testing.T) {
	h := newHarness(t, []tasks.Definition{taskDef("gotham.stakeout", okBody)}, nil)
	by := tasks.Actor{UserID: "bruce", AuthMethod: "token", CredentialID: "key-batphone"}
	id := h.start("gotham.stakeout", tasks.StartRequest{Params: mustParams(t, "docks"), Actor: by})
	if got := h.get(id).Actor; got != by {
		t.Fatalf("stored actor = %+v, want %+v", got, by)
	}
	if got := h.waitState(id, tasks.StateSucceeded).Actor; got != by {
		t.Fatalf("actor after the run = %+v, want %+v", got, by)
	}
}

// TestActorCredential_NotPartOfWhoTheActorIs: a same-actor rule is about the
// person and method, so a different key id does not make two actors different.
// The starter may still cancel its own run through another of its keys, a
// SameActorOnly start made through another key still folds (into a running run
// and into a queued one, which are two code paths), and another user is still
// refused on all of them. An actor with only a credential id names no one.
func TestActorCredential_NotPartOfWhoTheActorIs(t *testing.T) {
	bruce := tasks.Actor{UserID: "bruce", AuthMethod: "token", CredentialID: "key-batphone"}
	bruceAgain := tasks.Actor{UserID: "bruce", AuthMethod: "token", CredentialID: "key-batmobile"}
	selina := tasks.Actor{UserID: "selina", AuthMethod: "token", CredentialID: "key-batphone"}

	started := make(chan tasks.RunID, 4)
	def := taskDef("gotham.stakeout", blockUntilCtx(started), func(c *tasks.Common) { c.Dedupe = tasks.DedupePolicy{SameActorOnly: true} })
	h := newHarness(t, []tasks.Definition{def}, nil)
	id := h.start("gotham.stakeout", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "narrows"), Actor: bruce})
	<-started
	rec := h.waitState(id, tasks.StateRunning)
	start := func(c string, by tasks.Actor) tasks.RunRef {
		t.Helper()
		ref, err := h.x.Start(t.Context(), "gotham.stakeout", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, c), Actor: by})
		if err != nil {
			t.Fatalf("start %s: %v", c, err)
		}
		return ref
	}

	// Into the RUNNING run (start.go's running-fold path).
	if ref := start("narrows", bruceAgain); ref.ID != id || !ref.Existing {
		t.Errorf("SameActorOnly start through another key while it runs = %+v; want folded into %s", ref, id)
	}
	if ref := start("narrows", selina); ref.ID == id || ref.Existing {
		t.Errorf("SameActorOnly start by another user while it runs = %+v; want a new run", ref)
	}

	// Into a QUEUED run (start.go's queued-fold path): other params queue behind
	// the running run, started through one key and folded through another.
	queued := start("docks", bruce)
	if queued.ID == id || queued.Existing {
		t.Fatalf("a start with other params = %+v; want a new queued run", queued)
	}
	if ref := start("docks", bruceAgain); ref.ID != queued.ID || !ref.Existing {
		t.Errorf("SameActorOnly start through another key into a queued run = %+v; want folded into %s", ref, queued.ID)
	}
	if ref := start("docks", selina); ref.ID == queued.ID || ref.Existing {
		t.Errorf("SameActorOnly start by another user into a queued run = %+v; want a new run", ref)
	}

	nobody := grants{}
	if !h.e.mayCancel(rec, bruceAgain, nobody) {
		t.Error("the starter, through another key, may not cancel its own run")
	}
	if h.e.mayCancel(rec, selina, nobody) {
		t.Error("another user may cancel a run it did not start")
	}
	if h.e.mayCancel(rec, tasks.Actor{}, nobody) {
		t.Error("no identity may cancel the run")
	}
	// A credential id alone is no identity: a record with an empty actor is not
	// its starter's.
	if h.e.mayCancel(tasks.RunRecord{}, tasks.Actor{CredentialID: "key-batphone"}, nobody) {
		t.Error("an actor with only a credential id may cancel a run whose actor is empty")
	}
}

// TestRunView_CarriesNoCredentialID: the stored record keeps the key id, the
// run's OWN rc.Actor() has it (the diagnostics export resolves its viewer from
// it), and no read model does: Client.Get, Runs and Timeline return an Actor
// without it, so a module cannot learn which key started another run.
func TestRunView_CarriesNoCredentialID(t *testing.T) {
	by := tasks.Actor{UserID: "bruce", AuthMethod: "token", CredentialID: "key-batphone"}
	own := make(chan tasks.Actor, 1)
	h := newHarness(t, []tasks.Definition{taskDef("gotham.stakeout", func(rc *tasks.RC, _ noParams) (string, error) {
		own <- rc.Actor()
		return "case closed", nil
	})}, nil)
	id := h.start("gotham.stakeout", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "docks"), Actor: by})
	if got := <-own; got != by {
		t.Errorf("rc.Actor() = %+v, want the run's own actor %+v", got, by)
	}
	if got := h.waitState(id, tasks.StateSucceeded).Actor; got != by {
		t.Fatalf("stored actor = %+v, want %+v", got, by)
	}
	want := tasks.Actor{UserID: "bruce", AuthMethod: "token"}
	got, err := h.x.Get(t.Context(), id)
	if err != nil || got.Actor != want {
		t.Errorf("Get actor = %+v, %v; want %+v", got.Actor, err, want)
	}
	n := 0
	for v, err := range h.x.Runs(t.Context(), tasks.RunQuery{Kinds: []tasks.Kind{"gotham.stakeout"}}) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if v.Actor != want {
			t.Errorf("Runs actor = %+v, want %+v", v.Actor, want)
		}
	}
	if n != 1 {
		t.Errorf("Runs returned %d runs, want 1", n)
	}
	tl, err := h.x.Timeline(t.Context(), tasks.TimelineQuery{Kinds: []tasks.Kind{"gotham.stakeout"}})
	if err != nil || len(tl) != 1 || tl[0].Actor != want {
		t.Errorf("Timeline = %+v, %v; want one run with actor %+v", tl, err, want)
	}
}

// TestActorCredentialID_NeverLoggedAtInfo: the executor's logs, at info level
// and above, carry no credential id through a start, a run, a cancel, a pause
// and an unpause made by an actor that has one, nor through a run that fails.
func TestActorCredentialID_NeverLoggedAtInfo(t *testing.T) {
	const secretish = "key-SENTINEL-batphone"
	var buf syncBuffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	started := make(chan tasks.RunID, 2)
	defs := []tasks.Definition{
		taskDef("gotham.stakeout", blockUntilCtx(started)),
		taskDef("gotham.fails", func(*tasks.RC, noParams) (string, error) { return "", errors.New("the signal broke") }),
	}
	h := newHarness(t, defs, func(o *Options) { o.Logger = logger })
	by := tasks.Actor{UserID: "bruce", AuthMethod: "token", CredentialID: secretish}

	id := h.start("gotham.stakeout", tasks.StartRequest{Params: mustParams(t, "docks"), Actor: by})
	<-started
	h.waitState(id, tasks.StateRunning)
	if err := h.e.CancelAs(t.Context(), id, by); err != nil {
		t.Fatal(err)
	}
	h.waitState(id, tasks.StateCanceled)
	h.waitState(h.start("gotham.fails", tasks.StartRequest{Params: mustParams(t, "bowery"), Actor: by}), tasks.StateFailed)
	if err := h.e.PauseAs("Bat-Signal maintenance", by); err != nil {
		t.Fatal(err)
	}
	if err := h.e.UnpauseAs(by); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); strings.Contains(got, secretish) || strings.Contains(strings.ToLower(got), "credentialid") {
		t.Errorf("an info-or-above log line carries the credential id:\n%s", got)
	}
}

// syncBuffer is a log sink the executor's goroutines write to concurrently.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
