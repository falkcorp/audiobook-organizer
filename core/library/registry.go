// file: core/library/registry.go
// version: 1.0.0
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
	spec     RootSpec
	abs      string
	id       fileID
	chain    []fileID // self, parent, ... the filesystem root
	skip     []string // rel dirs walkers never enter
	why      error    // non-nil: disabled
	reusable *Root
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
	r.base = &localFS{id: c.spec.ID, root: osr, abs: c.abs, spec: c.spec, skip: c.skip,
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
	return m.Abs(p)
}

// staticOracle is the core.library.protected_paths list as a ProtectionOracle:
// a file under any listed path is protected. A path that cannot be resolved is
// protected (fail closed, 5L.5).
type staticOracle struct {
	m     *manager
	paths []string
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
	var chain []fileID
	for cur := resolved; ; {
		if st, serr := os.Stat(cur); serr == nil {
			if dev, ino, ok := libfs.InodeOf(st); ok {
				chain = append(chain, fileID{dev, ino})
			}
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		cur = parent
	}
	for _, e := range o.paths {
		if !filepath.IsAbs(e) {
			return true, true, fmt.Errorf("library: protected path %q is not absolute", e)
		}
		ex := filepath.Clean(resolveExistingPrefix(e))
		if isWithinRoot(resolved, ex) {
			return true, true, nil
		}
		if st, serr := os.Stat(ex); serr == nil {
			if dev, ino, ok := libfs.InodeOf(st); ok && slices.Contains(chain, fileID{dev, ino}) {
				return true, true, nil
			}
		}
	}
	return false, true, nil
}

// protectedForMove asks every oracle (the static list first); protected, or
// unknown, or an error, all refuse a Move (5L.3.3, 5L.5).
func (m *manager) protectedForMove(ctx context.Context, p Path) (bool, error) {
	oracles := append([]ProtectionOracle{m.reg.Load().protected}, m.opt.Oracles...)
	for _, o := range oracles {
		protected, loaded, err := o.Protected(ctx, p)
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
			_ = a.root.Close()
		}
		clear(r.app)
		r.appMu.Unlock()
		_ = r.os.Close()
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

func (r *Root) viewFor(caller storage.Owner) fsOps {
	target := func(p Path) (string, error) { return r.m.absFor(caller, p) }
	if r.m.canWrite(r, caller) && r.m.enabled(r.spec.Owner) {
		if r.m.seam.wrapFS != nil {
			// A test seam wraps the shared driver; give it the caller's target too.
			c := *r.base
			c.target = target
			return r.m.seam.wrapFS(r.spec.ID, &c)
		}
		c := *r.base
		c.target = target
		return &c
	}
	c := r.base.readOnlyView()
	c.target = target
	return c
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
	defer f.Close()
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
		_ = r.os.Remove(r.sentinelRel())
		return werr
	}
	if d, derr := libfs.OpenDir(r.os, r.spec.AppDir); derr == nil {
		_ = libfs.SyncDir(d)
		_ = d.Close()
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
// owner-only-writable, refusing a symlink or a foreign-owned directory.
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
	a := &localFS{id: r.spec.ID, root: sub, abs: filepath.Join(r.abs, filepath.FromSlash(rel)), spec: r.spec,
		mounted: func() error { return r.Mounted(context.Background()) }}
	r.app[name] = a
	return a, nil
}
