// file: internal/books/appconfig/undo_test.go
// version: 1.0.0
// guid: 2f7a9c64-1b38-4e05-9d62-c3a8e5b07f14
// last-edited: 2026-11-20

package appconfig

import (
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// undoRig is an Engine whose one Update touches every store an Update writes:
// two core keys (one a stored override, one a default), a registered secret key
// written through Apply, two unregistered secrets put (one replacing a value,
// one new), one unregistered secret cleared, and a residual row.
type undoRig struct {
	e                       *Engine
	f                       *fakeAccess
	res                     *fakeResidual
	hardcover, openai, goog secrets.Name
	pwd                     secrets.Name
	update                  config.Update
}

func secretName(t *testing.T, head string) secrets.Name {
	t.Helper()
	row, ok := cutover.Table().Row(head)
	if !ok || len(row.Targets) == 0 {
		t.Fatalf("no row for %s", head)
	}
	return secrets.Name(row.Targets[0].Path)
}

func newUndoRig(t *testing.T) *undoRig {
	t.Helper()
	r := &undoRig{
		f: newFake(), res: newFakeResidual(),
		hardcover: secretName(t, "hardcover_api_token"), openai: secretName(t, "openai_api_key"),
		goog: secretName(t, "google_books_api_key"), pwd: "core.http.basic_gate.password",
	}
	f := r.f
	f.vals["core.log.level"] = json.RawMessage(`"warn"`)
	f.vals["modules.books.scan.on_startup"] = json.RawMessage(`true`)
	f.defs["core.log.level"] = json.RawMessage(`"warn"`)
	f.defs["modules.books.scan.on_startup"] = json.RawMessage(`true`)
	f.regSecret[coreconfig.Path(r.pwd)] = true
	f.secrets[r.pwd] = "riddler"
	f.secrets[r.openai] = "penguin-old"
	f.secrets[r.goog] = "scarecrow"
	r.res.vals["protected_paths"] = json.RawMessage(`["/arkham"]`)
	r.e = New(f, nil, WithResidual(r.res))
	if err := r.e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// log_level starts as a stored override, scan_on_startup as a default.
	if _, err := r.e.Update(t.Context(), adminActor, set("log_level", `"info"`)); err != nil {
		t.Fatal(err)
	}
	r.update = set("log_level", `"debug"`, "scan_on_startup", `false`, "basic_auth_password", `"joker"`,
		"openai_api_key", `"harley"`, "hardcover_api_token", `"bane"`, "protected_paths", `["/blackgate"]`)
	r.update.Unset = []string{"google_books_api_key"}
	return r
}

// state is every store the Update writes, as plain values to compare.
type undoState struct {
	vals    map[coreconfig.Path]string
	over    map[coreconfig.Path]bool
	secrets map[secrets.Name]string
	res     map[string]string
}

func (r *undoRig) state() undoState {
	s := undoState{vals: map[coreconfig.Path]string{}, over: maps.Clone(r.f.over), secrets: maps.Clone(r.f.secrets), res: map[string]string{}}
	for k, v := range r.f.vals {
		s.vals[k] = string(v)
	}
	for k, v := range r.res.vals {
		s.res[k] = string(v)
	}
	return s
}

func (r *undoRig) wantUnchanged(t *testing.T, before undoState) {
	t.Helper()
	after := r.state()
	if !maps.Equal(before.vals, after.vals) || !maps.Equal(before.over, after.over) || !maps.Equal(before.secrets, after.secrets) || !maps.Equal(before.res, after.res) {
		t.Fatalf("a failed Update changed what it should have left alone\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestUpdate_AFailureAtEveryStepLeavesEveryStoreAsItWas: the update fails at each
// of its steps in turn; whatever committed before it is undone, and each
// compensation is asserted on the store it restores.
func TestUpdate_AFailureAtEveryStepLeavesEveryStoreAsItWas(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*undoRig)
		undone func(t *testing.T, r *undoRig) // what only this case's compensations can prove
	}{
		{"the core Apply fails: nothing committed, nothing to undo", func(r *undoRig) { r.f.failApply = 2 /* 1 was the setup write */ },
			func(t *testing.T, r *undoRig) {
				if len(r.f.puts) != 0 || len(r.f.dels) != 0 {
					t.Errorf("a secret was written after the Apply failed: puts %v dels %v", r.f.puts, r.f.dels)
				}
			}},
		{"the first secret put fails: the core keys are put back", func(r *undoRig) { r.f.putFail[r.hardcover] = true },
			func(t *testing.T, r *undoRig) {
				if got := string(r.f.vals["core.log.level"]); got != `"info"` || !r.f.over["core.log.level"] {
					t.Errorf("a stored override was not restored: log_level = %s override %v", got, r.f.over["core.log.level"])
				}
				if r.f.over["modules.books.scan.on_startup"] || string(r.f.vals["modules.books.scan.on_startup"]) != `true` {
					t.Error("a default was not restored by unsetting its override")
				}
				if r.f.secrets[r.pwd] != "riddler" {
					t.Error("a registered secret key written by Apply was not restored")
				}
			}},
		{"the second secret put fails: the first put is deleted again, the core keys restored", func(r *undoRig) { r.f.putFail[r.openai] = true },
			func(t *testing.T, r *undoRig) {
				if !slices.Contains(r.f.dels, r.hardcover) {
					t.Errorf("a secret that was new was not deleted again: dels %v", r.f.dels)
				}
				if r.f.secrets[r.pwd] != "riddler" {
					t.Error("the core Apply was not undone")
				}
			}},
		{"the clear fails: both puts are undone, the replaced secret gets its old value back", func(r *undoRig) { r.f.delFail[r.goog] = true },
			func(t *testing.T, r *undoRig) {
				if r.f.secrets[r.openai] != "penguin-old" {
					t.Errorf("a replaced secret was not restored: %q", r.f.secrets[r.openai])
				}
				if _, still := r.f.secrets[r.hardcover]; still {
					t.Error("a new secret was not deleted")
				}
			}},
		{"the residual save fails: every earlier step is undone, a cleared secret comes back", func(r *undoRig) { r.res.saveEr = errors.New("disk full") },
			func(t *testing.T, r *undoRig) {
				if r.f.secrets[r.goog] != "scarecrow" {
					t.Errorf("a cleared secret was not put back: %q", r.f.secrets[r.goog])
				}
				if r.f.secrets[r.openai] != "penguin-old" {
					t.Errorf("a replaced secret was not restored: %q", r.f.secrets[r.openai])
				}
				if _, still := r.f.secrets[r.hardcover]; still {
					t.Error("a new secret was not deleted")
				}
				if r.f.secrets[r.pwd] != "riddler" || string(r.f.vals["core.log.level"]) != `"info"` {
					t.Error("the core Apply was not undone")
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newUndoRig(t)
			before := r.state()
			c.inject(r)
			gen := r.e.Snapshot()
			_, err := r.e.Update(t.Context(), adminActor, r.update)
			if err == nil {
				t.Fatal("expected the injected failure")
			}
			if errors.Is(err, ErrRollbackIncomplete) {
				t.Fatalf("every compensation was possible, yet: %v", err)
			}
			r.wantUnchanged(t, before)
			c.undone(t, r)
			// The projection says the same: nothing the failed update set is visible.
			now := r.e.Snapshot()
			if now.LogLevel != gen.LogLevel || now.OpenAIAPIKey != gen.OpenAIAPIKey || now.HardcoverAPIToken != gen.HardcoverAPIToken ||
				!slices.Equal(now.ProtectedPaths, gen.ProtectedPaths) {
				t.Errorf("the projection kept part of the failed update: %+v", now)
			}
		})
	}
}

// TestUpdate_UndoesNewestFirstAsTheSameActor: the compensations run in the
// reverse of the order the steps committed, and the core keys are put back as
// the actor that wrote them (the keys it holds are the ones that actor was
// allowed to change).
func TestUpdate_UndoesNewestFirstAsTheSameActor(t *testing.T) {
	r := newUndoRig(t)
	r.res.saveEr = errors.New("disk full")
	r.f.trace = nil
	r.f.actors = nil
	if _, err := r.e.Update(t.Context(), adminActor, r.update); err == nil {
		t.Fatal("expected the residual save to fail")
	}
	// Forward: the core Apply, then the secret puts and the clear; the undo is
	// the mirror of them, newest first. A new secret's undo is its deletion, a
	// replaced one's is its old value put back, a cleared one's is a put.
	if len(r.f.trace) != 8 || r.f.trace[0] != "apply" || r.f.trace[7] != "apply" {
		t.Fatalf("trace = %v", r.f.trace)
	}
	inverse := map[string]string{
		"put " + string(r.hardcover): "del " + string(r.hardcover),
		"put " + string(r.openai):    "put " + string(r.openai),
		"del " + string(r.goog):      "put " + string(r.goog),
	}
	fwd, back := r.f.trace[1:4], r.f.trace[4:7]
	for i, op := range fwd {
		want, ok := inverse[op]
		if !ok {
			t.Fatalf("unexpected forward step %q in %v", op, r.f.trace)
		}
		if got := back[len(back)-1-i]; got != want {
			t.Errorf("undo of %q ran as %q at position %d, want %q (trace %v)", op, got, len(back)-1-i, want, r.f.trace)
		}
	}
	if len(r.f.actors) != 2 || r.f.actors[0].Subject != r.f.actors[1].Subject {
		t.Errorf("the undo ran as a different actor than the write: %+v", r.f.actors)
	}
}

// TestUpdate_AnUndoThatFailsDoesNotStopTheOthers: the newest compensation fails,
// and the older ones still run.
func TestUpdate_AnUndoThatFailsDoesNotStopTheOthers(t *testing.T) {
	r := newUndoRig(t)
	r.res.saveEr = errors.New("disk full")
	r.f.putFail[r.goog] = true // the forward step on goog is a clear; only its undo is a put
	_, err := r.e.Update(t.Context(), adminActor, r.update)
	if !errors.Is(err, ErrRollbackIncomplete) || !strings.Contains(err.Error(), "google_books_api_key") {
		t.Fatalf("the failed undo must be named: %v", err)
	}
	if r.f.secrets[r.openai] != "penguin-old" {
		t.Errorf("an older undo did not run after a newer one failed: openai = %q", r.f.secrets[r.openai])
	}
	if _, still := r.f.secrets[r.hardcover]; still {
		t.Error("the new secret was not deleted after a newer undo failed")
	}
	if r.f.secrets[r.pwd] != "riddler" {
		t.Error("the core keys were not restored after a newer undo failed")
	}
}

// TestUpdate_ARegisteredSecretThatCannotBeReadIsReported: the same for a secret
// that is a core key, written by the core Apply.
func TestUpdate_ARegisteredSecretThatCannotBeReadIsReported(t *testing.T) {
	r := newUndoRig(t)
	r.f.unreadble[r.pwd] = true
	r.res.saveEr = errors.New("disk full")
	_, err := r.e.Update(t.Context(), adminActor, r.update)
	if !errors.Is(err, ErrRollbackIncomplete) || !strings.Contains(err.Error(), string(r.pwd)) {
		t.Fatalf("an unrestorable registered secret must be reported: %v", err)
	}
}

// TestUpdate_ACompletedUpdateWritesEveryStoreOnce: the success path is untouched
// by the undo bookkeeping: one core Apply, the secrets, the residual.
func TestUpdate_ACompletedUpdateWritesEveryStoreOnce(t *testing.T) {
	r := newUndoRig(t)
	applies := len(r.f.applied)
	if _, err := r.e.Update(t.Context(), adminActor, r.update); err != nil {
		t.Fatal(err)
	}
	if len(r.f.applied) != applies+1 {
		t.Errorf("Apply ran %d times for one update", len(r.f.applied)-applies)
	}
	s := r.e.Snapshot()
	if s.LogLevel != "debug" || s.OpenAIAPIKey != "harley" || s.HardcoverAPIToken != "bane" || s.GoogleBooksAPIKey != "" ||
		!slices.Equal(s.ProtectedPaths, []string{"/blackgate"}) {
		t.Errorf("projection after a completed update: %+v", s)
	}
}

// TestUpdate_ACompensationThatFailsIsReportedAndTheRestStillRun: when the undo of
// the core Apply fails, the secrets are still restored and the error says which
// step could not be, joined to the original cause.
func TestUpdate_ACompensationThatFailsIsReportedAndTheRestStillRun(t *testing.T) {
	r := newUndoRig(t)
	r.f.putFail[r.openai] = true
	r.f.failApply = 3 // 1 setup, 2 the update's, 3 its undo
	_, err := r.e.Update(t.Context(), adminActor, r.update)
	if !errors.Is(err, ErrRollbackIncomplete) {
		t.Fatalf("a failed undo must say so: %v", err)
	}
	if !strings.Contains(err.Error(), "the core keys") || !strings.Contains(err.Error(), "storing openai_api_key") {
		t.Errorf("the error names neither the step that could not be undone nor the cause: %v", err)
	}
	if _, still := r.f.secrets[r.hardcover]; still {
		t.Error("the secret undo did not run after the core undo failed")
	}
	for _, leaked := range []string{"harley", "bane", "joker"} {
		if strings.Contains(err.Error(), leaked) {
			t.Errorf("the error carries a secret value %q", leaked)
		}
	}
}

// TestUpdate_ASecretThatCannotBeReadCannotBeRestored: overwriting an unreadable
// record is a repair, but if a later step fails the old value is gone, and the
// error says so rather than claiming a clean undo.
func TestUpdate_ASecretThatCannotBeReadCannotBeRestored(t *testing.T) {
	r := newUndoRig(t)
	r.f.unreadble[r.openai] = true
	r.res.saveEr = errors.New("disk full")
	_, err := r.e.Update(t.Context(), adminActor, r.update)
	if !errors.Is(err, ErrRollbackIncomplete) || !strings.Contains(err.Error(), "openai_api_key") {
		t.Fatalf("an unrestorable secret must be reported: %v", err)
	}
}
