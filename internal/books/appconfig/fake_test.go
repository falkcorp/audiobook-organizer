// file: internal/books/appconfig/fake_test.go
// version: 1.0.0
// guid: 2a6d8f14-7c39-4e51-9b0a-e3f5c1d7a862
// last-edited: 2026-11-19

package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
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
	regSecret map[coreconfig.Path]bool // registered secret keys
	secrets   map[secrets.Name]string  // the secret store
	unreadble map[secrets.Name]bool
	secretErr map[secrets.Name]error // a bare error Secret returns, with no Meta.Err
	envSecret map[secrets.Name]bool
	putFail   map[secrets.Name]bool
	pass      map[coreconfig.Path]coreconfig.OperatorValue
	gen       uint64
	hooks     []func(uint64)
	applyErr  error
	applied   []coreconfig.Patch
	puts      []secrets.Name
	dels      []secrets.Name
	by        []string
}

func newFake() *fakeAccess {
	return &fakeAccess{vals: map[coreconfig.Path]json.RawMessage{}, locked: map[coreconfig.Path]string{},
		regSecret: map[coreconfig.Path]bool{}, secrets: map[secrets.Name]string{}, unreadble: map[secrets.Name]bool{}, secretErr: map[secrets.Name]error{}, envSecret: map[secrets.Name]bool{}, putFail: map[secrets.Name]bool{},
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
	return coreconfig.KeyState{Path: p, Effective: v, LockedBy: f.locked[p]}, true
}

func (f *fakeAccess) Keys() iter.Seq2[coreconfig.Path, coreconfig.KeyState] {
	return func(func(coreconfig.Path, coreconfig.KeyState) bool) {}
}

func (f *fakeAccess) Apply(_ context.Context, p coreconfig.Patch, _ coreconfig.Actor) (coreconfig.Result, error) {
	f.mu.Lock()
	if f.applyErr != nil {
		f.mu.Unlock()
		return coreconfig.Result{}, f.applyErr
	}
	f.applied = append(f.applied, p)
	var applied []coreconfig.Path
	for k, v := range p.Set {
		if f.regSecret[k] {
			var plain string
			_ = json.Unmarshal(v, &plain)
			f.secrets[secrets.Name(k)] = plain
		} else {
			f.vals[k] = v
		}
		applied = append(applied, k)
	}
	for _, k := range p.Unset {
		delete(f.secrets, secrets.Name(k))
		applied = append(applied, k)
	}
	slices.Sort(applied)
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
	for k, v := range f.pass {
		cp[k] = v
	}
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
	f.by = append(f.by, by)
	return secrets.Meta{Name: n, Set: true}, nil
}

func (f *fakeAccess) DeleteSecret(_ context.Context, n secrets.Name, by string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.secrets, n)
	f.dels = append(f.dels, n)
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

func (r *fakeResidual) Load(context.Context) (map[string]json.RawMessage, error) {
	if r.loadEr != nil {
		return nil, r.loadEr
	}
	out := map[string]json.RawMessage{}
	for k, v := range r.vals {
		out[k] = v
	}
	return out, nil
}

func (r *fakeResidual) Save(_ context.Context, set map[string]json.RawMessage, unset []string) error {
	if r.saveEr != nil {
		return r.saveEr
	}
	r.saves++
	for k, v := range set {
		r.vals[k] = v
	}
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
