// file: internal/decisions/store.go
// version: 1.2.1
// guid: 0f7b3a91-d542-4e86-a1c3-78e9b60d2f15
// last-edited: 2026-10-21

package decisions

import (
	"context"
	crand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/keyedmutex"
)

// KV is the slice of the store this package needs. It is deliberately narrow:
// the decision store has no business with books, files or anything else on
// database.Store. *database.PebbleStore satisfies it (its RawKVStore reads plus
// database.RawBatchApplier and database.RawSnapshotter, which are deliberately
// not on RawKVStore or Store). It omits the unbounded
// ScanPrefix: decisions grow with every reject for the life of the library.
type KV interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
	// ApplyRawBatch must be atomic: a record, its verdict and its veto rows
	// commit together or not at all.
	ApplyRawBatch(writes []database.RawWrite) error
	// NewRawSnapshot opens a point-in-time view: Load reads its several scans
	// through one, so they agree with each other however many records commit
	// meanwhile. database.RawSnapshotter, which *PebbleStore implements.
	NewRawSnapshot() (database.RawSnapshot, error)
	// LockIdentity returns a pointer that identifies the engine behind this
	// KV, the same pointer for every wrapper of one engine and a different one
	// for each engine. Record's per-subject lock is keyed by it, so two Stores
	// over two wrappers of one store still exclude each other. *PebbleStore
	// returns its engine handle; a wrapper that embeds a KV inherits the
	// method and must not override it.
	LockIdentity() any
}

var (
	// ErrNotFound is returned by Get for an unknown record id.
	ErrNotFound = errors.New("decisions: record not found")
	// ErrNotRejected is returned for an unreject or reopen of a proposal
	// that has no active reject.
	ErrNotRejected = errors.New("decisions: proposal has no active reject")
	// ErrNotReopenable is returned for a reopen of a reject that cannot be
	// reopened: a sticky reject (no material hash, so no changed data to tell
	// apart) or a proposal whose material hash is one that was rejected.
	ErrNotReopenable = errors.New("decisions: reject cannot be reopened")
	// ErrHumanRejectLive is returned when a non-human decision tries to
	// replace a live reject a human made. Only a human replaces a human.
	ErrHumanRejectLive = errors.New("decisions: a human reject is live; only a human can replace it")
	// ErrCorrupt is returned when the log contradicts itself (a verdict
	// pointing at a record that is not there). The caller must fail closed.
	ErrCorrupt = errors.New("decisions: store is inconsistent")
)

const scanPage = 500

// subjectLocks serialises Record per (store identity, subject). It is
// package-level on purpose: the lock must protect the KEYS, and two Store
// values over one engine write the same keys, so a lock inside each Store let
// them interleave the verdict read-modify-write and fork the SupersedesID
// chain.
var subjectLocks keyedmutex.Mutex

// lockScope names the engine behind kv for lock purposes: its LockIdentity,
// which a wrapper inherits from the store it embeds. It is formatted with %p,
// so an identity that is not a pointer yields one shared fixed scope: the
// locks then over-serialise (every such KV shares them), they never miss.
func lockScope(kv KV) string { return fmt.Sprintf("%p", kv.LockIdentity()) }

// Store reads and writes decisions. Any number of Stores may share one engine,
// through the same KV value or through different wrappers of it: the
// per-subject lock is keyed by KV.LockIdentity, not by the KV value.
type Store struct {
	kv  KV
	now func() time.Time

	lockScope string

	idMu     sync.Mutex
	entropy  *ulid.MonotonicEntropy
	lastIDms uint64
}

// Option configures a Store.
type Option func(*Store)

// WithClock sets the clock behind DecidedAt and record ids (tests).
func WithClock(now func() time.Time) Option { return func(s *Store) { s.now = now } }

// WithEntropy sets the ULID entropy source (tests); crypto/rand by default.
func WithEntropy(r io.Reader) Option {
	return func(s *Store) { s.entropy = ulid.Monotonic(r, 0) }
}

// New returns a Store over kv.
func New(kv KV, opts ...Option) *Store {
	s := &Store{kv: kv, now: time.Now, entropy: ulid.Monotonic(crand.Reader, 0), lockScope: lockScope(kv)}
	for _, o := range opts {
		o(s)
	}
	return s
}

// newID returns a record id that sorts after every id this store issued
// before, even when the clock stands still or steps back.
func (s *Store) newID(at time.Time) (string, error) {
	s.idMu.Lock()
	defer s.idMu.Unlock()
	ms := ulid.Timestamp(at)
	if ms < s.lastIDms {
		ms = s.lastIDms
	}
	id, err := ulid.New(ms, s.entropy)
	if err != nil {
		return "", fmt.Errorf("decisions: new id: %w", err)
	}
	s.lastIDms = ms
	return id.String(), nil
}

func subjectLockKey(opKind, subjectKey string) string { return opKind + "\x00" + subjectKey }

// lockSubject takes the subject's lock on this store's KV.
func (s *Store) lockSubject(opKind, subjectKey string) func() {
	return subjectLocks.Lock(s.lockScope + "\x00" + subjectLockKey(opKind, subjectKey))
}

// Record appends one decision to the log and updates the verdict and indexes
// in one atomic commit. It returns the stored record.
//
//   - reject: the proposal becomes active (vetoed). A reject of two or more
//     entities also vetoes each pair among them, within Input.Family. A reject
//     of an already-rejected proposal replaces its edges and adds its material
//     hash to the rejected set. A non-human reject of a proposal a human
//     rejected is refused (ErrHumanRejectLive).
//   - unreject: human only. Lifts the veto, clears the rejected set and
//     deletes its edges. The record stays.
//   - reopened: the data changed (Input.MaterialHash is the new hash and must
//     differ from every rejected one; ErrNotReopenable otherwise, and for a
//     sticky reject). It allows NEW suggestions only: the rejected hashes stay
//     blocked and so do the edges.
//   - approve, applied, auto_applied, undone, skip: labels only. None vetoes
//     and none lifts a veto.
func (s *Store) Record(in Input) (*Record, error) {
	ids, err := in.validate()
	if err != nil {
		return nil, err
	}
	subject := subjectHash(in.OpKind, in.EntityType, ids, in.Scope) // ids are canonical
	unlock := s.lockSubject(in.OpKind, subject)
	defer unlock()

	prev, err := s.readVerdict(in.OpKind, subject)
	if err != nil {
		return nil, err
	}
	// The reject whose rows are live now, if any.
	var live *Record
	if prev.Live() {
		live, err = s.readRecord(prev.RejectID)
		if err != nil {
			return nil, fmt.Errorf("%w: verdict %s/%s names reject %q: %w", ErrCorrupt, in.OpKind, subject, prev.RejectID, err)
		}
	}
	if err := checkTransition(prev, live, &in); err != nil {
		return nil, fmt.Errorf("%w: %s %s", err, in.OpKind, subject)
	}

	at := s.now().UTC()
	id, err := s.newID(at)
	if err != nil {
		return nil, err
	}
	rec := Record{
		ID: id, SchemaVersion: SchemaVersion,
		OpKind: in.OpKind, OpVersion: in.OpVersion, RuleVersion: in.RuleVersion,
		SubjectKey: subject, Scope: in.Scope, MaterialHash: in.MaterialHash,
		EntityType: in.EntityType, EntityIDs: ids,
		PlanOpID: in.PlanOpID, PlanRowID: in.PlanRowID,
		Snapshot: in.Snapshot, Decision: in.Decision, Source: in.Source,
		ReasonCode: in.ReasonCode, ReasonText: in.ReasonText,
		ActorUserID: in.ActorUserID, ActorName: in.ActorName,
		DecidedAt: at,
	}
	verdict := Verdict{OpKind: in.OpKind, SubjectKey: subject, State: StateNone}
	if prev != nil {
		verdict = *prev
		rec.SupersedesID = prev.LatestID
	}
	verdict.LatestID = rec.ID

	var writes []database.RawWrite
	// dropLiveEdges deletes the veto rows of the reject that is live now, by
	// the shape that reject was written in.
	dropLiveEdges := func() error {
		if live == nil {
			return nil
		}
		keys, err := indexKeys(live)
		if err != nil {
			return err
		}
		for _, k := range keys {
			writes = append(writes, database.RawWrite{Key: k, Delete: true})
		}
		return nil
	}

	switch in.Decision {
	case DecisionReject:
		if len(ids) >= 2 {
			rec.Family = in.Family
			rec.IndexShape = indexShapeFor(len(ids))
		}
		if err := dropLiveEdges(); err != nil {
			return nil, err
		}
		verdict.State, verdict.RejectID = StateActive, rec.ID
		verdict.RejectedHashes = rejectedAfterReject(prev, in.MaterialHash)
		keys, err := indexKeys(&rec)
		if err != nil {
			return nil, err
		}
		for _, k := range keys {
			writes = append(writes, database.RawWrite{Key: k})
		}
		activeRaw, err := json.Marshal(ActiveRow{RejectedHashes: verdict.RejectedHashes})
		if err != nil {
			return nil, fmt.Errorf("decisions: encode active row: %w", err)
		}
		writes = append(writes, database.RawWrite{Key: activeKey(in.OpKind, subject), Value: activeRaw})
	case DecisionUnreject:
		if err := dropLiveEdges(); err != nil {
			return nil, err
		}
		verdict.State, verdict.RejectID, verdict.RejectedHashes = StateUnrejected, "", nil
		writes = append(writes, database.RawWrite{Key: activeKey(in.OpKind, subject), Delete: true})
	case DecisionReopened:
		// No edge, no hash and no active-row change: the reject stays in force
		// for the value it rejected.
		verdict.State = StateReopened
	}

	recRaw, err := json.Marshal(&rec)
	if err != nil {
		return nil, fmt.Errorf("decisions: encode record: %w", err)
	}
	verdictRaw, err := json.Marshal(&verdict)
	if err != nil {
		return nil, fmt.Errorf("decisions: encode verdict: %w", err)
	}
	writes = append(writes,
		database.RawWrite{Key: recordKey(rec.ID), Value: recRaw},
		database.RawWrite{Key: verdictKey(in.OpKind, subject), Value: verdictRaw},
		database.RawWrite{Key: opKey(in.OpKind, rec.ID)},
	)
	for _, eid := range ids {
		writes = append(writes, database.RawWrite{Key: entityKey(in.EntityType, eid, rec.ID)})
	}
	if err := s.kv.ApplyRawBatch(writes); err != nil {
		return nil, fmt.Errorf("decisions: commit %s: %w", rec.ID, err)
	}
	return &rec, nil
}

// checkTransition refuses a decision the current state does not allow.
func checkTransition(prev *Verdict, live *Record, in *Input) error {
	switch in.Decision {
	case DecisionReject:
		// A human's reject is replaced only by a human. Letting an auto or
		// llm reject take it over, even in the same family, would hand the
		// record to a source that may then be replaced by another, so the
		// human's family could be rewritten without a human.
		if live != nil && live.Source == SourceHuman && in.Source != SourceHuman {
			return ErrHumanRejectLive
		}
	case DecisionUnreject:
		if !prev.Live() {
			return ErrNotRejected
		}
	case DecisionReopened:
		if !prev.Live() {
			return ErrNotRejected
		}
		if len(prev.RejectedHashes) == 0 {
			return fmt.Errorf("%w: the reject is sticky (it recorded no material hash)", ErrNotReopenable)
		}
		if slices.Contains(prev.RejectedHashes, in.MaterialHash) {
			return fmt.Errorf("%w: the material hash is one that was rejected", ErrNotReopenable)
		}
	}
	return nil
}

// rejectedAfterReject is the rejected-hash set once a reject with material
// hash h lands on prev. A sticky reject (h == "") blocks whatever the data is
// and an existing sticky verdict stays sticky; otherwise h joins the set of a
// live verdict, or starts a new one.
func rejectedAfterReject(prev *Verdict, h string) []string {
	if h == "" {
		return nil
	}
	if !prev.Live() {
		return []string{h}
	}
	if len(prev.RejectedHashes) == 0 {
		return nil
	}
	out := append(slices.Clone(prev.RejectedHashes), h)
	slices.Sort(out)
	return slices.Compact(out)
}

// indexKeys are the veto rows a reject record owns, by the shape it persisted:
// pair edges (IndexEdges) or one membership row per member (IndexMembers). The
// same function names the rows to write and the rows to delete, so they can
// never drift, and it reads the record's own shape, never MaxEdgeSet. A reject
// of two or more entities with no recognisable shape is corrupt: deleting by a
// guess would strand veto rows that nothing could ever remove.
func indexKeys(rec *Record) ([]string, error) {
	n := len(rec.EntityIDs)
	if n < 2 {
		return nil, nil
	}
	if rec.Family == "" {
		return nil, fmt.Errorf("%w: reject %s of %d entities has no family", ErrCorrupt, rec.ID, n)
	}
	switch rec.IndexShape {
	case IndexMembers:
		keys := make([]string, 0, n)
		for _, id := range rec.EntityIDs {
			keys = append(keys, memberKey(rec.EntityType, id, rec.Family, rec.ID))
		}
		return keys, nil
	case IndexEdges:
		keys := make([]string, 0, n*(n-1)/2)
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				lo, hi := orderPair(rec.EntityIDs[i], rec.EntityIDs[j])
				keys = append(keys, edgeKey(rec.EntityType, lo, hi, rec.Family, rec.ID))
			}
		}
		return keys, nil
	}
	return nil, fmt.Errorf("%w: reject %s of %d entities has index shape %q", ErrCorrupt, rec.ID, n, rec.IndexShape)
}

// Get returns one record. The id's case is canonicalised (a ULID).
func (s *Store) Get(id string) (*Record, error) {
	id, err := CanonicalRecordID(id)
	if err != nil {
		return nil, err
	}
	return s.readRecord(id)
}

func (s *Store) readRecord(id string) (*Record, error) {
	raw, err := s.kv.GetRaw(recordKey(id))
	if err != nil {
		return nil, fmt.Errorf("decisions: read record %s: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("%w: decode record %s: %w", ErrCorrupt, id, err)
	}
	return &rec, nil
}

// Verdict returns the current state of a proposal, or nil when nothing was
// ever decided about it. subjectKey is canonicalised (lower-case hex) and
// opKind must be a valid key component (not empty, no control character): a
// malformed one is an error (ErrInvalidComponent), never "nothing decided",
// which would read as "not rejected". Lookup and History go through here.
func (s *Store) Verdict(opKind, subjectKey string) (*Verdict, error) {
	if err := checkComponent("op_kind", opKind); err != nil {
		return nil, err
	}
	subjectKey, err := CanonicalSubjectKey(subjectKey)
	if err != nil {
		return nil, err
	}
	return s.readVerdict(opKind, subjectKey)
}

func (s *Store) readVerdict(opKind, subjectKey string) (*Verdict, error) {
	raw, err := s.kv.GetRaw(verdictKey(opKind, subjectKey))
	if err != nil {
		return nil, fmt.Errorf("decisions: read verdict: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: decode verdict %s/%s: %w", ErrCorrupt, opKind, subjectKey, err)
	}
	return &v, nil
}

// Status is what a producer asks before proposing something again.
type Status struct {
	Verdict *Verdict
	// State is the effective state for the proposal as asked: StateActive
	// when a live reject blocks it, StateReopened when a reject is live but the
	// proposal's material hash was never rejected (a new suggestion), else the
	// stored state. Empty when nothing was ever decided about the proposal.
	State VerdictState
	// Blocked is true when the proposal must be skipped.
	Blocked bool
}

// Lookup finds a proposal by fingerprint. materialHash is MaterialHash of the
// proposal as it would be made now ("" for a sticky op). subjectKey is
// canonicalised; a malformed one is an error (never "not found", which would
// read as "not rejected").
//
// A live reject blocks the proposal when its material is any rejected one, so
// a reopen never lifts the block on the value that was rejected: if the data
// flips back, the proposal is blocked again.
func (s *Store) Lookup(opKind, subjectKey, materialHash string) (Status, error) {
	v, err := s.Verdict(opKind, subjectKey)
	if err != nil || v == nil {
		return Status{}, err
	}
	st := Status{Verdict: v, State: v.State}
	if v.Live() {
		if v.BlocksMaterial(materialHash) {
			st.State, st.Blocked = StateActive, true
		} else {
			st.State = StateReopened
		}
	}
	return st, nil
}

// History returns every record of a proposal, newest first, by following the
// supersedes chain from the verdict.
func (s *Store) History(opKind, subjectKey string) ([]Record, error) {
	v, err := s.Verdict(opKind, subjectKey)
	if err != nil || v == nil {
		return nil, err
	}
	var out []Record
	seen := map[string]bool{}
	for id := v.LatestID; id != ""; {
		if seen[id] {
			return nil, fmt.Errorf("%w: supersedes cycle at %s", ErrCorrupt, id)
		}
		seen[id] = true
		rec, err := s.readRecord(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
		id = rec.SupersedesID
	}
	return out, nil
}

// pager is the one method scanAll needs, so it reads a live KV or a snapshot.
type pager interface {
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
}

// scanAll pages through every pair under prefix in key order.
func scanAll(ctx context.Context, p pager, prefix string, fn func(database.KVPair) error) error {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pairs, next, err := p.ScanPrefixPage(prefix, after, scanPage)
		if err != nil {
			return fmt.Errorf("decisions: scan %s: %w", prefix, err)
		}
		for _, kv := range pairs {
			if err := fn(kv); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

func (s *Store) scanAll(ctx context.Context, prefix string, fn func(database.KVPair) error) error {
	return scanAll(ctx, s.kv, prefix, fn)
}

// Filter narrows an iteration.
type Filter struct {
	// OpKind, when set, visits only that kind (through its index).
	OpKind string
}

// Iterate visits records in ULID (decision time) order, for listing and
// JSONL export. fn returning an error stops the walk and returns that error.
//
// ULID order is the order ids were minted, which can differ slightly from
// commit order when several Store instances write to one KV: each Store keeps
// its own monotonic entropy and clock reading, and two records minted in the
// same millisecond by different Stores may commit in the opposite order. A
// subject's own history (History) is exact either way: it follows
// SupersedesID pointers, which the per-subject lock makes one linear chain, not
// id order.
func (s *Store) Iterate(ctx context.Context, f Filter, fn func(Record) error) error {
	if f.OpKind != "" {
		prefix := opPrefix(f.OpKind)
		return s.scanAll(ctx, prefix, func(p database.KVPair) error {
			rec, err := s.readRecord(strings.TrimPrefix(p.Key, prefix))
			if err != nil {
				return fmt.Errorf("%w: op index %s: %w", ErrCorrupt, p.Key, err)
			}
			return fn(*rec)
		})
	}
	return s.scanAll(ctx, prefixRecord, func(p database.KVPair) error {
		var rec Record
		if err := json.Unmarshal(p.Value, &rec); err != nil {
			return fmt.Errorf("%w: decode %s: %w", ErrCorrupt, p.Key, err)
		}
		return fn(rec)
	})
}

// ByEntity visits the records that name one entity, in decision order. The
// entity id is canonicalised (see CanonicalID).
func (s *Store) ByEntity(ctx context.Context, entityType, entityID string, fn func(Record) error) error {
	entityID, err := CanonicalID(entityID)
	if err != nil {
		return err
	}
	prefix := entityPrefix(entityType, entityID)
	return s.scanAll(ctx, prefix, func(p database.KVPair) error {
		rec, err := s.readRecord(strings.TrimPrefix(p.Key, prefix))
		if err != nil {
			return fmt.Errorf("%w: entity index %s: %w", ErrCorrupt, p.Key, err)
		}
		return fn(*rec)
	})
}

// checkQueryFamily refuses a family PairVetoed does not know. An unknown
// family is an error, never "not vetoed": a typo ("same-work") would otherwise
// read as no veto, the unsafe direction.
func checkQueryFamily(family string) error {
	if family == FamilyAny || slices.Contains(Families, family) {
		return nil
	}
	return invalid("unknown family %q, want one of %v or %q", family, Families, FamilyAny)
}

// PairVetoed reports whether an active reject vetoes joining a and b in the
// given family. Rows stored with FamilyAny veto every family; a query for
// FamilyAny asks whether the pair is vetoed in ANY family. It reads the store
// directly and does NO id resolution: a merged-away id is looked up as given
// (after canonicalisation: a ULID is upper-cased, an id with surrounding
// whitespace is an error), and an unknown family is an error. For a whole plan
// run load a Vetoes snapshot with an IDResolver instead.
func (s *Store) PairVetoed(entityType, a, b, family string) (bool, error) {
	if err := checkComponent("entity_type", entityType); err != nil {
		return false, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := checkQueryFamily(family); err != nil {
		return false, err
	}
	a, err := CanonicalID(a)
	if err != nil {
		return false, err
	}
	b, err = CanonicalID(b)
	if err != nil {
		return false, err
	}
	if a == b {
		return false, nil
	}
	ctx := context.Background()
	lo, hi := orderPair(a, b)
	errStop := errors.New("found")
	prefix := edgePairPrefix(entityType, lo, hi)
	err = s.scanAll(ctx, prefix, func(p database.KVPair) error {
		if f, _, ok := splitFamilyRecord(strings.TrimPrefix(p.Key, prefix)); ok && familyMatches(f, family) {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	mprefix := memberPrefix(entityType, a)
	err = s.scanAll(ctx, mprefix, func(p database.KVPair) error {
		f, rid, ok := splitFamilyRecord(strings.TrimPrefix(p.Key, mprefix))
		if !ok || !familyMatches(f, family) {
			return nil
		}
		raw, err := s.kv.GetRaw(memberKey(entityType, b, f, rid))
		if err != nil {
			return err
		}
		if raw != nil {
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		return true, nil
	}
	return false, err
}

// familyMatches reports whether a stored edge family answers a query: an
// exact match, a stored FamilyAny (vetoes every family), or a queried
// FamilyAny (asks about every family).
func familyMatches(stored, want string) bool {
	return want == FamilyAny || stored == want || stored == FamilyAny
}
