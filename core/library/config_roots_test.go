// file: core/library/config_roots_test.go
// version: 1.0.2
// guid: 0f5290ae-d4ce-4d30-b8bb-ced2d196b07b
// last-edited: 2026-11-11

package library

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"path/filepath"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
)

// The shape of the kernel's core.library section (kernel/core_sections.go,
// 05-library 5L.7), as far as library reads it. library cannot import kernel,
// so the test declares the same keys and protection tags.
type cfgLibrary struct {
	Roots          []json.RawMessage `json:"roots" protect:"admin"`
	Mappings       []json.RawMessage `json:"mappings" protect:"interactive"`
	ProtectedPaths []string          `json:"protected_paths" protect:"admin"`
	Transfer       struct {
		Workers int `json:"workers"`
	} `json:"transfer"`
	Hash struct {
		MaxBytesPerSec int64 `json:"max_bytes_per_sec"`
	} `json:"hash"`
	Watch struct {
		Debounce     config.Duration `json:"debounce"`
		PollInterval config.Duration `json:"poll_interval"`
	} `json:"watch"`
	FreeSpace struct {
		WarnPercent     int   `json:"warn_percent"`
		DefaultMinBytes int64 `json:"default_min_bytes" protect:"interactive"`
	} `json:"free_space"`
}

func cfgDefaults() cfgLibrary {
	d := cfgLibrary{Roots: []json.RawMessage{}, Mappings: []json.RawMessage{}, ProtectedPaths: []string{}}
	d.Transfer.Workers = 4
	d.Watch.Debounce = config.Duration(5 * time.Second)
	d.Watch.PollInterval = config.Duration(15 * time.Minute)
	d.FreeSpace.WarnPercent = 10
	d.FreeSpace.DefaultMinBytes = 5 << 30
	return d
}

type cfgRig struct {
	t     *testing.T
	store *config.Store
	h     *config.Handle[cfgLibrary]
	m     *manager
	roots map[RootID]string
	actor config.Actor
}

func newCfgRig(t *testing.T) *cfgRig {
	t.Helper()
	mem := configtest.NewMemory()
	cm := configtest.NewCommitter(mem)
	st, err := config.NewStore(config.Options{AppID: "gotham", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	h, err := config.Register(st.Registry(), config.Define("core.library", cfgDefaults()))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.Boot(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Attach(ctx, mem, cm); err != nil {
		t.Fatal(err)
	}
	ks := newStore(t).ks
	m, err := newManager(Options{Keyspace: ks, Config: st, InstallID: testInstall}, seams{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	stop, err := Bind(m, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return &cfgRig{t: t, store: st, h: h, m: m, roots: map[RootID]string{},
		actor: config.Actor{Subject: "alfred", Interactive: true, Owner: true, Can: func(string) bool { return true }}}
}

// entry is one core.library.roots element, as an operator would store it.
func (c *cfgRig) entry(id RootID, owner string, role Role, extra map[string]any) json.RawMessage {
	c.t.Helper()
	if _, ok := c.roots[id]; !ok {
		c.roots[id] = c.t.TempDir()
	}
	m := map[string]any{"id": string(id), "path": c.roots[id], "role": role.String(), "owner": owner, "sentinel": "required"}
	maps.Copy(m, extra)
	b, err := json.Marshal(m)
	if err != nil {
		c.t.Fatal(err)
	}
	return b
}

// waitFor polls cond until it holds: OnChange delivers on its own goroutine, so
// a live apply is prompt but not synchronous with Apply.
func (c *cfgRig) waitFor(what string, cond func() bool) {
	c.t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		if cond() {
			return
		}
	}
	c.t.Fatalf("timed out waiting for %s", what)
}

func (c *cfgRig) rootIDs() (out []RootID) {
	for _, r := range c.m.Roots(RootFilter{}) {
		out = append(out, r.ID())
	}
	return out
}

func (c *cfgRig) set(key config.Path, v any) {
	c.t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Fatal(err)
	}
	if _, err := c.store.Apply(context.Background(), config.Patch{Set: map[config.Path]json.RawMessage{key: b}}, c.actor); err != nil {
		c.t.Fatalf("Apply %s: %v", key, err)
	}
}

// Roots are config entries: written through config.Store.Apply they reach the
// registry without a restart, decode back to the spec that was written, keep
// their handle when unchanged and are retired when changed or removed.
func TestConfigEntryRoots_RoundTrip(t *testing.T) {
	c := newCfgRig(t)
	if got := c.rootIDs(); len(got) != 0 {
		t.Fatalf("%v before any entry", got)
	}
	lib := c.entry("books-library", "books", RoleLibrary, map[string]any{"name": "Books", "min_free": 0, "quarantine": ".failed", "watch": "off"})
	imp := c.entry("import-1", "books", RoleImport, map[string]any{"watch": "notify", "aliases": []map[string]any{{"unc": "//gotham-nas/audio"}}})
	c.set("core.library.roots", []json.RawMessage{lib, imp})
	c.waitFor("both roots", func() bool { return len(c.m.Roots(RootFilter{})) == 2 })

	roots := c.m.Roots(RootFilter{})
	if len(roots) != 2 || roots[0].ID() != "books-library" || roots[1].ID() != "import-1" {
		t.Fatalf("roots after Apply = %v", roots)
	}
	ls, is := roots[0].Spec(), roots[1].Spec()
	if ls.Name != "Books" || ls.Role != RoleLibrary || ls.Owner != "books" || ls.MinFree != 0 || ls.Quarantine != ".failed" ||
		ls.AppDir != DefaultAppDir || ls.Watch != WatchOff || ls.Path == "" {
		t.Errorf("books-library spec = %+v", ls)
	}
	if is.MinFree != MinFreeDefault || is.Watch != WatchNotify || is.Quarantine != DefaultQuarantine || len(is.Aliases) != 1 || is.Aliases[0].UNC != "//gotham-nas/audio" {
		t.Errorf("import-1 spec = %+v (an omitted min_free must take the default marker)", is)
	}
	for _, r := range roots {
		if r.Disabled() != nil {
			t.Errorf("root %s disabled: %v", r.ID(), r.Disabled())
		}
	}
	// The Spec marshals back to an entry that decodes to the same spec.
	b, _ := json.Marshal(ls)
	var again RootSpec
	if err := json.Unmarshal(b, &again); err != nil || again.ID != ls.ID || again.MinFree != ls.MinFree || again.Quarantine != ls.Quarantine || again.Role != ls.Role {
		t.Errorf("round trip = %+v, %v", again, err)
	}

	// An unrelated key changes nothing about the roots; a changed entry retires
	// only that root; an unchanged one keeps its handle.
	keep, other := roots[0], roots[1]
	c.set("core.library.watch.debounce", "9s")
	c.waitFor("the debounce", func() bool { return c.m.reg.Load().set.debounce == 9*time.Second })
	if r, _ := c.m.Root("books-library"); r != keep {
		t.Error("an unrelated change replaced an unchanged root")
	}
	imp2 := c.entry("import-1", "books", RoleImport, map[string]any{"watch": "poll"})
	c.set("core.library.roots", []json.RawMessage{lib, imp2})
	c.waitFor("the changed entry", func() bool { r, err := c.m.Root("import-1"); return err == nil && r.Spec().Watch == WatchPoll })
	if r, _ := c.m.Root("books-library"); r != keep {
		t.Error("an unchanged entry lost its handle")
	}
	r2, _ := c.m.Root("import-1")
	if r2 == other || r2.Spec().Watch != WatchPoll {
		t.Errorf("a changed entry kept its old root: %+v", r2.Spec())
	}
	// apply publishes the new registry and then retires what it replaced, so
	// retirement is waited for, not read the instant the new root is visible.
	c.waitFor("the replaced root to retire", func() bool {
		select {
		case <-other.gone:
			return true
		default:
			return false
		}
	})
	// Removing an entry removes the root.
	c.set("core.library.roots", []json.RawMessage{lib})
	c.waitFor("the removal", func() bool { _, err := c.m.Root("import-1"); return err != nil })
	if _, err := c.m.Root("import-1"); err == nil {
		t.Error("a removed entry is still registered")
	}
	c.waitFor("the removed root to retire", func() bool {
		select {
		case <-r2.gone:
			return true
		default:
			return false
		}
	})
	// The other keys apply live too.
	if got := c.m.reg.Load().set.debounce; got != 9*time.Second {
		t.Errorf("debounce = %v, want 9s", got)
	}
	c.set("core.library.protected_paths", []string{filepath.Join(c.roots["books-library"], "Seeding")})
	c.waitFor("the protected path", func() bool { return len(c.m.reg.Load().set.protected) == 1 })
	if got := c.m.reg.Load().set.protected; len(got) != 1 {
		t.Errorf("protected paths = %v", got)
	}
	c.set("core.library.mappings", []Mapping{{Source: "download:qbit-1", Remote: "/downloads/complete", Local: Path{Root: "books-library", Rel: "incoming"}}})
	c.waitFor("the mapping", func() bool { return len(c.m.Mapper().Mappings("download:qbit-1")) == 1 })
	if p, err := c.m.Mapper().ToLocal("download:qbit-1", "/downloads/complete/Gotham/a.m4b"); err != nil || p != (Path{Root: "books-library", Rel: "incoming/Gotham/a.m4b"}) {
		t.Errorf("mapping applied live: ToLocal = %v, %v", p, err)
	}
	c.set("core.library.hash.max_bytes_per_sec", 1<<20)
	c.waitFor("the hash rate", func() bool { return c.m.reg.Load().set.hashRate == 1<<20 })
	if got := c.m.reg.Load().set.hashRate; got != 1<<20 {
		t.Errorf("hash rate = %d", got)
	}
}

// One entry that does not decode is skipped and reported; the entries around
// it load.
func TestConfigEntryRootsSkipsABadEntry(t *testing.T) {
	c := newCfgRig(t)
	good := c.entry("books-library", "books", RoleLibrary, nil)
	bad := json.RawMessage(`{"id":"import-9","path":"/nowhere","role":"bogus","owner":"books"}`)
	c.set("core.library.roots", []json.RawMessage{bad, good})
	c.waitFor("the good entry", func() bool { _, err := c.m.Root("books-library"); return err == nil })
	if _, err := c.m.Root("books-library"); err != nil {
		t.Errorf("the good entry did not load: %v", err)
	}
	if _, err := c.m.Root("import-9"); err == nil {
		t.Error("the bad entry was registered")
	}
	if err := c.m.Reload(context.Background()); err == nil {
		t.Error("Reload did not report the bad entry")
	}
	// A root that cannot be resolved registers disabled and is retried.
	gone := c.entry("import-2", "books", RoleImport, map[string]any{"path": filepath.Join(c.t.TempDir(), "not-there")})
	c.set("core.library.roots", []json.RawMessage{good, gone})
	c.waitFor("the unresolvable root", func() bool { _, err := c.m.Root("import-2"); return err == nil })
	r, err := c.m.Root("import-2")
	if err != nil || r.Disabled() == nil {
		t.Errorf("an unresolvable root = %v, disabled %v; want registered and disabled", err, r)
	}
}
