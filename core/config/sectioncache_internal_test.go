// file: core/config/sectioncache_internal_test.go
// version: 1.0.0
// guid: 5a0d1c52-63f0-4c77-b9a4-0a9e1f7e3b61
// last-edited: 2026-11-12

package config

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/secrets"
)

// cacheCave is a section with one key of most kinds: an enum, a bounded
// restart key, a FailClosed enum, a protected key, a nested struct, a list
// and a secret.
type cacheCave struct {
	Level   string      `json:"level" schema:"enum=debug|info|warn"`
	Workers int         `json:"workers" reload:"restart" schema:"minimum=1,maximum=64"`
	Mode    string      `json:"mode" schema:"enum=night|day"`
	Signal  string      `json:"signal" protect:"interactive"`
	Gadget  cacheGadget `json:"gadget"`
	Allies  []string    `json:"allies"`
	APIKey  secrets.Ref `json:"api_key"`
}

type cacheGadget struct {
	Name  string `json:"name"`
	Count int    `json:"count" schema:"minimum=0"`
}

// cacheCaveTwin has cacheCave's field names, JSON and defaults, and is another
// type with another tag on level: a cache keyed on anything but the type would
// hand cacheCave's template to this one.
type cacheCaveTwin struct {
	Level   string      `json:"level" schema:"enum=debug|info"`
	Workers int         `json:"workers" reload:"restart" schema:"minimum=1,maximum=64"`
	Mode    string      `json:"mode" schema:"enum=night|day"`
	Signal  string      `json:"signal" protect:"interactive"`
	Gadget  cacheGadget `json:"gadget"`
	Allies  []string    `json:"allies"`
	APIKey  secrets.Ref `json:"api_key"`
}

// cacheRogue is the element of a map-typed section.
type cacheRogue struct {
	Name   string `json:"name"`
	Threat int    `json:"threat" schema:"minimum=0"`
}

// cacheBeacon is a list element holding an entry secret and a bound field.
type cacheBeacon struct {
	ID       string      `json:"id"`
	Callsign string      `json:"callsign" secret:"bind"`
	Key      secrets.Ref `json:"key" protect:"admin"`
}

type cacheBeacons struct {
	Beacons []cacheBeacon `json:"beacons" protect:"owner"`
}

const (
	cavePath    Path = "core.gotham.cave"
	flaresPath  Path = "core.gotham.flares"
	roguesPath  Path = "core.gotham.rogues"
	beaconsPath Path = "core.gotham.beacons"
)

func cacheCaveDefaults() cacheCave {
	return cacheCave{
		Level: "info", Workers: 4, Mode: "night", Signal: "bat-signal",
		Gadget: cacheGadget{Name: "batarang", Count: 3}, Allies: []string{"robin"},
	}
}

func cacheCaveOpts() []SectionOption {
	return []SectionOption{
		Docs(map[Path]string{"core.gotham.cave.level": "how loud the cave is"}),
		FailClosed("core.gotham.cave.mode", json.RawMessage(`"night"`), json.RawMessage(`"day"`)),
		Permissions("core:cave.read", "core:cave.write"),
		EnvBinding("core.gotham.cave.level", "GOTHAM_LOUDNESS"),
	}
}

// sectionSet is the sections every test registers; each is one cache entry.
func sectionSet() []any {
	return []any{
		Define(cavePath, cacheCaveDefaults(), cacheCaveOpts()...),
		Define(flaresPath, flares{Flares: []flare{{ID: "batcave", Issuer: "alfred"}}}),
		Define(roguesPath, map[string]cacheRogue{"bane": {Name: "Bane", Threat: 9}}),
		Define(beaconsPath, cacheBeacons{}),
	}
}

const sectionSetSize = 4

func newCacheStore(t testing.TB, appID string, c *templateCache) *Store {
	t.Helper()
	s, err := NewStore(Options{AppID: appID, Environ: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	s.builds = c
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}

// uncachedStore builds every section from scratch, as Register did before
// the cache.
func uncachedStore(t testing.TB, appID string) *Store {
	t.Helper()
	return newCacheStore(t, appID, newTemplateCache(0))
}

func registerSet(t testing.TB, s *Store) []any {
	t.Helper()
	hs, err := RegisterSections(s.Registry(), sectionSet()...)
	if err != nil {
		t.Fatal(err)
	}
	return hs
}

func admin() Actor {
	return Actor{Subject: "alfred", Interactive: true, Can: func(string) bool { return true }}
}

// attach loads s over no stored rows and attaches it, so Apply works.
func attach(t testing.TB, s *Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Attach(ctx, rowsOnly{}, nopCommitter{}); err != nil {
		t.Fatal(err)
	}
}

var probes = []string{
	``, `null`, `""`, `"info"`, `"warn"`, `"night"`, `"dusk"`, `0`, `7`, `65`, `-1`, `true`, `[]`, `["robin"]`,
	`{}`, `{"name":"batarang","count":2}`, `{"name":"bane","threat":-4}`,
	`[{"id":"batcave","issuer":"alfred","callsign":"owl"}]`, `[{"id":"batcave"},{"id":"batcave"}]`,
}

// fingerprint renders everything the store derives from its sections that a
// caller or a later step can observe: the section and key tables, the reflected
// and per-key schemas, the declared and canonical defaults, and how every key
// answers a set of probe values, errors included.
func fingerprint(t testing.TB, s *Store) string {
	t.Helper()
	var b strings.Builder
	for _, sec := range s.sections {
		schema, err := json.Marshal(sec.schema)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "section %s map=%v read=%s write=%s defaults=%s zero=%s schema=%s lists=%d\n",
			sec.path, sec.isMap, sec.permRead, sec.permWrite, sec.defaults, sec.elemZero, schema, len(sec.entryLists))
		var all []*keyInfo
		for _, k := range sec.keys {
			all = append(all, k)
			all = append(all, k.entrySecrets...)
		}
		for _, k := range all {
			fail := make([]string, len(k.failOrder))
			for i, o := range k.failOrder {
				fail[i] = string(o)
			}
			fmt.Fprintf(&b, "key %s rel=%v idx=%v reload=%v protect=%v secret=%v sens=%v def=%s raw=%s fail=%v reserved=%v bind=%v id=%d elem=%d list=%v\n",
				k.path("x"), k.rel, k.index, k.reload, k.protect, k.secret, k.sensitive, k.def, k.schemaRaw, fail,
				slices.Sorted(maps.Keys(k.reservedIDs)), k.bindNames, k.idIdx, k.elemIdx, k.list != nil)
			if k.resolved == nil {
				continue
			}
			for _, p := range probes {
				c, err := k.canonical(k.path("bane"), json.RawMessage(p))
				fmt.Fprintf(&b, "  probe %q -> %s / %v\n", p, c, err)
			}
		}
	}
	doc, err := s.Schema(nil)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(&b, "schema %s\n", doc)
	return b.String()
}

// TestSectionCache_StoresAreIndependent: two stores with different AppIDs
// built from the same sections share only the frozen schemas. A write to one
// is invisible in the other, and what a caller can reach (the bytes of a key's
// state, the sections and keys themselves) is private to its store.
func TestSectionCache_StoresAreIndependent(t *testing.T) {
	c := newTemplateCache(maxSectionTemplates)
	a, b := newCacheStore(t, "gotham", c), newCacheStore(t, "metropolis", c)
	ha, hb := registerSet(t, a), registerSet(t, b)
	if got := c.builds.Load(); got != sectionSetSize {
		t.Fatalf("builds = %d after two stores, want %d: the second store built something again", got, sectionSetSize)
	}
	attach(t, a)
	attach(t, b)

	// The built values are distinct objects that point at their own store.
	for i := range a.sections {
		sa, sb := a.sections[i], b.sections[i]
		if sa == sb || sa.bind == sb.bind || sa.bind.store != a || sb.bind.store != b {
			t.Fatalf("section %s: the two stores share a section or a binding", sa.path)
		}
		for j, ka := range sa.keys {
			kb := sb.keys[j]
			if ka == kb || ka.sec != sa || kb.sec != sb {
				t.Fatalf("key %s: the two stores share a key or one points at the other's section", ka.path("x"))
			}
		}
	}

	cave := func(h []any) *Handle[cacheCave] { return h[0].(*Handle[cacheCave]) }
	res, err := a.Apply(context.Background(), Patch{Set: map[Path]json.RawMessage{
		"core.gotham.cave.level":   json.RawMessage(`"warn"`),
		"core.gotham.cave.workers": json.RawMessage(`9`),
	}}, admin())
	if err != nil {
		t.Fatalf("Apply on the first store: %v", err)
	}
	if len(res.Applied) != 2 {
		t.Fatalf("Applied = %v", res.Applied)
	}
	if got := cave(ha).Get().Level; got != "warn" {
		t.Errorf("first store level = %q after its own write, want warn", got)
	}
	if got := cave(hb).Get().Level; got != "info" {
		t.Errorf("second store level = %q after a write to the first, want its default info", got)
	}
	if got := cave(hb).Pending().Workers; got != 4 {
		t.Errorf("second store pending workers = %d after a write to the first, want its default 4", got)
	}
	if got := cave(hb).Generation(); got != 0 && got == cave(ha).Generation() {
		t.Errorf("both stores report generation %d after one wrote", got)
	}
	ksa, _ := a.Lookup("core.gotham.cave.level")
	ksb, _ := b.Lookup("core.gotham.cave.level")
	if string(ksa.Effective) != `"warn"` || string(ksb.Effective) != `"info"` {
		t.Errorf("key state effective: first %s second %s", ksa.Effective, ksb.Effective)
	}

	// The settings schema names its own store.
	da, _ := a.Schema(nil)
	db, _ := b.Schema(nil)
	if !strings.Contains(string(da), `"$id":"gotham"`) || !strings.Contains(string(db), `"$id":"metropolis"`) {
		t.Errorf("schema ids: first %.80s second %.80s", da, db)
	}

	// Bytes a caller holds are its store's alone: scribbling on them reaches
	// neither the other store nor a later read of the same one.
	ksa2, _ := a.Lookup("core.gotham.cave.mode")
	before, _ := b.Lookup("core.gotham.cave.mode")
	wantSchema, wantEff := string(before.Schema), string(before.Effective)
	for i := range ksa2.Schema {
		ksa2.Schema[i] = 'X'
	}
	for i := range ksa2.Effective {
		ksa2.Effective[i] = 'X'
	}
	after, _ := b.Lookup("core.gotham.cave.mode")
	if string(after.Schema) != wantSchema || string(after.Effective) != wantEff {
		t.Errorf("a caller's writes to its key state reached the other store: schema %s effective %s", after.Schema, after.Effective)
	}
	// The key's own sub-schema is the one thing shared; it reads the same
	// through both stores and a write through the store API never touches it.
	if fa, fb := fingerprint(t, a), fingerprint(t, b); strings.Replace(fa, `"$id":"gotham"`, "", 1) != strings.Replace(fb, `"$id":"metropolis"`, "", 1) {
		t.Errorf("the two stores' sections differ after one was written to:\n%s\n---\n%s", fa, fb)
	}
}

// TestSectionCache_MatchesUncachedBuild: the cached build is observably the
// uncached one, for every section of the set, valid and invalid alike: defaults,
// schemas, key tables, validation answers and error text.
func TestSectionCache_MatchesUncachedBuild(t *testing.T) {
	c := newTemplateCache(maxSectionTemplates)
	registerSet(t, newCacheStore(t, "gotham", c)) // warm: the compared store is an instance
	cached := newCacheStore(t, "gotham", c)
	registerSet(t, cached)
	plain := uncachedStore(t, "gotham")
	registerSet(t, plain)
	if fc, fp := fingerprint(t, cached), fingerprint(t, plain); fc != fp {
		t.Errorf("cached build differs from the uncached build:\n%s\n--- uncached ---\n%s", fc, fp)
	}
	if got := c.builds.Load(); got != sectionSetSize {
		t.Errorf("builds = %d, want %d", got, sectionSetSize)
	}

	// Errors are the same too, on a miss and on a hit.
	bad := []struct {
		name string
		sec  any
	}{
		{"docs name no key", Define(cavePath, cacheCaveDefaults(), Docs(map[Path]string{"core.gotham.cave.nope": "x"}))},
		{"waiver names no key", Define(cavePath, cacheCaveDefaults(), ProtectionWaivers(map[Path]string{"core.gotham.cave.nope": "x"}))},
		{"failclosed on a non-enum", Define(cavePath, cacheCaveDefaults(), FailClosed("core.gotham.cave.signal", json.RawMessage(`"a"`)))},
		{"failclosed misses a value", Define(cavePath, cacheCaveDefaults(), FailClosed("core.gotham.cave.mode", json.RawMessage(`"night"`)))},
		{"bad path", Define("gotham", cacheCaveDefaults())},
		{"default out of range", Define(cavePath, cacheCave{Level: "info", Workers: 400, Mode: "night"})},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			var errs [3]string
			for i, s := range []*Store{newCacheStore(t, "gotham", c), newCacheStore(t, "gotham", c), uncachedStore(t, "gotham")} {
				_, err := RegisterSections(s.Registry(), tc.sec)
				if err == nil {
					t.Fatalf("store %d: registered a section that must fail", i)
				}
				errs[i] = err.Error()
			}
			if errs[0] != errs[2] || errs[1] != errs[2] {
				t.Errorf("errors differ: first %q, repeat %q, uncached %q", errs[0], errs[1], errs[2])
			}
		})
	}
	if got := c.size(); got != sectionSetSize {
		t.Errorf("cache holds %d templates after failed builds, want only the %d good ones", got, sectionSetSize)
	}
}

// TestSectionCache_ChangedSpecMissesTheCache: any change to what a spec
// records builds anew, so a stale entry never serves another spec, whichever
// of the two was built first.
func TestSectionCache_ChangedSpecMissesTheCache(t *testing.T) {
	base := func() any { return Define(cavePath, cacheCaveDefaults(), cacheCaveOpts()...) }
	mut := func(f func(*cacheCave)) cacheCave {
		d := cacheCaveDefaults()
		f(&d)
		return d
	}
	opts := func(drop int, extra ...SectionOption) []SectionOption {
		o := cacheCaveOpts()
		if drop >= 0 {
			o = slices.Delete(o, drop, drop+1)
		}
		return append(o, extra...)
	}
	const docs, failClosed, perms, env = 0, 1, 2, 3
	variants := []struct {
		name string
		sec  any
		same bool // builds the same section as the base: only the build count tells it apart
	}{
		{"path", Define("core.gotham.cave2", cacheCaveDefaults(), cacheCaveOpts()...), false},
		{"type", Define(cavePath, cacheCaveTwin(cacheCaveDefaults()), cacheCaveOpts()...), false},
		{"default string", Define(cavePath, mut(func(c *cacheCave) { c.Level = "warn" }), cacheCaveOpts()...), false},
		{"default number", Define(cavePath, mut(func(c *cacheCave) { c.Workers = 5 }), cacheCaveOpts()...), false},
		{"default nested", Define(cavePath, mut(func(c *cacheCave) { c.Gadget.Count = 4 }), cacheCaveOpts()...), false},
		{"default list", Define(cavePath, mut(func(c *cacheCave) { c.Allies = []string{"robin", "batgirl"} }), cacheCaveOpts()...), false},
		{"docs text", Define(cavePath, cacheCaveDefaults(), opts(docs, Docs(map[Path]string{"core.gotham.cave.level": "how quiet the cave is"}))...), false},
		{"docs key", Define(cavePath, cacheCaveDefaults(), opts(docs, Docs(map[Path]string{"core.gotham.cave.signal": "how loud the cave is"}))...), false},
		{"docs removed", Define(cavePath, cacheCaveDefaults(), opts(docs)...), false},
		{"docs invalid key", Define(cavePath, cacheCaveDefaults(), opts(docs, Docs(map[Path]string{"core.gotham.cave.nope": "x"}))...), false},
		{"waiver invalid key", Define(cavePath, cacheCaveDefaults(), opts(-1, ProtectionWaivers(map[Path]string{"core.gotham.cave.nope": "x"}))...), false},
		{"waiver valid key", Define(cavePath, cacheCaveDefaults(), opts(-1, ProtectionWaivers(map[Path]string{"core.gotham.cave.signal": "x"}))...), true},
		{"failclosed removed", Define(cavePath, cacheCaveDefaults(), opts(failClosed)...), false},
		{"failclosed order", Define(cavePath, cacheCaveDefaults(), opts(failClosed, FailClosed("core.gotham.cave.mode", json.RawMessage(`"day"`), json.RawMessage(`"night"`)))...), false},
		{"failclosed bytes", Define(cavePath, cacheCaveDefaults(), opts(failClosed, FailClosed("core.gotham.cave.mode", json.RawMessage(`"night" `), json.RawMessage(`"day"`)))...), true},
		{"permissions read", Define(cavePath, cacheCaveDefaults(), opts(perms, Permissions("core:cave.look", "core:cave.write"))...), false},
		{"permissions write", Define(cavePath, cacheCaveDefaults(), opts(perms, Permissions("core:cave.read", "core:cave.edit"))...), false},
		{"permissions removed", Define(cavePath, cacheCaveDefaults(), opts(perms)...), false},
		{"env binding invalid key", Define(cavePath, cacheCaveDefaults(), opts(env, EnvBinding("core.gotham.cave.nope", "GOTHAM_X"))...), false},
		{"env binding empty name", Define(cavePath, cacheCaveDefaults(), opts(env, EnvBinding("core.gotham.cave.level", ""))...), false},
		{"env binding removed", Define(cavePath, cacheCaveDefaults(), opts(env)...), true},
	}
	for _, v := range variants {
		for _, order := range []string{"base first", "variant first"} {
			t.Run(v.name+"/"+order, func(t *testing.T) {
				c := newTemplateCache(maxSectionTemplates)
				first, second := base(), v.sec
				if order == "variant first" {
					first, second = second, first
				}
				var got, want [2]string
				for i, sec := range []any{first, second} {
					s := newCacheStore(t, "gotham", c)
					if _, err := RegisterSections(s.Registry(), sec); err != nil {
						got[i] = "error: " + err.Error()
					} else {
						got[i] = fingerprint(t, s)
					}
					u := uncachedStore(t, "gotham")
					if _, err := RegisterSections(u.Registry(), sec); err != nil {
						want[i] = "error: " + err.Error()
					} else {
						want[i] = fingerprint(t, u)
					}
				}
				for i := range got {
					if got[i] != want[i] {
						t.Errorf("build %d of the pair (%s) differs from the uncached build:\n%s\n--- uncached ---\n%s", i, order, got[i], want[i])
					}
				}
				if !v.same && got[0] == got[1] {
					t.Errorf("the variant %q built exactly like the base: the case proves nothing", v.name)
				}
				if b := c.builds.Load(); b != 2 {
					t.Errorf("builds = %d, want 2: the changed spec was served from the other's entry", b)
				}
			})
		}
	}
}

// TestSectionCache_ConcurrentStores: parallel kernels register the same
// sections into stores of their own. Each builds once per spec in total, no
// store sees another's write, and validation through the shared schemas is race
// free (run under -race).
func TestSectionCache_ConcurrentStores(t *testing.T) {
	const stores = 16
	c := newTemplateCache(maxSectionTemplates)
	norm := func(p string, i int) string { return strings.Replace(p, fmt.Sprintf(`"$id":"gotham%d"`, i), "", 1) }
	var wg sync.WaitGroup
	prints := make([]string, stores)
	errc := make(chan error, stores)
	for i := range stores {
		wg.Go(func() {
			s, err := NewStore(Options{AppID: fmt.Sprintf("gotham%d", i), Environ: []string{}})
			if err != nil {
				errc <- err
				return
			}
			defer s.Close(context.Background())
			s.builds = c
			hs, err := RegisterSections(s.Registry(), sectionSet()...)
			if err != nil {
				errc <- err
				return
			}
			ctx := context.Background()
			if err := s.Load(ctx); err != nil {
				errc <- err
				return
			}
			if _, err := s.Attach(ctx, rowsOnly{}, nopCommitter{}); err != nil {
				errc <- err
				return
			}
			want := 10 + i
			if _, err := s.Apply(ctx, Patch{Set: map[Path]json.RawMessage{"core.gotham.cave.workers": json.RawMessage(fmt.Sprint(want))}}, admin()); err != nil {
				errc <- err
				return
			}
			if got := hs[0].(*Handle[cacheCave]).Pending().Workers; got != want {
				errc <- fmt.Errorf("store %d pending workers = %d, want %d: it read another store's write", i, got, want)
				return
			}
			if _, err := s.Schema(nil); err != nil {
				errc <- err
				return
			}
			prints[i] = fingerprint(t, s)
		})
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		t.Error(err)
	}
	if got := c.builds.Load(); got != sectionSetSize {
		t.Errorf("builds = %d across %d concurrent stores, want %d: single-flight did not hold", got, stores, sectionSetSize)
	}
	for i := 1; i < stores; i++ {
		// Only the settings schema's $id, the store's own AppID, may differ.
		if norm(prints[i], i) != norm(prints[0], 0) {
			t.Errorf("store %d's sections differ from store 0's", i)
			break
		}
	}
}

// TestSectionCache_SecondBuildIsCheaper: registering sections the cache has
// built costs a fraction of the first time. Only a ratio is asserted, never a
// duration: the first build of this set is the reference, the fastest of three
// later ones must be under a third of it.
func TestSectionCache_SecondBuildIsCheaper(t *testing.T) {
	c := newTemplateCache(maxSectionTemplates)
	register := func(appID string) time.Duration {
		s := newCacheStore(t, appID, c)
		start := time.Now()
		registerSet(t, s)
		return time.Since(start)
	}
	first := register("gotham")
	fastest := time.Duration(1<<63 - 1)
	for i := range 3 {
		fastest = min(fastest, register(fmt.Sprintf("gotham%d", i)))
	}
	t.Logf("first build %v, fastest of the next three %v (%.1fx)", first, fastest, float64(first)/float64(fastest))
	if fastest*3 >= first {
		t.Errorf("fastest later build %v is not under a third of the first %v: the cache does not save the build", fastest, first)
	}
	if got := c.builds.Load(); got != sectionSetSize {
		t.Errorf("builds = %d, want %d", got, sectionSetSize)
	}
}

// TestSectionCache_Bounded: past the bound a spec is built uncached and kept
// out, and still registers correctly. Specs with a computed default (core.paths
// takes a per-boot directory) are the unbounded kind.
func TestSectionCache_Bounded(t *testing.T) {
	c := newTemplateCache(2)
	for i := range 5 {
		s := newCacheStore(t, "gotham", c)
		d := cacheCaveDefaults()
		d.Workers = 1 + i
		hs, err := RegisterSections(s.Registry(), Define(cavePath, d))
		if err != nil {
			t.Fatalf("spec %d: %v", i, err)
		}
		if got := hs[0].(*Handle[cacheCave]).Get().Workers; got != 1+i {
			t.Errorf("spec %d: workers = %d, want %d", i, got, 1+i)
		}
		if got := c.size(); got > 2 {
			t.Fatalf("after spec %d the cache holds %d templates, bound is 2", i, got)
		}
	}
	if got := c.builds.Load(); got != 2 {
		t.Errorf("cached builds = %d, want 2 (the first two specs; the rest are built uncached)", got)
	}
	// An entry cached before the bound was hit still serves.
	s := newCacheStore(t, "gotham", c)
	d := cacheCaveDefaults()
	if _, err := RegisterSections(s.Registry(), Define(cavePath, func() cacheCave { d.Workers = 1; return d }())); err != nil {
		t.Fatal(err)
	}
	if got := c.builds.Load(); got != 2 {
		t.Errorf("cached builds = %d after re-registering a cached spec, want 2", got)
	}
}

// blink marshals as the cave's defaults, but panics on its second marshal:
// the first is the cache's key, the second is the build's.
type blink struct{ calls *atomic.Int32 }

func (b blink) MarshalJSON() ([]byte, error) {
	if b.calls.Add(1) == 2 {
		panic("the Joker has been here")
	}
	return json.Marshal(cacheCaveDefaults())
}

// TestSectionCache_PanickedBuildIsNotServed: a build that panics reaches only
// its own goroutine; the next caller gets an error, not a nil template, and the
// entry is dropped so a later caller builds again.
func TestSectionCache_PanickedBuildIsNotServed(t *testing.T) {
	c := newTemplateCache(maxSectionTemplates)
	var calls atomic.Int32
	spec := func() *sectionSpec {
		return &sectionSpec{path: cavePath, defaults: blink{&calls}, typ: reflect.TypeFor[cacheCave]()}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the first build did not panic: the fixture proves nothing")
			}
		}()
		_, _ = c.section(spec())
	}()
	if _, err := c.section(spec()); !errors.Is(err, errTemplatePanicked) {
		t.Fatalf("after a panicked build err = %v, want errTemplatePanicked", err)
	}
	if got := c.size(); got != 0 {
		t.Errorf("cache holds %d templates after a panicked build, want 0", got)
	}
	sec, err := c.section(spec())
	if err != nil || sec == nil {
		t.Fatalf("a later caller got (%v, %v), want a fresh build", sec, err)
	}
}

// TestSectionCache_KeyHasNoAmbiguity: keys of specs that differ only in where
// a string ends are different keys.
func TestSectionCache_KeyHasNoAmbiguity(t *testing.T) {
	typ := reflect.TypeFor[cacheCave]()
	mk := func(docs map[Path]string, env ...envBinding) *sectionSpec {
		return &sectionSpec{path: cavePath, defaults: cacheCaveDefaults(), typ: typ, docs: docs, envBindings: env}
	}
	specs := []*sectionSpec{
		mk(map[Path]string{"ab": "c"}),
		mk(map[Path]string{"a": "bc"}),
		mk(map[Path]string{"a": "b", "c": ""}),
		mk(map[Path]string{"": "abc"}),
		mk(nil, envBinding{"a", "b"}),
		mk(nil, envBinding{"ab", ""}),
		mk(nil, envBinding{"a", "b"}, envBinding{"", ""}),
		mk(nil),
	}
	seen := map[specKey]int{}
	for i, sp := range specs {
		k, ok := keyOfSpec(sp)
		if !ok {
			t.Fatalf("spec %d has no key", i)
		}
		if j, dup := seen[k]; dup {
			t.Errorf("specs %d and %d share a key", j, i)
		}
		seen[k] = i
	}
	// One spec always keys alike, whatever order its maps were filled in.
	a := mk(map[Path]string{"x": "1", "y": "2", "z": "3"})
	for range 20 {
		k1, _ := keyOfSpec(a)
		k2, _ := keyOfSpec(mk(map[Path]string{"z": "3", "y": "2", "x": "1"}))
		if k1 != k2 {
			t.Fatal("the same spec keyed differently")
		}
	}
	if _, ok := keyOfSpec(&sectionSpec{path: cavePath}); ok {
		t.Error("a spec without a type has a key; buildSection must refuse it uncached")
	}
}

// TestSectionCache_FieldsAreClassified: every field of a built section and of a
// key is either shared with the template (frozen after the build), private to
// the instance (a copy, or one the store sets after instantiate) or a plain
// value. Adding a field fails here until it is classified, and a field marked
// private must not alias the template's.
func TestSectionCache_FieldsAreClassified(t *testing.T) {
	type class string
	const (
		shared  class = "shared"  // same pointer as the template's: frozen once built
		private class = "private" // never the template's memory
		value   class = "value"   // a scalar, or an immutable reflect value
		store   class = "store"   // set by Store after instantiate: zero in an instance
		caller  class = "caller"  // the caller's own spec
	)
	sectionFields := map[string]class{
		"path": value, "spec": caller, "isMap": value, "keys": private, "byRel": private,
		"permRead": value, "permWrite": value, "schema": shared, "defaults": private, "elemZero": private,
		"bind": store, "eff": store, "pend": store, "entryLists": private,
	}
	keyFields := map[string]class{
		"sec": private, "rel": private, "index": private, "typ": value, "reload": value, "protect": value,
		"secret": value, "sensitive": value, "schema": shared, "schemaRaw": private, "resolved": shared,
		"def": private, "failOrder": private, "list": private, "elemIdx": value, "entrySecrets": private,
		"idIdx": value, "reservedIDs": private, "bindNames": private,
	}
	for _, tc := range []struct {
		typ   reflect.Type
		table map[string]class
	}{{reflect.TypeFor[section](), sectionFields}, {reflect.TypeFor[keyInfo](), keyFields}} {
		for f := range tc.typ.Fields() {
			if _, ok := tc.table[f.Name]; !ok {
				t.Errorf("%s.%s is not classified for the section cache: say whether an instance shares it, copies it or the store sets it", tc.typ.Name(), f.Name)
			}
		}
		for name := range tc.table {
			if _, ok := tc.typ.FieldByName(name); !ok {
				t.Errorf("%s has no field %s: the table is stale", tc.typ.Name(), name)
			}
		}
	}

	// Build the template and an instance of every shape, and check each field
	// against its class, requiring every private field to have been non-empty
	// somewhere so the check cannot pass on an empty fixture.
	tpl := buildAll(t)
	exercised := map[string]bool{}
	check := func(owner string, table map[string]class, a, b reflect.Value) {
		for name, cl := range table {
			fa, fb := a.FieldByName(name), b.FieldByName(name)
			switch cl {
			case shared:
				if fa.Kind() == reflect.Pointer && !fa.IsNil() {
					exercised[owner+"."+name] = true
					if fa.Pointer() != fb.Pointer() {
						t.Errorf("%s.%s is not shared with the template: every store would pay to rebuild it", owner, name)
					}
				}
			case private:
				switch fa.Kind() {
				case reflect.Slice, reflect.Map:
					if fa.Len() > 0 {
						exercised[owner+"."+name] = true
						if fa.Pointer() == fb.Pointer() {
							t.Errorf("%s.%s aliases the template's memory", owner, name)
						}
					}
				case reflect.Pointer:
					if !fa.IsNil() {
						exercised[owner+"."+name] = true
						if fa.Pointer() == fb.Pointer() {
							t.Errorf("%s.%s points at the template's value", owner, name)
						}
					}
				}
			case store:
				if !fa.IsZero() {
					t.Errorf("%s.%s is set on a fresh instance; only the store sets it", owner, name)
				}
			}
		}
	}
	for _, tp := range tpl {
		inst := tp.instantiate(&sectionSpec{})
		check("section", sectionFields, reflect.ValueOf(inst).Elem(), reflect.ValueOf(tp).Elem())
		if inst.spec == nil || inst.spec == tp.spec {
			t.Errorf("section %s: an instance must carry its caller's spec, not the template's", tp.path)
		}
		var ta, ia []*keyInfo
		for _, k := range tp.keys {
			ta = append(ta, append([]*keyInfo{k}, k.entrySecrets...)...)
		}
		for _, k := range inst.keys {
			ia = append(ia, append([]*keyInfo{k}, k.entrySecrets...)...)
		}
		for i := range ta {
			check("keyInfo", keyFields, reflect.ValueOf(ia[i]).Elem(), reflect.ValueOf(ta[i]).Elem())
			if ia[i].sec != inst {
				t.Errorf("key %s points at a section other than its instance", ia[i].path("x"))
			}
		}
	}
	for owner, table := range map[string]map[string]class{"section": sectionFields, "keyInfo": keyFields} {
		for name, cl := range table {
			if (cl == private || cl == shared) && !exercised[owner+"."+name] {
				t.Errorf("no fixture exercises %s.%s non-empty: the aliasing check proved nothing for it", owner, name)
			}
		}
	}
}

// buildAll builds each section of the set straight through buildSection.
func buildAll(t testing.TB) []*section {
	t.Helper()
	var out []*section
	s := newCacheStore(t, "gotham", newTemplateCache(0))
	for _, sec := range sectionSet() {
		// Register's own path decorates the spec with its type; reuse it to
		// get the built section out of the store.
		if _, err := sec.(registrar).registerWith(s.Registry()); err != nil {
			t.Fatal(err)
		}
	}
	out = append(out, s.sections...)
	if len(out) != sectionSetSize {
		t.Fatalf("built %d sections, want %d", len(out), sectionSetSize)
	}
	return out
}
