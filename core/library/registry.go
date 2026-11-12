// file: core/library/registry.go
// version: 1.3.0
// guid: 363acb46-296b-4fe8-bd5c-6b1160ab673b
// last-edited: 2026-11-11

package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/libfs"
	"github.com/falkcorp/mediacore/storage"
)

// settings is the effective value of every core.library.* key library reads
// (05-library 5L.7), with the defaults of the table for a key the store does
// not hold.
type settings struct {
	roots          []RootSpec
	mappings       []Mapping
	protected      []string
	workers        int
	hashRate       int64 // bytes per second; 0 = unlimited
	debounce       time.Duration
	pollInterval   time.Duration
	warnPercent    int
	defaultMinFree int64
}

func defaultSettings() settings {
	return settings{
		workers: defaultWorkers, debounce: defaultDebounce, pollInterval: defaultPollInterval,
		warnPercent: defaultWarnPercent, defaultMinFree: defaultMinFreeBytes,
	}
}

// registry is one immutable snapshot of the registered roots.
type registry struct {
	byID      map[RootID]*Root
	order     []RootID // sorted
	set       settings
	protected *staticOracle
}

func emptyRegistry() *registry {
	return &registry{byID: map[RootID]*Root{}, set: defaultSettings(), protected: &staticOracle{}}
}

// readSettings reads the effective values through the manager's ConfigSource.
// A root entry that does not decode is skipped and reported in the returned
// error; the entries around it still load.
func (m *manager) readSettings() (settings, error) {
	set := defaultSettings()
	if m.opt.Config == nil {
		return set, nil
	}
	var errs []error
	raw := func(key string) (json.RawMessage, bool) {
		ks, ok := m.opt.Config.Lookup(config.Path(key))
		if !ok || len(ks.Effective) == 0 || string(ks.Effective) == "null" {
			return nil, false
		}
		return ks.Effective, true
	}
	get := func(key string, into any) {
		if b, ok := raw(key); ok {
			if err := json.Unmarshal(b, into); err != nil {
				errs = append(errs, fmt.Errorf("library: %s: %w", key, err))
			}
		}
	}
	elems := func(key string) []json.RawMessage {
		var list []json.RawMessage
		get(key, &list)
		return list
	}
	for i, e := range elems(keyRoots) {
		var s RootSpec
		if err := json.Unmarshal(e, &s); err != nil {
			errs = append(errs, fmt.Errorf("library: %s[%d]: %w", keyRoots, i, err))
			continue
		}
		set.roots = append(set.roots, s)
	}
	for i, e := range elems(keyMappings) {
		var mp Mapping
		if err := json.Unmarshal(e, &mp); err != nil {
			errs = append(errs, fmt.Errorf("library: %s[%d]: %w", keyMappings, i, err))
			continue
		}
		set.mappings = append(set.mappings, mp)
	}
	get(keyProtectedPaths, &set.protected)
	var d config.Duration
	if b, ok := raw(keyWatchDebounce); ok {
		if err := json.Unmarshal(b, &d); err != nil {
			errs = append(errs, fmt.Errorf("library: %s: %w", keyWatchDebounce, err))
		} else if d > 0 {
			set.debounce = d.Std()
		}
	}
	if b, ok := raw(keyWatchPollInterval); ok {
		if err := json.Unmarshal(b, &d); err != nil {
			errs = append(errs, fmt.Errorf("library: %s: %w", keyWatchPollInterval, err))
		} else if d > 0 {
			set.pollInterval = d.Std()
		}
	}
	var n int
	if b, ok := raw(keyTransferWorkers); ok && json.Unmarshal(b, &n) == nil && n > 0 {
		set.workers = n
	}
	if b, ok := raw(keyWarnPercent); ok && json.Unmarshal(b, &n) == nil && n >= 0 && n <= 100 {
		set.warnPercent = n
	}
	var n64 int64
	if b, ok := raw(keyHashMaxBytesPerSec); ok && json.Unmarshal(b, &n64) == nil && n64 > 0 {
		set.hashRate = n64
	}
	if b, ok := raw(keyDefaultMinBytes); ok && json.Unmarshal(b, &n64) == nil && n64 >= 0 {
		set.defaultMinFree = n64
	}
	return set, errors.Join(errs...)
}

// Reload re-reads the core.library.* entries and reconciles the registry
// (05-library 5L.4 steps 1 and 3). A root whose entry is unchanged keeps its
// handle (and its watchers); a changed or removed root is retired. A root that
// fails (an unresolvable path, an overlap, a dangerous path) is registered
// disabled and retried at the next Reload. A decode error in one entry is
// returned after the others load.
func (m *manager) Reload(ctx context.Context) error {
	set, rerr := m.readSettings()
	if err := m.apply(ctx, set); err != nil {
		return errors.Join(rerr, err)
	}
	return rerr
}

// candidate is one entry on its way to becoming a Root.
type candidate struct {
	spec  RootSpec
	abs   string
	id    fileID
	chain []fileID // self, parent, ... the filesystem root
	skip  []string // rel dirs walkers never enter
	why   error    // non-nil: disabled
}

func (m *manager) apply(ctx context.Context, set settings) error {
	m.reload.Lock()
	defer m.reload.Unlock()
	if m.closed.Load() {
		return errors.New("library: manager is closed")
	}
	old := m.reg.Load()
	cands := m.identifyAll(set)
	m.decideOverlaps(cands)
	next := &registry{byID: make(map[RootID]*Root, len(cands)), set: set}
	for _, c := range cands {
		next.byID[c.spec.ID] = m.makeRoot(c, old.byID[c.spec.ID], set)
	}
	for id := range next.byID {
		next.order = append(next.order, id)
	}
	slices.Sort(next.order)
	next.protected = &staticOracle{m: m, paths: slices.Clone(set.protected)}
	m.reg.Store(next)
	for id, r := range old.byID {
		if next.byID[id] != r {
			r.retire()
		}
	}
	m.mapper.Store(newPathMapper(set.mappings, m.Resolve))
	if cur := m.slots.Load(); cap(cur.ch) != min(max(set.workers, 1), maxTransferWorkers) {
		m.slots.Store(newSlotPool(set.workers))
	}
	for _, id := range next.order {
		r := next.byID[id]
		if r.why == nil {
			m.bootstrapSentinel(ctx, r)
		}
	}
	return nil
}

// identifyAll validates each spec and resolves its path once (EvalSymlinks),
// recording the directory's device+inode and its ancestors' for the overlap
// check. An invalid or duplicate spec is logged and left out: with no valid ID
// there is nothing to register it as.
func (m *manager) identifyAll(set settings) []*candidate {
	var out []*candidate
	seen := map[RootID]bool{}
	for _, raw := range set.roots {
		spec := raw.normalized()
		if err := spec.validate(); err != nil {
			m.log.Error("library: root entry refused", "root", spec.ID, "err", err)
			continue
		}
		if seen[spec.ID] {
			m.log.Error("library: duplicate root id; the later entry is ignored", "root", spec.ID)
			continue
		}
		seen[spec.ID] = true
		c := &candidate{spec: spec}
		c.abs, c.id, c.chain, c.why = identify(spec.Path)
		if c.why == nil && IsDangerousRoot(c.abs) {
			c.why = fmt.Errorf("%w: %s", ErrDangerousRoot, c.abs)
		}
		out = append(out, c)
	}
	return out
}

// fileID is a directory's device and inode; zero where the platform has none.
type fileID struct{ dev, ino uint64 }

func (f fileID) zero() bool { return f == fileID{} }

// identify resolves p once and returns its resolved path, its identity and its
// ancestors' identities (nearest first, self at index 0).
func identify(p string) (abs string, id fileID, chain []fileID, err error) {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fileID{}, nil, fmt.Errorf("resolve %s: %w", p, err)
	}
	resolved = filepath.Clean(resolved)
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fileID{}, nil, fmt.Errorf("stat %s: %w", resolved, err)
	}
	if !fi.IsDir() {
		return "", fileID{}, nil, fmt.Errorf("%s is not a directory", resolved)
	}
	for cur := resolved; ; {
		st, serr := os.Stat(cur)
		if serr != nil {
			break
		}
		if dev, ino, ok := libfs.InodeOf(st); ok {
			fid := fileID{dev, ino}
			chain = append(chain, fid)
			if cur == resolved {
				id = fid
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	return resolved, id, chain, nil
}

// nesting says how two roots' directories relate.
type nesting uint8

const (
	nestNone nesting = iota
	nestSame
	nestAInB // a is inside b
	nestBInA // b is inside a
)

// relate compares two candidates by path AND by device+inode of the directory
// and its ancestors: the second catches a bind mount (same directory under two
// paths) and a case-folding filesystem (two spellings of one path).
func relate(a, b *candidate) nesting {
	switch {
	case a.abs == b.abs, !a.id.zero() && a.id == b.id:
		return nestSame
	case isWithinRoot(a.abs, b.abs), !b.id.zero() && slices.Contains(a.chain, b.id):
		return nestAInB
	case isWithinRoot(b.abs, a.abs), !a.id.zero() && slices.Contains(b.chain, a.id):
		return nestBInA
	}
	return nestNone
}

// relUnder is inner's path relative to outer, found by position in the
// ancestor chain when the spellings differ (a case-folding filesystem).
func relUnder(inner, outer *candidate) string {
	if isWithinRoot(inner.abs, outer.abs) && inner.abs != outer.abs {
		rel, err := filepath.Rel(outer.abs, inner.abs)
		if err == nil {
			return filepath.ToSlash(rel)
		}
	}
	if outer.id.zero() {
		return ""
	}
	k := slices.Index(inner.chain, outer.id)
	if k <= 0 {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(inner.abs), "/")
	if k > len(parts) {
		return ""
	}
	return strings.Join(parts[len(parts)-k:], "/")
}

// decideOverlaps applies 5L.3.8: roots of different owners may not overlap
// (the later entry is disabled), and same-owner nesting is allowed with the
// outer root skipping the inner. The same directory twice is refused whoever
// owns it.
func (m *manager) decideOverlaps(cands []*candidate) {
	for i, c := range cands {
		if c.why != nil {
			continue
		}
		for _, a := range cands[:i] {
			if a.why != nil {
				continue
			}
			rel := relate(a, c)
			if rel == nestNone {
				continue
			}
			if rel == nestSame || a.spec.Owner != c.spec.Owner {
				c.why = fmt.Errorf("%w: %s (owner %s) and %s (owner %s)", ErrRootOverlap, c.spec.ID, c.spec.Owner, a.spec.ID, a.spec.Owner)
				m.log.Error("library: root disabled", "root", c.spec.ID, "err", c.why)
				break
			}
			// Same owner, nested: the outer root's walkers skip the inner one.
			outer, inner := a, c
			if rel == nestAInB {
				outer, inner = c, a
			}
			if r := relUnder(inner, outer); r != "" {
				outer.skip = append(outer.skip, r)
				m.log.Warn("library: nested roots of one owner; the outer root skips the inner", "outer", outer.spec.ID, "inner", inner.spec.ID, "rel", r)
			}
		}
	}
	for _, c := range cands {
		if c.why != nil {
			continue
		}
		c.skip = append(c.skip, c.spec.AppDir, c.spec.Quarantine)
		for _, e := range m.opt.ExcludePaths {
			ex := filepath.Clean(resolveExistingPrefix(e))
			if isWithinRoot(ex, c.abs) && ex != c.abs {
				if rel, err := filepath.Rel(c.abs, ex); err == nil {
					c.skip = append(c.skip, filepath.ToSlash(rel))
					m.log.Warn("library: the application's own directory is inside a root; walkers skip it", "root", c.spec.ID, "rel", filepath.ToSlash(rel))
				}
			} else if isWithinRoot(c.abs, ex) {
				m.log.Warn("library: a root is inside the application's own directory", "root", c.spec.ID)
			}
		}
		slices.Sort(c.skip)
		c.skip = slices.Compact(c.skip)
	}
}

// makeRoot returns old when it is unchanged, else a new Root for c.
func (m *manager) makeRoot(c *candidate, old *Root, set settings) *Root {
	if old != nil && c.why == nil && old.why == nil && old.abs == c.abs && old.id == c.id &&
		reflect.DeepEqual(old.spec, c.spec) && slices.Equal(old.skip, c.skip) && !old.isRetired() {
		return old
	}
	r := &Root{m: m, spec: c.spec, abs: c.abs, id: c.id, skip: c.skip, why: c.why, gone: make(chan struct{}),
		app: map[string]*localFS{}}
	if r.why != nil {
		return r
	}
	osr, err := os.OpenRoot(c.abs)
	if err != nil {
		r.why = fmt.Errorf("open %s: %w", c.abs, err)
		return r
	}
	r.os = osr
	r.base = &localFS{id: c.spec.ID, root: osr, abs: c.abs, spec: c.spec, skip: c.skip, log: m.log,
		mounted: func() error { return r.Mounted(context.Background()) },
		// The engine has checked the caller may read the target's root.
		target: m.Abs}
	r.rw = r.base
	if m.seam.wrapFS != nil {
		r.rw = m.seam.wrapFS(c.spec.ID, r.base)
	}
	return r
}

// Resolve maps an absolute host path to the most specific root containing it.
func (m *manager) Resolve(abs string) (Path, error) {
	if !filepath.IsAbs(abs) {
		return Path{}, fmt.Errorf("%w: %q is not absolute", ErrNotUnderRoot, abs)
	}
	resolved := filepath.Clean(resolveExistingPrefix(abs))
	var best *Root
	for _, r := range m.reg.Load().byID {
		if r.why != nil || !isWithinRoot(resolved, r.abs) {
			continue
		}
		if best == nil || len(r.abs) > len(best.abs) {
			best = r
		}
	}
	if best == nil {
		return Path{}, fmt.Errorf("%w: %s", ErrNotUnderRoot, abs)
	}
	rel := ""
	if resolved != best.abs {
		rel = filepath.ToSlash(resolved[len(best.abs)+1:])
	}
	p := Path{Root: best.spec.ID, Rel: rel}
	if err := p.Validate(); err != nil {
		return Path{}, err
	}
	return p, nil
}

// Abs returns p's resolved host path, checked to lie inside its root after
// symlink resolution.
func (m *manager) Abs(p Path) (string, error) {
	r, err := m.need(p)
	if err != nil {
		return "", err
	}
	abs := r.abs
	if p.Rel != "" {
		abs = filepath.Join(r.abs, filepath.FromSlash(p.Rel))
	}
	if resolved := filepath.Clean(resolveExistingPrefix(abs)); !isWithinRoot(resolved, r.abs) {
		return "", fmt.Errorf("%w: %s resolves to %s", ErrEscapesRoot, p, resolved)
	}
	return abs, nil
}

// absFor is Abs for a caller that must be able to read p's root.
func (m *manager) absFor(caller storage.Owner, p Path) (string, error) {
	r, err := m.need(p)
	if err != nil {
		return "", err
	}
	if !m.canRead(r, caller) {
		return "", fmt.Errorf("%w: %s", ErrNotReadable, p.Root)
	}
	if m.hiddenFrom(caller, r, p) {
		return "", fmt.Errorf("%w: %s", ErrReservedPath, p)
	}
	return m.Abs(p)
}

// hiddenFrom reports whether p is in r's AppDir or Quarantine area and caller is
// a reader that is neither the owner nor core: it may not see them.
func (m *manager) hiddenFrom(caller storage.Owner, r *Root, p Path) bool {
	return caller != CoreOwner && caller != r.spec.Owner && r.reservedHit(p, false)
}

// staticOracle is the core.library.protected_paths list as a ProtectionOracle:
// a path under a listed path, a listed path under the path (moving a directory
// carries its whole subtree), or a path that IS a listed path, is protected.
// Each test is made by name after symlink resolution and by device+inode, so a
// bind mount or a second name for the same directory counts. A path that
// cannot be resolved is protected (fail closed, 5L.5).
type staticOracle struct {
	m     *manager
	paths []string
}

// inodeChain is the device+inode of path and of every ancestor of it that
// exists.
func inodeChain(path string) []fileID {
	var chain []fileID
	for cur := path; ; {
		if st, serr := os.Stat(cur); serr == nil {
			if dev, ino, ok := libfs.InodeOf(st); ok {
				chain = append(chain, fileID{dev, ino})
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return chain
		}
		cur = parent
	}
}

// Protected implements ProtectionOracle. loaded is always true: the list is
// in memory.
func (o *staticOracle) Protected(_ context.Context, p Path) (protected, loaded bool, err error) {
	if o == nil || len(o.paths) == 0 {
		return false, true, nil
	}
	abs, err := o.m.Abs(p)
	if err != nil {
		return true, true, err
	}
	resolved := filepath.Clean(resolveExistingPrefix(abs))
	chain := inodeChain(resolved)
	var self fileID
	haveSelf := false
	if st, serr := os.Stat(resolved); serr == nil {
		if dev, ino, ok := libfs.InodeOf(st); ok {
			self, haveSelf = fileID{dev, ino}, true
		}
	}
	for _, e := range o.paths {
		if !filepath.IsAbs(e) {
			return true, true, fmt.Errorf("library: protected path %q is not absolute", e)
		}
		ex := filepath.Clean(resolveExistingPrefix(e))
		// The path is, or is inside, a protected path; or a protected path is
		// inside the path (a directory carries its subtree along).
		if isWithinRoot(resolved, ex) || isWithinRoot(ex, resolved) {
			return true, true, nil
		}
		if st, serr := os.Stat(ex); serr == nil {
			if dev, ino, ok := libfs.InodeOf(st); ok && slices.Contains(chain, fileID{dev, ino}) {
				return true, true, nil
			}
		}
		// The same two tests by inode, for the second direction: the path's own
		// inode among the protected path's ancestors.
		if haveSelf && slices.Contains(inodeChain(ex), self) {
			return true, true, nil
		}
	}
	return false, true, nil
}

// ProtectedUnder implements SubtreeOracle: the list answers for a whole
// directory because Protected already tests both directions.
func (o *staticOracle) ProtectedUnder(ctx context.Context, dir Path) (bool, bool, error) {
	return o.Protected(ctx, dir)
}

// protectedForMove asks every oracle (the static list first); protected, or
// unknown, or an error, all refuse a Move or a Quarantine (5L.3.3, 5L.5). dir
// says the source is a directory: the whole subtree moves, so each oracle must
// be able to answer for a subtree (SubtreeOracle), and one that cannot refuses.
func (m *manager) protectedForMove(ctx context.Context, p Path, dir bool) (bool, error) {
	oracles := append([]ProtectionOracle{m.reg.Load().protected}, m.opt.Oracles...)
	for _, o := range oracles {
		var protected, loaded bool
		var err error
		switch so, isSub := o.(SubtreeOracle); {
		case dir && isSub:
			protected, loaded, err = so.ProtectedUnder(ctx, p)
		case dir:
			return true, fmt.Errorf("library: a protection oracle (%T) cannot answer for the directory %s; it is not moved", o, p)
		default:
			protected, loaded, err = o.Protected(ctx, p)
		}
		if err != nil || !loaded || protected {
			return true, err
		}
	}
	return false, nil
}

// ---- Root ----

// rootState is the root_state gauge's label.
type rootState uint8

const (
	stateOK rootState = iota
	stateUnmounted
	stateDisabled
	stateDegraded
)

func (s rootState) String() string {
	return [...]string{"ok", "unmounted", "disabled", "degraded"}[s]
}

// Root is one registered root. It is goroutine-safe; its spec and resolved
// path never change (a changed entry makes a new Root).
type Root struct {
	m     *manager
	spec  RootSpec // normalized
	abs   string   // resolved host path
	id    fileID
	skip  []string
	why   error // non-nil: the root is disabled, and why
	os    *os.Root
	base  *localFS // full-rights local driver
	rw    fsOps    // base, possibly wrapped by a test seam
	gone  chan struct{}
	retOK sync.Once
	appMu sync.Mutex
	app   map[string]*localFS
	// polling is set while the watcher has fallen back to polling (degraded).
	polling atomic.Bool
}

// ID is the root's ID.
func (r *Root) ID() RootID { return r.spec.ID }

// Spec is the normalized spec the root was registered from.
func (r *Root) Spec() RootSpec {
	s := r.spec
	s.Aliases = slices.Clone(s.Aliases)
	return s
}

// Disabled reports why the root is disabled, or nil.
func (r *Root) Disabled() error {
	if r.why == nil {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrRootDisabled, r.why)
}

func (r *Root) isRetired() bool {
	select {
	case <-r.gone:
		return true
	default:
		return false
	}
}

// usable is nil for a registered, live root.
func (r *Root) usable() error {
	if err := r.Disabled(); err != nil {
		return err
	}
	if r.isRetired() {
		return fmt.Errorf("%w: %s was replaced by a reload", ErrRootDisabled, r.spec.ID)
	}
	return nil
}

// retire ends the root's life: watchers stop and its handles close. os.Root
// closes safely under in-flight operations.
func (r *Root) retire() {
	r.retOK.Do(func() {
		close(r.gone)
		if r.os == nil {
			return
		}
		r.appMu.Lock()
		for _, a := range r.app {
			closeLogged(r.m.log, a.root, "an app directory handle")
		}
		clear(r.app)
		r.appMu.Unlock()
		closeLogged(r.m.log, r.os, "the root handle")
	})
}

// minFree is the floor a transfer into the root must leave free.
func (r *Root) minFree() int64 {
	if r.spec.MinFree == MinFreeDefault {
		return r.m.reg.Load().set.defaultMinFree
	}
	return r.spec.MinFree
}

// FS returns the confined filesystem for caller. A caller that cannot write the
// root (not the owner, an external root, a disabled owner) gets a read-only
// view whose mutating methods return ErrReadOnlyRoot; a caller that cannot read
// it gets ErrNotReadable.
func (r *Root) FS(caller storage.Owner) (RootFS, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	if !r.m.canRead(r, caller) {
		return nil, fmt.Errorf("%w: %s", ErrNotReadable, r.spec.ID)
	}
	return r.viewFor(caller), nil
}

// GuardedFS returns a read-only view for a read made on a person's behalf, such
// as the directory picker: the root's AppDir and Quarantine area are hidden and
// refused whoever owns the root, a symlink that leads into them is refused where
// it is read (the opened descriptor is examined, not only the name), and every
// mutating method returns ErrReadOnlyRoot. Core's own code reads through
// FS(CoreOwner), which is unguarded because core's own code is trusted; a
// request is not core, so a request-driven read never takes that view.
func (r *Root) GuardedFS() (RootReader, error) {
	if err := r.usable(); err != nil {
		return nil, err
	}
	c := r.base.readOnlyView()
	c.sealed, c.hidden = true, true
	c.race = r.m.seam.fsRace
	return c, nil
}

func (r *Root) viewFor(caller storage.Owner) fsOps {
	target := func(p Path) (string, error) {
		abs, err := r.m.absFor(caller, p)
		if err != nil {
			return "", err
		}
		// A link into the AppDir or the quarantine area would reach them by a
		// name the views do not guard.
		if caller != CoreOwner {
			if tr, nerr := r.m.need(p); nerr == nil && tr.reservedHit(p, false) {
				return "", fmt.Errorf("%w: %s", ErrReservedPath, p)
			}
		}
		return abs, nil
	}
	// Only core touches the root's AppDir and Quarantine area through a
	// filesystem view. A module may read its own (the held files, its own app
	// directories are reached through Root.AppDir), never write them; a reader
	// that is not the owner (the download capability) sees neither.
	guard := func(c *localFS) {
		c.target = target
		if caller != CoreOwner {
			c.sealed = true
			c.hidden = caller != r.spec.Owner
		}
	}
	if r.m.canWrite(r, caller) && r.m.enabled(r.spec.Owner) {
		c := *r.base
		guard(&c)
		if r.m.seam.wrapFS != nil {
			// A test seam wraps the shared driver; give it the caller's target too.
			return r.m.seam.wrapFS(r.spec.ID, &c)
		}
		return &c
	}
	c := r.base.readOnlyView()
	guard(c)
	return c
}

// reservedHit reports whether p is in this root's AppDir or Quarantine area,
// by spelling, by where its symlinks lead and by directory identity
// (localFS.reserved). carries: a directory that contains one of them counts (a
// Move or Quarantine carries it).
func (r *Root) reservedHit(p Path, carries bool) bool {
	if r.base == nil {
		return false
	}
	return r.base.reserved(p.Rel, carries)
}

// refuseReserved is ErrReservedPath when caller (anyone but core) names the
// root's own areas.
func (m *manager) refuseReserved(caller storage.Owner, r *Root, p Path, carries bool) error {
	if caller == CoreOwner || !r.reservedHit(p, carries) {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrReservedPath, p)
}

// Usage is the capacity of the filesystem holding the root.
func (r *Root) Usage(ctx context.Context) (Usage, error) {
	if err := ctx.Err(); err != nil {
		return Usage{}, err
	}
	if err := r.usable(); err != nil {
		return Usage{}, err
	}
	return r.m.seam.usage(r.abs)
}

// state is the root's root_state label now.
func (r *Root) state(ctx context.Context) rootState {
	switch {
	case r.why != nil || r.isRetired():
		return stateDisabled
	case r.Mounted(ctx) != nil:
		return stateUnmounted
	case r.polling.Load():
		return stateDegraded
	}
	if u, err := r.Usage(ctx); err == nil && u.Total > 0 {
		if pct := int(100 * u.Avail / u.Total); pct < r.m.reg.Load().set.warnPercent {
			return stateDegraded
		}
	}
	return stateOK
}

// ---- sentinel ----

// sentinelFile is <AppDir>/root.json: which install owns the volume and which
// root it is.
type sentinelFile struct {
	Install string `json:"install"`
	Root    RootID `json:"root"`
}

const sentinelName = "root.json"

func (r *Root) sentinelRel() string { return path.Join(r.spec.AppDir, sentinelName) }

// Mounted verifies, fresh on every call, that the root's directory is still
// the one that was opened and that its sentinel names this install and this
// root. A SentinelBootstrap root with no sentinel yet passes when it holds
// non-dot content (HEAD's heuristic, Mounted) and gets its sentinel written.
// An external root is checked by the heuristic alone: nobody writes in it.
func (r *Root) Mounted(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.usable(); err != nil {
		return err
	}
	if err := r.checkIdentity(); err != nil {
		return err
	}
	if r.spec.Role == RoleExternal {
		return r.heuristic()
	}
	sf, err := r.readSentinel()
	switch {
	case err == nil:
		if sf.Install != r.m.opt.InstallID || sf.Root != r.spec.ID {
			return fmt.Errorf("%w: %s's sentinel names install %q root %q", ErrNotMounted, r.spec.ID, sf.Install, sf.Root)
		}
		return nil
	case errors.Is(err, fs.ErrNotExist):
		if r.spec.Sentinel != SentinelBootstrap {
			return fmt.Errorf("%w: %s has no sentinel", ErrNotMounted, r.spec.ID)
		}
		if herr := r.heuristic(); herr != nil {
			return herr
		}
		if werr := r.writeSentinel(); werr != nil && !errors.Is(werr, fs.ErrExist) {
			r.m.log.Warn("library: could not write the sentinel of a bootstrap root", "root", r.spec.ID, "err", werr)
		}
		return nil
	default:
		return fmt.Errorf("%w: %s's sentinel is unreadable: %w", ErrNotMounted, r.spec.ID, err)
	}
}

// heuristic is libroot's check: the root holds at least one non-dot entry.
func (r *Root) heuristic() error {
	if err := Mounted(r.abs); err != nil {
		return fmt.Errorf("%w: %w", ErrNotMounted, err)
	}
	return nil
}

// checkIdentity compares the directory now at the root's path with the one the
// handle was opened on. A share that mounted (or went away) after the open
// changes the inode under the same path; the handle still sees the old
// directory, so every destructive operation must refuse until Reload re-opens.
func (r *Root) checkIdentity() error {
	st, err := os.Stat(r.abs)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNotMounted, r.spec.ID, err)
	}
	if dev, ino, ok := libfs.InodeOf(st); ok && !r.id.zero() && (fileID{dev, ino}) != r.id {
		return fmt.Errorf("%w: %s's directory changed since it was opened (a mount appeared or went away); reload", ErrNotMounted, r.spec.ID)
	}
	return nil
}

func (r *Root) readSentinel() (sentinelFile, error) {
	f, err := r.os.Open(r.sentinelRel())
	if err != nil {
		return sentinelFile{}, err
	}
	defer closeLogged(r.m.log, f, "the sentinel")
	var sf sentinelFile
	dec := json.NewDecoder(io.LimitReader(f, 4096))
	if err := dec.Decode(&sf); err != nil {
		return sentinelFile{}, fmt.Errorf("decode: %w", err)
	}
	return sf, nil
}

// writeSentinel writes the sentinel with O_EXCL: it never replaces one. It
// returns fs.ErrExist when one is there.
func (r *Root) writeSentinel() error {
	if r.spec.Role == RoleExternal {
		return fmt.Errorf("%w: an external root takes no sentinel", ErrReadOnlyRoot)
	}
	if err := r.ensureAppDirBase(); err != nil {
		return err
	}
	b, err := json.Marshal(sentinelFile{Install: r.m.opt.InstallID, Root: r.spec.ID})
	if err != nil {
		return err
	}
	f, err := r.os.OpenFile(r.sentinelRel(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return withRollback(werr, "the partial sentinel", r.os.Remove(r.sentinelRel()))
	}
	if d, derr := libfs.OpenDir(r.os, r.spec.AppDir); derr == nil {
		if serr := libfs.SyncDir(d); serr != nil {
			r.m.log.Warn("library: could not sync the app directory after writing the sentinel", "root", r.spec.ID, "err", serr)
		}
		closeLogged(r.m.log, d, "the app directory")
	}
	return nil
}

// bootstrapSentinel runs at registration: a SentinelBootstrap root whose
// heuristic passes gets its sentinel (5L.10), and the state is logged.
func (m *manager) bootstrapSentinel(ctx context.Context, r *Root) {
	if r.spec.Sentinel != SentinelBootstrap || r.spec.Role == RoleExternal {
		return
	}
	if err := r.Mounted(ctx); err != nil {
		m.log.Warn("library: root is not mounted", "root", r.spec.ID, "err", err, slog.String("state", "unmounted"))
	}
}

// WriteSentinel writes root id's sentinel when none exists and never replaces
// one: an identical sentinel is a no-op, a different one is fs.ErrExist.
func (m *manager) WriteSentinel(ctx context.Context, id RootID) error {
	r, err := m.Root(id)
	if err != nil {
		return err
	}
	if err := r.usable(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := r.checkIdentity(); err != nil {
		return err
	}
	err = r.writeSentinel()
	if errors.Is(err, fs.ErrExist) {
		sf, rerr := r.readSentinel()
		if rerr == nil && sf.Install == m.opt.InstallID && sf.Root == id {
			return nil
		}
		return fmt.Errorf("library: %s already has a different sentinel: %w", id, fs.ErrExist)
	}
	return err
}

// ---- AppDir ----

// appDirMode: owner-writable only.
const appDirMode fs.FileMode = 0o755

// ensureAppDirBase creates <root>/<AppDir> when absent and refuses a symlink, a
// non-directory or a directory this process does not own; world write is
// removed (libroot.ensureReportsDir semantics, through the root's handle).
func (r *Root) ensureAppDirBase() error {
	return r.ensureDir(r.spec.AppDir)
}

func (r *Root) ensureDir(rel string) error {
	st, err := r.os.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		if merr := r.os.MkdirAll(rel, appDirMode); merr != nil {
			return merr
		}
		st, err = r.os.Lstat(rel)
	}
	if err != nil {
		return err
	}
	switch {
	case st.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("library: %s/%s is a symlink; refusing to use it", r.spec.ID, rel)
	case !st.IsDir():
		return fmt.Errorf("library: %s/%s is not a directory", r.spec.ID, rel)
	case !OwnedByCurrentUser(st):
		return fmt.Errorf("library: %s/%s is not owned by the user this process runs as; refusing to trust it", r.spec.ID, rel)
	}
	if st.Mode().Perm()&worldWrite != 0 {
		if err := r.os.Chmod(rel, st.Mode().Perm()&^worldWrite); err != nil {
			return fmt.Errorf("library: %s/%s is writable by everyone and could not be tightened: %w", r.spec.ID, rel, err)
		}
	}
	return nil
}

// AppDir returns a confined directory under <root>/<AppDir>/<name>, created
// owner-only-writable, refusing a symlink or a foreign-owned directory. It is
// core's: a name that leads into the root's Quarantine area is ErrReservedPath,
// and the view it returns is writable whatever owner the root has. A module
// reaches its own through ModuleRoot.AppDir, which names the directory for the
// module and checks that the module may write the root.
func (r *Root) AppDir(ctx context.Context, name string) (RootFS, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := r.usable(); err != nil {
		return nil, err
	}
	if err := ValidateRel(name); err != nil {
		return nil, err
	}
	// The quarantine area lives under the AppDir by default, so <AppDir>/quarantine
	// IS the area: a writable view of it would let its holder delete a held file.
	if want, q := path.Join(r.spec.AppDir, name), r.spec.Quarantine; relWithinFold(want, q) || relWithinFold(q, want) {
		return nil, fmt.Errorf("%w: %s is the root's quarantine area", ErrReservedPath, want)
	}
	if err := r.checkIdentity(); err != nil {
		return nil, err
	}
	r.appMu.Lock()
	defer r.appMu.Unlock()
	if a, ok := r.app[name]; ok {
		return a, nil
	}
	if err := r.ensureAppDirBase(); err != nil {
		return nil, err
	}
	rel := path.Join(r.spec.AppDir, name)
	for i := 0; ; {
		j := strings.IndexByte(rel[i:], '/')
		sub := rel
		if j >= 0 {
			sub = rel[:i+j]
		}
		if err := r.ensureDir(sub); err != nil {
			return nil, err
		}
		if j < 0 {
			break
		}
		i += j + 1
	}
	sub, err := r.os.OpenRoot(rel)
	if err != nil {
		return nil, err
	}
	a := &localFS{id: r.spec.ID, root: sub, abs: filepath.Join(r.abs, filepath.FromSlash(rel)), spec: r.spec, log: r.m.log,
		mounted: func() error { return r.Mounted(context.Background()) }}
	r.app[name] = a
	return a, nil
}
