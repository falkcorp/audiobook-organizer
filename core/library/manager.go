// file: core/library/manager.go
// version: 1.4.1
// guid: 3356d9de-7d9b-4473-880c-196e0a555910
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/internal/keyedmutex"
	"github.com/falkcorp/mediacore/internal/libfs"
	"github.com/falkcorp/mediacore/metrics"
	"github.com/falkcorp/mediacore/storage"
)

// The interfaces below are the spec's Manager and Scope (05-library 5L.3.1),
// each composed of embedded role interfaces. core's interface-width gate
// allows eight entries per declaration with no baseline raise; Manager and
// Scope each have more than eight methods, so each is the union of the same
// methods grouped by what they do. The method sets are the spec's, exactly.

// RootRegistry is the read side of the registry: which roots exist and where a
// host path falls.
type RootRegistry interface {
	Root(id RootID) (*Root, error)
	Roots(filter RootFilter) []*Root
	// Resolve maps an absolute host path to the most specific root that contains
	// it (separator boundary). ErrNotUnderRoot if none.
	Resolve(abs string) (Path, error)
	// Abs returns the resolved host path, for handing to external tools
	// (ffmpeg, a tagger). The result is checked to be inside the root after
	// symlink resolution.
	Abs(p Path) (string, error)
}

// Transferer moves bytes between roots. Mutating calls name the caller
// explicitly on Manager; Scope supplies it implicitly.
type Transferer interface {
	Transfer(ctx context.Context, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error)
	// TransferIn performs the same transfer, but the step that finishes its
	// journal row is staged into the caller's batch b instead of committing on
	// its own, so "file moved" and the caller's rows become durable in ONE
	// commit. library binds its own c/library/ keyspace to b; the caller
	// commits b.
	TransferIn(ctx context.Context, b *storage.Batch, caller storage.Owner, src, dst Path, opts TransferOptions) (TransferResult, error)
}

// Hasher hashes a file through its root.
type Hasher interface {
	Hash(ctx context.Context, p Path) (Digest, error)
}

// Quarantiner holds files aside and gives them back.
type Quarantiner interface {
	Quarantine(ctx context.Context, caller storage.Owner, p Path, reason string) (QuarantineID, error)
	Release(ctx context.Context, caller storage.Owner, id QuarantineID, to *Path) (Path, error)
}

// Watcher yields debounced change batches for a root.
type Watcher interface {
	Watch(ctx context.Context, id RootID, opts WatchOptions) (iter.Seq[[]Event], error)
}

// Manager is the core service. Core subsystems and the kernel hold it; a
// module never does, it receives a Scope.
//
// The value New returns also implements Reloader, Recoverer, Purger,
// SentinelWriter, QuarantineLister and io.Closer.
type Manager interface {
	RootRegistry
	Transferer
	Hasher
	Quarantiner
	Watcher
	Mapper() PathMapper
}

// ScopeRoots is the root-facing half of Scope.
type ScopeRoots interface {
	Owner() storage.Owner
	Roots(filter RootFilter) []*ModuleRoot // roots the module owns or may read, each bound to the module's owner
	FS(id RootID) (RootFS, error)          // read-only unless the module may write the root
	Resolve(abs string) (Path, error)
	Abs(p Path) (string, error)
}

// ScopeTransfers is the transfer half of Scope, the owner bound in.
type ScopeTransfers interface {
	Transfer(ctx context.Context, src, dst Path, opts TransferOptions) (TransferResult, error)
	TransferIn(ctx context.Context, b *storage.Batch, src, dst Path, opts TransferOptions) (TransferResult, error)
	// PendingTransfers lists this owner's TransferIn journal rows whose file
	// operation finished but whose caller batch never committed (a crash
	// between the two); the module reconciles each one idempotently.
	PendingTransfers(ctx context.Context) ([]TransferRecord, error)
}

// ScopeQuarantine is the quarantine half of Scope, the owner bound in.
type ScopeQuarantine interface {
	Quarantine(ctx context.Context, p Path, reason string) (QuarantineID, error)
	Release(ctx context.Context, id QuarantineID, to *Path) (Path, error)
}

// Scope is the module-scoped handle (module.Deps.Library). The kernel builds
// one per module with that module's owner bound in, so a module cannot act as
// another owner and "a RoleLibrary root is written by its owner only" has an
// enforcement point the caller cannot spoof.
type Scope interface {
	ScopeRoots
	ScopeTransfers
	Hasher
	ScopeQuarantine
	Watcher
	Mapper() PathMapper
}

// Reloader re-reads the core.library.* entries and reconciles the registry,
// the mappings and the protected paths. The kernel calls it after the storage
// migrations and Bind calls it on every change.
type Reloader interface {
	Reload(ctx context.Context) error
}

// Purger deletes a quarantined item for good, a separate permissioned action.
type Purger interface {
	Purge(ctx context.Context, id QuarantineID) error
}

// QuarantineLister lists one owner's quarantine records.
type QuarantineLister interface {
	Quarantined(ctx context.Context, owner storage.Owner) ([]QuarantineRecord, error)
}

// SentinelWriter writes a root's sentinel when none exists, never replacing
// one: the step that makes a new SentinelRequired root mountable.
type SentinelWriter interface {
	WriteSentinel(ctx context.Context, id RootID) error
}

// CapabilityDownload is the capability name the download roots' move-out
// right is checked against (05-library 5L.3.8).
const CapabilityDownload = "download"

// ConfigSource is what library reads of config: the effective value of a key.
// *config.Store satisfies it. It is narrower than *config.Store on purpose
// (the spec's Options.Config is a *config.Store), so a test or another host can
// stand in without a store and library depends on one method.
type ConfigSource interface {
	Lookup(p config.Path) (config.KeyState, bool)
}

// Options is library's constructor input, built by the kernel at 06 P5.
type Options struct {
	Keyspace storage.Keyspace // c/library/: the transfer journal and quarantine records
	// Config supplies core.library.roots, mappings and the other keys; nil
	// means the defaults and no roots.
	Config  ConfigSource
	Oracles []ProtectionOracle // the download adapters' seeding oracles; the static core.library.protected_paths list is added by Reload
	Logger  *slog.Logger
	Metrics *metrics.Scope // core scope "mediacore.library"
	// InstallID is this installation's identity, the half of a root's sentinel
	// that says which install owns the volume. Required.
	InstallID string
	// Enabled reports whether an owner's module is enabled; nil means every
	// owner is. A disabled owner's roots stay readable and take no transfer,
	// quarantine or watch (05-library 5L.3.8).
	Enabled func(owner storage.Owner) bool
	// HasCapability reports whether a module holds a capability (CapabilityDownload);
	// nil means none does.
	HasCapability func(owner storage.Owner, capability string) bool
	// ExcludePaths are the app's own directories (the DB's parent, the state
	// directory): one inside a root is warned about and skipped by its walkers.
	ExcludePaths []string
}

// seams are the test hooks; New leaves them zero and fills the defaults.
type seams struct {
	wrapFS    func(id RootID, f fsOps) fsOps         // fault injection around every root's filesystem
	wrapStore func(name string, s rowStore) rowStore // fault injection around the journal ("transfer") and quarantine records ("quarantine")
	usage     func(abs string) (Usage, error)        // statfs
	now       func() time.Time
	sleep     func(ctx context.Context, d time.Duration) error // the hash limiter's wait
	newID     func() string
	watchSrc  watchSourceFunc
	inodeOf   func(fs.FileInfo) (dev, ino uint64, ok bool) // the by-identity area test (localFS.inode)
	noHandles bool                                         // behave as a platform without directory-relative syscalls (Windows)
	fsRace    func(stage, name string)                     // GuardedFS: a hidden view's read open calls it at "checked", "resolved", "parent" and "opened" (see localFS.race)
}

// needHandles refuses a mutation on a platform without directory-relative
// syscalls (Windows). Everything a transfer, a quarantine or a release does goes
// through them: a path-based stand-in is not confined to the directory the caller
// proved, and its no-replace rename replaces what another writer created in
// between. The handle-based Windows follow-up (MoveFileEx without
// REPLACE_EXISTING) lifts this.
func (m *manager) needHandles() error {
	if libfs.HasHandles && !m.seam.noHandles {
		return nil
	}
	return fmt.Errorf("%w: this platform has no directory-relative operations, so nothing is transferred, quarantined or released", ErrNotSupported)
}

// Settings defaults (05-library 5L.7).
const (
	defaultWorkers        = 4
	defaultDebounce       = 5 * time.Second
	defaultPollInterval   = 15 * time.Minute
	defaultWarnPercent    = 10
	defaultMinFreeBytes   = 5 << 30
	maxTransferWorkers    = 64
	keyRoots              = "core.library.roots"
	keyMappings           = "core.library.mappings"
	keyProtectedPaths     = "core.library.protected_paths"
	keyTransferWorkers    = "core.library.transfer.workers"
	keyHashMaxBytesPerSec = "core.library.hash.max_bytes_per_sec"
	keyWatchDebounce      = "core.library.watch.debounce"
	keyWatchPollInterval  = "core.library.watch.poll_interval"
	keyWarnPercent        = "core.library.free_space.warn_percent"
	keyDefaultMinBytes    = "core.library.free_space.default_min_bytes"
)

// manager is the Manager. It is goroutine-safe: the registry is an immutable
// snapshot behind an atomic pointer, replaced whole by Reload.
type manager struct {
	opt  Options
	log  *slog.Logger
	seam seams

	reg    atomic.Pointer[registry]
	mapper atomic.Pointer[pathMapper]
	slots  atomic.Pointer[slotPool]
	reload sync.Mutex // serializes Reload
	// hashSlots bounds concurrent Hash calls to the CPU count: each reads a whole
	// file and runs a SHA-256, and a caller fanning out over a library must not
	// open one reader per book.
	hashSlots *slotPool

	locks    keyedmutex.Mutex // striped per-path locks (5L.6)
	jrn      rowStore         // c/library/transfer/
	qrn      rowStore         // c/library/quarantine/
	ctr      *counters
	lim      limiter  // core.library.hash.max_bytes_per_sec
	inflight sync.Map // row ID -> struct{}: rows a live call owns, so Recover leaves them alone
	closed   atomic.Bool
}

var (
	_ Manager          = (*manager)(nil)
	_ Reloader         = (*manager)(nil)
	_ Recoverer        = (*manager)(nil)
	_ Purger           = (*manager)(nil)
	_ QuarantineLister = (*manager)(nil)
	_ SentinelWriter   = (*manager)(nil)
	_ io.Closer        = (*manager)(nil)
)

// New builds the Manager. It reads nothing from the store and starts no
// goroutine; the kernel loads the roots with Reload after storage migrations
// (05-library 5L.4).
func New(opts Options) (Manager, error) { return newManager(opts, seams{}) }

func newManager(opts Options, sm seams) (*manager, error) {
	if opts.InstallID == "" {
		return nil, fmt.Errorf("%w: Options.InstallID is empty", ErrInvalidOptions)
	}
	if opts.Keyspace.Prefix() == "" {
		return nil, fmt.Errorf("%w: Options.Keyspace is the zero keyspace", ErrInvalidOptions)
	}
	m := &manager{opt: opts, seam: sm, ctr: newCounters()}
	m.log = opts.Logger
	if m.log == nil {
		m.log = slog.New(slog.DiscardHandler)
	}
	if m.seam.usage == nil {
		m.seam.usage = statUsage
	}
	if m.seam.now == nil {
		m.seam.now = time.Now
	}
	if m.seam.newID == nil {
		m.seam.newID = newULID
	}
	m.jrn = m.store("transfer")
	m.qrn = m.store("quarantine")
	m.reg.Store(emptyRegistry())
	m.mapper.Store(newPathMapper(nil, nil))
	m.slots.Store(newSlotPool(defaultWorkers))
	m.hashSlots = newSlotPool(runtime.NumCPU())
	if opts.Metrics != nil {
		if err := m.instrument(opts.Metrics); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// store is the row store of one c/library/ sub-keyspace, behind the test seam.
func (m *manager) store(name string) rowStore {
	var s rowStore = newRowStore(m.opt.Keyspace.Sub(name))
	if m.seam.wrapStore != nil {
		s = m.seam.wrapStore(name, s)
	}
	return s
}

// Close retires every root: watchers end and the *os.Root handles close.
// In-flight operations finish their current step against the closed handle
// and fail; the journal recovers them at the next boot (05-library 5L.4 step 6).
func (m *manager) Close() error {
	if !m.closed.CompareAndSwap(false, true) {
		return nil
	}
	m.reload.Lock()
	defer m.reload.Unlock()
	old := m.reg.Swap(emptyRegistry())
	for _, r := range old.byID {
		r.retire()
	}
	return nil
}

// Mapper returns the current path mapper; each Reload swaps in a new one.
func (m *manager) Mapper() PathMapper { return m.mapper.Load() }

func (m *manager) enabled(owner storage.Owner) bool {
	return m.opt.Enabled == nil || m.opt.Enabled(owner)
}

func (m *manager) hasCapability(owner storage.Owner, capability string) bool {
	return m.opt.HasCapability != nil && m.opt.HasCapability(owner, capability)
}

// canRead: core, the owner, or (download roots) a module with the capability.
func (m *manager) canRead(r *Root, caller storage.Owner) bool {
	if caller == CoreOwner || caller == r.spec.Owner {
		return true
	}
	return r.spec.Role == RoleDownload && m.hasCapability(caller, CapabilityDownload)
}

// canWrite: the owner only, and never an external root.
func (m *manager) canWrite(r *Root, caller storage.Owner) bool {
	return caller == r.spec.Owner && r.spec.Role != RoleExternal
}

// canMoveOut: whoever may write the root, or a module with the download
// capability on a download root.
func (m *manager) canMoveOut(r *Root, caller storage.Owner) bool {
	if m.canWrite(r, caller) {
		return true
	}
	return r.spec.Role == RoleDownload && m.hasCapability(caller, CapabilityDownload)
}

// Root returns the registered root id.
func (m *manager) Root(id RootID) (*Root, error) {
	r, ok := m.reg.Load().byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownRoot, id)
	}
	return r, nil
}

// Roots lists the roots matching filter, ordered by ID. Readable without an
// Owner keeps every root; with an Owner it keeps the roots that owner may read.
func (m *manager) Roots(f RootFilter) []*Root {
	reg := m.reg.Load()
	out := make([]*Root, 0, len(reg.order))
	for _, id := range reg.order {
		r := reg.byID[id]
		if f.Role != RoleInvalid && r.spec.Role != f.Role {
			continue
		}
		if f.Owner != "" {
			if f.Readable && !m.canRead(r, f.Owner) {
				continue
			}
			if !f.Readable && r.spec.Owner != f.Owner {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// need is the root p names, usable for work: registered and not disabled.
func (m *manager) need(p Path) (*Root, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	r, err := m.Root(p.Root)
	if err != nil {
		return nil, err
	}
	if err := r.usable(); err != nil {
		return nil, err
	}
	return r, nil
}

// slotPool bounds concurrent transfers to core.library.transfer.workers. A pool
// is replaced, never resized, so a transfer releases the pool it took from.
type slotPool struct{ ch chan struct{} }

func newSlotPool(n int) *slotPool {
	n = min(max(n, 1), maxTransferWorkers)
	return &slotPool{ch: make(chan struct{}, n)}
}

func (p *slotPool) acquire(ctx context.Context) error {
	select {
	case p.ch <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *slotPool) release() { <-p.ch }

// lockPaths takes the striped lock of every path in sorted order and returns
// the release function.
func (m *manager) lockPaths(paths ...Path) (unlock func()) {
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		keys = append(keys, p.String())
	}
	slices.Sort(keys)
	var unlocks []func()
	var last string
	for i, k := range keys {
		if i > 0 && k == last {
			continue
		}
		last = k
		unlocks = append(unlocks, m.locks.Lock(k))
	}
	return func() {
		for _, unlock := range slices.Backward(unlocks) {
			unlock()
		}
	}
}

// Bind reloads m whenever the config section behind h changes, so a change made
// through config.Store.Apply reaches the registry without a restart (05-library
// 5L.4). It returns the function that stops the subscription. The handle's
// value is not read: Reload reads the effective values of the core.library.*
// keys from the manager's ConfigSource, so any section shape that holds them
// (the kernel's core.library section) works.
func Bind[T any](m Manager, h *config.Handle[T]) (stop func(), err error) {
	r, ok := m.(Reloader)
	if !ok {
		return nil, errors.New("library: Bind needs a Manager that implements Reloader")
	}
	return h.OnChange(func(ctx context.Context, _, _ *T) {
		if err := r.Reload(ctx); err != nil {
			if lg, ok := m.(interface{ logger() *slog.Logger }); ok {
				lg.logger().Error("library: reload after a config change failed", "err", err)
			}
		}
	}), nil
}

func (m *manager) logger() *slog.Logger { return m.log }
