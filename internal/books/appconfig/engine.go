// file: internal/books/appconfig/engine.go
// version: 1.0.0
// guid: 6c2f8a4d-9e13-4b75-a1c0-d4e7f3b82a59
// last-edited: 2026-11-19

package appconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	coreconfig "github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
)

// Engine is the Facade over the App's core config store. It owns the projection
// of HEAD's flat Config and rebuilds it, through the rename table, whenever a
// core Apply commits.
type Engine struct {
	acc     Store
	tab     *cutover.RenameTable
	rows    []projRow
	byHead  map[string]projRow
	publish func(config.Config)
	log     *logger.StandardLogger

	// hooks run after every Update commits (OnUpdate); guarded by writeMu.
	hooks []namedHook

	// writeMu serialises Mutate and Update. It is not held by a rebuild, so the
	// OnCommit hook an Apply fires can rebuild while a write waits in Apply.
	writeMu sync.Mutex

	// mu serialises rebuilds and guards the fields below; readers of the
	// projection take the RLock.
	mu         sync.RWMutex
	cur        config.Config
	locks      map[string]string
	unreadable map[string]bool // secret rows whose record cannot be decrypted
	missing    map[string]bool // rows whose canonical key the store does not register
	gen        atomic.Uint64   // projections published
}

var _ config.Facade = (*Engine)(nil)

type namedHook struct {
	name string
	fn   config.UpdateHook
}

// OnUpdate registers fn to run after every Update that commits, in registration
// order, under the write lock (so a hook sees the projection its Update made and
// two hooks never interleave with another write). name labels its failure in
// UpdateResult.PostCommit. Register before the Engine serves writes.
func (e *Engine) OnUpdate(name string, fn config.UpdateHook) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	e.hooks = append(e.hooks, namedHook{name, fn})
}

// Store is the slice of the App's core config store the Engine uses. It is
// satisfied by module.ConfigAccess (Deps.Config), which this package may not
// import: only internal/books may, so books hands the value in.
type Store interface {
	Lookup(p coreconfig.Path) (coreconfig.KeyState, bool)
	Apply(ctx context.Context, p coreconfig.Patch, a coreconfig.Actor) (coreconfig.Result, error)
	OnCommit(fn func(gen uint64))
	PassthroughValues() iter.Seq2[coreconfig.Path, coreconfig.OperatorValue]
	Secret(ctx context.Context, name secrets.Name) (secrets.Value, secrets.Meta, error)
	PutSecret(ctx context.Context, name secrets.Name, v secrets.Value, by string) (secrets.Meta, error)
	DeleteSecret(ctx context.Context, name secrets.Name, by string) error
}

// New builds an Engine over acc. publish receives every projection (production:
// config.Project); a nil publish keeps the projection to the Engine alone, which
// is what a test of the Engine wants.
func New(acc Store, publish func(config.Config)) *Engine {
	tab := cutover.Table()
	rows := plan(tab)
	byHead := make(map[string]projRow, len(rows))
	for _, p := range rows {
		byHead[p.row.Head] = p
	}
	return &Engine{acc: acc, tab: tab, rows: rows, byHead: byHead, publish: publish, log: logger.New("appconfig"),
		locks: map[string]string{}, unreadable: map[string]bool{}, missing: map[string]bool{}}
}

// Start projects once and then re-projects on every commit: after Start the
// Engine is current. It returns the first projection's error; later failures
// are logged and leave the previous projection in place.
func (e *Engine) Start(ctx context.Context) error {
	if err := e.Rebuild(ctx); err != nil {
		return err
	}
	e.acc.OnCommit(func(uint64) {
		if err := e.Rebuild(context.WithoutCancel(ctx)); err != nil {
			e.log.Error("appconfig: re-projecting the config after a commit failed; the previous projection stays: %s", logger.SanitizeLogErr(err))
		}
	})
	return nil
}

// projRow is one rename-table row the projection reads: where its value is
// stored and which HEAD field it fills.
type projRow struct {
	row    *cutover.Row
	segs   []string // Head split into the JSON path of the field
	target cutover.Target
}

// plan lists the rows the projection reads, in table order. A row is read when
// it names a field of Config (not a container, a removed key, a process name or
// a map/array element) and a concrete canonical location of class k, s or o. The
// fold and entry rows, and the class-i fields that stay in the residual blob,
// have their own readers.
func plan(tab *cutover.RenameTable) []projRow {
	var out []projRow
	for i := range tab.Rows {
		r := &tab.Rows[i]
		if r.Container || r.Removed || r.Process || strings.ContainsAny(r.Head, "[]<>") {
			continue
		}
		var want func(cutover.Target) bool
		switch r.Class {
		case cutover.ClassKey:
			want = func(t cutover.Target) bool { return t.Kind == cutover.TargetKey }
		case cutover.ClassSecret:
			want = func(t cutover.Target) bool { return t.Kind == cutover.TargetSecret }
		case cutover.ClassEnv:
			want = func(t cutover.Target) bool { return t.Kind == cutover.TargetKey || t.Kind == cutover.TargetPassthrough }
		default:
			continue
		}
		for _, t := range r.Targets {
			if want(t) && concrete(t.Path) {
				out = append(out, projRow{row: r, segs: strings.Split(r.Head, "."), target: t})
				break
			}
		}
	}
	return out
}

// concrete reports whether a target path names one key: no wildcard, placeholder
// or list syntax.
func concrete(p string) bool { return p != "" && !strings.ContainsAny(p, "*<>[] ,") }

// Rebuild projects the store into Config and publishes it. It is serialised,
// so two commits' rebuilds never publish out of order.
func (e *Engine) Rebuild(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.rebuildLocked(ctx)
}

func (e *Engine) rebuildLocked(ctx context.Context) error {
	cfg, locks, unreadable, missing, err := e.project(ctx)
	if err != nil {
		return err
	}
	e.cur, e.locks, e.unreadable, e.missing = cfg, locks, unreadable, missing
	e.gen.Add(1)
	if e.publish != nil {
		e.publish(cfg)
	}
	return nil
}

// project reads every projected row and builds the Config.
func (e *Engine) project(ctx context.Context) (cfg config.Config, locks map[string]string, unreadable, missing map[string]bool, err error) {
	locks, unreadable, missing = map[string]string{}, map[string]bool{}, map[string]bool{}
	pass := maps.Collect(e.acc.PassthroughValues())
	for _, p := range e.rows {
		val, by, state, rerr := e.read(ctx, p, pass)
		switch state {
		case readMissing:
			missing[p.row.Head] = true
			continue
		case readUnreadable:
			unreadable[p.row.Head] = true
			continue
		case readUnset:
			continue
		}
		if rerr != nil {
			return cfg, nil, nil, nil, fmt.Errorf("appconfig: row %s: %w", p.row.Head, rerr)
		}
		if by != "" {
			locks[p.row.Head] = by
		}
		f, ok := fieldAt(&cfg, p.segs)
		if !ok {
			return cfg, nil, nil, nil, fmt.Errorf("appconfig: row %s names no field of Config", p.row.Head)
		}
		if err := json.Unmarshal(val, f.Addr().Interface()); err != nil {
			return cfg, nil, nil, nil, fmt.Errorf("appconfig: row %s: %q does not fit %s: %w", p.row.Head, val, f.Type(), err)
		}
	}
	return cfg, locks, unreadable, missing, nil
}

// readState is what read found.
type readState int

const (
	readOK readState = iota
	readUnset
	readMissing
	readUnreadable
)

// read returns the value of one row as HEAD's JSON for its field, and the lock
// reason of its canonical key.
func (e *Engine) read(ctx context.Context, p projRow, pass map[coreconfig.Path]coreconfig.OperatorValue) (json.RawMessage, string, readState, error) {
	t := p.target
	switch t.Kind {
	case cutover.TargetSecret:
		v, meta, err := e.acc.Secret(ctx, secrets.Name(t.Path))
		switch {
		case errors.Is(err, secrets.ErrNotSet):
			return nil, "", readUnset, nil
		case errors.Is(err, secrets.ErrKeyUnavailable), errors.Is(err, secrets.ErrCorrupt), meta.Err != nil:
			return nil, "", readUnreadable, nil
		case err != nil:
			return nil, "", readOK, err
		}
		raw, merr := json.Marshal(v.Reveal())
		return raw, secretLock(meta), readOK, merr
	case cutover.TargetPassthrough:
		ov, ok := pass[coreconfig.Path(t.Path)]
		if !ok {
			return nil, "", readUnset, nil
		}
		out, err := unconvert(t.Convert, ov.Value)
		if errors.Is(err, cutover.ErrNotStaged) {
			return nil, "", readUnset, nil
		}
		return out, ov.By, readOK, err
	default:
		st, ok := e.acc.Lookup(coreconfig.Path(t.Path))
		if !ok {
			return nil, "", readMissing, nil
		}
		out, err := unconvert(t.Convert, st.Effective)
		if errors.Is(err, cutover.ErrNotStaged) {
			return nil, "", readUnset, nil
		}
		return out, st.LockedBy, readOK, err
	}
}

// secretLock is the lock reason of a secret an operator layer supplies.
func secretLock(m secrets.Meta) string {
	if m.Source == secrets.SourceEnv {
		return "env"
	}
	return ""
}

// unconvert turns a canonical value back into HEAD's. Identity until the
// inverses of the table's converters land.
func unconvert(name string, in json.RawMessage) (json.RawMessage, error) {
	if name != "" {
		return nil, fmt.Errorf("converter %q has no inverse yet", name)
	}
	return in, nil
}

// Snapshot is the projection as a value copy.
func (e *Engine) Snapshot() config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cur
}

// Generation counts published projections.
func (e *Engine) Generation() uint64 { return e.gen.Load() }

// Locks is every locked HEAD setting with what locks it.
func (e *Engine) Locks() map[string]string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return maps.Clone(e.locks)
}

// EnvLocked is the locked settings' names, sorted.
func (e *Engine) EnvLocked() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Sorted(maps.Keys(e.locks))
}

// View is GET /api/v1/config's content: every projected row, ordered by name,
// at one generation. A secret carries its record's metadata and never a value.
func (e *Engine) View() config.View {
	e.mu.RLock()
	cur, locks, unreadable, gen := e.cur, maps.Clone(e.locks), maps.Clone(e.unreadable), e.gen.Load()
	e.mu.RUnlock()
	keys := make([]config.KeyView, 0, len(e.rows))
	for _, p := range e.rows {
		kv := config.KeyView{Name: p.row.Head, Class: config.KeyClass(p.row.Class), LockedBy: locks[p.row.Head], Admin: p.row.Admin}
		switch p.target.Kind {
		case cutover.TargetSecret:
			kv.Secret = e.secretMeta(p, unreadable[p.row.Head])
		default:
			if f, ok := fieldAt(&cur, p.segs); ok {
				if raw, err := json.Marshal(f.Interface()); err == nil {
					kv.Value = raw
				}
			}
			if st, ok := e.acc.Lookup(coreconfig.Path(p.target.Path)); ok {
				kv.RestartPending = st.Pending
			}
		}
		keys = append(keys, kv)
	}
	slices.SortFunc(keys, func(a, b config.KeyView) int { return strings.Compare(a.Name, b.Name) })
	return config.View{Generation: gen, Keys: keys}
}

// secretMeta is a secret row's SecretMeta: from the key's own metadata when the
// store registers it, from the secret store when it does not.
func (e *Engine) secretMeta(p projRow, unreadable bool) *config.SecretMeta {
	name := secrets.Name(p.target.Path)
	if st, ok := e.acc.Lookup(coreconfig.Path(p.target.Path)); ok && st.Secret != nil {
		return toSecretMeta(*st.Secret, unreadable)
	}
	_, meta, err := e.acc.Secret(context.Background(), name)
	if err != nil && !errors.Is(err, secrets.ErrNotSet) && meta.Name == "" {
		return &config.SecretMeta{Unreadable: true}
	}
	return toSecretMeta(meta, unreadable)
}

func toSecretMeta(m secrets.Meta, unreadable bool) *config.SecretMeta {
	return &config.SecretMeta{Set: m.Set, Source: sourceName(m.Source), UpdatedAt: m.UpdatedAt,
		Fingerprint: m.Fingerprint, Unreadable: unreadable || m.Err != nil}
}

// sourceName is a secret source as the settings page shows it.
func sourceName(s secrets.Source) string {
	switch s {
	case secrets.SourceStored:
		return "stored"
	case secrets.SourceEnv:
		return "env"
	}
	return ""
}
