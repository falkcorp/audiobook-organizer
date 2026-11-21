// file: internal/books/appconfig/fake_test.go
// version: 1.3.0
// guid: 2a6d8f14-7c39-4e51-9b0a-e3f5c1d7a862
// last-edited: 2026-11-19

package appconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"maps"
	"slices"
	"sync"

	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// fakeAccess is a module.ConfigAccess over maps: just enough of core's store to
// drive the Engine without booting a kernel. Apply writes the values, bumps the
// generation and fires the OnCommit hooks, as core does.
type fakeAccess struct {
	mu        sync.Mutex
	vals      map[coreconfig.Path]json.RawMessage // effective values of registered keys
	locked    map[coreconfig.Path]string
	pending   map[coreconfig.Path]bool // keys Lookup reports as stored-not-yet-effective
	regSecret map[coreconfig.Path]bool // registered secret keys
	secrets   map[secrets.Name]string  // the secret store
	unreadble map[secrets.Name]bool
	secretErr map[secrets.Name]error // a bare error Secret returns, with no Meta.Err
	envSecret map[secrets.Name]bool
	putFail   map[secrets.Name]bool
	delFail   map[secrets.Name]bool
	over      map[coreconfig.Path]bool            // keys a stored override holds
	defs      map[coreconfig.Path]json.RawMessage // what an Unset falls back to
	failApply int                                 // the 1-based Apply call that fails (0 = none)
	applyN    int
	pass      map[coreconfig.Path]coreconfig.OperatorValue
	gen       uint64
	hooks     []func(uint64)
	applyErr  error
	applied   []coreconfig.Patch
	actors    []coreconfig.Actor // who each Apply ran as
	puts      []secrets.Name
	dels      []secrets.Name
	by        []string
	trace     []string // every committed write in order: apply, put <name>, del <name>
}

func newFake() *fakeAccess {
	return &fakeAccess{vals: map[coreconfig.Path]json.RawMessage{}, locked: map[coreconfig.Path]string{},
		regSecret: map[coreconfig.Path]bool{}, secrets: map[secrets.Name]string{}, unreadble: map[secrets.Name]bool{}, secretErr: map[secrets.Name]error{}, envSecret: map[secrets.Name]bool{}, putFail: map[secrets.Name]bool{}, delFail: map[secrets.Name]bool{}, over: map[coreconfig.Path]bool{}, defs: map[coreconfig.Path]json.RawMessage{},
		pass: map[coreconfig.Path]coreconfig.OperatorValue{}}
}

func (f *fakeAccess) Lookup(p coreconfig.Path) (coreconfig.KeyState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.regSecret[p] {
		_, set := f.secrets[secrets.Name(p)]
		return coreconfig.KeyState{Path: p, Secret: &secrets.Meta{Name: secrets.Name(p), Set: set}}, true
	}
	v, ok := f.vals[p]
	if !ok {
		return coreconfig.KeyState{}, false
	}
	src := coreconfig.SourceDefault
	if f.over[p] {
		src = coreconfig.SourceOverride
	}
	return coreconfig.KeyState{Path: p, Effective: v, Source: src, LockedBy: f.locked[p], Pending: f.pending[p]}, true
}

func (f *fakeAccess) Keys() iter.Seq2[coreconfig.Path, coreconfig.KeyState] {
	return func(func(coreconfig.Path, coreconfig.KeyState) bool) {}
}

func (f *fakeAccess) Apply(_ context.Context, p coreconfig.Patch, a coreconfig.Actor) (coreconfig.Result, error) {
	f.mu.Lock()
	f.actors = append(f.actors, a)
	f.applyN++
	if f.failApply != 0 && f.applyN == f.failApply {
		f.mu.Unlock()
		return coreconfig.Result{}, errors.New("core store is read-only")
	}
	if f.applyErr != nil {
		f.mu.Unlock()
		return coreconfig.Result{}, f.applyErr
	}
	f.applied = append(f.applied, p)
	f.trace = append(f.trace, "apply")
	var applied []coreconfig.Path
	// Like core: a patch that leaves every value as it is commits nothing and
	// moves no generation, yet reports the patch's paths as applied.
	changed := false
	for k, v := range p.Set {
		if f.regSecret[k] {
			var plain string
			_ = json.Unmarshal(v, &plain)
			changed = changed || f.secrets[secrets.Name(k)] != plain
			f.secrets[secrets.Name(k)] = plain
		} else {
			changed = changed || !bytes.Equal(f.vals[k], v)
			f.vals[k] = v
			f.over[k] = true
		}
		applied = append(applied, k)
	}
	for _, k := range p.Unset {
		_, had := f.secrets[secrets.Name(k)]
		_, hadVal := f.vals[k]
		changed = changed || had || hadVal || f.over[k]
		delete(f.secrets, secrets.Name(k))
		if d, ok := f.defs[k]; ok {
			f.vals[k] = d
		}
		delete(f.over, k)
		applied = append(applied, k)
	}
	slices.Sort(applied)
	if !changed {
		gen := f.gen
		f.mu.Unlock()
		return coreconfig.Result{Generation: gen, Applied: applied}, nil
	}
	f.gen++
	gen, hooks := f.gen, slices.Clone(f.hooks)
	f.mu.Unlock()
	for _, h := range hooks {
		h(gen)
	}
	return coreconfig.Result{Generation: gen, Applied: applied}, nil
}

func (f *fakeAccess) Generation() uint64 { f.mu.Lock(); defer f.mu.Unlock(); return f.gen }

func (f *fakeAccess) OnCommit(fn func(uint64)) {
	f.mu.Lock()
	f.hooks = append(f.hooks, fn)
	f.mu.Unlock()
}

func (f *fakeAccess) PassthroughValues() iter.Seq2[coreconfig.Path, coreconfig.OperatorValue] {
	f.mu.Lock()
	cp := map[coreconfig.Path]coreconfig.OperatorValue{}
	maps.Copy(cp, f.pass)
	f.mu.Unlock()
	return func(yield func(coreconfig.Path, coreconfig.OperatorValue) bool) {
		for _, k := range slices.Sorted(mapKeys(cp)) {
			if !yield(k, cp[k]) {
				return
			}
		}
	}
}

func mapKeys[K comparable, V any](m map[K]V) iter.Seq[K] {
	return func(yield func(K) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func (f *fakeAccess) Secret(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unreadble[n] {
		return secrets.Value{}, secrets.Meta{Name: n, Set: true, Err: secrets.ErrCorrupt}, secrets.ErrCorrupt
	}
	if err := f.secretErr[n]; err != nil {
		return secrets.Value{}, secrets.Meta{Name: n, Set: true}, err
	}
	v, ok := f.secrets[n]
	if !ok {
		return secrets.Value{}, secrets.Meta{Name: n}, secrets.ErrNotSet
	}
	src := secrets.SourceStored
	if f.envSecret[n] {
		src = secrets.SourceEnv
	}
	return secrets.NewValue(v), secrets.Meta{Name: n, Set: true, Source: src, Fingerprint: "abcd1234"}, nil
}

func (f *fakeAccess) PutSecret(_ context.Context, n secrets.Name, v secrets.Value, by string) (secrets.Meta, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putFail[n] {
		return secrets.Meta{}, errors.New("secret store is read-only")
	}
	f.secrets[n] = v.Reveal()
	f.puts = append(f.puts, n)
	f.trace = append(f.trace, "put "+string(n))
	f.by = append(f.by, by)
	return secrets.Meta{Name: n, Set: true}, nil
}

func (f *fakeAccess) DeleteSecret(_ context.Context, n secrets.Name, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.delFail[n] {
		return errors.New("secret store is read-only")
	}
	delete(f.secrets, n)
	f.dels = append(f.dels, n)
	f.trace = append(f.trace, "del "+string(n))
	f.by = append(f.by, by)
	return nil
}

// fakeResidual is a Residual over a map; Redact blanks the value of a head named in redact.
type fakeResidual struct {
	vals   map[string]json.RawMessage
	saves  int
	loadEr error
	saveEr error
	redact map[string]bool
}

func newFakeResidual() *fakeResidual {
	return &fakeResidual{vals: map[string]json.RawMessage{}, redact: map[string]bool{}}
}

func (r *fakeResidual) Load(_ context.Context, _ []string) (map[string]json.RawMessage, error) {
	if r.loadEr != nil {
		return nil, r.loadEr
	}
	out := map[string]json.RawMessage{}
	maps.Copy(out, r.vals)
	return out, nil
}

func (r *fakeResidual) Save(_ context.Context, set map[string]json.RawMessage, unset []string) error {
	if r.saveEr != nil {
		return r.saveEr
	}
	r.saves++
	maps.Copy(r.vals, set)
	for _, k := range unset {
		delete(r.vals, k)
	}
	return nil
}

func (r *fakeResidual) Redact(head string, v json.RawMessage) json.RawMessage {
	if r.redact[head] {
		return json.RawMessage(`"********"`)
	}
	return v
}
