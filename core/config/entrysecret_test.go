// file: core/config/entrysecret_test.go
// version: 1.0.1
// guid: d87e4d85-933b-47a2-acce-30fd13cf619a
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// Entry secrets (F3-OIDC-SECRET; keys.go "Entry secrets"): a secrets.Ref in a
// list entry is a secret key of its own, "<list parent>.<id>.<ref>", the
// shape of 05-authn §8's core.auth.oidc.<id>.client_secret.

const (
	towerPath   config.Path = "core.gotham.watchtower"
	kSignals    config.Path = towerPath + ".signals"
	kBatcaveKey config.Path = towerPath + ".batcave.key"
	kArkhamKey  config.Path = towerPath + ".arkham.key"
	kWayneKey   config.Path = towerPath + ".wayne.key"
	// leak is a fake secret a client or operator pastes where it does not
	// belong; it must appear in no error, log line, report or dump.
	leak = "jokers-wild-hunter2"
)

// signal is a list entry with a secret of its own, bound to its issuer.
type signal struct {
	ID     string      `json:"id"`
	Issuer string      `json:"issuer" secret:"bind"`
	Note   string      `json:"note,omitempty"`
	Key    secrets.Ref `json:"key" protect:"admin"`
}

// watchtower mirrors core.auth.oidc: a plain key beside an owner list.
type watchtower struct {
	Base    string   `json:"base"`
	Signals []signal `json:"signals" protect:"owner"`
}

// Validate refuses an entry whose issuer or note is "joker", quoting the
// issuer: in a section holding entry secrets the message must not survive.
func (w *watchtower) Validate() error {
	for _, s := range w.Signals {
		if strings.HasPrefix(s.Issuer, "joker") || strings.HasPrefix(s.Note, "joker") {
			return &config.FieldError{Path: kSignals, Err: errors.New("issuer " + s.Issuer + " is not trusted")}
		}
	}
	return nil
}

// mapResolver serves the secrets it holds, by name: stored, or from the
// environment for a name in env.
type mapResolver struct {
	mu   sync.Mutex
	vals map[secrets.Name]string
	env  map[secrets.Name]bool
}

func (m *mapResolver) meta(n secrets.Name) secrets.Meta {
	_, ok := m.vals[n]
	src := secrets.SourceStored
	if m.env[n] {
		src = secrets.SourceEnv
	}
	if !ok {
		return secrets.Meta{Name: n}
	}
	return secrets.Meta{Name: n, Set: true, Source: src}
}

func (m *mapResolver) Get(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vals[n]
	if !ok {
		return secrets.Value{}, secrets.Meta{Name: n}, secrets.ErrNotSet
	}
	return secrets.NewValue(v), m.meta(n), nil
}

func (m *mapResolver) Meta(_ context.Context, n secrets.Name) (secrets.Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.meta(n), nil
}

// syncCommitter commits through cm and then applies the commit's secret
// writes to res, as the secrets store would have them.
type syncCommitter struct {
	cm  *configtest.Committer
	res *mapResolver
}

func (c syncCommitter) Commit(ctx context.Context, cm config.Commit) error {
	if err := c.cm.Commit(ctx, cm); err != nil {
		return err
	}
	c.res.mu.Lock()
	defer c.res.mu.Unlock()
	for _, w := range cm.Secrets {
		if w.Delete {
			delete(c.res.vals, w.Name)
			continue
		}
		c.res.vals[w.Name] = w.Value.Reveal()
	}
	return nil
}

// towerRig is a store holding the watchtower section only.
type towerRig struct {
	t    *testing.T
	st   *config.Store
	h    *config.Handle[watchtower]
	mem  *configtest.Memory
	cm   *configtest.Committer
	logs *logRecorder
	res  *mapResolver
}

type towerOpts struct {
	file, env map[config.Path]string
	rows      map[config.Path]string
	flags     map[config.Path]string
	ownerSet  bool
}

// newTower registers the section; it does not boot.
func newTower(t *testing.T, o towerOpts) *towerRig {
	t.Helper()
	r := &towerRig{t: t, mem: configtest.NewMemory(), logs: &logRecorder{}, res: &mapResolver{vals: map[secrets.Name]string{}}}
	r.cm = configtest.NewCommitter(r.mem)
	opts := config.Options{AppID: "gotham", Logger: slog.New(r.logs), Flags: o.flags}
	if o.ownerSet {
		opts.OwnerSet = func() bool { return true }
	}
	st, err := config.NewStore(opts)
	if err != nil {
		t.Fatal(err)
	}
	r.st = st
	t.Cleanup(func() {
		if err := st.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	config.SetOperatorSources(st, source("file", o.file), source("env", o.env))
	if r.h, err = config.Register(st.Registry(), config.Define(towerPath, watchtower{Signals: []signal{}})); err != nil {
		t.Fatal(err)
	}
	for p, v := range o.rows {
		r.mem.Put(config.Override{Path: p, Value: json.RawMessage(v), By: "alfred"})
	}
	return r
}

// start loads, binds the resolver and attaches; it returns Load's or
// Attach's error.
func (r *towerRig) start() (config.AttachReport, error) {
	ctx := context.Background()
	if err := r.st.Load(ctx); err != nil {
		return config.AttachReport{}, err
	}
	r.st.SetSecrets(r.res)
	return r.st.Attach(ctx, r.mem, syncCommitter{cm: r.cm, res: r.res})
}

// startedTower is newTower + start, failing t on an error.
func startedTower(t *testing.T, o towerOpts) *towerRig {
	t.Helper()
	r := newTower(t, o)
	if _, err := r.start(); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *towerRig) apply(a config.Actor, set map[config.Path]string, unset ...config.Path) (config.Result, error) {
	p := config.Patch{Set: map[config.Path]json.RawMessage{}, Unset: unset}
	for k, v := range set {
		p.Set[k] = json.RawMessage(v)
	}
	return r.st.Apply(context.Background(), p, a)
}

// mustApply applies and fails t on an error.
func (r *towerRig) mustApply(a config.Actor, set map[config.Path]string, unset ...config.Path) {
	r.t.Helper()
	if _, err := r.apply(a, set, unset...); err != nil {
		r.t.Fatalf("Apply %v: %v", set, err)
	}
}

// lastSecrets is the secret writes of the last commit.
func (r *towerRig) lastSecrets() []secrets.Write {
	r.t.Helper()
	cms := r.cm.Commits()
	if len(cms) == 0 {
		r.t.Fatal("no commit")
	}
	return cms[len(cms)-1].Secrets
}

// deletes lists the names the last commit deletes, sorted.
func (r *towerRig) deletes() []string {
	r.t.Helper()
	var out []string
	for _, w := range r.lastSecrets() {
		if w.Delete {
			out = append(out, string(w.Name))
		}
	}
	slices.Sort(out)
	return out
}

// owner is an interactive admin with owner proof.
func owner() config.Actor {
	return config.Actor{Subject: "bruce", Interactive: true, Owner: true, Can: func(string) bool { return true }}
}

const twoSignals = `[{"id":"batcave","issuer":"https://id.example.com"},{"id":"arkham","issuer":"https://id.example.com"}]`

// TestEntrySecret_RegisterShapes: only a direct Ref field of a struct list
// entry with a string id, with its own protect tag, in a struct section,
// below the root, is accepted; every other shape keeps failing.
func TestEntrySecret_RegisterShapes(t *testing.T) {
	type noID struct {
		Name string      `json:"name"`
		Key  secrets.Ref `json:"key" protect:"admin"`
	}
	type intID struct {
		ID  int         `json:"id"`
		Key secrets.Ref `json:"key" protect:"admin"`
	}
	type deep struct {
		ID   string `json:"id"`
		Auth struct {
			Key secrets.Ref `json:"key" protect:"admin"`
		} `json:"auth"`
	}
	type refList struct {
		ID   string        `json:"id"`
		Keys []secrets.Ref `json:"keys" protect:"admin"`
	}
	type untagged struct {
		ID  string      `json:"id"`
		Key secrets.Ref `json:"key"`
	}
	type bootRef struct {
		ID  string      `json:"id"`
		Key secrets.Ref `json:"key" protect:"admin" reload:"boot"`
	}
	type hidden struct {
		ID  string      `json:"id"`
		Key secrets.Ref `json:"-" protect:"admin"`
	}
	type ptrList struct {
		Signals []*signal `json:"signals"`
	}
	type arrList struct {
		Signals [2]signal `json:"signals"`
	}
	type mapOf struct {
		Signals map[string]signal `json:"signals"`
	}
	type bootList struct {
		Signals []signal `json:"signals" reload:"boot"`
	}
	type twoLists struct {
		Signals []signal `json:"signals"`
		Flares  []signal `json:"flares"`
	}
	type stricterRef struct {
		ID  string      `json:"id"`
		Key secrets.Ref `json:"key" protect:"owner"`
	}
	type adminList struct {
		Signals []stricterRef `json:"signals" protect:"admin"`
	}
	refused := map[string]any{
		"no id": config.Define("core.gotham.a", struct {
			L []noID `json:"l"`
		}{}),
		"int id": config.Define("core.gotham.b", struct {
			L []intID `json:"l"`
		}{}),
		"nested Ref": config.Define("core.gotham.c", struct {
			L []deep `json:"l"`
		}{}),
		"list of Refs": config.Define("core.gotham.d", struct {
			L []refList `json:"l"`
		}{}),
		"no protect tag": config.Define("core.gotham.e", struct {
			L []untagged `json:"l"`
		}{}),
		"boot Ref": config.Define("core.gotham.f", struct {
			L []bootRef `json:"l"`
		}{}),
		"json dash Ref": config.Define("core.gotham.g", struct {
			L []hidden `json:"l"`
		}{}),
		"pointer entries": config.Define("core.gotham.h", ptrList{}),
		"array":           config.Define("core.gotham.i", arrList{}),
		"map":             config.Define("core.gotham.j", mapOf{}),
		"boot list":       config.Define("core.gotham.k", bootList{}),
		"two lists":       config.Define("core.gotham.l", twoLists{}),
		"root list":       config.Define("core.gotham.m", []signal{}),
		"map section":     config.Define("core.gotham.n", map[string]watchtower{}),
	}
	for name, sec := range refused {
		st, err := config.NewStore(config.Options{AppID: "gotham"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.RegisterSections(st.Registry(), sec); !errors.Is(err, config.ErrInvalid) {
			t.Errorf("%s: Register = %v, want ErrInvalid", name, err)
		}
	}
	st, err := config.NewStore(config.Options{AppID: "gotham"})
	if err != nil {
		t.Fatal(err)
	}
	// A Ref stricter than its list is its own key's class, not ignored.
	if _, err := config.Register(st.Registry(), config.Define("core.gotham.strict", adminList{})); err != nil {
		t.Fatalf("a Ref stricter than its list: %v", err)
	}
	if _, err := config.Register(st.Registry(), config.Define(towerPath, watchtower{})); err != nil {
		t.Fatalf("the accepted shape: %v", err)
	}
}

// TestEntrySecret_NamespaceOverlap: no other section may lie under an entry
// list's parent, registered before or after it.
func TestEntrySecret_NamespaceOverlap(t *testing.T) {
	type lamp struct {
		On bool `json:"on"`
	}
	for _, order := range [][]any{
		{config.Define(towerPath, watchtower{}), config.Define(towerPath+".batcave", lamp{})},
		{config.Define(towerPath+".batcave", lamp{}), config.Define(towerPath, watchtower{})},
	} {
		st, err := config.NewStore(config.Options{AppID: "gotham"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.RegisterSections(st.Registry(), order...); !errors.Is(err, config.ErrDuplicate) {
			t.Errorf("Register = %v, want ErrDuplicate", err)
		}
	}
}

// TestEntrySecret_KeysAndBinding: each entry's secret is a key of its own,
// live and protected by its own tag, listed only while an entry names it,
// and the snapshot's Ref resolves under that name.
func TestEntrySecret_KeysAndBinding(t *testing.T) {
	ctx := context.Background()
	r := startedTower(t, towerOpts{})
	if _, ok := r.st.Lookup(kBatcaveKey); ok {
		t.Fatal("a secret is listed before any entry names it")
	}
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	for _, p := range []config.Path{kBatcaveKey, kArkhamKey} {
		ks, ok := r.st.Lookup(p)
		if !ok {
			t.Fatalf("%s not listed", p)
		}
		if ks.Section != towerPath || ks.Protection != config.ProtectAdmin || ks.Reload != config.ReloadLive || ks.Effective != nil || ks.Secret == nil || ks.Secret.Set {
			t.Errorf("%s: %+v", p, ks)
		}
		if !strings.Contains(string(ks.Schema), `"x-mediacore-secret":true`) || !strings.Contains(string(ks.Schema), `"writeOnly":true`) {
			t.Errorf("%s schema %s", p, ks.Schema)
		}
	}
	if _, ok := r.st.Lookup(towerPath + ".robin.key"); ok {
		t.Error("an ID no entry holds is listed")
	}
	ks, _ := r.st.Lookup(kSignals)
	if !strings.Contains(string(ks.Effective), `"key":{"secret":"`+string(kBatcaveKey)+`"}`) {
		t.Errorf("the list's value %s does not carry the entry's reference", ks.Effective)
	}
	r.res.vals[secrets.Name(kBatcaveKey)] = "wayne-tech"
	got := r.h.Get().Signals
	if got[0].Key.Name() != secrets.Name(kBatcaveKey) || got[1].Key.Name() != secrets.Name(kArkhamKey) {
		t.Fatalf("Refs named %s, %s", got[0].Key.Name(), got[1].Key.Name())
	}
	if v, err := got[0].Key.Resolve(ctx); err != nil || v.Reveal() != "wayne-tech" {
		t.Errorf("Resolve = %v", err)
	}
	if _, err := got[1].Key.Resolve(ctx); !errors.Is(err, secrets.ErrNotSet) {
		t.Errorf("an unset entry secret resolves with %v", err)
	}
	schema, err := r.st.Schema(nil)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Properties map[string]struct {
			Properties map[string]struct {
				Items struct {
					Properties map[string]json.RawMessage `json:"properties"`
					Required   []string                   `json:"required"`
				} `json:"items"`
			} `json:"properties"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &doc); err != nil {
		t.Fatal(err)
	}
	items := doc.Properties[string(towerPath)].Properties["signals"].Items
	var key struct {
		Template  string `json:"x-mediacore-entry-secret"`
		WriteOnly bool   `json:"writeOnly"`
		Type      string `json:"type"`
	}
	if err := json.Unmarshal(items.Properties["key"], &key); err != nil {
		t.Fatal(err)
	}
	if key.Template != string(towerPath)+".<id>.key" || key.WriteOnly || key.Type != "object" {
		t.Errorf("the entry's key schema %s", items.Properties["key"])
	}
	for _, req := range items.Required {
		if req == "key" {
			t.Error("an entry must supply its reference")
		}
	}
}

// TestEntrySecret_EntriesRefused: an entry with no id, an id that is not a
// path segment, a reserved or repeated id, or a reference naming another
// secret is refused at validation, naming the entry, never the id.
func TestEntrySecret_EntriesRefused(t *testing.T) {
	r := startedTower(t, towerOpts{})
	for name, v := range map[string]string{
		"no id":         `[{"issuer":"x"}]`,
		"empty id":      `[{"id":"","issuer":"x"}]`,
		"upper case":    `[{"id":"Batcaveq","issuer":"x"}]`,
		"dash":          `[{"id":"bat-caveq","issuer":"x"}]`,
		"digit first":   `[{"id":"9batcaveq","issuer":"x"}]`,
		"too long":      `[{"id":"b` + strings.Repeat("q", 48) + `","issuer":"x"}]`,
		"duplicate":     `[{"id":"batcaveq","issuer":"x"},{"id":"batcaveq","issuer":"y"}]`,
		"reserved list": `[{"id":"signals","issuer":"x"}]`,
		"reserved key":  `[{"id":"base","issuer":"x"}]`,
		"foreign ref":   `[{"id":"batcaveq","issuer":"x","key":{"secret":"core.http.basic_gate.password"}}]`,
		"other entry":   `[{"id":"batcaveq","issuer":"x","key":{"secret":"core.gotham.watchtower.arkham.key"}}]`,
	} {
		_, err := r.apply(owner(), map[config.Path]string{kSignals: v})
		switch {
		case !errors.Is(err, config.ErrInvalid):
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		case strings.Contains(err.Error(), "batcaveq") || strings.Contains(err.Error(), "caveq"):
			t.Errorf("%s: the error quotes the id: %v", name, err)
		case !strings.Contains(err.Error(), "entr"):
			t.Errorf("%s: the error names no entry: %v", name, err)
		}
	}
	if n := r.cm.Calls(); n != 0 {
		t.Errorf("%d commits", n)
	}
	for _, v := range []string{
		`[{"id":"batcave","issuer":"x","key":{"secret":""}}]`,
		`[{"id":"batcave","issuer":"x","key":{"secret":"core.gotham.watchtower.batcave.key"}}]`,
	} {
		if _, err := r.apply(owner(), map[config.Path]string{kSignals: v}); err != nil {
			t.Errorf("%s: %v", v, err)
		}
	}
}

// TestEntrySecret_Writes: an entry's secret is written through Apply like a
// secret key, by its own protection, only while an entry names it.
func TestEntrySecret_Writes(t *testing.T) {
	r := startedTower(t, towerOpts{ownerSet: true})
	if _, err := r.apply(admin(), map[config.Path]string{kSignals: twoSignals}); !errors.Is(err, config.ErrProtected) {
		t.Fatalf("an admin without owner proof wrote the owner list: %v", err)
	}
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	if _, err := r.apply(admin(), map[config.Path]string{kWayneKey: `"x"`}); !errors.Is(err, config.ErrUnknownKey) {
		t.Errorf("a secret no entry names: %v, want ErrUnknownKey", err)
	}
	if _, err := r.apply(admin(), map[config.Path]string{kSignals: twoSignals[:len(twoSignals)-1] + `,{"id":"wayne","issuer":"x"}]`, kWayneKey: `"x"`}); !errors.Is(err, config.ErrUnknownKey) {
		t.Errorf("an entry and its secret in one patch: %v, want ErrUnknownKey", err)
	}
	if _, err := r.apply(manager(), map[config.Path]string{kBatcaveKey: `"x"`}); !errors.Is(err, config.ErrProtected) {
		t.Errorf("a manager wrote an admin entry secret: %v", err)
	}
	calls := r.cm.Calls()
	r.mustApply(admin(), map[config.Path]string{kBatcaveKey: `"wayne-tech"`})
	if r.cm.Calls() != calls+1 {
		t.Fatal("no commit")
	}
	w := r.lastSecrets()
	if len(w) != 1 || w[0].Name != secrets.Name(kBatcaveKey) || w[0].Value.Reveal() != "wayne-tech" || w[0].Delete || w[0].By != "alfred" {
		t.Fatalf("writes %+v", w)
	}
	if rows := r.mem.Rows(); len(rows) != 1 {
		t.Errorf("rows %v: an entry secret wrote a config row", rows)
	}
	r.mustApply(admin(), nil, kBatcaveKey)
	if w := r.lastSecrets(); len(w) != 1 || !w[0].Delete || w[0].Name != secrets.Name(kBatcaveKey) {
		t.Errorf("unset writes %+v", w)
	}
}

// TestEntrySecret_ChangedEntriesDelete: an Apply deletes, in the same
// commit, the secret of every entry it does not keep as it was: removed,
// renamed, added or rebound (a secret:"bind" field changed). A kept entry's
// secret survives a change to a field that does not bind it.
func TestEntrySecret_ChangedEntriesDelete(t *testing.T) {
	r := startedTower(t, towerOpts{})
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	if got := r.deletes(); !slices.Equal(got, []string{string(kArkhamKey), string(kBatcaveKey)}) {
		t.Fatalf("adding two entries deletes %v", got)
	}
	r.res.vals[secrets.Name(kArkhamKey)] = "asylum"
	r.res.vals[secrets.Name(kBatcaveKey)] = "cave"
	const batcaveOnly = `[{"id":"batcave","issuer":"https://id.example.com"}]`
	r.mustApply(owner(), map[config.Path]string{kSignals: batcaveOnly})
	if got := r.deletes(); !slices.Equal(got, []string{string(kArkhamKey)}) || r.lastSecrets()[0].By != "bruce" {
		t.Fatalf("a removal deletes %v", got)
	}
	if _, ok := r.st.Lookup(kArkhamKey); ok {
		t.Error("a removed entry's secret is still listed")
	}
	if _, err := r.apply(admin(), map[config.Path]string{kArkhamKey: `"x"`}); !errors.Is(err, config.ErrUnknownKey) {
		t.Errorf("a removed entry's secret is writable: %v", err)
	}
	// A field that does not bind the secret keeps it.
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"https://id.example.com","note":"rooftop"}]`})
	if got := r.deletes(); len(got) != 0 || r.res.vals[secrets.Name(kBatcaveKey)] != "cave" {
		t.Fatalf("a note change deletes %v", got)
	}
	// A rebound entry (its issuer changed) loses its secret, as a rename.
	if _, err := r.apply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"https://idp.example.org"}]`, kBatcaveKey: `"x"`}); !errors.Is(err, config.ErrInvalid) {
		t.Errorf("setting the secret of an entry the patch rebinds: %v, want ErrInvalid", err)
	}
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"https://idp.example.org"}]`})
	if got := r.deletes(); !slices.Equal(got, []string{string(kBatcaveKey)}) {
		t.Fatalf("an issuer change deletes %v", got)
	}
	if v, err := r.h.Get().Signals[0].Key.Resolve(context.Background()); !errors.Is(err, secrets.ErrNotSet) {
		t.Errorf("a rebound entry resolves %v (%v)", v.IsZero(), err)
	}
	// A rename deletes the old ID's secret and starts the new one unset.
	r.res.vals[secrets.Name(kWayneKey)] = "stale"
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"wayne","issuer":"https://idp.example.org"}]`})
	if got := r.deletes(); !slices.Equal(got, []string{string(kBatcaveKey), string(kWayneKey)}) {
		t.Fatalf("a rename deletes %v", got)
	}
	// An unset list goes back to its default, removing every entry.
	r.mustApply(owner(), nil, kSignals)
	if got := r.deletes(); !slices.Equal(got, []string{string(kWayneKey)}) {
		t.Fatalf("an unset deletes %v", got)
	}
	// A reorder keeps every entry as it was.
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://id.example.com"},{"id":"batcave","issuer":"https://id.example.com"}]`})
	if got := r.deletes(); len(got) != 0 {
		t.Errorf("a reorder deletes %v", got)
	}
}

// TestEntrySecret_ClearRefusals: a change that cannot delete an entry's
// secret is refused whole.
func TestEntrySecret_ClearRefusals(t *testing.T) {
	r := startedTower(t, towerOpts{env: map[config.Path]string{kArkhamKey: `"asylum"`, kWayneKey: `"manor"`}, rows: map[config.Path]string{kSignals: twoSignals}})
	ks, ok := r.st.Lookup(kArkhamKey)
	if !ok || ks.Source != config.SourceEnv || ks.LockedBy == "" {
		t.Fatalf("an operator-set entry secret: %+v", ks)
	}
	if _, err := r.apply(admin(), map[config.Path]string{kArkhamKey: `"x"`}); !errors.Is(err, config.ErrLocked) {
		t.Errorf("an operator-set entry secret was written: %v", err)
	}
	if _, err := r.apply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"https://id.example.com"}]`}); !errors.Is(err, config.ErrLocked) {
		t.Errorf("removing an entry whose secret an operator sets: %v, want ErrLocked", err)
	}
	if _, err := r.apply(owner(), map[config.Path]string{kSignals: twoSignals[:len(twoSignals)-1] + `,{"id":"wayne","issuer":"x"}]`}); !errors.Is(err, config.ErrLocked) {
		t.Errorf("adding an entry whose secret an operator sets: %v, want ErrLocked", err)
	}
	if _, err := r.apply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://id.example.com"}]`, kBatcaveKey: `"x"`}); !errors.Is(err, config.ErrInvalid) {
		t.Errorf("setting the secret of an entry the patch removes: %v, want ErrInvalid", err)
	}
	calls := r.cm.Calls()
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://id.example.com"}]`}, kBatcaveKey)
	if got := r.deletes(); !slices.Equal(got, []string{string(kBatcaveKey)}) || len(r.lastSecrets()) != 1 {
		t.Errorf("an explicit unset with its removal writes %+v", r.lastSecrets())
	}
	if r.cm.Calls() != calls+1 {
		t.Errorf("%d commits, want 1", r.cm.Calls()-calls)
	}

	// A secret stricter than its list: removing or adding its entry needs
	// the secret's protection too.
	type vaultEntry struct {
		ID  string      `json:"id"`
		Key secrets.Ref `json:"key" protect:"owner"`
	}
	type vaults struct {
		Doors []vaultEntry `json:"doors" protect:"admin"`
	}
	st, err := config.NewStore(config.Options{AppID: "gotham", OwnerSet: func() bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close(context.Background()) })
	if _, err := config.Register(st.Registry(), config.Define("core.gotham.vaults", vaults{})); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	mem := configtest.NewMemory()
	if err := st.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Attach(ctx, mem, configtest.NewCommitter(mem)); err != nil {
		t.Fatal(err)
	}
	patch := func(v string) config.Patch {
		return config.Patch{Set: map[config.Path]json.RawMessage{"core.gotham.vaults.doors": json.RawMessage(v)}}
	}
	var pe *config.ProtectedError
	if _, err := st.Apply(ctx, patch(`[{"id":"batcave"}]`), admin()); !errors.As(err, &pe) || pe.Path != "core.gotham.vaults.batcave.key" {
		t.Errorf("an admin added an entry whose secret is owner: %v", err)
	}
	if _, err := st.Apply(ctx, patch(`[{"id":"batcave"}]`), owner()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Apply(ctx, patch(`[]`), admin()); !errors.As(err, &pe) || pe.Path != "core.gotham.vaults.batcave.key" {
		t.Errorf("an admin removed an entry whose secret is owner: %v", err)
	}
	if _, err := st.Apply(ctx, patch(`[]`), owner()); err != nil {
		t.Errorf("the owner: %v", err)
	}
}

// TestEntrySecret_QuarantinedRowIDsDeleted: the entries of a quarantined
// stored row count as gone, so an Apply deletes their secrets whether it
// drops them or adds them again.
func TestEntrySecret_QuarantinedRowIDsDeleted(t *testing.T) {
	r := newTower(t, towerOpts{rows: map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"x","note":"joker"},{"id":"wayne","issuer":"x"}]`}})
	rep, err := r.start()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quarantined[kSignals] == nil {
		t.Fatalf("report %+v", rep)
	}
	r.res.vals[secrets.Name(kWayneKey)] = "stale"
	// wayne is kept exactly as the quarantined row had it, and still loses
	// the secret: a quarantined row is not a live list.
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"x"},{"id":"wayne","issuer":"x"}]`})
	if got := r.deletes(); !slices.Equal(got, []string{string(kArkhamKey), string(kBatcaveKey), string(kWayneKey)}) {
		t.Errorf("deletes %v", got)
	}
}

// stagerRec records the secret writes an Importer stages.
type stagerRec struct {
	writes []string
	fail   error
}

func (f *stagerRec) Stage(_ context.Context, b *storage.Batch, w secrets.Write) error {
	if b == nil || !w.Delete || w.By != "cutover:m2" {
		return fmt.Errorf("unexpected stage %+v", w)
	}
	f.writes = append(f.writes, string(w.Name))
	return f.fail
}

// TestEntrySecret_ImporterClears: an import that replaces a stored list
// deletes, in the step's batch, the secrets of the entries it drops or
// rebinds, as Apply does, and none of an entry it adds or keeps as it was.
func TestEntrySecret_ImporterClears(t *testing.T) {
	stage := func(t *testing.T, row string, st config.SecretStager, next string) (*towerRig, error) {
		t.Helper()
		r := newTower(t, towerOpts{})
		if row != "" {
			r.mem.Put(config.Override{Path: kSignals, Value: json.RawMessage(row), By: "alfred"})
		}
		if err := r.st.Load(context.Background()); err != nil {
			t.Fatal(err)
		}
		r.st.SetImportTarget(r.mem)
		if st != nil {
			r.st.SetImportSecrets(st)
		}
		return r, r.st.Importer().Stage(new(storage.Batch), kSignals, json.RawMessage(next), "cutover:m2")
	}
	const row = `[{"id":"arkham","issuer":"https://id.example.com"},{"id":"batcave","issuer":"https://id.example.com"},{"id":"wayne","issuer":"https://id.example.com"}]`
	const next = `[{"id":"batcave","issuer":"https://idp.example.org"},{"id":"wayne","issuer":"https://id.example.com","note":"manor"},{"id":"robin","issuer":"x"}]`
	rec := &stagerRec{}
	r, err := stage(t, row, rec, next)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(rec.writes)
	if !slices.Equal(rec.writes, []string{string(kArkhamKey), string(kBatcaveKey)}) {
		t.Errorf("an import deletes %v, want the dropped and the rebound entry's", rec.writes)
	}
	if got := string(r.mem.Rows()[kSignals].Value); !strings.Contains(got, "robin") {
		t.Errorf("row %s", got)
	}
	// Without a stager, an import that must delete is refused and stages no row.
	if r, err := stage(t, row, nil, next); err == nil || !strings.Contains(err.Error(), "SetImportSecrets") || strings.Contains(string(r.mem.Rows()[kSignals].Value), "robin") {
		t.Errorf("an import with no stager: %v", err)
	}
	// One with nothing to delete needs none.
	if _, err := stage(t, "", nil, next); err != nil {
		t.Errorf("a first import: %v", err)
	}
	if _, err := stage(t, row, nil, row[:len(row)-1]+`,{"id":"robin","issuer":"x"}]`); err != nil {
		t.Errorf("an import that only adds: %v", err)
	}
	// A stager failure fails the Stage.
	if _, err := stage(t, row, &stagerRec{fail: errors.New("sealed")}, next); err == nil || !strings.Contains(err.Error(), "sealed") {
		t.Errorf("a stager failure: %v", err)
	}
}

// TestEntrySecret_BindTagMisuse: Register refuses a secret tag anywhere but
// on an exported, non-secret field of an entry type, and any value but bind.
func TestEntrySecret_BindTagMisuse(t *testing.T) {
	type onRef struct {
		ID  string      `json:"id"`
		Key secrets.Ref `json:"key" protect:"admin" secret:"bind"`
	}
	type badValue struct {
		ID  string      `json:"id" secret:"yes"`
		Key secrets.Ref `json:"key" protect:"admin"`
	}
	type noSecret struct {
		ID     string `json:"id"`
		Issuer string `json:"issuer" secret:"bind"`
	}
	type inner struct {
		Issuer string `json:"issuer" secret:"bind"`
	}
	type nestedBind struct {
		ID  string      `json:"id"`
		In  inner       `json:"in"`
		Key secrets.Ref `json:"key" protect:"admin"`
	}
	for name, sec := range map[string]any{
		"on a Ref": config.Define("core.gotham.misuse", struct {
			L []onRef `json:"l" protect:"owner"`
		}{}),
		"a value other than bind": config.Define("core.gotham.misuse", struct {
			L []badValue `json:"l" protect:"owner"`
		}{}),
		"in an entry with no secret": config.Define("core.gotham.misuse", struct {
			L []noSecret `json:"l" protect:"owner"`
		}{}),
		"below the entry": config.Define("core.gotham.misuse", struct {
			L []nestedBind `json:"l" protect:"owner"`
		}{}),
		"on a section field": config.Define("core.gotham.misuse", struct {
			Base string `json:"base" secret:"bind"`
		}{}),
	} {
		st, err := config.NewStore(config.Options{AppID: "gotham"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.RegisterSections(st.Registry(), sec); !errors.Is(err, config.ErrInvalid) || !strings.Contains(err.Error(), "secret") {
			t.Errorf("%s: Register = %v, want ErrInvalid", name, err)
		}
	}
}

// TestEntrySecret_RevivalOperatorRemovedEntry (the review's repro): an
// operator layer supplies "arkham" and the store holds a secret under its
// name; once the operator drops the entry, an owner who adds a new "arkham"
// at another issuer starts it unset.
func TestEntrySecret_RevivalOperatorRemovedEntry(t *testing.T) {
	ctx := context.Background()
	mem := configtest.NewMemory()
	res := &mapResolver{vals: map[secrets.Name]string{}}
	boot := func(file map[config.Path]string) *towerRig {
		r := newTower(t, towerOpts{file: file})
		r.mem, r.cm, r.res = mem, configtest.NewCommitter(mem), res
		if _, err := r.start(); err != nil {
			t.Fatal(err)
		}
		return r
	}
	r1 := boot(map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://old-idp.example"}]`})
	// An operator-supplied entry takes no stored secret, and serves none.
	if _, err := r1.apply(admin(), map[config.Path]string{kArkhamKey: `"old-client-secret"`}); !errors.Is(err, config.ErrLocked) {
		t.Fatalf("an admin stored the secret of an operator entry: %v", err)
	}
	res.vals[secrets.Name(kArkhamKey)] = "old-client-secret" // a record from before the operator took the list
	if _, err := r1.h.Get().Signals[0].Key.Resolve(ctx); !errors.Is(err, secrets.ErrNotSet) {
		t.Fatalf("an operator entry resolves a stored secret: %v", err)
	}
	if ks, _ := r1.st.Lookup(kArkhamKey); ks.Secret == nil || ks.Secret.Set {
		t.Errorf("an operator entry's stored secret reads set: %+v", ks.Secret)
	}

	r2 := boot(map[config.Path]string{})
	if _, ok := r2.st.Lookup(kArkhamKey); ok {
		t.Error("boot 2 lists the dropped entry's secret")
	}
	r2.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://new-idp.example"}]`})
	if got := r2.deletes(); !slices.Equal(got, []string{string(kArkhamKey)}) {
		t.Fatalf("adding arkham deletes %v", got)
	}
	got, err := r2.h.Get().Signals[0].Key.Resolve(ctx)
	if !errors.Is(err, secrets.ErrNotSet) {
		t.Errorf("REVIVAL: the new arkham resolves the stale secret (%v, %v)", got.IsZero(), err)
	}
}

// TestEntrySecret_RevivalOperatorReAddsAtBoot (the review's repro): a stored
// "arkham" has a stored secret; an operator file that supplies its own
// "arkham" at another issuer binds only an operator-layer secret.
func TestEntrySecret_RevivalOperatorReAddsAtBoot(t *testing.T) {
	ctx := context.Background()
	const attacker = `[{"id":"arkham","issuer":"https://attacker.example"}]`
	r := newTower(t, towerOpts{rows: map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://old.example"}]`}, file: map[config.Path]string{kSignals: attacker}})
	r.res.vals[secrets.Name(kArkhamKey)] = "old"
	if _, err := r.start(); err != nil {
		t.Fatal(err)
	}
	v := r.h.Get().Signals[0]
	if v.Issuer != "https://attacker.example" {
		t.Fatalf("issuer %s", v.Issuer)
	}
	if got, err := v.Key.Resolve(ctx); !errors.Is(err, secrets.ErrNotSet) {
		t.Fatalf("an operator entry resolves the stored secret (%v, %v)", got.IsZero(), err)
	}
	if ks, _ := r.st.Lookup(kArkhamKey); ks.Secret == nil || ks.Secret.Set || ks.Source != config.SourceDefault {
		t.Errorf("KeyState %+v", ks)
	}
	// With the secret from the operator too, the entry is served.
	r = newTower(t, towerOpts{file: map[config.Path]string{kSignals: attacker}})
	r.res.vals[secrets.Name(kArkhamKey)] = "from-env"
	r.res.env = map[secrets.Name]bool{secrets.Name(kArkhamKey): true}
	if _, err := r.start(); err != nil {
		t.Fatal(err)
	}
	if got, err := r.h.Get().Signals[0].Key.Resolve(ctx); err != nil || got.Reveal() != "from-env" {
		t.Errorf("an operator entry with an operator secret: %v", err)
	}
	if ks, _ := r.st.Lookup(kArkhamKey); ks.Secret == nil || !ks.Secret.Set || ks.Source != config.SourceEnv {
		t.Errorf("KeyState %+v", ks)
	}
}

// TestEntrySecret_NeverLeaks: a secret pasted into the list — as the
// reference's string, as its name, beside it, or in a field a Validator
// quotes — appears in no error, quarantine report, log line, dump or schema.
func TestEntrySecret_NeverLeaks(t *testing.T) {
	values := map[string]string{
		"as a string":   `[{"id":"batcave","issuer":"x","key":"` + leak + `"}]`,
		"as a name":     `[{"id":"batcave","issuer":"x","key":{"secret":"` + leak + `"}}]`,
		"beside it":     `[{"id":"batcave","issuer":"x","key":{"value":"` + leak + `"}}]`,
		"unknown field": `[{"id":"batcave","issuer":"x","` + leak + `":1}]`,
		"in the issuer": `[{"id":"batcave","issuer":"joker ` + leak + `"}]`,
		"in the id":     `[{"id":"` + leak + `","issuer":"x"}]`,
	}
	check := func(t *testing.T, name, where, s string) {
		t.Helper()
		if strings.Contains(s, leak) || strings.Contains(s, "hunter2") {
			t.Errorf("%s: %s carries the secret: %s", name, where, s)
		}
	}
	for name, v := range values {
		// Apply.
		r := startedTower(t, towerOpts{})
		_, err := r.apply(owner(), map[config.Path]string{kSignals: v})
		if err == nil {
			t.Fatalf("%s: Apply accepted it", name)
		}
		check(t, name, "Apply's error", err.Error())

		// A stored row: quarantined at Attach.
		r = newTower(t, towerOpts{rows: map[config.Path]string{kSignals: v}})
		rep, err := r.start()
		if err != nil {
			t.Fatalf("%s: Attach: %v", name, err)
		}
		if rep.Quarantined[kSignals] == nil {
			t.Errorf("%s: the row is not quarantined: %+v", name, rep)
		}
		for p, e := range rep.Quarantined {
			check(t, name, "the quarantine report of "+string(p), e.Error())
		}
		ks, _ := r.st.Lookup(kSignals)
		if ks.Quarantined != nil {
			check(t, name, "KeyState.Quarantined", ks.Quarantined.Error())
		}
		check(t, name, "the logs", r.logs.all())

		// An operator value: Load fails, redacted.
		for _, o := range []towerOpts{{env: map[config.Path]string{kSignals: v}}, {flags: map[config.Path]string{kSignals: v}}} {
			r = newTower(t, o)
			_, err = r.start()
			if err == nil {
				t.Fatalf("%s: an operator value booted", name)
			}
			check(t, name, "Load's error", err.Error())
			check(t, name, "the logs", r.logs.all())
		}

		// The operator file: dumped without the list.
		r = newTower(t, towerOpts{file: map[config.Path]string{kSignals: v, towerPath + ".base": `"gotham"`}})
		dump, err := r.st.RedactedConfigFile()
		if err != nil {
			t.Fatal(err)
		}
		check(t, name, "the redacted file", string(dump))
		if !strings.Contains(string(dump), "gotham") {
			t.Errorf("%s: the redacted file lost a plain key: %s", name, dump)
		}
		if !r.st.ConfigFileHasSecrets() {
			t.Errorf("%s: a file setting a list with entry secrets is not archived as secret", name)
		}
	}
	// A flag never sets an entry secret (it would show in the process list).
	r := newTower(t, towerOpts{flags: map[config.Path]string{kBatcaveKey: leak}})
	_, err := r.start()
	if err == nil || !strings.Contains(err.Error(), "never set by a flag") {
		t.Fatalf("a flagged entry secret: %v", err)
	}
	check(t, "flag", "Load's error", err.Error())
	// A stored row at an entry secret's path is never read as its value.
	r = newTower(t, towerOpts{rows: map[config.Path]string{kBatcaveKey: `"` + leak + `"`}})
	rep, err := r.start()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quarantined[kBatcaveKey] == nil {
		t.Fatalf("a row at an entry secret's path: %+v", rep)
	}
	check(t, "row", "the quarantine report", rep.Quarantined[kBatcaveKey].Error())
	schema, err := r.st.Schema(nil)
	if err != nil {
		t.Fatal(err)
	}
	check(t, "row", "the schema", string(schema))
}

// TestEntrySecret_MetaFollowsEntries: KeyState.Secret is read for every
// entry the list holds and dropped with the entry.
func TestEntrySecret_MetaFollowsEntries(t *testing.T) {
	r := newTower(t, towerOpts{rows: map[config.Path]string{kSignals: twoSignals}})
	r.res.vals[secrets.Name(kArkhamKey)] = "asylum"
	if _, err := r.start(); err != nil {
		t.Fatal(err)
	}
	ks, ok := r.st.Lookup(kArkhamKey)
	if !ok || ks.Secret == nil || !ks.Secret.Set || ks.Source != config.SourceOverride {
		t.Fatalf("%+v", ks)
	}
	if ks, _ := r.st.Lookup(kBatcaveKey); ks.Secret == nil || ks.Secret.Set || ks.Source != config.SourceDefault {
		t.Errorf("an unset entry secret: %+v", ks)
	}
	// An entry added by Apply starts unset, whatever its name held.
	r.res.vals[secrets.Name(kWayneKey)] = "manor"
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals[:len(twoSignals)-1] + `,{"id":"wayne","issuer":"x"}]`})
	if ks, ok := r.st.Lookup(kWayneKey); !ok || ks.Secret == nil || ks.Secret.Set {
		t.Errorf("an added entry's stale record: %+v", ks)
	}
	r.mustApply(admin(), map[config.Path]string{kWayneKey: `"manor"`})
	if ks, _ := r.st.Lookup(kWayneKey); ks.Secret == nil || !ks.Secret.Set {
		t.Errorf("a set entry secret: %+v", ks)
	}
	var n int
	for p, ks := range r.st.Keys() {
		if ks.Secret != nil {
			n++
			if ks.Effective != nil {
				t.Errorf("%s carries a value", p)
			}
		}
	}
	if n != 3 {
		t.Errorf("%d secret keys, want 3", n)
	}
}
