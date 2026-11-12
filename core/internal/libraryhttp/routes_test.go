// file: core/internal/libraryhttp/routes_test.go
// version: 1.0.0
// guid: 7529d641-2d0b-4ac5-8a2c-fb770d54c388
// last-edited: 2026-11-11

package libraryhttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/rbac"
	"github.com/falkcorp/mediacore/storage"
)

// Fixtures are Batman-themed: roots name the Batcave's shelves and folders
// are Gotham places. library.Root is concrete, so the tests build a real
// library.Manager over temp directories, booting storage the way the kernel
// does (storage.Open, Prepare, Migrate) and never importing coretest.

const libOwner storage.Owner = "core/library"

// grants is a fake checker: exactly the scopes it names.
type grants map[rbac.Permission]bool

func (g grants) Has(p rbac.Permission) bool { return g[p] }

func (g grants) Satisfies(r rbac.Requirement) bool {
	ps := r.Permissions()
	switch r.Kind() {
	case rbac.KindAuthenticated:
		return len(g) > 0
	case rbac.KindAllOf:
		for _, p := range ps {
			if !g[p] {
				return false
			}
		}
		return len(ps) > 0
	case rbac.KindAnyOf:
		return slices.ContainsFunc(ps, g.Has)
	}
	return false
}

func (grants) Confined() bool { return false }

func (g grants) Concrete() []rbac.Permission {
	var out []rbac.Permission
	for p := range g {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func session(user string, perms ...rbac.Permission) *auth.Principal {
	g := grants{}
	for _, p := range perms {
		g[p] = true
	}
	return &auth.Principal{User: &auth.User{ID: auth.UserID(user), Username: user}, Method: auth.MethodSession, CredentialID: "cred-" + user, Perms: g}
}

// configSource serves core.library.roots.
type configSource struct{ roots []library.RootSpec }

func (c configSource) Lookup(p config.Path) (config.KeyState, bool) {
	if p != "core.library.roots" {
		return config.KeyState{}, false
	}
	b, err := json.Marshal(c.roots)
	if err != nil {
		panic(err)
	}
	return config.KeyState{Path: p, Effective: b}, true
}

func bootKeyspace(t *testing.T) storage.Keyspace {
	t.Helper()
	ctx := context.Background()
	o, err := storage.Open(ctx, storage.OpenOptions{
		Path:            filepath.Join(t.TempDir(), "main.pebble"),
		Owners:          []storage.OwnerSpec{{Owner: libOwner, Spec: storage.Spec{Keyspace: true}, Enabled: true}},
		Build:           "libraryhttp+fixture",
		FreeSpaceMargin: 1,
		FS:              storage.FSOf(vfs.NewMem()),
	})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db, err := o.Prepare(ctx)
	if err == nil {
		err = db.Migrate(ctx)
	}
	if err != nil {
		_ = o.Close()
		t.Fatalf("boot: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ks, ok := db.Keyspace(libOwner)
	if !ok {
		t.Fatal("no library keyspace")
	}
	return ks
}

// newManager registers specs (each Path defaults to a fresh temp directory,
// recorded in dirs) and writes the sentinel of every non-external root.
func newManager(t *testing.T, specs ...library.RootSpec) (library.Manager, map[library.RootID]string) {
	t.Helper()
	dirs := map[library.RootID]string{}
	for i := range specs {
		if specs[i].Path == "" {
			specs[i].Path = t.TempDir()
		}
		specs[i].Watch = library.WatchOff
		dirs[specs[i].ID] = specs[i].Path
	}
	m, err := library.New(library.Options{Keyspace: bootKeyspace(t), Config: configSource{specs}, InstallID: "install-gotham-1"})
	if err != nil {
		t.Fatalf("library.New: %v", err)
	}
	t.Cleanup(func() { _ = m.(interface{ Close() error }).Close() })
	if err := m.(library.Reloader).Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, s := range specs {
		if _, err := os.Stat(s.Path); s.Role == library.RoleExternal || err != nil {
			continue // an external root has no sentinel; a missing directory registers disabled
		}
		if err := m.(library.SentinelWriter).WriteSentinel(context.Background(), s.ID); err != nil {
			t.Fatalf("sentinel %s: %v", s.ID, err)
		}
	}
	return m, dirs
}

func rootSpec(id string, role library.Role) library.RootSpec {
	return library.RootSpec{ID: library.RootID(id), Name: strings.ToUpper(id[:1]) + id[1:], Owner: "books", Role: role}
}

func mkdirs(t *testing.T, base string, rels ...string) {
	t.Helper()
	for _, r := range rels {
		if err := os.MkdirAll(filepath.Join(base, filepath.FromSlash(r)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func touch(t *testing.T, base, rel string) {
	t.Helper()
	p := filepath.Join(base, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

const scopeOther rbac.Permission = "core:tasks.view"

type rig struct {
	t    *testing.T
	srv  *Server
	dirs map[library.RootID]string
}

func newRig(t *testing.T, specs ...library.RootSpec) *rig {
	t.Helper()
	m, dirs := newManager(t, specs...)
	srv, err := New(Deps{Library: m})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return &rig{t: t, srv: srv, dirs: dirs}
}

type result struct {
	code int
	body []byte
	hdr  http.Header
}

// get serves GET /browse?query as p (nil: no principal).
func (r *rig) get(p *auth.Principal, query url.Values) result {
	r.t.Helper()
	target := BasePath + "/browse"
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	return r.do(p, http.MethodGet, target)
}

func (r *rig) do(p *auth.Principal, method, target string) result {
	req := httptest.NewRequest(method, target, nil)
	if p != nil {
		req = req.WithContext(auth.WithPrincipal(req.Context(), p))
	}
	rec := httptest.NewRecorder()
	r.srv.Handler().ServeHTTP(rec, req)
	return result{code: rec.Code, body: rec.Body.Bytes(), hdr: rec.Header()}
}

func (res result) decode(t *testing.T, into any) {
	t.Helper()
	if err := json.Unmarshal(res.body, into); err != nil {
		t.Fatalf("decode %q: %v", res.body, err)
	}
}

type problemResp struct {
	Code     string `json:"code"`
	Status   int    `json:"status"`
	Required string `json:"required_permission"`
}

func (res result) problem(t *testing.T) problemResp {
	t.Helper()
	if ct := res.hdr.Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type = %q, body %s", ct, res.body)
	}
	var p problemResp
	res.decode(t, &p)
	return p
}

func q(kv ...string) url.Values {
	v := url.Values{}
	for i := 0; i < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

var browser = session("lucius", scopeBrowse)

func names(l listingResponse) []string {
	var out []string
	for _, e := range l.Entries {
		out = append(out, e.Name)
	}
	return out
}

func list(t *testing.T, r *rig, query url.Values) listingResponse {
	t.Helper()
	res := r.get(browser, query)
	if res.code != 200 {
		t.Fatalf("GET %v = %d %s", query, res.code, res.body)
	}
	var l listingResponse
	res.decode(t, &l)
	return l
}

func TestNew_RequiresLibrary(t *testing.T) {
	if _, err := New(Deps{}); !errors.Is(err, errNoLibrary) {
		t.Fatalf("err = %v, want errNoLibrary", err)
	}
}

func TestRoutes_Table(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	routes := r.srv.Routes()
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	rt := routes[0]
	if rt.Method != "GET" || rt.Path != "/browse" || rt.Name != "browseLibrary" || rt.Handler == nil {
		t.Errorf("route = %+v", rt)
	}
	if rt.Perm.Kind() != rbac.KindAllOf || rt.Perm.String() != "core:library.browse" {
		t.Errorf("floor = %v %q", rt.Perm.Kind(), rt.Perm.String())
	}
	if got, want := rt.Pattern(), "GET /api/core/v1/library/browse"; got != want {
		t.Errorf("pattern = %q, want %q", got, want)
	}
	// The returned table is a copy.
	routes[0].Name = "x"
	if r.srv.Routes()[0].Name != "browseLibrary" {
		t.Error("Routes shares its backing array")
	}
}

func TestBrowse_NoPrincipal_401(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	res := r.get(nil, nil)
	if res.code != 401 || res.problem(t).Code != "unauthenticated" {
		t.Fatalf("= %d %s", res.code, res.body)
	}
}

func TestBrowse_WithoutPermission_403(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	for name, p := range map[string]*auth.Principal{
		"other scope": session("harvey", scopeOther),
		"no checker":  {User: &auth.User{ID: "harvey"}, Method: auth.MethodSession},
	} {
		for _, query := range []url.Values{nil, q("root", "books-library")} {
			res := r.get(p, query)
			if res.code != 403 {
				t.Fatalf("%s %v = %d %s", name, query, res.code, res.body)
			}
			pr := res.problem(t)
			if pr.Code != "permission_denied" || pr.Required != "core:library.browse" {
				t.Errorf("%s problem = %+v", name, pr)
			}
		}
	}
}

func TestBrowse_Roots(t *testing.T) {
	r := newRig(t,
		rootSpec("books-library", library.RoleLibrary),
		rootSpec("import-arkham", library.RoleImport),
		library.RootSpec{ID: "dl-batmobile", Owner: library.CoreOwner, Role: library.RoleDownload},
		rootSpec("itunes-wayne", library.RoleExternal),
	)
	touch(t, r.dirs["itunes-wayne"], "Music/track.m4a") // an external root is mounted by content
	res := r.get(browser, nil)
	if res.code != 200 {
		t.Fatalf("= %d %s", res.code, res.body)
	}
	var got rootsResponse
	res.decode(t, &got)
	want := []rootInfo{
		{ID: "books-library", Name: "Books-library", Role: "library", Path: "books-library:"},
		{ID: "dl-batmobile", Name: "dl-batmobile", Role: "download", Path: "dl-batmobile:"}, // no Name: the ID
		{ID: "import-arkham", Name: "Import-arkham", Role: "import", Path: "import-arkham:"},
		{ID: "itunes-wayne", Name: "Itunes-wayne", Role: "external", Path: "itunes-wayne:"},
	}
	if !slices.Equal(got.Roots, want) {
		t.Fatalf("roots = %+v\nwant %+v", got.Roots, want)
	}
}

func TestBrowse_RootsEmptyIsArray(t *testing.T) {
	r := newRig(t)
	res := r.get(browser, nil)
	if res.code != 200 || !strings.Contains(string(res.body), `"roots":[]`) {
		t.Fatalf("= %d %s", res.code, res.body)
	}
}

func TestBrowse_ListsDirectoriesSortedFilesHidden(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	d := r.dirs["books-library"]
	mkdirs(t, d, "Gotham", "Arkham", "Blackgate", "Gotham/Crime Alley")
	touch(t, d, "notes.txt")
	touch(t, d, "Arkham/inmate.m4b")
	l := list(t, r, q("root", "books-library"))
	if got := names(l); !slices.Equal(got, []string{"Arkham", "Blackgate", "Gotham"}) {
		t.Fatalf("names = %v", got)
	}
	if l.Root != "books-library" || l.Rel != "" || l.Parent != nil || l.Truncated {
		t.Errorf("listing = %+v", l)
	}
	for _, e := range l.Entries {
		if !e.IsDir || e.Rel != e.Name {
			t.Errorf("entry = %+v", e)
		}
	}
	if !strings.Contains(string(r.get(browser, q("root", "books-library")).body), `"parent":null`) {
		t.Error("root listing must carry parent: null")
	}
	// An empty rel is the root itself.
	if l2 := list(t, r, q("root", "books-library", "rel", "")); !slices.Equal(names(l2), names(l)) {
		t.Errorf("empty rel = %v", names(l2))
	}
	// A directory with only files lists nothing, as [] not null.
	res := r.get(browser, q("root", "books-library", "rel", "Arkham"))
	if res.code != 200 || !strings.Contains(string(res.body), `"entries":[]`) {
		t.Fatalf("= %d %s", res.code, res.body)
	}
}

func TestBrowse_ParentAtDepths(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	mkdirs(t, r.dirs["books-library"], "Gotham/Crime Alley/Monarch Theatre")
	for _, c := range []struct {
		rel    string
		parent *pathRef
		names  []string
		first  string
	}{
		{"", nil, []string{"Gotham"}, "Gotham"},
		{"Gotham", &pathRef{Root: "books-library", Rel: ""}, []string{"Crime Alley"}, "Gotham/Crime Alley"},
		{"Gotham/Crime Alley", &pathRef{Root: "books-library", Rel: "Gotham"}, []string{"Monarch Theatre"}, "Gotham/Crime Alley/Monarch Theatre"},
		{"Gotham/Crime Alley/Monarch Theatre", &pathRef{Root: "books-library", Rel: "Gotham/Crime Alley"}, nil, ""},
	} {
		l := list(t, r, q("root", "books-library", "rel", c.rel))
		if (l.Parent == nil) != (c.parent == nil) || (l.Parent != nil && *l.Parent != *c.parent) {
			t.Errorf("rel %q parent = %+v, want %+v", c.rel, l.Parent, c.parent)
		}
		if !slices.Equal(names(l), c.names) {
			t.Errorf("rel %q names = %v", c.rel, names(l))
		}
		if c.first != "" && l.Entries[0].Rel != c.first {
			t.Errorf("rel %q entry rel = %q, want %q", c.rel, l.Entries[0].Rel, c.first)
		}
		if l.Rel != c.rel {
			t.Errorf("rel echo = %q", l.Rel)
		}
	}
}

func TestBrowse_DotDirectoriesHidden(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	d := r.dirs["books-library"]
	mkdirs(t, d, "Gotham", ".alternates", ".Trash", "Gotham/.hidden-cave", "Gotham/Wayne Tower")
	if got := names(list(t, r, q("root", "books-library"))); !slices.Equal(got, []string{"Gotham"}) {
		t.Fatalf("root names = %v", got)
	}
	if got := names(list(t, r, q("root", "books-library", "rel", "Gotham"))); !slices.Equal(got, []string{"Wayne Tower"}) {
		t.Fatalf("Gotham names = %v", got)
	}
	// Browsing inside one is refused, the same way whether or not it exists.
	for _, rel := range []string{".alternates", "Gotham/.hidden-cave", ".nothing-here"} {
		res := r.get(browser, q("root", "books-library", "rel", rel))
		if res.code != 403 || res.problem(t).Code != "library.path_hidden" {
			t.Errorf("rel %q = %d %s", rel, res.code, res.body)
		}
	}
}

func TestBrowse_AppDirAndQuarantineHiddenAndRefused(t *testing.T) {
	spec := rootSpec("books-library", library.RoleLibrary)
	spec.AppDir = "Batcave-State" // non-dot, so only the AppDir rule can hide it
	spec.Quarantine = "Vault/Held"
	r := newRig(t, spec)
	d := r.dirs["books-library"]
	mkdirs(t, d, "Gotham", "Batcave-State/cache", "Vault/Held/arkham-escapee", "Vault/Other")
	// The sentinel already put files in the AppDir; neither reserved directory
	// is listed. Vault holds the quarantine area but is not itself reserved.
	if got := names(list(t, r, q("root", "books-library"))); !slices.Equal(got, []string{"Gotham", "Vault"}) {
		t.Fatalf("root names = %v", got)
	}
	if got := names(list(t, r, q("root", "books-library", "rel", "Vault"))); !slices.Equal(got, []string{"Other"}) {
		t.Fatalf("Vault names = %v", got)
	}
	for _, rel := range []string{"Batcave-State", "Batcave-State/cache", "batcave-state", "Vault/Held", "Vault/Held/arkham-escapee", "VAULT/held"} {
		res := r.get(browser, q("root", "books-library", "rel", rel))
		if res.code != 403 || res.problem(t).Code != "library.path_hidden" {
			t.Errorf("rel %q = %d %s", rel, res.code, res.body)
		}
	}
}

func TestBrowse_DefaultAppDirHidden(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	mkdirs(t, r.dirs["books-library"], "Gotham")
	if got := names(list(t, r, q("root", "books-library"))); !slices.Equal(got, []string{"Gotham"}) {
		t.Fatalf("names = %v", got)
	}
	res := r.get(browser, q("root", "books-library", "rel", library.DefaultAppDir))
	if res.code != 403 {
		t.Fatalf("= %d %s", res.code, res.body)
	}
	res = r.get(browser, q("root", "books-library", "rel", library.DefaultQuarantine))
	if res.code != 403 {
		t.Fatalf("= %d %s", res.code, res.body)
	}
}

func TestBrowse_BadRel_400(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	mkdirs(t, r.dirs["books-library"], "Gotham")
	for _, rel := range []string{"..", "../Gotham", "Gotham/..", "Gotham/../..", "/Gotham", "/etc", `Gotham\Crime Alley`, `..\Gotham`, "Gotham/", "./Gotham", "Gotham//Alley", "Gotham\x00"} {
		res := r.get(browser, q("root", "books-library", "rel", rel))
		if res.code != 400 || res.problem(t).Code != "library.invalid_path" {
			t.Errorf("rel %q = %d %s", rel, res.code, res.body)
		}
	}
	// rel without root.
	res := r.get(browser, q("rel", "Gotham"))
	if res.code != 400 {
		t.Errorf("rel without root = %d %s", res.code, res.body)
	}
}

func TestBrowse_UnknownRoot_404(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	for _, id := range []string{"joker-library", "Books-Library", "../books-library", "books-library:"} {
		res := r.get(browser, q("root", id))
		if res.code != 404 || res.problem(t).Code != "library.unknown_root" {
			t.Errorf("root %q = %d %s", id, res.code, res.body)
		}
	}
}

func TestBrowse_MissingAndNotADirectory(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	d := r.dirs["books-library"]
	mkdirs(t, d, "Gotham")
	touch(t, d, "Gotham/ledger.txt")
	res := r.get(browser, q("root", "books-library", "rel", "Gotham/Narrows"))
	if res.code != 404 || res.problem(t).Code != "library.path_not_found" {
		t.Errorf("missing = %d %s", res.code, res.body)
	}
	res = r.get(browser, q("root", "books-library", "rel", "Gotham/ledger.txt"))
	if res.code != 400 || res.problem(t).Code != "library.not_a_directory" {
		t.Errorf("file = %d %s", res.code, res.body)
	}
}

func TestBrowse_SymlinkToDirectoryIsNotADirectory(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	d := r.dirs["books-library"]
	mkdirs(t, d, "Gotham", "Gotham/Real Alley")
	if err := os.Symlink("Gotham", filepath.Join(d, "Gotham-Link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// A link into the reserved area must not be a way around the hiding.
	if err := os.Symlink(library.DefaultAppDir, filepath.Join(d, "Cave-Link")); err != nil {
		t.Fatal(err)
	}
	if got := names(list(t, r, q("root", "books-library"))); !slices.Equal(got, []string{"Gotham"}) {
		t.Fatalf("names = %v, a symlink is not a directory and is not listed", got)
	}
	for _, rel := range []string{"Gotham-Link", "Gotham-Link/Real Alley", "Cave-Link"} {
		res := r.get(browser, q("root", "books-library", "rel", rel))
		if res.code != 400 || res.problem(t).Code != "library.not_a_directory" {
			t.Errorf("rel %q = %d %s", rel, res.code, res.body)
		}
	}
}

func TestBrowse_ExternalRootIsBrowsable(t *testing.T) {
	r := newRig(t, rootSpec("itunes-wayne", library.RoleExternal))
	d := r.dirs["itunes-wayne"]
	mkdirs(t, d, "Music/Gotham Symphony")
	touch(t, d, "Music/Gotham Symphony/overture.m4a")
	if got := names(list(t, r, q("root", "itunes-wayne", "rel", "Music"))); !slices.Equal(got, []string{"Gotham Symphony"}) {
		t.Fatalf("names = %v", got)
	}
}

func TestBrowse_UnmountedRoot_409(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	mkdirs(t, r.dirs["books-library"], "Gotham")
	// The sentinel is what says the volume is the right one; without it the
	// root reads as an empty tree, which must not be answered as a listing.
	if err := os.Remove(filepath.Join(r.dirs["books-library"], library.DefaultAppDir, "root.json")); err != nil {
		t.Fatalf("remove sentinel: %v", err)
	}
	res := r.get(browser, q("root", "books-library"))
	if res.code != 409 || res.problem(t).Code != "library.not_mounted" {
		t.Fatalf("= %d %s", res.code, res.body)
	}
	if strings.Contains(string(res.body), r.dirs["books-library"]) {
		t.Errorf("the problem names the host path: %s", res.body)
	}
}

func TestBrowse_DisabledRoot_503(t *testing.T) {
	// A root whose directory does not exist registers disabled.
	spec := rootSpec("books-library", library.RoleLibrary)
	spec.Path = filepath.Join(t.TempDir(), "gone")
	m, _ := newManager(t, spec)
	srv, err := New(Deps{Library: m})
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, srv: srv}
	res := r.get(browser, q("root", "books-library"))
	if res.code != 503 || res.problem(t).Code != "library.root_disabled" {
		t.Fatalf("= %d %s", res.code, res.body)
	}
	// It is still listed among the roots.
	if res := r.get(browser, nil); res.code != 200 || !strings.Contains(string(res.body), "books-library") {
		t.Fatalf("roots = %d %s", res.code, res.body)
	}
}

func TestBrowse_Truncated(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	d := r.dirs["books-library"]
	for i := 0; i < maxEntries+3; i++ {
		mkdirs(t, d, fmt.Sprintf("Gotham/gargoyle-%05d", i))
	}
	l := list(t, r, q("root", "books-library", "rel", "Gotham"))
	if len(l.Entries) != maxEntries || !l.Truncated {
		t.Fatalf("entries = %d truncated = %v", len(l.Entries), l.Truncated)
	}
	if l.Entries[0].Name != "gargoyle-00000" || l.Entries[maxEntries-1].Name != fmt.Sprintf("gargoyle-%05d", maxEntries-1) {
		t.Errorf("first/last = %s / %s", l.Entries[0].Name, l.Entries[maxEntries-1].Name)
	}
	// Exactly at the cap is not truncated.
	mkdirs(t, d, "Arkham")
	for i := 0; i < maxEntries; i++ {
		mkdirs(t, d, fmt.Sprintf("Arkham/cell-%05d", i))
	}
	l = list(t, r, q("root", "books-library", "rel", "Arkham"))
	if len(l.Entries) != maxEntries || l.Truncated {
		t.Fatalf("at cap: entries = %d truncated = %v", len(l.Entries), l.Truncated)
	}
	if strings.Contains(string(r.get(browser, q("root", "books-library", "rel", "Arkham")).body), "truncated") {
		t.Error("truncated is present on a complete listing")
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	r := newRig(t, rootSpec("books-library", library.RoleLibrary))
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if res := r.do(browser, m, BasePath+"/browse"); res.code != http.StatusMethodNotAllowed {
			t.Errorf("%s = %d", m, res.code)
		}
	}
	if res := r.do(browser, http.MethodGet, BasePath+"/elsewhere"); res.code != http.StatusNotFound {
		t.Errorf("unknown path = %d", res.code)
	}
}

func TestMapError(t *testing.T) {
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{fmt.Errorf("w: %w", library.ErrNotReadable), 403, "library.root_not_readable"},
		{library.ErrReservedPath, 403, "library.path_hidden"},
		{library.ErrUnknownRoot, 404, "library.unknown_root"},
		{library.ErrBadRel, 400, "library.invalid_path"},
		{library.ErrEscapesRoot, 400, "library.invalid_path"},
		{library.ErrNotMounted, 409, "library.not_mounted"},
		{library.ErrRootDisabled, 503, "library.root_disabled"},
		{library.ErrOwnerDisabled, 503, "library.root_disabled"},
		{os.ErrNotExist, 404, "library.path_not_found"},
	} {
		p, ok := mapError(c.err)
		if !ok || p.Status != c.status || p.Code != c.code {
			t.Errorf("%v = %+v %v, want %d %s", c.err, p, ok, c.status, c.code)
		}
	}
	if _, ok := mapError(errors.New("boom")); ok {
		t.Error("an unknown error mapped")
	}
}
