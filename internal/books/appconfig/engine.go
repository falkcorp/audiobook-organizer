// file: internal/books/appconfig/engine.go
// version: 1.2.2
// guid: 6c2f8a4d-9e13-4b75-a1c0-d4e7f3b82a59
// last-edited: 2026-11-19

package appconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"reflect"
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
	acc    Store
	tab    *cutover.RenameTable
	rows   []projRow
	byHead map[string]projRow
	// foldHeads are the HEAD fields written through core's OIDC provider list,
	// and oidcSecret each provider's client-secret key (the entry's reference).
	foldHeads  map[string]bool
	oidcSecret map[string]string
	// below is, for a row whose field holds other rows' fields (download_client
	// holds the Deluge password, a secret row of its own), their paths: the
	// holder's own value leaves them out, so a secret never lands in the residual.
	below   map[string][][]string
	res     Residual
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
	unrep      map[string]bool // rows whose canonical value HEAD cannot hold
	base       func() config.Config
	gen        atomic.Uint64 // projections published
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
	Generation() uint64
	OnCommit(fn func(gen uint64))
	PassthroughValues() iter.Seq2[coreconfig.Path, coreconfig.OperatorValue]
	Secret(ctx context.Context, name secrets.Name) (secrets.Value, secrets.Meta, error)
	PutSecret(ctx context.Context, name secrets.Name, v secrets.Value, by string) (secrets.Meta, error)
	DeleteSecret(ctx context.Context, name secrets.Name, by string) error
	ListSecrets(ctx context.Context, prefix string) ([]secrets.Meta, error)
}

// New builds an Engine over acc. publish receives every projection (production:
// config.Project); a nil publish keeps the projection to the Engine alone, which
// is what a test of the Engine wants.
func New(acc Store, publish func(config.Config), opts ...Option) *Engine {
	e := &Engine{acc: acc, publish: publish, log: logger.New("appconfig"),
		locks: map[string]string{}, unreadable: map[string]bool{}, missing: map[string]bool{}, unrep: map[string]bool{}}
	for _, o := range opts {
		o(e)
	}
	tab := cutover.Table()
	rows := plan(tab, e.res != nil)
	byHead := make(map[string]projRow, len(rows))
	for _, p := range rows {
		byHead[p.row.Head] = p
	}
	e.tab, e.rows, e.byHead = tab, rows, byHead
	e.foldHeads, e.oidcSecret = map[string]bool{}, map[string]string{}
	for i := range tab.Rows {
		r := &tab.Rows[i]
		if foldsIntoOIDC(r) && hasField(strings.Split(r.Head, ".")) {
			e.foldHeads[r.Head] = true
		}
		for _, id := range []string{"github", "google"} {
			if r.Head == "oauth_"+id+"_client_secret" {
				for _, t := range r.Targets {
					if t.Kind == cutover.TargetSecret {
						e.oidcSecret[id] = t.Path
					}
				}
			}
		}
	}
	e.below = map[string][][]string{}
	for _, p := range rows {
		for _, q := range rows {
			if len(q.segs) > len(p.segs) && slices.Equal(q.segs[:len(p.segs)], p.segs) {
				e.below[p.row.Head] = append(e.below[p.row.Head], q.segs)
			}
		}
	}
	return e
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
	row      *cutover.Row
	segs     []string // Head split into the JSON path of the field
	target   cutover.Target
	residual bool // class i: read from and written to the Residual, no core key
	kind     rowKind
}

// rowKind says how a row is read and written when it is not a plain key.
type rowKind uint8

const (
	// kindKey is an ordinary row: one key, one secret, a passthrough or a
	// residual field.
	kindKey rowKind = iota
	// kindFold is an oauth_* field that lives inside core's OIDC provider list
	// (projectFolds reads it, FoldOIDC writes it).
	kindFold
	// kindRoots is root_dir or path_aliases, which live in core's library roots
	// (projectFolds reads them, FoldRoots writes them).
	kindRoots
)

// plan lists the rows the projection reads, in table order. A row is read when
// it names a field of Config (not a container, a removed key, a process name or
// a map/array element) and a concrete canonical location of class k, s or o. The
// fold and entry rows, and the class-i fields that stay in the residual blob,
// have their own readers.
func plan(tab *cutover.RenameTable, residual bool) []projRow {
	var out []projRow
	for i := range tab.Rows {
		r := &tab.Rows[i]
		if r.Container || r.Removed || r.Process || strings.ContainsAny(r.Head, "[]<>") {
			continue
		}
		if r.Class == cutover.ClassInput {
			if foldsIntoRoots(r) {
				// root_dir and path_aliases come from core.library.roots: read by
				// projectFolds, written by FoldRoots.
				if segs := strings.Split(r.Head, "."); hasField(segs) {
					out = append(out, projRow{row: r, segs: segs, kind: kindRoots})
				}
				continue
			}
			if segs := strings.Split(r.Head, "."); residual && concrete(r.Head) && hasField(segs) {
				out = append(out, projRow{row: r, segs: segs, residual: true})
			}
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
		if r.Class == cutover.ClassKey && foldsIntoOIDC(r) && !hasKeyTarget(r) {
			// A field only the provider list stores: no key of its own.
			if segs := strings.Split(r.Head, "."); hasField(segs) {
				out = append(out, projRow{row: r, segs: segs, kind: kindFold})
			}
			continue
		}
		for _, t := range r.Targets {
			if want(t) && concrete(t.Path) {
				out = append(out, projRow{row: r, segs: strings.Split(r.Head, "."), target: t})
				break
			}
		}
	}
	// Holders before the rows inside them, so a nested row's value is the last
	// word on its field.
	slices.SortStableFunc(out, func(a, b projRow) int { return len(a.segs) - len(b.segs) })
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
	p, err := e.project(ctx)
	if err != nil {
		return err
	}
	e.cur, e.locks, e.unreadable, e.missing, e.unrep = p.cfg, p.locks, p.unreadable, p.missing, p.unrep
	e.gen.Add(1)
	if e.publish != nil {
		e.publish(p.cfg)
	}
	return nil
}

// projection is one build of the Config and what it learned on the way.
type projection struct {
	cfg        config.Config
	locks      map[string]string
	unreadable map[string]bool // secrets that cannot be decrypted
	missing    map[string]bool // rows whose key the store does not register
	unrep      map[string]bool // rows whose canonical value HEAD cannot hold
}

// project reads every projected row and builds the Config: the base (what no row
// stores), the rows in table order (holders first), then the two folds.
func (e *Engine) project(ctx context.Context) (*projection, error) {
	out := &projection{locks: map[string]string{}, unreadable: map[string]bool{}, missing: map[string]bool{}, unrep: map[string]bool{}}
	if e.base != nil {
		out.cfg = e.base()
	}
	pass := maps.Collect(e.acc.PassthroughValues())
	var resv map[string]json.RawMessage
	if e.res != nil {
		heads := make([]string, 0, len(e.rows))
		for _, p := range e.rows {
			if p.residual {
				heads = append(heads, p.row.Head)
			}
		}
		var err error
		if resv, err = e.res.Load(ctx, heads); err != nil {
			return nil, fmt.Errorf("appconfig: reading the residual settings: %w", err)
		}
	}
	set := func(head string, val json.RawMessage) error {
		f, ok := fieldAt(&out.cfg, strings.Split(head, "."))
		if !ok {
			return fmt.Errorf("appconfig: row %s names no field of Config", head)
		}
		if err := json.Unmarshal(val, f.Addr().Interface()); err != nil {
			// HEAD holds some operator values as the text its environment gave
			// (LOGIN_SATURATED_RATE "2"); core types the key (a number). The
			// number's own text is the value HEAD would have held.
			if text, ok := scalarText(val); ok && f.Kind() == reflect.String {
				f.SetString(text)
				return nil
			}
			return fmt.Errorf("appconfig: row %s: %q does not fit %s: %w", head, val, f.Type(), err)
		}
		return nil
	}
	for _, p := range e.rows {
		if p.kind != kindKey {
			continue // projectFolds reads these
		}
		var (
			val   json.RawMessage
			by    string
			state readState
			rerr  error
		)
		if p.residual {
			state = readOK
			if val = resv[p.row.Head]; val == nil {
				state = readUnset
			}
		} else {
			val, by, state, rerr = e.read(ctx, p, pass)
		}
		switch {
		case errors.Is(rerr, cutover.ErrNotRepresentable):
			e.noteUnrepresentable(out, p.row.Head, rerr)
			continue
		case errors.Is(rerr, cutover.ErrNotApplicable):
			continue // never stored: the base holds it
		case rerr != nil:
			return nil, fmt.Errorf("appconfig: row %s: %w", p.row.Head, rerr)
		}
		switch state {
		case readMissing:
			out.missing[p.row.Head] = true
			continue
		case readUnreadable:
			out.unreadable[p.row.Head] = true
			continue
		case readUnset:
			continue
		}
		if by != "" {
			out.locks[p.row.Head] = by
		}
		if err := set(p.row.Head, val); err != nil {
			return nil, err
		}
	}
	if err := e.projectFolds(out, set); err != nil {
		return nil, err
	}
	return out, nil
}

// The two list keys whose entries fold several HEAD fields (the rows with a
// fold or entry target): core's OIDC providers and library roots.
const (
	oidcProvidersKey = "core.auth.oidc.providers"
	libraryRootsKey  = "core.library.roots"
)

// projectFolds unfolds the OIDC providers into the oauth_* fields and the
// library roots into root_dir and path_aliases, with the inverse functions of M2's
// planner (cutover.UnfoldOIDC, UnfoldRoots).
func (e *Engine) projectFolds(out *projection, set func(string, json.RawMessage) error) error {
	if st, ok := e.acc.Lookup(oidcProvidersKey); !ok {
		out.missing["oauth_enabled"] = true
	} else if len(st.Effective) > 0 {
		heads, err := cutover.UnfoldOIDC(st.Effective)
		switch {
		case errors.Is(err, cutover.ErrNotRepresentable):
			e.noteUnrepresentable(out, "oauth_enabled", err)
		case err != nil:
			return fmt.Errorf("appconfig: %s: %w", oidcProvidersKey, err)
		default:
			for _, h := range slices.Sorted(maps.Keys(heads)) {
				if err := set(h, heads[h]); err != nil {
					return err
				}
				if st.LockedBy != "" {
					out.locks[h] = st.LockedBy
				}
			}
		}
	}
	if st, ok := e.acc.Lookup(libraryRootsKey); !ok {
		out.missing["root_dir"] = true
	} else if len(st.Effective) > 0 {
		rootDir, aliases, err := cutover.UnfoldRoots(st.Effective)
		switch {
		case errors.Is(err, cutover.ErrNotRepresentable):
			e.noteUnrepresentable(out, "root_dir", err)
		case err != nil:
			return fmt.Errorf("appconfig: %s: %w", libraryRootsKey, err)
		default:
			if err := set("root_dir", mustMarshal(rootDir)); err != nil {
				return err
			}
			if len(aliases) > 0 && string(aliases) != "null" {
				if err := set("path_aliases", aliases); err != nil {
					return err
				}
			}
			if st.LockedBy != "" {
				out.locks["root_dir"], out.locks["path_aliases"] = st.LockedBy, st.LockedBy
			}
		}
	}
	return nil
}

// noteUnrepresentable records a row whose canonical value HEAD cannot hold: the
// field keeps its base value and the fault is logged once per rebuild. The
// operator wrote something core accepts; refusing to publish any projection over
// it would freeze every other setting.
func (e *Engine) noteUnrepresentable(out *projection, head string, err error) {
	out.unrep[head] = true
	e.log.Error("appconfig: %s: %s; the setting keeps its default until the value is one HEAD can hold", head, logger.SanitizeLogErr(err))
}

// Unrepresentable lists the settings whose canonical value HEAD cannot hold,
// sorted.
func (e *Engine) Unrepresentable() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Sorted(maps.Keys(e.unrep))
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
		out, err := cutover.Unconvert(t.Convert, ov.Value)
		return out, ov.By, readOK, err
	default:
		st, ok := e.acc.Lookup(coreconfig.Path(t.Path))
		if !ok {
			return nil, "", readMissing, nil
		}
		out, err := cutover.Unconvert(t.Convert, st.Effective)
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

// own is the field of cfg that p's row stores, with the fields of the rows
// inside it zeroed on a private copy of cfg.
func (e *Engine) own(cfg config.Config, p projRow) (reflect.Value, bool) {
	for _, segs := range e.below[p.row.Head] {
		if f, ok := fieldAt(&cfg, segs); ok {
			f.SetZero()
		}
	}
	return fieldAt(&cfg, p.segs)
}

// Snapshot is the projection as a value copy.
func (e *Engine) Snapshot() config.Config {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.cur
}

// Missing lists the rows whose canonical key the core store does not register,
// sorted: a wiring fault (the table names a key no section declares), reported
// here and by the oracle test rather than projected as a silent zero.
func (e *Engine) Missing() []string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return slices.Sorted(maps.Keys(e.missing))
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
		// Default-deny: a Value only for a field HEAD's GET serialised (exposed).
		// The environment-only fields (json:"-") keep their name, class and lock
		// so the page can say they are set by the operator, and never their value
		// or, for a secret, its metadata.
		shown := exposed(p.segs)
		switch {
		case p.residual:
			if f, ok := e.own(cur, p); ok && shown {
				if raw, err := json.Marshal(f.Interface()); err == nil {
					kv.Value = e.res.Redact(p.row.Head, raw)
				}
			}
		case p.target.Kind == cutover.TargetSecret:
			// A secret HEAD's GET never returned (json:"-") shows not even whether
			// it is set or its fingerprint.
			if shown {
				kv.Secret = e.secretMeta(p, unreadable[p.row.Head])
			}
		default:
			if f, ok := e.own(cur, p); ok && shown {
				if raw, err := json.Marshal(f.Interface()); err == nil {
					kv.Value = raw
				}
			}
			if st, ok := e.acc.Lookup(coreconfig.Path(e.keyOf(p))); ok {
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

// hasField reports whether Config has a field at segs.
func hasField(segs []string) bool {
	var c config.Config
	_, ok := fieldAt(&c, segs)
	return ok
}

// foldsIntoRoots reports whether a row is one of the HEAD fields that live in
// core's library roots (an entry target of that list).
func foldsIntoRoots(r *cutover.Row) bool {
	for _, t := range r.Targets {
		if t.Kind == cutover.TargetEntry && t.List == libraryRootsKey {
			return true
		}
	}
	return false
}

// foldsIntoOIDC reports whether a row is one of the HEAD fields that live in
// core's OIDC provider list (a fold target of that list).
func foldsIntoOIDC(r *cutover.Row) bool {
	for _, t := range r.Targets {
		if t.Kind == cutover.TargetFold && t.List == oidcProvidersKey {
			return true
		}
	}
	return false
}

// hasKeyTarget reports whether a row also has an ordinary key of its own (the
// allow-list: M2 writes it to the Cloudflare Access key besides the fold).
func hasKeyTarget(r *cutover.Row) bool {
	for _, t := range r.Targets {
		if t.Kind == cutover.TargetKey && concrete(t.Path) {
			return true
		}
	}
	return false
}

// keyOf is the core key a row's value is stored in: its own target, or the list
// a fold or roots row lives in.
func (e *Engine) keyOf(p projRow) string {
	switch p.kind {
	case kindFold:
		return oidcProvidersKey
	case kindRoots:
		return libraryRootsKey
	}
	return p.target.Path
}

// scalarText is the literal text of a JSON number or boolean, and false for any
// other JSON value.
func scalarText(raw json.RawMessage) (string, bool) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "", false
	}
	switch v.(type) {
	case float64, bool:
		return string(bytes.TrimSpace(raw)), true
	}
	return "", false
}
