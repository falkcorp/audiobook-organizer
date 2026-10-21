// file: internal/decisions/store.go
// version: 1.0.0
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
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/keyedmutex"
)

// KV is the slice of the store this package needs. It is deliberately narrow:
// the decision store has no business with books, files or anything else on
// database.Store. database.RawKVStore satisfies it. It omits the unbounded
// ScanPrefix: decisions grow with every reject for the life of the library.
type KV interface {
	GetRaw(key string) ([]byte, error)
	ScanPrefixPage(prefix, after string, limit int) (pairs []database.KVPair, next string, err error)
	// ApplyRawBatch must be atomic: a record, its verdict and its veto rows
	// commit together or not at all.
	ApplyRawBatch(writes []database.RawWrite) error
}

var (
	// ErrNotFound is returned by Get for an unknown record id.
	ErrNotFound = errors.New("decisions: record not found")
	// ErrNotRejected is returned for an unreject or reopen of a proposal
	// that has no active reject.
	ErrNotRejected = errors.New("decisions: proposal has no active reject")
	// ErrCorrupt is returned when the log contradicts itself (a verdict
	// pointing at a record that is not there). The caller must fail closed.
	ErrCorrupt = errors.New("decisions: store is inconsistent")
)

const scanPage = 500

// Store reads and writes decisions.
type Store struct {
	kv  KV
	now func() time.Time

	// locks serialises Record per subject: it reads the verdict, links
	// SupersedesID to it and writes the next one.
	locks keyedmutex.Mutex

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
	s := &Store{kv: kv, now: time.Now, entropy: ulid.Monotonic(crand.Reader, 0)}
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

// Record appends one decision to the log and updates the verdict and indexes
// in one atomic commit. It returns the stored record.
//
//   - reject: the proposal becomes active (vetoed). A reject of two or more
//     entities also vetoes each pair among them, within Input.Family. A reject
//     of an already-rejected proposal replaces its edges.
//   - unreject: lifts the veto and deletes its edges. The record stays.
//   - reopened: lifts the veto because the data changed (see MaterialHash).
//   - approve, applied, auto_applied, undone, skip: labels only. None vetoes
//     and none lifts a veto.
func (s *Store) Record(in Input) (*Record, error) {
	ids, err := in.validate()
	if err != nil {
		return nil, err
	}
	subject := SubjectKey(in.OpKind, in.EntityType, ids, in.Scope)
	unlock := s.locks.Lock(subjectLockKey(in.OpKind, subject))
	defer unlock()

	prev, err := s.Verdict(in.OpKind, subject)
	if err != nil {
		return nil, err
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
	// dropLiveEdges deletes the veto rows of the reject that is live now.
	dropLiveEdges := func() error {
		if prev == nil || prev.State != StateActive {
			return nil
		}
		old, err := s.Get(prev.RejectID)
		if err != nil {
			return fmt.Errorf("%w: verdict %s/%s names reject %q: %v", ErrCorrupt, in.OpKind, subject, prev.RejectID, err)
		}
		for _, k := range indexKeys(old) {
			writes = append(writes, database.RawWrite{Key: k, Delete: true})
		}
		return nil
	}

	switch in.Decision {
	case DecisionReject:
		if len(ids) >= 2 {
			rec.Family = in.Family
		}
		if err := dropLiveEdges(); err != nil {
			return nil, err
		}
		verdict.State, verdict.RejectID, verdict.MaterialHash = StateActive, rec.ID, in.MaterialHash
		for _, k := range indexKeys(&rec) {
			writes = append(writes, database.RawWrite{Key: k})
		}
	case DecisionUnreject, DecisionReopened:
		if prev == nil || prev.State != StateActive {
			return nil, fmt.Errorf("%w: %s %s", ErrNotRejected, in.OpKind, subject)
		}
		if err := dropLiveEdges(); err != nil {
			return nil, err
		}
		verdict.RejectID = ""
		if in.Decision == DecisionUnreject {
			verdict.State = StateUnrejected
		} else {
			verdict.State = StateReopened
		}
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

// indexKeys are the veto rows a reject record owns: pair edges for a set of up
// to MaxEdgeSet entities, membership rows for a larger one. The same function
// names the rows to write and the rows to delete, so they can never drift.
func indexKeys(rec *Record) []string {
	n := len(rec.EntityIDs)
	if n < 2 || rec.Family == "" {
		return nil
	}
	if n > MaxEdgeSet {
		keys := make([]string, 0, n)
		for _, id := range rec.EntityIDs {
			keys = append(keys, memberKey(rec.EntityType, id, rec.Family, rec.ID))
		}
		return keys
	}
	keys := make([]string, 0, n*(n-1)/2)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			lo, hi := orderPair(rec.EntityIDs[i], rec.EntityIDs[j])
			keys = append(keys, edgeKey(rec.EntityType, lo, hi, rec.Family, rec.ID))
		}
	}
	return keys
}

// Get returns one record.
func (s *Store) Get(id string) (*Record, error) {
	raw, err := s.kv.GetRaw(recordKey(id))
	if err != nil {
		return nil, fmt.Errorf("decisions: read record %s: %w", id, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	var rec Record
	if err := json.Unmarshal(raw, &rec); err != nil {
		return nil, fmt.Errorf("%w: decode record %s: %v", ErrCorrupt, id, err)
	}
	return &rec, nil
}

// Verdict returns the current state of a proposal, or nil when nothing was
// ever decided about it.
func (s *Store) Verdict(opKind, subjectKey string) (*Verdict, error) {
	raw, err := s.kv.GetRaw(verdictKey(opKind, subjectKey))
	if err != nil {
		return nil, fmt.Errorf("decisions: read verdict: %w", err)
	}
	if raw == nil {
		return nil, nil
	}
	var v Verdict
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("%w: decode verdict %s/%s: %v", ErrCorrupt, opKind, subjectKey, err)
	}
	return &v, nil
}

// Status is what a producer asks before proposing something again.
type Status struct {
	Verdict *Verdict
	// State is the effective state: StateReopened when the verdict is active
	// but the proposal's material hash differs from the rejected one.
	// Empty when nothing was ever decided about the proposal.
	State VerdictState
	// Blocked is true when the proposal must be skipped.
	Blocked bool
}

// Lookup finds a proposal by fingerprint. materialHash is MaterialHash of the
// proposal as it would be made now ("" for a sticky op).
func (s *Store) Lookup(opKind, subjectKey, materialHash string) (Status, error) {
	v, err := s.Verdict(opKind, subjectKey)
	if err != nil || v == nil {
		return Status{}, err
	}
	st := Status{Verdict: v, State: v.State, Blocked: v.Blocks()}
	if v.State == StateActive && v.MaterialHash != "" && materialHash != "" && v.MaterialHash != materialHash {
		st.State, st.Blocked = StateReopened, false
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
		rec, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
		id = rec.SupersedesID
	}
	return out, nil
}

// scanAll pages through every pair under prefix in key order.
func (s *Store) scanAll(ctx context.Context, prefix string, fn func(database.KVPair) error) error {
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		pairs, next, err := s.kv.ScanPrefixPage(prefix, after, scanPage)
		if err != nil {
			return fmt.Errorf("decisions: scan %s: %w", prefix, err)
		}
		for _, p := range pairs {
			if err := fn(p); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}

// Filter narrows an iteration.
type Filter struct {
	// OpKind, when set, visits only that kind (through its index).
	OpKind string
}

// Iterate visits records in ULID (decision time) order, for listing and
// JSONL export. fn returning an error stops the walk and returns that error.
func (s *Store) Iterate(ctx context.Context, f Filter, fn func(Record) error) error {
	if f.OpKind != "" {
		prefix := opPrefix(f.OpKind)
		return s.scanAll(ctx, prefix, func(p database.KVPair) error {
			rec, err := s.Get(strings.TrimPrefix(p.Key, prefix))
			if err != nil {
				return fmt.Errorf("%w: op index %s: %v", ErrCorrupt, p.Key, err)
			}
			return fn(*rec)
		})
	}
	return s.scanAll(ctx, prefixRecord, func(p database.KVPair) error {
		var rec Record
		if err := json.Unmarshal(p.Value, &rec); err != nil {
			return fmt.Errorf("%w: decode %s: %v", ErrCorrupt, p.Key, err)
		}
		return fn(rec)
	})
}

// ByEntity visits the records that name one entity, in decision order.
func (s *Store) ByEntity(ctx context.Context, entityType, entityID string, fn func(Record) error) error {
	prefix := entityPrefix(entityType, entityID)
	return s.scanAll(ctx, prefix, func(p database.KVPair) error {
		rec, err := s.Get(strings.TrimPrefix(p.Key, prefix))
		if err != nil {
			return fmt.Errorf("%w: entity index %s: %v", ErrCorrupt, p.Key, err)
		}
		return fn(*rec)
	})
}

// PairVetoed reports whether an active reject vetoes joining a and b in the
// given family (rows stored with FamilyAny veto every family). It reads the
// store directly and does NO id resolution: a merged-away id is looked up as
// given. For a whole plan run load a Vetoes snapshot with an IDResolver
// instead.
func (s *Store) PairVetoed(entityType, a, b, family string) (bool, error) {
	if a == b {
		return false, nil
	}
	ctx := context.Background()
	lo, hi := orderPair(a, b)
	errStop := errors.New("found")
	prefix := edgePairPrefix(entityType, lo, hi)
	err := s.scanAll(ctx, prefix, func(p database.KVPair) error {
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

func familyMatches(stored, want string) bool { return stored == want || stored == FamilyAny }

func unmarshalVerdict(p database.KVPair, v *Verdict) error {
	if err := json.Unmarshal(p.Value, v); err != nil {
		return fmt.Errorf("%w: decode verdict %s: %v", ErrCorrupt, p.Key, err)
	}
	return nil
}
