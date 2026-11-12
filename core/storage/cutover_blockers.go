// file: core/storage/cutover_blockers.go
// version: 1.0.0
// guid: e2707512-d0a3-4f0a-9995-f2597358cfd3
// last-edited: 2026-11-12

package storage

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/falkcorp/mediacore/storage/keys"
)

// `storage cutover-blockers fix` and `delete` (05-storage §5S.3.13 "Cutover
// blockers", F4-06): the operator's way out of a blocked cutover. They edit
// one legacy row of a store whose cutover has not started, with no migration
// step run and the store never prepared, and commit the edit, a record of it
// and its audit event in ONE batch.
//
// The retired-claim seam. A cutover build declares its legacy families
// ClaimRetired, which admits Delete and refuses Set (claims.go), and a fix
// must Set a repaired row. The seam is blockerRange, a distinct unexported
// Handle (never a LegacyRange, whose Set stays refused on a retired claim):
// it exists only inside EditCutoverBlocker, on a store BeginCutoverBlockers
// opened and nothing has prepared, and it admits Set and Delete for exactly
// the keys inside the owning cutover step's Source ranges that resolve to the
// owner's retired claims, and nothing else: no DeleteRange, no key outside
// Source, no key an out-of-claim access (the shadow dial) would let through.
// Reads are unrestricted within the owner's claims, as a retired claim's
// are. Nothing outside this file can obtain the handle.
//
// The validation. A fix or delete is staged into an INDEXED batch, the
// owning step's Preflight is re-run over a reader that overlays the staged
// writes on the committed state (Recheck), and only if that report is
// acceptable is anything else staged and the batch committed. A refused edit
// therefore commits nothing and leaves the store byte-identical: the record's
// counter is a scan plus a commit-time precondition (no Sequence, whose lease
// refill is a durable write outside the batch), and the audit event is staged
// last, after the check, because audit's own sequence lease is such a write.

// ErrCutoverStarted: `storage cutover-blockers` was asked to open a store
// whose cutover has started (an owner's level at M7's target, or a cutover
// step with a record in c/storage/dm/): its source rows are partly imported,
// so listing, fixing or deleting them is meaningless and unsafe. The way back
// is the pre-upgrade archive.
var ErrCutoverStarted = errors.New("storage: the cutover has started on this store, so storage cutover-blockers does not open it; restore the pre-upgrade archive with make rollback-restore")

// The actions of a CutoverBlockerEdit and of its record.
const (
	CutoverBlockerFix    = "fix"
	CutoverBlockerDelete = "delete"
)

// blockerSpans is a cutover step's Source as the seam sees it. It is held by
// pointer so two handles over different Sources are different capabilities
// (handleOf.same).
type blockerSpans struct{ spans []span }

func (b *blockerSpans) contains(k []byte) bool {
	for _, sp := range b.spans {
		if sp.contains(k) {
			return true
		}
	}
	return false
}

// blockerRange is the seam's handle: owner's legacy range, writable by Set
// and Delete only inside fix's spans.
type blockerRange struct {
	db    *DB
	owner Owner
	fix   *blockerSpans
}

func (blockerRange) handle() {}

func (r blockerRange) scope() handleOf {
	return handleOf{db: r.db, owner: r.owner, legacy: true, fix: r.fix}
}

// BeginCutoverBlockers is the open for `storage cutover-blockers`: the
// explicit exception to ErrOfflinePendingSteps (§5S.4 "Offline open" rule 2).
// It returns o's store with no migration step run and nothing written, and
// leaves it unprepared, so PreflightSource keeps working and Prepare,
// Offline, Migrate and the level step stay refused. steps are the build's
// cutover steps. It refuses (nothing written) when:
//
//   - the store was prepared, or was already opened this way;
//   - the store is empty (a fresh install has no source rows);
//   - the cutover has started (CutoverStarted): ErrCutoverStarted;
//   - the open is read-write and the store has no install marker, or a killed
//     boot left an upgrade attempt: neither is the pristine store the
//     pre-freeze release serves, and a write here would join serve's own
//     first writes uncovered. A read-only open (the listing) passes both.
//
// A store whose Pebble format serve would ratchet is not refused: the edit
// writes at the format it has.
func (o *Opened) BeginCutoverBlockers(ctx context.Context, steps []CutoverStep) (*DB, error) {
	db := o.db
	st := db.boot
	switch {
	case st.prepared:
		return nil, errors.New("storage: BeginCutoverBlockers after Prepare: the cutover-blockers open runs before the boot's P4a")
	case st.blockersOnly:
		return nil, errors.New("storage: BeginCutoverBlockers called twice")
	case st.fresh:
		return nil, errors.New("storage: the store is empty: there are no cutover blockers to list, fix or delete")
	}
	started, err := o.CutoverStarted(ctx, steps)
	switch {
	case err != nil:
		return nil, err
	case started:
		return nil, ErrCutoverStarted
	case db.readOnly:
	case !st.marker:
		return nil, errors.New("storage: the store has no install marker: it is not the pre-cutover store a release served; start the pre-freeze release once first")
	case st.attempt != "":
		return nil, fmt.Errorf("storage: upgrade attempt %s was left uncompleted by a killed boot: serve completes it, so a cutover-blockers edit would join its writes", st.attempt)
	}
	st.blockersOnly = true
	return db, nil
}

// blockersOpen reports whether the store is in the state the seam works in.
func (db *DB) blockersOpen() error {
	st := db.boot
	switch {
	case st == nil || !st.blockersOnly:
		return errors.New("storage: not opened with BeginCutoverBlockers")
	case st.prepared:
		return errors.New("storage: a cutover-blockers edit after Prepare is refused")
	}
	return nil
}

// CutoverBlockerEdit is one `fix` or `delete` for EditCutoverBlocker.
type CutoverBlockerEdit struct {
	Step   string  // the owning cutover step's ID: the record's namespace
	Owner  Owner   // the step's owner
	Source []Range // the step's Source: the only keys Stage may Set or Delete
	Key    []byte  // the blocking row's absolute key
	Action string  // CutoverBlockerFix or CutoverBlockerDelete
	Reason string  // the blocker's reason, kept in the record; never a value
	Build  string  // the build's binary ID
	At     time.Time
	// Stage stages the change into b (the step's FixBlocker or
	// DeleteBlocker) and returns the IDs it removed. b's origin is the seam
	// handle, so b.Set and b.Delete reach only Source's keys.
	Stage func(ctx context.Context, b *Batch) (ids []string, err error)
	// Recheck runs the step's Preflight over r, the committed state with the
	// staged change laid over it. A non-nil error refuses the edit.
	Recheck func(ctx context.Context, r Reader) error
	// Audit stages the core.storage.cutover_blocker_* event into b. It runs
	// after Recheck passed, as the last thing staged.
	Audit func(b *Batch, ids []string) error
}

// CutoverBlockerRecord is the value at CutoverBlockerKey: what the report of
// the step and its core.storage.cutover_step audit record carry (05-storage
// §5S.3.13). Key is the PrintableKey form; no value is ever recorded.
type CutoverBlockerRecord struct {
	Key    string   `json:"key"`
	Action string   `json:"action"`
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
	At     string   `json:"at"` // RFC 3339, UTC
	Build  string   `json:"build"`
}

// CutoverBlockerKey is record n of step, relative to storage's own keyspace
// c/storage/: cutover/blockers/<step>/<n>, n big-endian so a step's records
// list in the order they were made. n counts from 1 per step.
func CutoverBlockerKey(step string, n uint64) []byte {
	return keys.Join(keys.Str("cutover"), keys.Str("blockers"), keys.Str(step), keys.U64(n))
}

func cutoverBlockerPrefix(step string) []byte {
	return keys.Prefix(keys.Str("cutover"), keys.Str("blockers"), keys.Str(step))
}

// nextCutoverBlockerN is one more than the highest record number of step.
func (db *DB) nextCutoverBlockerN(ctx context.Context, ks Keyspace, step string) (uint64, error) {
	p := cutoverBlockerPrefix(step)
	for kv, err := range ks.Scan(ctx, ScanOptions{Start: p, End: keys.PrefixEnd(p), Reverse: true, Limit: 1, KeysOnly: true}) {
		if err != nil {
			return 0, fmt.Errorf("storage: the cutover-blocker records of %s: %w", step, err)
		}
		segs, err := keys.Split(kv.Key)
		if err != nil || len(segs) == 0 || len(segs[len(segs)-1]) != 8 {
			return 0, fmt.Errorf("storage: a cutover-blocker record of %s has a malformed key", step)
		}
		return binary.BigEndian.Uint64(segs[len(segs)-1]) + 1, nil
	}
	return 1, nil
}

// EditCutoverBlocker commits e as ONE batch: the change Stage staged, the
// record at CutoverBlockerKey(e.Step, n) and the audit event, or nothing. It
// refuses unless the store is in BeginCutoverBlockers's state, is read-write,
// and e's Recheck passes the change over the overlay (see the file's
// comment); a refusal leaves the store byte-identical. The record's key is
// guarded by a commit-time precondition (absent), so two writers that chose
// the same n cannot both commit. It returns n.
func (db *DB) EditCutoverBlocker(ctx context.Context, e CutoverBlockerEdit) (n uint64, err error) {
	if err := db.blockersOpen(); err != nil {
		return 0, err
	}
	switch {
	case db.readOnly:
		return 0, fmt.Errorf("storage: cutover-blocker %s on a read-only open: %w", e.Action, ErrReadOnly)
	case e.Action != CutoverBlockerFix && e.Action != CutoverBlockerDelete:
		return 0, fmt.Errorf("storage: cutover-blocker action %q is neither fix nor delete", e.Action)
	case e.Step == "" || len(e.Key) == 0 || len(e.Source) == 0:
		return 0, errors.New("storage: a cutover-blocker edit names its step, its key and the step's Source")
	case e.Stage == nil || e.Recheck == nil || e.Audit == nil:
		return 0, errors.New("storage: a cutover-blocker edit stages its change, rechecks it and stages its audit event")
	}
	info := db.owners[e.Owner]
	if info == nil || len(info.spec.Claims) == 0 {
		return 0, fmt.Errorf("storage: owner %s has no legacy range for a cutover-blocker edit", e.Owner)
	}
	fix := &blockerSpans{}
	for _, r := range e.Source {
		sp := span{start: slices.Clone(r.Start), end: slices.Clone(r.End)}
		if bytes.Compare(sp.start, sp.end) >= 0 {
			return 0, fmt.Errorf("storage: step %s's Source range [%s, %s) is empty or inverted", e.Step, db.keyRef(sp.start), db.keyRef(sp.end))
		}
		fix.spans = append(fix.spans, sp)
	}
	h := blockerRange{db: db, owner: e.Owner, fix: fix}
	b := newBatch(h, true)
	defer func() { err = errors.Join(err, b.Close()) }()

	ids, err := e.Stage(ctx, b)
	if err != nil {
		return 0, fmt.Errorf("storage: stage the %s of %s's blocker: %w", e.Action, e.Step, err)
	}
	if b.err != nil {
		return 0, b.err
	}
	if b.Count() == 0 {
		return 0, fmt.Errorf("storage: the %s of %s's blocker staged no change", e.Action, e.Step)
	}
	// The change must be the one named: a fix leaves the row in place (its
	// value is the operator's), a delete removes it.
	switch _, gerr := b.Get(ctx, e.Key); {
	case e.Action == CutoverBlockerFix && gerr != nil:
		return 0, fmt.Errorf("storage: the fix of %s's blocker did not leave the row in place: %w", e.Step, gerr)
	case e.Action == CutoverBlockerDelete && !errors.Is(gerr, ErrNotFound):
		return 0, fmt.Errorf("storage: the delete of %s's blocker did not remove the row", e.Step)
	}
	// The overlay: the legacy range read through the batch, its own writes
	// first, without a write method behind it.
	overlay := readOnlyReader{r: LegacyRange{db: db, owner: e.Owner, tx: &MigrationTx{b: b}}}
	if err := e.Recheck(ctx, overlay); err != nil {
		return 0, fmt.Errorf("storage: the %s of %s's blocker is refused, nothing written: %w", e.Action, e.Step, err)
	}

	ks := db.storageKS()
	if n, err = db.nextCutoverBlockerN(ctx, ks, e.Step); err != nil {
		return 0, err
	}
	if ids == nil {
		ids = []string{}
	}
	raw, err := json.Marshal(CutoverBlockerRecord{Key: printableKey(e.Key), Action: e.Action, IDs: ids, Reason: e.Reason,
		At: e.At.UTC().Format(time.RFC3339), Build: e.Build})
	if err != nil {
		return 0, fmt.Errorf("storage: encode the cutover-blocker record: %w", err)
	}
	rec := CutoverBlockerKey(e.Step, n)
	b.Bind(ks)
	if err := b.In(ks).Set(rec, raw); err != nil {
		return 0, err
	}
	// storage's keyspace is not Serialized, so Expect refuses it; the same
	// precondition is staged directly: the record's key must be absent.
	b.expect(append(slices.Clone(ks.prefix), rec...), nil, true)
	if err := e.Audit(b, ids); err != nil {
		return 0, fmt.Errorf("storage: stage the audit event of the %s: %w", e.Action, err)
	}
	if err := b.Commit(ctx, Sync); err != nil {
		return 0, err
	}
	return n, nil
}
