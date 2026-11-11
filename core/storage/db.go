// file: core/storage/db.go
// version: 1.5.0
// guid: 30500128-a267-4af7-ba39-fae150d4e708
// last-edited: 2026-11-10

package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
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
	prefix     []byte      // keyspace prefix ("m/<id>/" or "c/<sub>/"); nil without Spec.Keyspace
	serialized []span      // absolute Serialized ranges of the keyspace
	data       []*DataStep // the owner's data migrations (OpenOptions.DataSteps), in declaration order
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

	// violations counts out-of-claim accesses by owner, op class and mode
	// (violation); the kernel serves them as
	// <namespace>_storage_claim_violations_total (§5S.8, §5S.9.4 step 2).
	// violationKeys holds the first few distinct keys of each series
	// (ClaimViolationKeys), so a count can be traced to the claim that made
	// it even when the caller swallowed the error.
	violationsMu  sync.Mutex
	violations    map[ClaimViolation]uint64
	violationKeys map[ClaimViolation][]violationKey

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
// storage's own owner is always present. The DAG's shape is Open's check,
// once the data steps hang on their owners (attachDataSteps): a versioned
// step's After may name a data step, which the specs alone do not list.
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
		return nil, false, fmt.Errorf("storage: %s %s in a %s claim of %s: %w", op, s.db.keyRef(key), iv.mode, s.owner, ErrClaimMode)
	}
	return slices.Clone(key), iv.serialized, nil
}

// violation is an out-of-claim access: refused (strict, the default) or, with
// the emergency dial core.storage.claims.mode=shadow, logged and allowed,
// persisting nothing (§5S.0 D-S2). Keys under c/ and m/ are never reachable
// through a legacy range, shadow or not.
//
// Shadow rescues point accesses only (Get, Set, Delete, a batch's ops).
// It does not widen a scan: an iterator stays clipped to the handle's
// ownedSpans, so a family the claims miss is invisible to every scan in
// either mode, and an op that lists it sees nothing rather than an error.
//
// The shadow Warn is rate-limited per series: it fires on the 1st, 2nd,
// 4th, 8th, ... access (a power of two) and carries the series count, so a
// hot unclaimed family cannot flood the log. The counter and
// ClaimViolationKeys see every access. Neither the Warn nor the error carries
// any of the key's bytes, only keyRef's claim name and length.
func (db *DB) violation(owner Owner, op opKind, key []byte) error {
	reserved := bytes.HasPrefix(key, []byte("c/")) || bytes.HasPrefix(key, []byte("m/"))
	v := ClaimViolation{Owner: owner, Op: "write", Mode: "refused"}
	if op == opRead {
		v.Op = "read"
	}
	if !db.set.shadow || reserved {
		db.countViolation(v, key)
		return fmt.Errorf("storage: %s %s by %s: %w", op, db.keyRef(key), owner, ErrOutsideKeyspace)
	}
	v.Mode = "shadow"
	if n := db.countViolation(v, key); n&(n-1) == 0 {
		db.log.Warn("storage: out-of-claim access allowed by core.storage.claims.mode=shadow",
			"owner", string(owner), "op", op.String(), "key", db.keyRef(key), "count", n)
	}
	return nil
}

// keyRef describes key in an error or a log line without any of its bytes. A
// key can carry a secret (books' session rows are sess:<bearer token>) and
// storage's errors and Warns reach logs, so no message in this package carries
// a data key, any part of one, or anything computed from its bytes (R1-23
// review SF1; CodeQL go/clear-text-logging and go/weak-sensitive-data-hashing).
// It names the declared claim that holds the key, by longest match
// (`key under claim "joker:"`, `claimed key "joker_counter"`): strings from an
// owner's spec, chosen by comparing the key and never cut from it. A key under
// the reserved roots is named by its root, and a key in no claim only as
// "unclaimed key". Then comes the key's length: `key under claim "sess:" len=21`.
// Two keys can share a description; where a report lists distinct keys,
// numberShared keeps them apart.
func (db *DB) keyRef(key []byte) string {
	where := "unclaimed key"
	switch {
	case bytes.HasPrefix(key, []byte("c/")):
		where = `key under reserved "c/"`
	case bytes.HasPrefix(key, []byte("m/")):
		where = `key under reserved "m/"`
	default:
		if c := db.claims.claimOf(key); c != "" {
			where = c
		}
	}
	return fmt.Sprintf("%s len=%d", where, len(key))
}

// numberShared returns descs with an ordinal (#1, #2, ...) appended to each
// description more than one entry shares, numbered in order. Each entry
// describes a distinct key, and keyRef's description (a claim and a length)
// can be the same for two keys, so the ordinal is what tells them apart.
func numberShared(descs []string) []string {
	shared := make(map[string]int, len(descs))
	for _, d := range descs {
		shared[d]++
	}
	next := make(map[string]int, len(descs))
	out := make([]string, len(descs))
	for i, d := range descs {
		if shared[d] > 1 {
			next[d]++
			d = fmt.Sprintf("%s #%d", d, next[d])
		}
		out[i] = d
	}
	return out
}

// ClaimViolation is one series of <namespace>_storage_claim_violations_total
// (§5S.8): the owner whose handle made an out-of-claim access, its op class
// ("read" or "write") and what storage did ("refused", or "shadow" when the
// dial core.storage.claims.mode=shadow allowed it).
type ClaimViolation struct {
	Owner Owner
	Op    string
	Mode  string
}

// maxViolationKeys is how many distinct keys ClaimViolationKeys keeps per
// series.
const maxViolationKeys = 4

// violationKey is one distinct key of a violation series and how many of the
// series' accesses it made. raw is the key itself, held only to tell distinct
// keys apart: it is never logged, printed or returned. ClaimViolationKeys
// reports desc (keyRef's description) instead.
type violationKey struct {
	raw  string
	desc string
	n    uint64
}

// countViolation counts one access of series v at key and returns the
// series' new count.
func (db *DB) countViolation(v ClaimViolation, key []byte) uint64 {
	db.violationsMu.Lock()
	defer db.violationsMu.Unlock()
	if db.violations == nil {
		db.violations = make(map[ClaimViolation]uint64)
		db.violationKeys = make(map[ClaimViolation][]violationKey)
	}
	db.violations[v]++
	ks := db.violationKeys[v]
	if i := slices.IndexFunc(ks, func(k violationKey) bool { return k.raw == string(key) }); i >= 0 {
		ks[i].n++
	} else if len(ks) < maxViolationKeys {
		db.violationKeys[v] = append(ks, violationKey{raw: string(key), desc: db.keyRef(key), n: 1})
	}
	return db.violations[v]
}

// ClaimViolations is every out-of-claim access counted since the store
// opened, by series. A series appears from its first access.
func (db *DB) ClaimViolations() map[ClaimViolation]uint64 {
	db.violationsMu.Lock()
	defer db.violationsMu.Unlock()
	return maps.Clone(db.violations)
}

// ClaimViolationKeys describes the first few distinct keys (at most
// maxViolationKeys) of each series ClaimViolations counts, in the order they
// were first seen: keyRef's description, an ordinal when two of the keys
// share one, and how many of the series' accesses the key made, as in
// `unclaimed key len=6 #2 x3`. It never carries a key's bytes. Accesses by
// keys past the first few count in the series but in no entry here.
func (db *DB) ClaimViolationKeys() map[ClaimViolation][]string {
	db.violationsMu.Lock()
	defer db.violationsMu.Unlock()
	out := make(map[ClaimViolation][]string, len(db.violationKeys))
	for v, ks := range db.violationKeys {
		descs := make([]string, len(ks))
		for i, k := range ks {
			descs[i] = k.desc
		}
		descs = numberShared(descs)
		for i, k := range ks {
			descs[i] = fmt.Sprintf("%s x%d", descs[i], k.n)
		}
		out[v] = descs
	}
	return out
}

// ShadowViolations is how many out-of-claim accesses the shadow dial has
// allowed since the store opened, over every owner and op; 0 under strict
// claims, which refuse them instead.
func (db *DB) ShadowViolations() uint64 {
	var n uint64
	for v, c := range db.ClaimViolations() {
		if v.Mode == "shadow" {
			n += c
		}
	}
	return n
}

// ClaimsShadow reports whether the store runs the shadow dial.
func (db *DB) ClaimsShadow() bool { return db.set.shadow }

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
