// file: core/library/scope_roots_test.go
// version: 1.0.0
// guid: 477893f7-2318-41f3-9e4e-75d432dcf610
// last-edited: 2026-11-11

package library

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/storage"
)

// scopeRig is a books module's world: its own library root, a core-owned
// download root it may read (and, being a download root, move out of), and a tv
// root it may not read at all.
func scopeRig(t *testing.T) (*rig, Scope) {
	t.Helper()
	caps := func(o storage.Owner, c string) bool { return c == CapabilityDownload && o == books }
	r := newRig(t, newStore(t), rigOpts{caps: caps},
		spec("books-library", books, RoleLibrary), spec("dl-qbit-1", CoreOwner, RoleDownload), spec("tv-library", tv, RoleLibrary))
	sc, err := NewScope(r.m, books)
	if err != nil {
		t.Fatal(err)
	}
	return r, sc
}

func moduleRootByID(t *testing.T, sc Scope, id RootID) *ModuleRoot {
	t.Helper()
	for _, mr := range sc.Roots(RootFilter{}) {
		if mr.ID() == id {
			return mr
		}
	}
	t.Fatalf("the scope did not list %s", id)
	return nil
}

// A module that walks Scope.Roots and calls everything it finds still acts only
// as its own owner. The review probe called Roots()[i].FS(CoreOwner).Remove and
// Roots()[i].AppDir("evil") on a root the module could only read.
func TestScopeRootsActOnlyAsTheScopesOwner(t *testing.T) {
	r, sc := scopeRig(t)
	seed := r.put("dl-qbit-1", "complete/Batman Begins.m4b", "seeding bytes")
	roots := sc.Roots(RootFilter{})
	var ids []RootID
	for _, mr := range roots {
		ids = append(ids, mr.ID())
	}
	if want := []RootID{"books-library", "dl-qbit-1"}; !slices.Equal(ids, want) {
		t.Fatalf("Roots = %v, want %v (tv-library is not the module's to read)", ids, want)
	}

	dl := moduleRootByID(t, sc, "dl-qbit-1")
	fsys, err := dl.FS()
	if err != nil {
		t.Fatal(err)
	}
	if err := fsys.Remove(seed.Rel); !errors.Is(err, ErrReadOnlyRoot) {
		t.Fatalf("remove through the download root's FS = %v, want ErrReadOnlyRoot", err)
	}
	if _, err := fsys.Open(seed.Rel); err != nil {
		t.Errorf("a reader of the download root cannot open its file: %v", err)
	}
	if !r.exists("dl-qbit-1", seed.Rel) {
		t.Fatal("the seeding file was deleted")
	}
	if _, err := dl.AppDir(ctxbg()); !errors.Is(err, ErrReadOnlyRoot) {
		t.Fatalf("AppDir on a root the module only reads = %v, want ErrReadOnlyRoot", err)
	}
	if r.exists("dl-qbit-1", DefaultAppDir+"/books") {
		t.Error("AppDir created a directory in a root the module cannot write")
	}

	own := moduleRootByID(t, sc, "books-library")
	app, err := own.AppDir(ctxbg())
	if err != nil {
		t.Fatalf("AppDir on its own root: %v", err)
	}
	f, err := app.CreateExclusive("report.json", 0o644)
	if err != nil {
		t.Fatalf("write in its own AppDir: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if !r.exists("books-library", DefaultAppDir+"/books/report.json") {
		t.Error("the module's AppDir is not <root>/<AppDir>/<owner>")
	}
	// The directory is the module's: the sentinel one level up is out of reach.
	if _, err := app.Stat("../root.json"); err == nil {
		t.Error("the module's AppDir reaches the sentinel through ..")
	}
	if _, err := app.Stat("root.json"); err == nil {
		t.Error("the module's AppDir shows the sentinel")
	}
	if err := app.Remove("../root.json"); err == nil || !r.exists("books-library", DefaultAppDir+"/"+sentinelName) {
		t.Errorf("remove of the sentinel through the module's AppDir = %v", err)
	}
}

// The owner is the module's own at every depth: another module's scope lists the
// same root differently and its AppDir lands beside, not inside, the first's.
func TestModuleRootAppDirIsNamedForTheOwner(t *testing.T) {
	r := newRig(t, newStore(t), rigOpts{}, spec("books-library", books, RoleLibrary), spec("shared-lib", "core/tasks", RoleLibrary))
	sc, err := NewScope(r.m, "core/tasks")
	if err != nil {
		t.Fatal(err)
	}
	app, err := moduleRootByID(t, sc, "shared-lib").AppDir(ctxbg())
	if err != nil {
		t.Fatal(err)
	}
	f, err := app.CreateExclusive("x", 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	// "core/tasks" is one path component: a module named "core" has its own
	// directory and not the parent of this one.
	if !r.exists("shared-lib", DefaultAppDir+"/core%2Ftasks/x") {
		t.Error("an owner with a slash did not get one escaped directory")
	}
	if r.exists("shared-lib", DefaultAppDir+"/core") {
		t.Error("an owner with a slash created a directory for the owner it extends")
	}
}

// A disabled owner may not create anything under the root.
func TestModuleRootAppDirNeedsAnEnabledOwner(t *testing.T) {
	on := true
	r := newRig(t, newStore(t), rigOpts{enabled: func(storage.Owner) bool { return on }}, spec("books-library", books, RoleLibrary))
	sc, err := NewScope(r.m, books)
	if err != nil {
		t.Fatal(err)
	}
	mr := moduleRootByID(t, sc, "books-library")
	on = false
	if _, err := mr.AppDir(ctxbg()); !errors.Is(err, ErrReadOnlyRoot) {
		t.Fatalf("AppDir of a disabled owner = %v, want ErrReadOnlyRoot", err)
	}
	if r.exists("books-library", DefaultAppDir+"/books") {
		t.Error("a disabled owner got a directory")
	}
}

// A ModuleRoot is only ever made by Scope.Roots; the zero value answers with
// errors rather than panicking, and a nil one says it has no ID.
func TestModuleRootZeroValueIsInert(t *testing.T) {
	for name, mr := range map[string]*ModuleRoot{"zero": new(ModuleRoot), "nil": nil} {
		if mr.ID() != "" {
			t.Errorf("%s: ID = %q", name, mr.ID())
		}
		if _, err := mr.FS(); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%s: FS = %v", name, err)
		}
		if _, err := mr.AppDir(ctxbg()); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%s: AppDir = %v", name, err)
		}
		if _, err := mr.Usage(ctxbg()); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%s: Usage = %v", name, err)
		}
		if err := mr.Mounted(ctxbg()); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%s: Mounted = %v", name, err)
		}
		if err := mr.Disabled(); !errors.Is(err, ErrInvalidOptions) {
			t.Errorf("%s: Disabled = %v", name, err)
		}
		if mr.Spec().ID != "" {
			t.Errorf("%s: Spec = %+v", name, mr.Spec())
		}
	}
}

// Nothing a module can reach from its Scope names another owner as a parameter
// or hands back a root's full-rights driver: no method of Scope or ModuleRoot
// takes a storage.Owner, and none returns a *Root, a Root, the driver
// interface or an os.Root. The check walks the exported method sets with
// reflection, so a method added later is covered without anyone remembering to.
func TestModuleSurfaceHasNoCallerOrDriverHandle(t *testing.T) {
	banned := []reflect.Type{
		reflect.TypeFor[*Root](), reflect.TypeFor[Root](), reflect.TypeFor[fsOps](),
		reflect.TypeFor[*localFS](), reflect.TypeFor[localFS](), reflect.TypeFor[*os.Root](), reflect.TypeFor[Manager](),
	}
	var reaches func(t reflect.Type, seen map[reflect.Type]bool) (reflect.Type, bool)
	reaches = func(ty reflect.Type, seen map[reflect.Type]bool) (reflect.Type, bool) {
		if seen[ty] {
			return nil, false
		}
		seen[ty] = true
		for _, b := range banned {
			if ty == b {
				return b, true
			}
		}
		switch ty.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Chan:
			return reaches(ty.Elem(), seen)
		case reflect.Map:
			if b, ok := reaches(ty.Key(), seen); ok {
				return b, true
			}
			return reaches(ty.Elem(), seen)
		case reflect.Func:
			for i := range ty.NumIn() {
				if b, ok := reaches(ty.In(i), seen); ok {
					return b, true
				}
			}
			for i := range ty.NumOut() {
				if b, ok := reaches(ty.Out(i), seen); ok {
					return b, true
				}
			}
		}
		return nil, false
	}
	owner := reflect.TypeFor[storage.Owner]()
	check := func(what string, ty reflect.Type, skipRecv bool) {
		for i := range ty.NumMethod() {
			m := ty.Method(i)
			if !m.IsExported() {
				continue
			}
			first := 0
			if skipRecv {
				first = 1
			}
			for j := first; j < m.Type.NumIn(); j++ {
				if m.Type.In(j) == owner {
					t.Errorf("%s.%s takes a storage.Owner", what, m.Name)
				}
			}
			if b, ok := reaches(m.Type, map[reflect.Type]bool{}); ok {
				t.Errorf("%s.%s reaches %v", what, m.Name, b)
			}
		}
	}
	check("Scope", reflect.TypeFor[Scope](), false)
	check("ModuleRoot", reflect.TypeFor[*ModuleRoot](), true)
	check("RootFS", reflect.TypeFor[RootFS](), false)
	mr := reflect.TypeFor[ModuleRoot]()
	for f := range mr.Fields() {
		if f.IsExported() {
			t.Errorf("ModuleRoot exports the field %s", f.Name)
		}
	}
}

// AppDir is core's and names a directory of the caller's choosing, but it never
// hands out a writable view of the quarantine area: <AppDir>/quarantine IS the
// area by default, and a module-held file deleted through it leaves the record
// saying it is held.
func TestAppDirRefusesTheQuarantineArea(t *testing.T) {
	r, _ := transferRig(t, rigOpts{})
	victim := r.put("books-library", "Gotham/held.m4b", "held bytes")
	id, err := r.m.Quarantine(ctxbg(), books, victim, "corrupt")
	if err != nil {
		t.Fatal(err)
	}
	root, err := r.m.Root("books-library")
	if err != nil {
		t.Fatal(err)
	}
	held := DefaultQuarantine + "/" + string(id) + "/Gotham/held.m4b"
	for _, name := range []string{"quarantine", "quarantine/" + string(id), "QUARANTINE", "Quarantine/x"} {
		if fsys, err := root.AppDir(ctxbg(), name); !errors.Is(err, ErrReservedPath) || fsys != nil {
			t.Errorf("AppDir(%q) = %v, %v; want ErrReservedPath", name, fsys, err)
		}
	}
	if !r.exists("books-library", held) {
		t.Fatal("the held file is gone")
	}
	// A sibling name is an ordinary app directory.
	app, err := root.AppDir(ctxbg(), "covers")
	if err != nil {
		t.Fatalf("AppDir(covers): %v", err)
	}
	if _, err := app.Stat("."); err != nil {
		t.Error(err)
	}
	// A quarantine area configured outside the AppDir is not a name under it.
	nested := spec("books-nested", books, RoleLibrary)
	nested.AppDir, nested.Quarantine = "state", "state/held"
	rn := newRig(t, newStore(t), rigOpts{}, nested)
	nroot, err := rn.m.Root("books-nested")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nroot.AppDir(ctxbg(), "held"); !errors.Is(err, ErrReservedPath) {
		t.Errorf("AppDir(held) under a nested quarantine = %v, want ErrReservedPath", err)
	}
	if _, err := nroot.AppDir(ctxbg(), "covers"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("AppDir(covers) under a nested quarantine: %v", err)
	}
}
