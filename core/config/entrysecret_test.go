// file: core/config/entrysecret_test.go
// version: 1.0.0
// guid: 3d0a6f8e-5b1c-4f27-9e43-8a2d7c61b5f4
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/config/configtest"
	"github.com/falkcorp/mediacore/secrets"
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

// signal is a list entry with a secret of its own.
type signal struct {
	ID     string      `json:"id"`
	Issuer string      `json:"issuer"`
	Key    secrets.Ref `json:"key" protect:"admin"`
}

// watchtower mirrors core.auth.oidc: a plain key beside an owner list.
type watchtower struct {
	Base    string   `json:"base"`
	Signals []signal `json:"signals" protect:"owner"`
}

// Validate refuses an entry whose issuer is "joker", quoting the issuer: in a
// section holding entry secrets the message must not survive.
func (w *watchtower) Validate() error {
	for _, s := range w.Signals {
		if strings.HasPrefix(s.Issuer, "joker") {
			return &config.FieldError{Path: kSignals, Err: errors.New("issuer " + s.Issuer + " is not trusted")}
		}
	}
	return nil
}

// mapResolver serves the secrets it holds, by name.
type mapResolver struct {
	mu   sync.Mutex
	vals map[secrets.Name]string
}

func (m *mapResolver) Get(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.vals[n]
	if !ok {
		return secrets.Value{}, secrets.Meta{Name: n}, secrets.ErrNotSet
	}
	return secrets.NewValue(v), secrets.Meta{Name: n, Set: true, Source: secrets.SourceStored}, nil
}

func (m *mapResolver) Meta(_ context.Context, n secrets.Name) (secrets.Meta, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.vals[n]
	return secrets.Meta{Name: n, Set: ok, Source: secrets.SourceStored}, nil
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
	return r.st.Attach(ctx, r.mem, r.cm)
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
		"no id":           config.Define("core.gotham.a", struct{ L []noID `json:"l"` }{}),
		"int id":          config.Define("core.gotham.b", struct{ L []intID `json:"l"` }{}),
		"nested Ref":      config.Define("core.gotham.c", struct{ L []deep `json:"l"` }{}),
		"list of Refs":    config.Define("core.gotham.d", struct{ L []refList `json:"l"` }{}),
		"no protect tag":  config.Define("core.gotham.e", struct{ L []untagged `json:"l"` }{}),
		"boot Ref":        config.Define("core.gotham.f", struct{ L []bootRef `json:"l"` }{}),
		"json dash Ref":   config.Define("core.gotham.g", struct{ L []hidden `json:"l"` }{}),
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

// TestEntrySecret_RemovalDeletes: removing or renaming an entry deletes its
// secret in the same commit, so no later entry inherits it.
func TestEntrySecret_RemovalDeletes(t *testing.T) {
	r := startedTower(t, towerOpts{})
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	r.res.vals[secrets.Name(kArkhamKey)] = "asylum"
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"x"}]`})
	if w := r.lastSecrets(); len(w) != 1 || w[0].Name != secrets.Name(kArkhamKey) || !w[0].Delete || w[0].By != "bruce" {
		t.Fatalf("removal writes %+v", w)
	}
	if _, ok := r.st.Lookup(kArkhamKey); ok {
		t.Error("a removed entry's secret is still listed")
	}
	if _, err := r.apply(admin(), map[config.Path]string{kArkhamKey: `"x"`}); !errors.Is(err, config.ErrUnknownKey) {
		t.Errorf("a removed entry's secret is writable: %v", err)
	}
	// A rename is a removal: the secret is never carried to the new ID.
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"wayne","issuer":"x"}]`})
	if w := r.lastSecrets(); len(w) != 1 || w[0].Name != secrets.Name(kBatcaveKey) || !w[0].Delete {
		t.Fatalf("rename writes %+v", w)
	}
	// An unset list goes back to its default, removing every entry.
	r.mustApply(owner(), nil, kSignals)
	if w := r.lastSecrets(); len(w) != 1 || w[0].Name != secrets.Name(kWayneKey) || !w[0].Delete {
		t.Fatalf("unset writes %+v", w)
	}
	// A list rewritten with the same entries deletes nothing.
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"https://id.example.com"},{"id":"batcave","issuer":"x"}]`})
	if w := r.lastSecrets(); len(w) != 0 {
		t.Errorf("a reorder writes %+v", w)
	}
}

// TestEntrySecret_RemovalRefusals: a removal that cannot delete the secret
// is refused whole.
func TestEntrySecret_RemovalRefusals(t *testing.T) {
	r := startedTower(t, towerOpts{env: map[config.Path]string{kArkhamKey: `"asylum"`}})
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals})
	ks, ok := r.st.Lookup(kArkhamKey)
	if !ok || ks.Source != config.SourceEnv || ks.LockedBy == "" {
		t.Fatalf("an operator-set entry secret: %+v", ks)
	}
	calls := r.cm.Calls()
	if _, err := r.apply(admin(), map[config.Path]string{kArkhamKey: `"x"`}); !errors.Is(err, config.ErrLocked) {
		t.Errorf("an operator-set entry secret was written: %v", err)
	}
	if _, err := r.apply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"x"}]`}); !errors.Is(err, config.ErrLocked) {
		t.Errorf("removing an entry whose secret an operator sets: %v, want ErrLocked", err)
	}
	if _, err := r.apply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"x"}]`, kBatcaveKey: `"x"`}); !errors.Is(err, config.ErrInvalid) {
		t.Errorf("setting the secret of an entry the patch removes: %v, want ErrInvalid", err)
	}
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"x"}]`}, kBatcaveKey)
	if w := r.lastSecrets(); len(w) != 1 || w[0].Name != secrets.Name(kBatcaveKey) || !w[0].Delete {
		t.Errorf("an explicit unset with its removal writes %+v", w)
	}
	if r.cm.Calls() != calls+1 {
		t.Errorf("%d commits, want 1", r.cm.Calls()-calls)
	}

	// A secret stricter than its list: removing its entry needs the
	// secret's protection too.
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
	if _, err := st.Apply(ctx, patch(`[{"id":"batcave"}]`), admin()); err != nil {
		t.Fatal(err)
	}
	var pe *config.ProtectedError
	if _, err := st.Apply(ctx, patch(`[]`), admin()); !errors.As(err, &pe) || pe.Path != "core.gotham.vaults.batcave.key" {
		t.Errorf("an admin removed an entry whose secret is owner: %v", err)
	}
	if _, err := st.Apply(ctx, patch(`[]`), owner()); err != nil {
		t.Errorf("the owner: %v", err)
	}
}

// TestEntrySecret_QuarantinedRowIDsDeleted: an entry of a stored row that
// was quarantined still has its secret deleted when Apply replaces the list
// without it.
func TestEntrySecret_QuarantinedRowIDsDeleted(t *testing.T) {
	r := newTower(t, towerOpts{rows: map[config.Path]string{kSignals: `[{"id":"arkham","issuer":"joker-gas"}]`}})
	rep, err := r.start()
	if err != nil {
		t.Fatal(err)
	}
	if rep.Quarantined[kSignals] == nil {
		t.Fatalf("report %+v", rep)
	}
	r.mustApply(owner(), map[config.Path]string{kSignals: `[{"id":"batcave","issuer":"x"}]`})
	if w := r.lastSecrets(); len(w) != 1 || w[0].Name != secrets.Name(kArkhamKey) || !w[0].Delete {
		t.Errorf("writes %+v", w)
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
	// An entry added by Apply whose name holds a record reads it.
	r.res.vals[secrets.Name(kWayneKey)] = "manor"
	r.mustApply(owner(), map[config.Path]string{kSignals: twoSignals[:len(twoSignals)-1] + `,{"id":"wayne","issuer":"x"}]`})
	if ks, ok := r.st.Lookup(kWayneKey); !ok || ks.Secret == nil || !ks.Secret.Set {
		t.Errorf("an added entry's stored secret: %+v", ks)
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
