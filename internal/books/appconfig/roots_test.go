// file: internal/books/appconfig/roots_test.go
// version: 0.1.0
// guid: c65fd2ed-c37e-476d-9ae8-7e04eb814273
// last-edited: 2026-11-21

package appconfig

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreconfig "github.com/falkcorp/mediacore/config"
)

// The library roots before M6 (05-config 1.12.1 "Class-i fields"; 05-library
// 5L.4 step 1): root_dir and path_aliases are class i, which M2 leaves in the
// settings blob and M6 (F7) moves into core.library.roots. Until then the
// blob is where they are read and written; core.library.roots only overlays
// them when an operator layer sets it.

const operatorRoots = `[{"id":"books-library","path":"/srv/blackgate","role":"library","owner":"books"}]`

func withBlob(kv ...string) func(*fakeAccess, *fakeResidual) {
	return func(_ *fakeAccess, r *fakeResidual) {
		for i := 0; i < len(kv); i += 2 {
			r.vals[kv[i]] = json.RawMessage(kv[i+1])
		}
	}
}

func withRoots(list, lockedBy string) func(*fakeAccess, *fakeResidual) {
	return func(f *fakeAccess, _ *fakeResidual) {
		f.vals[libraryRootsKey] = json.RawMessage(list)
		if lockedBy != "" {
			f.locked[libraryRootsKey] = lockedBy
		}
	}
}

func keyView(t *testing.T, e *Engine, name string) config.KeyView {
	t.Helper()
	for _, k := range e.View().Keys {
		if k.Name == name {
			return k
		}
	}
	t.Fatalf("the view lists no %s", name)
	return config.KeyView{}
}

// TestRoots_TheBlobIsTheirHome: the projection reads root_dir and path_aliases
// from the blob, shows them unlocked and admin, and does not read a stored
// core.library.roots (nothing books writes before M6 puts one there, and M6
// stages the list from the blob, so it is no source of truth): with the blob
// empty the root is empty even when that key holds one.
func TestRoots_TheBlobIsTheirHome(t *testing.T) {
	stored := `[{"id":"books-library","path":"/srv/core-stored","aliases":[{"windows":"Y:\\core","unc":"","smb_url":""}]}]`
	e, _, _ := foldEngineRes(t, `[]`, withRoots(stored, ""),
		withBlob("root_dir", `"/srv/gotham"`, "path_aliases", `[{"root":"/srv/gotham","windows":"Z:\\gotham","unc":"","smb_url":""}]`))
	got := e.Snapshot()
	if got.RootDir != "/srv/gotham" || len(got.PathAliases) != 1 || got.PathAliases[0].Windows != `Z:\gotham` {
		t.Fatalf("root=%q aliases=%+v, want the blob's", got.RootDir, got.PathAliases)
	}
	for _, name := range []string{"root_dir", "path_aliases"} {
		if k := keyView(t, e, name); k.LockedBy != "" || !k.Admin {
			t.Errorf("%s: %+v, want unlocked and admin", name, k)
		}
	}
	if v := keyView(t, e, "root_dir").Value; string(v) != `"/srv/gotham"` {
		t.Errorf("view root_dir = %s", v)
	}

	e, _, _ = foldEngineRes(t, `[]`, withRoots(stored, ""))
	if got := e.Snapshot(); got.RootDir != "" || len(got.PathAliases) != 0 {
		t.Fatalf("an empty blob projected root=%q aliases=%+v from a stored core.library.roots", got.RootDir, got.PathAliases)
	}
}

// TestRoots_WritesLandInTheBlob: a save of root_dir or path_aliases is a blob
// write and never a core one (M6 overwrites core.library.roots from the blob at
// F7); "" is HEAD's "no root"; a root M6 would quarantine (relative, or holding
// a NUL) is refused naming root_dir, with nothing written anywhere.
func TestRoots_WritesLandInTheBlob(t *testing.T) {
	e, f, res := foldEngineRes(t, `[]`, withRoots(`[]`, ""))
	if _, err := e.Update(t.Context(), adminActor, set("root_dir", `"/srv/library"`, "path_aliases", `[{"root":"/srv/library","windows":"Z:\\lib","unc":"","smb_url":""}]`)); err != nil {
		t.Fatal(err)
	}
	if string(res.vals["root_dir"]) != `"/srv/library"` || !strings.Contains(string(res.vals["path_aliases"]), `Z:\\lib`) {
		t.Fatalf("blob after the save: %v", res.vals)
	}
	if len(f.applied) != 0 || string(f.vals[libraryRootsKey]) != `[]` {
		t.Fatalf("a root save wrote core: applied=%d roots=%s", len(f.applied), f.vals[libraryRootsKey])
	}
	if got := e.Snapshot(); got.RootDir != "/srv/library" || len(got.PathAliases) != 1 {
		t.Fatalf("projection root=%q aliases=%+v", got.RootDir, got.PathAliases)
	}
	// A root-only save keeps the stored aliases (HEAD's PUT merged one field).
	if _, err := e.Update(t.Context(), adminActor, set("root_dir", `"/srv/arkham"`)); err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot(); got.RootDir != "/srv/arkham" || len(got.PathAliases) != 1 {
		t.Fatalf("after a root-only save: root=%q aliases=%+v", got.RootDir, got.PathAliases)
	}
	if _, err := e.Update(t.Context(), adminActor, set("path_aliases", `[]`)); err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot(); len(got.PathAliases) != 0 || got.RootDir != "/srv/arkham" {
		t.Fatalf("after clearing aliases: root=%q aliases=%+v", got.RootDir, got.PathAliases)
	}

	saves := res.saves
	for _, bad := range []string{`"relative/dir"`, `"/srv/lib\u0000rary"`, `" /srv/padded"`} {
		_, err := e.Update(t.Context(), adminActor, set("root_dir", bad))
		var fe *coreconfig.FieldError
		if !errors.Is(err, coreconfig.ErrInvalid) || !errors.As(err, &fe) || fe.Path != "root_dir" {
			t.Errorf("root_dir %s: err = %v, want ErrInvalid naming root_dir", bad, err)
		}
	}
	if res.saves != saves || e.Snapshot().RootDir != "/srv/arkham" {
		t.Fatalf("a refused root wrote: saves %d -> %d, root %q", saves, res.saves, e.Snapshot().RootDir)
	}
	for _, blank := range []string{`""`, `"   "`} {
		if _, err := e.Update(t.Context(), adminActor, set("root_dir", blank)); err != nil {
			t.Fatalf("root_dir %s (no root): %v", blank, err)
		}
	}
	if _, err := e.Update(t.Context(), adminActor, config.Update{Unset: []string{"root_dir"}}); err != nil {
		t.Fatal(err)
	}
	if _, has := res.vals["root_dir"]; has || e.Snapshot().RootDir != "" {
		t.Fatalf("an unset left root_dir %s", res.vals["root_dir"])
	}
}

// TestRoots_OperatorLayerOverlaysAndLocks: an operator layer that sets
// core.library.roots (ABO_CORE_LIBRARY_ROOTS, ROOT_DIR's canonical name, or the
// operator file) is authoritative over the blob: its books-library path is
// root_dir, its aliases are path_aliases (none here, so the blob's are not
// shown), both are locked by it, and a save of either is refused with nothing
// written.
func TestRoots_OperatorLayerOverlaysAndLocks(t *testing.T) {
	const by = "env ABO_CORE_LIBRARY_ROOTS"
	e, f, res := foldEngineRes(t, `[]`, withRoots(operatorRoots, by),
		withBlob("root_dir", `"/srv/gotham"`, "path_aliases", `[{"root":"/srv/gotham","windows":"Z:\\gotham","unc":"","smb_url":""}]`))
	got := e.Snapshot()
	if got.RootDir != "/srv/blackgate" || len(got.PathAliases) != 0 {
		t.Fatalf("root=%q aliases=%+v, want the operator's", got.RootDir, got.PathAliases)
	}
	if l := e.Locks(); l["root_dir"] != by || l["path_aliases"] != by {
		t.Fatalf("locks = %v", l)
	}
	if k := keyView(t, e, "root_dir"); k.LockedBy != by || string(k.Value) != `"/srv/blackgate"` {
		t.Fatalf("view root_dir = %+v", k)
	}
	for _, u := range []config.Update{
		set("root_dir", `"/srv/arkham"`),
		set("path_aliases", `[]`),
		{Unset: []string{"root_dir"}},
	} {
		_, err := e.Update(t.Context(), adminActor, u)
		var locked *coreconfig.LockedError
		if !errors.As(err, &locked) || locked.By != by {
			t.Errorf("%+v over the operator's roots: err = %v, want a LockedError by %s", u, err, by)
		}
	}
	if res.saves != 0 || len(f.applied) != 0 {
		t.Fatalf("a refused save wrote: blob saves %d, core applies %d", res.saves, len(f.applied))
	}

	// An operator list with no books-library entry is still the effective
	// list (as it is after M6, when the projection reads core.library.roots):
	// root_dir is empty, and locked, not the blob's.
	e, _, _ = foldEngineRes(t, `[]`, withRoots(`[]`, by), withBlob("root_dir", `"/srv/gotham"`))
	if got := e.Snapshot().RootDir; got != "" || e.Locks()["root_dir"] != by {
		t.Fatalf("an operator list without books-library: root=%q locks=%v", got, e.Locks())
	}

	// The operator's aliases come through, each with its root's path.
	withAliases := `[{"id":"books-library","path":"/srv/blackgate","aliases":[{"windows":"B:\\gate","unc":"","smb_url":""}]}]`
	e, _, _ = foldEngineRes(t, `[]`, withRoots(withAliases, by))
	if got := e.Snapshot().PathAliases; len(got) != 1 || got[0].Root != "/srv/blackgate" || got[0].Windows != `B:\gate` {
		t.Fatalf("operator aliases = %+v", got)
	}
}

// TestRoots_NoResidualNoRoots: an Engine without a Residual projects no class-i
// row, so an operator's roots have nothing to overlay and root_dir is not a
// setting it lists.
func TestRoots_NoResidualNoRoots(t *testing.T) {
	f := newFake()
	f.vals[libraryRootsKey] = json.RawMessage(operatorRoots)
	f.locked[libraryRootsKey] = "env ABO_CORE_LIBRARY_ROOTS"
	e := New(f, nil)
	if err := e.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := e.Snapshot().RootDir; got != "" {
		t.Fatalf("root = %q with no residual", got)
	}
	if _, listed := e.byHead["root_dir"]; listed {
		t.Fatal("root_dir is a row of an Engine with no residual")
	}
}
