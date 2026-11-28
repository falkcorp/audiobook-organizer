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
// person, so a different session or key id does not make two actors different.
// A new sign-in still lets the starter cancel its own run, and SameActorOnly
// still folds a start made through another credential; another user is
// still refused on both.
func TestActorCredential_NotPartOfWhoTheActorIs(t *testing.T) {
	bruce := tasks.Actor{UserID: "bruce", AuthMethod: "session", CredentialID: "sess-one"}
	bruceAgain := tasks.Actor{UserID: "bruce", AuthMethod: "session", CredentialID: "sess-two"}
	selina := tasks.Actor{UserID: "selina", AuthMethod: "session", CredentialID: "sess-one"}

	started := make(chan tasks.RunID, 4)
	def := taskDef("gotham.stakeout", blockUntilCtx(started), func(c *tasks.Common) { c.Dedupe = tasks.DedupePolicy{SameActorOnly: true} })
	h := newHarness(t, []tasks.Definition{def}, nil)
	id := h.start("gotham.stakeout", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "narrows"), Actor: bruce})
	<-started
	rec := h.waitState(id, tasks.StateRunning)

	ref, err := h.x.Start(t.Context(), "gotham.stakeout", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "narrows"), Actor: bruceAgain})
	if err != nil || ref.ID != id || !ref.Existing {
		t.Errorf("SameActorOnly start through another credential = %+v, %v; want folded into %s", ref, err, id)
	}
	ref, err = h.x.Start(t.Context(), "gotham.stakeout", tasks.StartRequest{Mode: tasks.ModeRequestLive, Params: mustParams(t, "narrows"), Actor: selina})
	if err != nil || ref.ID == id || ref.Existing {
		t.Errorf("SameActorOnly start by another user = %+v, %v; want a new run", ref, err)
	}

	nobody := grants{}
	if !h.e.mayCancel(rec, bruceAgain, nobody) {
		t.Error("the starter, signed in again, may not cancel its own run")
	}
	if h.e.mayCancel(rec, selina, nobody) {
		t.Error("another user may cancel a run it did not start")
	}
	if h.e.mayCancel(rec, tasks.Actor{}, nobody) {
		t.Error("no identity may cancel the run")
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
