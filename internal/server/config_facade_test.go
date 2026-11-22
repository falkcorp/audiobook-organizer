// file: internal/server/config_facade_test.go
// version: 1.2.0
// guid: 3d8a6c52-1e47-4b09-9f2a-5c7e0b1d4a68
// last-edited: 2026-11-19

package server

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

	coreconfig "github.com/falkcorp/mediacore/config"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// serverConfigFacade is the config.Facade the route tests run GET/PUT
// /api/v1/config against. Production binds *appconfig.Engine over core's
// store, but no helper boots that store with every key registered (the keys
// come from the plugins, wired in F3-07 e4), so this stands in for it over the
// process-wide Config the tests already drive:
//
//   - the key set, classes and admin flags are the rename table's (the Engine's
//     own source), resolved onto Config fields the way the Engine does;
//   - a secret key's View carries metadata only, an operator-layer key is
//     read-only, and a Config field tagged json:"-" shows no value (HEAD never
//     serialized those; the real Engine must hold the same line, see
//     TestConfigGET_NeverReturnsInviteSecrets_RealEngine);
//   - Update refuses in core's order (unknown, protection, lock, invalid) with
//     core's errors, and commits all keys or none.
//
// Protection follows core's meets(): an admin key needs an interactive admin
// Actor, and the owner trust root (config.ChangedOwnerTrustRoot) needs the
// owner's proof while an owner is set and the host's configuration while none
// is. There is no auth-off exemption, as in core.
type serverConfigFacade struct {
	mu     sync.Mutex
	gen    atomic.Uint64
	locks  map[string]string
	actors []config.Actor // who each Update ran as, in order
}

// installServerConfigFacade binds a fresh serverConfigFacade behind the config
// package for the test and restores the previous binding afterwards.
func installServerConfigFacade(t testing.TB) *serverConfigFacade {
	t.Helper()
	f := &serverConfigFacade{locks: map[string]string{}}
	prev := config.Install(f)
	t.Cleanup(func() { config.Install(prev) })
	return f
}

var _ config.Facade = (*serverConfigFacade)(nil)

func (f *serverConfigFacade) Snapshot() config.Config { return config.Snapshot() }

func (f *serverConfigFacade) Mutate(fn func(*config.Config)) error {
	c := config.Snapshot()
	fn(&c)
	config.Project(c)
	f.gen.Add(1)
	return nil
}

func (f *serverConfigFacade) Generation() uint64 { return f.gen.Load() }

func (f *serverConfigFacade) Locks() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]string, len(f.locks))
	maps.Copy(out, f.locks)
	return out
}

// facadeField is the Config field a rename-table Head names, with its struct
// field (for the json tag).
func facadeField(cfg *config.Config, head string) (reflect.Value, reflect.StructField, bool) {
	v := reflect.ValueOf(cfg).Elem()
	var sf reflect.StructField
	for seg := range strings.SplitSeq(head, ".") {
		if v.Kind() != reflect.Struct {
			return reflect.Value{}, sf, false
		}
		found := false
		for i := range v.NumField() {
			if facadeFieldName(v.Type().Field(i)) == seg {
				v, sf, found = v.Field(i), v.Type().Field(i), true
				break
			}
		}
		if !found {
			return reflect.Value{}, sf, false
		}
	}
	return v, sf, true
}

// facadeFieldName is the name the rename table gives a struct field: its json
// name, else (json "-") its mapstructure name.
func facadeFieldName(f reflect.StructField) string {
	if !f.IsExported() {
		return ""
	}
	if name, _, _ := strings.Cut(f.Tag.Get("json"), ","); name != "" && name != "-" {
		return name
	}
	if name, _, _ := strings.Cut(f.Tag.Get("mapstructure"), ","); name != "" {
		return name
	}
	if f.Tag.Get("json") == "" && !f.Anonymous {
		return f.Name
	}
	return ""
}

// facadeRow is the rename-table row of a settable HEAD key, and whether the
// Facade serves it at all.
func facadeRow(head string) (*cutover.Row, bool) {
	r, ok := cutover.Table().Row(head)
	if !ok || r.Container || r.Removed || r.Process || strings.ContainsAny(r.Head, "[]<>") {
		return nil, false
	}
	switch r.Class {
	case cutover.ClassKey, cutover.ClassSecret, cutover.ClassInput, cutover.ClassEnv:
		return r, true
	}
	return nil, false
}

func (f *serverConfigFacade) View() config.View {
	cur := config.Snapshot()
	locks := f.Locks()
	var keys []config.KeyView
	for i := range cutover.Table().Rows {
		r := &cutover.Table().Rows[i]
		if _, ok := facadeRow(r.Head); !ok {
			continue
		}
		fv, sf, ok := facadeField(&cur, r.Head)
		if !ok {
			continue
		}
		kv := config.KeyView{Name: r.Head, Class: config.KeyClass(r.Class), LockedBy: locks[r.Head], Admin: r.Admin}
		switch {
		case r.Class == cutover.ClassSecret:
			kv.Secret = &config.SecretMeta{Set: !fv.IsZero()}
		case sf.Tag.Get("json") == "-":
			// HEAD's JSON never carried this field.
		default:
			if raw, err := json.Marshal(fv.Interface()); err == nil {
				kv.Value = raw
			}
		}
		keys = append(keys, kv)
	}
	slices.SortFunc(keys, func(a, b config.KeyView) int { return strings.Compare(a.Name, b.Name) })
	return config.View{Generation: f.gen.Load(), Keys: keys}
}

const facadeAdminPerm = "core:admin"

func (f *serverConfigFacade) Update(_ context.Context, a config.Actor, u config.Update) (config.UpdateResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actors = append(f.actors, a)

	var both []error
	for _, k := range u.Unset {
		if _, dup := u.Set[k]; dup {
			both = append(both, &coreconfig.FieldError{Path: coreconfig.Path(k), Err: errors.New("both set and unset in one patch")})
		}
	}
	applied := slices.Sorted(func(yield func(string) bool) {
		seen := map[string]bool{}
		for k := range u.Set {
			seen[k] = true
		}
		for _, k := range u.Unset {
			seen[k] = true
		}
		for k := range seen {
			if !yield(k) {
				return
			}
		}
	})

	// Unknown keys first, naming every one.
	var unknown []string
	prior := config.Snapshot()
	for _, k := range applied {
		if _, ok := facadeRow(k); !ok {
			unknown = append(unknown, k)
		} else if _, _, ok := facadeField(&prior, k); !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		return config.UpdateResult{}, fmt.Errorf("%w: %s", coreconfig.ErrUnknownKey, strings.Join(unknown, ", "))
	}
	for _, k := range applied {
		if r, _ := facadeRow(k); r.Class == cutover.ClassEnv {
			return config.UpdateResult{}, config.ErrKeyReadOnly
		}
	}

	// Decode onto a candidate; nothing is written until every key is accepted.
	candidate := prior.Clone()
	verrs := both
	for _, k := range applied {
		fv, _, _ := facadeField(candidate, k)
		if _, isSet := u.Set[k]; !isSet {
			fv.SetZero()
			continue
		}
		raw := u.Set[k]
		if r, _ := facadeRow(k); r.Class == cutover.ClassSecret {
			var plain string
			if err := json.Unmarshal(raw, &plain); err != nil {
				verrs = append(verrs, &coreconfig.FieldError{Path: coreconfig.Path(k), Err: errors.New("a secret is a string")})
				continue
			}
			fv.SetString(plain) // "" clears
			continue
		}
		fresh := reflect.New(fv.Type())
		if err := json.Unmarshal(raw, fresh.Interface()); err != nil {
			verrs = append(verrs, &coreconfig.FieldError{Path: coreconfig.Path(k), Err: err})
			continue
		}
		fv.Set(fresh.Elem())
	}

	// Protection, in core's order. The owner trust root first: it holds with
	// auth off too, and with no owner configured nobody sets it through the API.
	if trust := config.ChangedOwnerTrustRoot(&prior, candidate); len(trust) > 0 {
		need := coreconfig.ProtectOwner
		if strings.TrimSpace(prior.OwnerEmail) == "" {
			need = coreconfig.ProtectOperator
		} else if a.Owner && a.Interactive && a.Can != nil && a.Can(facadeAdminPerm) {
			need = coreconfig.ProtectNone
		}
		if need != coreconfig.ProtectNone {
			return config.UpdateResult{}, &coreconfig.ProtectedError{Path: coreconfig.Path(trust[0]), Need: need}
		}
	}
	admin := a.Interactive && a.Can != nil && a.Can(facadeAdminPerm)
	var refused []error
	for _, k := range applied {
		if r, _ := facadeRow(k); r.Admin && !admin {
			refused = append(refused, &coreconfig.ProtectedError{Path: coreconfig.Path(k), Need: coreconfig.ProtectAdmin})
		}
	}
	if len(refused) > 0 {
		return config.UpdateResult{}, errors.Join(refused...)
	}
	for _, k := range applied {
		if by, ok := f.locks[k]; ok {
			refused = append(refused, &coreconfig.LockedError{Path: coreconfig.Path(k), By: by})
		}
	}
	if len(refused) > 0 {
		return config.UpdateResult{}, errors.Join(refused...)
	}

	// Validation: a write that introduces invalidity is refused, naming the keys.
	if err := candidate.Validate(); err != nil {
		if pre := prior.Clone(); pre.Validate() == nil {
			path := coreconfig.Path(applied[0])
			verrs = append(verrs, &coreconfig.FieldError{Path: path, Err: err})
		}
	}
	if len(verrs) > 0 {
		return config.UpdateResult{}, fmt.Errorf("%w: %w", coreconfig.ErrInvalid, errors.Join(verrs...))
	}

	config.Project(*candidate)
	gen := f.gen.Add(1)
	return config.UpdateResult{Generation: gen, Applied: applied}, nil
}

// configKey is the entry of a GET/PUT /api/v1/config body's "keys" array that
// names name, and whether the body has one.
func configKey(body map[string]any, name string) (map[string]any, bool) {
	keys, _ := body["keys"].([]any)
	for _, k := range keys {
		if m, ok := k.(map[string]any); ok && m["name"] == name {
			return m, true
		}
	}
	return nil, false
}
