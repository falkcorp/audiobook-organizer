// file: internal/books/appconfig/firstrun_test.go
// version: 1.0.0
// guid: 91e4b7c2-6a38-4d05-8f1b-c20d3e5a7946
// last-edited: 2026-11-19

package appconfig

import (
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// setupPrincipal is who the settings handler builds in the first-run window: no
// user, no session, no admin role, holding only config.FirstRunPerm.
var setupPrincipal = config.Actor{Can: func(p string) bool { return p == config.FirstRunPerm }}

// TestFirstRun_WizardKeysNeedNoAdmin: the wizard's save (library path, playlists,
// setup_complete) is written by the window's actor: the library path, an admin
// field, reaches the blob, and core sees an actor that meets its keys'
// protection for exactly that write.
func TestFirstRun_WizardKeysNeedNoAdmin(t *testing.T) {
	e, f := foldEngine(t, `[]`)
	// The playlist directory's parent must exist, as it had to for HEAD's PUT.
	lib := t.TempDir()
	res, err := e.Update(t.Context(), setupPrincipal, set("root_dir", strconv.Quote(lib), "playlist_dir", strconv.Quote(lib+"/playlists"), "setup_complete", `true`))
	if err != nil {
		t.Fatal(err)
	}
	if e.Snapshot().RootDir != lib || !e.Snapshot().SetupComplete || !slices.Contains(res.Applied, "root_dir") {
		t.Fatalf("root=%q setup_complete=%v applied=%v", e.Snapshot().RootDir, e.Snapshot().SetupComplete, res.Applied)
	}
	if len(f.actors) != 1 || !f.actors[0].Interactive || !f.actors[0].Can("core:admin") {
		t.Fatalf("core saw %+v; want an actor that meets the key's protection", f.actors)
	}
}

// TestFirstRun_OnlyTheWizardsKeys: the window opens nothing else. An update that
// names the library path next to a key the Engine guards itself is refused with
// nothing written; one that names a key outside the list that core guards reaches
// core as the caller they are (not the window's elevated actor), so core refuses
// it.
func TestFirstRun_OnlyTheWizardsKeys(t *testing.T) {
	t.Run("engine-guarded key beside the library path", func(t *testing.T) {
		for name, u := range map[string]config.Update{
			"a fold key":     set("root_dir", `"/srv/library"`, "oauth_enabled", `true`),
			"a clear":        {Unset: []string{"path_aliases"}},
			"a clear beside": {Set: set("root_dir", `"/srv/library"`).Set, Unset: []string{"oauth_enabled"}},
		} {
			e, f := foldEngine(t, `[]`)
			_, err := e.Update(t.Context(), setupPrincipal, u)
			if !errors.Is(err, coreconfig.ErrForbidden) {
				t.Fatalf("%s: err = %v; want ErrForbidden", name, err)
			}
			if len(f.applied) != 0 || e.Snapshot().RootDir != "" {
				t.Fatalf("%s: a refused update wrote", name)
			}
		}
	})
	t.Run("core-guarded key reaches core unelevated", func(t *testing.T) {
		for name, u := range map[string]config.Update{
			"another core key":      set("enable_auth", `false`),
			"a wizard key beside":   set("playlist_dir", `"/srv/p"`, "scan_on_startup", `true`),
			"a plain key":           set("scan_on_startup", `true`),
			"a wizard key and auth": set("root_dir", `"/srv/library"`, "enable_auth", `false`),
		} {
			e, f := foldEngine(t, `[]`)
			_, err := e.Update(t.Context(), setupPrincipal, u)
			if name == "a wizard key and auth" {
				if !errors.Is(err, coreconfig.ErrForbidden) {
					t.Fatalf("%s: err = %v", name, err)
				}
				continue
			}
			if err != nil {
				continue // refused before core: also fine
			}
			for _, a := range f.actors {
				if a.Can != nil && a.Can("core:admin") {
					t.Fatalf("%s: core saw an elevated actor for an update outside the window's keys", name)
				}
			}
		}
	})
}

// TestFirstRun_NeedsTheWindow: without config.FirstRunPerm (a user exists, or
// setup is done) the wizard's admin key is refused like any other, for an actor
// with no admin role and for one without a session.
func TestFirstRun_NeedsTheWindow(t *testing.T) {
	for name, a := range map[string]config.Actor{
		"no principal":        {},
		"an editor's session": {Subject: "user:robin", Interactive: true, Can: func(string) bool { return false }},
		"an admin API key":    {Subject: "user:alfred", Interactive: false, Can: func(p string) bool { return p != config.FirstRunPerm }},
	} {
		t.Run(name, func(t *testing.T) {
			e, f := foldEngine(t, `[]`)
			_, err := e.Update(t.Context(), a, set("root_dir", `"/srv/library"`))
			if !errors.Is(err, coreconfig.ErrForbidden) || len(f.applied) != 0 {
				t.Fatalf("err = %v applied=%d", err, len(f.applied))
			}
		})
	}
}

// TestFirstRun_AdminKeepsItsOwnActor: an admin writing the wizard's keys is not
// rewritten into the window's actor: core sees who they are.
func TestFirstRun_AdminKeepsItsOwnActor(t *testing.T) {
	e, f := foldEngine(t, `[]`)
	if _, err := e.Update(t.Context(), adminActor, set("root_dir", `"/srv/library"`, "setup_complete", `true`)); err != nil {
		t.Fatal(err)
	}
	if len(f.actors) != 1 || f.actors[0].Subject != adminActor.Subject || !f.actors[0].Owner {
		t.Fatalf("core saw %+v", f.actors)
	}
}

// TestFirstRun_KeyListIsExact pins the list to what the wizard writes.
func TestFirstRun_KeyListIsExact(t *testing.T) {
	want := []string{"root_dir", "playlist_dir", "setup_complete", "openai_api_key", "enable_ai_parsing"}
	if len(firstRunKeys) != len(want) {
		t.Fatalf("firstRunKeys = %v; want %v", firstRunKeys, want)
	}
	for i := range want {
		if firstRunKeys[i] != want[i] {
			t.Fatalf("firstRunKeys = %v; want %v", firstRunKeys, want)
		}
	}
}

// TestFirstRun_ActorIsNotTheOwner: the actor core sees for a wizard write is
// interactive and holds every permission, but is never the owner: the window
// opens nothing that needs the owner's proof, and an owner set from the
// environment before the first user exists keeps its keys.
func TestFirstRun_ActorIsNotTheOwner(t *testing.T) {
	a := firstRunActor(config.Actor{Subject: "first-run", Can: func(p string) bool { return p == config.FirstRunPerm }})
	if !a.Interactive || a.Owner || a.Can == nil || !a.Can(adminPerm) || a.Subject != "first-run" {
		t.Fatalf("window actor = %+v", a)
	}
}
