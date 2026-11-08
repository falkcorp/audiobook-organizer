// file: core/storage/db.go
// version: 1.0.1
// guid: 30500128-a267-4af7-ba39-fae150d4e708
// last-edited: 2026-11-08

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"regexp"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	cpebble "github.com/cockroachdb/pebble/v2"

	ipebble "github.com/falkcorp/mediacore/internal/pebble"
	"github.com/falkcorp/mediacore/storage/keys"
)

// stripeCount is the size of the commit stripe table (§5S.3.2).
const stripeCount = 4096

// storageOwner is the owner of storage's own bookkeeping keyspace c/storage/.
const storageOwner Owner = "core/storage"

// coreLevelOwner is the one level owner of every core subsystem (§5S.3.7).
const coreLevelOwner Owner = "core"

// coreOrder is the kernel's fixed core subsystem order, the DAG's tie-break
// (§5S.3.5 "Order"). A core owner absent from it may declare no step.
func coreOrder() []string {
	return []string{"storage", "secrets", "config", "log", "auth", "rbac", "audit", "tasks", "outbox", "index",
		"library", "providerhttp", "integrations", "ai", "download", "indexer", "notify", "acquire", "backup",
		"updater", "health", "arrfacade"}
}

// moduleID is 03 §3.6's module ID grammar, which a module owner follows.
func moduleID() *regexp.Regexp { return regexp.MustCompile(`^[a-z][a-z0-9]{1,15}$`) }

// ownerInfo is one registered owner.
type ownerInfo struct {
	owner      Owner
	spec       Spec
	enabled    bool
	core       bool
	prefix     []byte // keyspace prefix ("m/<id>/" or "c/<sub>/"); nil without Spec.Keyspace
	serialized []span // absolute Serialized ranges of the keyspace
}

// levelOwner is the owner a level stamp is kept under: "core" for every core
// subsystem, the module ID for a module.
func (o *ownerInfo) levelOwner() Owner {
	if o.core {
		return coreLevelOwner
	}
	return o.owner
}

// isSerialized reports whether an absolute keyspace key lies in a Serialized
// range of its owner.
func (o *ownerInfo) isSerialized(abs []byte) bool {
	for _, s := range o.serialized {
		if s.contains(abs) {
			return true
		}
	}
	return false
}

// settings are the engine-independent limits of one DB.
type settings struct {
	maxBatchBytes int64
	seqLease      uint64
	migMaxBytes   int64
	migMaxOps     int
	closeTimeout  time.Duration
	shadow        bool
}

// DB is the install's single system-of-record store. Only the kernel holds a
// *DB; everyone else holds Keyspaces and LegacyRanges cut from it.
type DB struct {
	eng      *ipebble.Engine
	log      *slog.Logger
	clock    func() time.Time
	readOnly bool
	set      settings

	owners map[Owner]*ownerInfo
	claims *claimIndex

	stripes [stripeCount]sync.Mutex

	seqMu sync.Mutex
	seqs  map[string]*Sequence

	closed atomic.Bool

	// boot is the boot state Open read and Prepare and Migrate advance; nil
	// for a read-only DB.
	boot *bootState

	// hooks are test seams; zero outside tests.
	hooks testHooks
}

// testHooks are the seams the crash and ordering tests use.
type testHooks struct {
	// stripes is told how many stripes each commit took.
	stripes func(n int)
	// beforeStampCommit runs inside a level step after the sidecars are
	// written and before the stamp batch commits.
	beforeStampCommit func() error
	// beforeComplete runs in step 9 after the batch and the sidecars,
	// before the attempt's checkpoint is completed.
	beforeComplete func() error
	// seqLoaded runs in Sequence.Next after it loaded the lease and before
	// it takes an ID from it.
	seqLoaded func()
	// beforeApply runs inside Commit after the preconditions passed, under
	// the stripes, before the batch is applied.
	beforeApply func()
}

// keyspacePrefix is an owner's keyspace prefix: "m/<id>/" for a module,
// "c/<sub>/" for "core/<sub>".
func keyspacePrefix(o Owner) ([]byte, bool, error) {
	s := string(o)
	if sub, ok := cutPrefix(s, "core/"); ok {
		if err := keys.ValidName(sub); err != nil {
			return nil, true, fmt.Errorf("core owner %q: %w", o, err)
		}
		return []byte("c/" + sub + "/"), true, nil
	}
	if !moduleID().MatchString(s) {
		return nil, false, fmt.Errorf("owner %q is neither a module ID ([a-z][a-z0-9]{1,15}) nor core/<subsystem>", o)
	}
	return []byte("m/" + s + "/"), false, nil
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return "", false
}

// coreSub is a core owner's subsystem name.
func coreSub(o Owner) string {
	s, _ := cutPrefix(string(o), "core/")
	return s
}

// register builds the owner table and the claim index from the build's
// specs, and validates them (§5S.3.4 items 1-3, §5S.3.5 registration rules).
// storage's own owner is always present.
func register(specs []OwnerSpec) (map[Owner]*ownerInfo, *claimIndex, []claimRef, error) {
	owners := make(map[Owner]*ownerInfo, len(specs)+1)
	var probs []string
	var refs []claimRef
	all := append([]OwnerSpec{{Owner: storageOwner, Spec: Spec{Keyspace: true}, Enabled: true}}, specs...)
	for i, os := range all {
		if i > 0 && os.Owner == storageOwner {
			probs = append(probs, "owner core/storage is storage's own and cannot be declared")
			continue
		}
		if _, dup := owners[os.Owner]; dup {
			probs = append(probs, fmt.Sprintf("owner %q declared twice (item 1)", os.Owner))
			continue
		}
		prefix, core, err := keyspacePrefix(os.Owner)
		if err != nil {
			probs = append(probs, err.Error())
			continue
		}
		info := &ownerInfo{owner: os.Owner, spec: os.Spec, enabled: os.Enabled, core: core}
		if os.Spec.Keyspace {
			info.prefix = prefix
			for _, sub := range os.Spec.Serialized {
				p := append(slices.Clone(prefix), sub...)
				info.serialized = append(info.serialized, span{start: p, end: keys.PrefixEnd(p)})
			}
		} else if len(os.Spec.Serialized) > 0 {
			probs = append(probs, fmt.Sprintf("%s: Serialized without Keyspace", os.Owner))
		}
		if core && len(os.Spec.Claims) > 0 {
			probs = append(probs, fmt.Sprintf("%s: a core subsystem declares no legacy claims (it never receives a LegacyRange)", os.Owner))
		}
		for _, c := range os.Spec.Claims {
			r := claimRef{owner: os.Owner, c: c, sp: claimSpan(c)}
			r.depth = len(c.Prefix)
			if c.Key != "" {
				r.depth = len(c.Key) + 1
			}
			refs = append(refs, r)
		}
		probs = append(probs, specProblems(info)...)
		owners[os.Owner] = info
	}
	claimProbs := validateClaims(refs)
	if len(probs) > 0 {
		return nil, nil, nil, &SpecError{Problems: probs}
	}
	if len(claimProbs) > 0 {
		return nil, nil, nil, &ClaimConflictError{Problems: claimProbs}
	}
	if err := checkDAGShape(owners); err != nil {
		return nil, nil, nil, err
	}
	return owners, buildClaimIndex(refs), refs, nil
}

// handleOf is the scope of a handle: who it belongs to and how a key maps.
type handleOf struct {
	db     *DB
	owner  Owner
	legacy bool
	prefix []byte // keyspace prefix; nil for a legacy range
	tx     *MigrationTx
	ro     bool
}

func (k Keyspace) scope() handleOf {
	return handleOf{db: k.db, owner: k.owner, prefix: k.prefix, tx: k.tx, ro: k.ro}
}

func (r LegacyRange) scope() handleOf {
	return handleOf{db: r.db, owner: r.owner, legacy: true, tx: r.tx, ro: r.ro}
}

func scopeOf(h Handle) (handleOf, bool) {
	switch v := h.(type) {
	case Keyspace:
		return v.scope(), v.db != nil
	case LegacyRange:
		return v.scope(), v.db != nil
	}
	return handleOf{}, false
}

// same reports whether two scopes are the same capability.
func (s handleOf) same(o handleOf) bool {
	return s.db == o.db && s.owner == o.owner && s.legacy == o.legacy && bytes.Equal(s.prefix, o.prefix)
}

// check validates the scope before an operation.
func (s handleOf) check() error {
	if s.db == nil {
		return ErrInvalidKeyspace
	}
	if s.db.closed.Load() {
		return ErrClosed
	}
	return nil
}

// absolute maps a key of the handle to its absolute key and checks it for
// op: a keyspace key is relative and always lands under the prefix; a legacy
// key is absolute and must resolve, by longest match, to one of the owner's
// claims whose mode admits op. serialized reports a Serialized range.
func (s handleOf) absolute(key []byte, op opKind) (abs []byte, serialized bool, err error) {
	if !s.legacy {
		abs = append(slices.Clone(s.prefix), key...)
		info := s.db.owners[s.owner]
		return abs, info != nil && info.isSerialized(abs), nil
	}
	iv, ok := s.db.claims.resolve(key)
	if !ok || iv.owner != s.owner {
		if err := s.db.violation(s.owner, op, key); err != nil {
			return nil, false, err
		}
		return slices.Clone(key), false, nil
	}
	if !iv.mode.allows(op) {
		return nil, false, fmt.Errorf("storage: %s %q in a %s claim of %s: %w", op, key, iv.mode, s.owner, ErrClaimMode)
	}
	return slices.Clone(key), iv.serialized, nil
}

// violation is an out-of-claim access: refused (strict, the default) or, with
// the emergency dial core.storage.claims.mode=shadow, logged and allowed,
// persisting nothing (§5S.0 D-S2). Keys under c/ and m/ are never reachable
// through a legacy range, shadow or not.
func (db *DB) violation(owner Owner, op opKind, key []byte) error {
	reserved := bytes.HasPrefix(key, []byte("c/")) || bytes.HasPrefix(key, []byte("m/"))
	if !db.set.shadow || reserved {
		return fmt.Errorf("storage: %s %q by %s: %w", op, key, owner, ErrOutsideKeyspace)
	}
	db.log.Warn("storage: out-of-claim access allowed by core.storage.claims.mode=shadow",
		"owner", string(owner), "op", op.String(), "key", fmt.Sprintf("%q", key))
	return nil
}

// ownedSpans are the absolute ranges a legacy handle may read, clamped to
// [lo, hi); for a keyspace the one range of its prefix.
func (s handleOf) ownedSpans() []span {
	if !s.legacy {
		return []span{{start: s.prefix, end: keys.PrefixEnd(s.prefix)}}
	}
	ivs := s.db.claims.byOwner[s.owner]
	out := make([]span, 0, len(ivs))
	for _, iv := range ivs {
		if n := len(out); n > 0 && bytes.Equal(out[n-1].end, iv.start) {
			out[n-1].end = iv.end
			continue
		}
		out = append(out, iv.span)
	}
	return out
}

// reader is the read surface of the engine, a snapshot and an indexed batch.
type reader interface {
	Get(key []byte) ([]byte, io.Closer, error)
	NewIter(o *cpebble.IterOptions) (*cpebble.Iterator, error)
}

// lease takes an engine lease or reports the DB closed.
func (db *DB) lease() (func(), error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	release, err := db.eng.Lease()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrClosed, err)
	}
	return release, nil
}

// getCommitted reads one absolute key's committed value. ok is false when
// absent.
func (db *DB) getCommitted(abs []byte) (value []byte, ok bool, err error) {
	release, err := db.lease()
	if err != nil {
		return nil, false, err
	}
	defer release()
	return getFrom(db.eng.DB(), abs)
}

func getFrom(r reader, abs []byte) ([]byte, bool, error) {
	v, closer, err := r.Get(abs)
	if errors.Is(err, cpebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("storage: get: %w", err)
	}
	out := slices.Clone(v)
	if out == nil {
		out = []byte{}
	}
	return out, true, closer.Close()
}

// Keyspace issues owner's keyspace handle, for the kernel. ok is false when
// the owner is not registered or declared no keyspace.
func (db *DB) Keyspace(owner Owner) (Keyspace, bool) {
	info := db.owners[owner]
	if info == nil || info.prefix == nil {
		return Keyspace{}, false
	}
	return Keyspace{db: db, owner: owner, prefix: info.prefix, ro: db.readOnly}, true
}

// Legacy issues owner's legacy range, for the kernel. ok is false when the
// owner declared no claims (the handle is then the zero value).
func (db *DB) Legacy(owner Owner) (LegacyRange, bool) {
	info := db.owners[owner]
	if info == nil || len(info.spec.Claims) == 0 {
		return LegacyRange{}, false
	}
	return LegacyRange{db: db, owner: owner, ro: db.readOnly}, true
}

// Close drains the engine's leases up to core.storage.close_timeout, syncs
// the WAL and closes the engine (§5S.4 "App.Run ends"). Every handle fails
// with ErrClosed afterwards. On a drain timeout the engine stays open and
// the error says so; Close may be called again.
func (db *DB) Close() error {
	db.closed.Store(true)
	return db.labelled(labelClose, func() error { return db.eng.Close(db.set.closeTimeout) })
}

// commitStripes locks the stripes of the absolute keys (or all of them) in
// ascending order and returns the unlock.
func (db *DB) commitStripes(keysToLock [][]byte, all bool) func() {
	var idx []int
	if all {
		idx = make([]int, stripeCount)
		for i := range idx {
			idx[i] = i
		}
	} else {
		for _, k := range keysToLock {
			idx = append(idx, stripeOf(k))
		}
		slices.Sort(idx)
		idx = slices.Compact(idx)
	}
	if db.hooks.stripes != nil {
		db.hooks.stripes(len(idx))
	}
	for _, i := range idx {
		db.stripes[i].Lock()
	}
	return func() {
		for _, i := range slices.Backward(idx) {
			db.stripes[i].Unlock()
		}
	}
}

// stripeOf hashes an absolute key to its stripe (FNV-1a).
func stripeOf(k []byte) int {
	h := uint32(2166136261)
	for _, c := range k {
		h ^= uint32(c)
		h *= 16777619
	}
	return int(h % stripeCount)
}

// RetryOnConflict re-runs fn (which builds and commits a fresh batch) while it
// returns ErrConflict, up to attempts, with jittered backoff; ctx-aware.
func RetryOnConflict(ctx context.Context, attempts int, fn func(ctx context.Context) error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := range attempts {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if err = fn(ctx); !errors.Is(err, ErrConflict) {
			return err
		}
		if i == attempts-1 {
			break
		}
		// Jittered backoff: 1ms << i, capped at 64ms, plus up to the same
		// again, so two writers that conflicted do not retry in step.
		base := time.Millisecond << min(i, 6)
		t := time.NewTimer(base + rand.N(base))
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	return err
}
