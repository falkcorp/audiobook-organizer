// file: internal/database/pebble_store_ops_v3.go
// version: 1.10.0
// guid: e48755db-c26d-42a2-a232-49daf9732335
// last-edited: 2026-10-14

// pebble_store_ops_v3 implements OpsV3Store for PebbleDB. Key schema (all
// prefixed with "opv3:"; state-and-persistence.md section 2):
//
//	opv3:run:{op_id}                               → JSON(RunRecord) without the runHot fields
//	opv3:prog:{op_id}                              → JSON(runHot)  progress, last_progress_at, last_checkpoint_at, v2_row_hash
//	opv3:q:{999-prio:03d}:{queued_ns:020d}:{op_id} → ""  dispatch order; queued with empty wait_reason only
//	opv3:ix:state:{state}:{op_id}                  → ""  exact count per state
//	opv3:ix:def:{def_id}:{queued_ns:020d}:{op_id}  → ""  runs of one def
//	opv3:ix:done:{completed_ns:020d}:{op_id}       → ""  timeline window (completed_at set)
//	opv3:ix:parent:{parent_id}:{op_id}             → ""  pipeline children
//	opv3:evt:{op_id}:{ts_ns:020d}:{seq:06d}        → JSON(Transition)
//	opv3:ckpt:{op_id}                              → JSON(Checkpoint)
//	opv3:snap:{op_id}:{page:06d}                   → JSON([]string)
//	opv3:snap:{op_id}:chunks                       → JSON([]int32)
//	opv3:ledger:{op_id}                            → JSON(Ledger)
//	opv3:intent:{op_id}:{seq:010d}                 → JSON(Intent)
//	opv3:item:{op_id}:{outcome}:{key}              → JSON(ItemOutcome)
//	opv3:log:{op_id}:{ts_ns:020d}:{seq:010d}       → JSON(LogLine)
//	opv3:rowmark:{op_id}:{row_id}                → JSON(RowMarkV3)  (pebble_store_ops_v3_rowmarks.go)
//	opv3:sched:{def_id}                            → JSON(SchedState)  (written by 05-PR8)
//	opv3:def:{def_id}                              → JSON(DefSnapshot)
//	opv3:meta:counts                               → JSON(OpCounts)
//
// TWO WRITE PATHS, ONE BATCH EACH.
//
//   - The v2 façade. The v2 registry still writes through the opv2 store
//     methods (InsertOperationV2, UpdateOperationV2Status[If],
//     ResetOperationV2ForResume, DeleteOperationV2, and every commitOpV2Row
//     writer). Each of those stages the v3 projection of the row it writes
//     (stageRunV3FromV2) in the SAME batch, under the opsMu it already holds.
//   - The native methods (InsertRunV3, TransitionRunV3, DeleteRunV3) write
//     the v3 record and stage the mirrored opv2: row through
//     stageOpV2RowWrite / stageOpV2RowDelete, again in one batch.
//
// So every change to either family commits together with the other, and the
// previous binary, which reads only opv2:, can be redeployed at any time
// (owner decision D25: the mirror stays on until 05-PR16).
//
// RUN RECORD SPLIT. A run is stored under two keys. opv3:prog: holds the
// fields a progress, phase or checkpoint tick changes (runHot) and the hash of
// the opv2:op: row the record was last synced with; opv3:run: holds the rest,
// including the large Params and Result blobs. put writes each key only when
// its bytes change, so a tick writes the small key and not the record
// (BenchmarkOpsV3ProgressTick). loadRunV3 is the one place the two are joined;
// every reader goes through it.
//
// The stored opv3:run: record, not the back-mapped v2 status, is the "prev"
// of every write: ToV2 loses information (stopping and running both mirror as
// running, timed_out as canceled, queued{pause} as queued), so deriving prev
// from the v2 row would leave index and counter entries for a state the run
// is not in.

package database

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

// opsV3Log is the subsystem logger for the opv3 keyspace.
var opsV3Log = logger.New("database.opsv3")

const (
	opv3Prefix         = "opv3:"
	opv3RunPrefix      = "opv3:run:"
	opv3ProgPrefix     = "opv3:prog:"
	opv3QueuePrefix    = "opv3:q:"
	opv3IxStatePrefix  = "opv3:ix:state:"
	opv3IxDefPrefix    = "opv3:ix:def:"
	opv3IxDonePrefix   = "opv3:ix:done:"
	opv3IxParentPrefix = "opv3:ix:parent:"
	opv3EvtPrefix      = "opv3:evt:"
	opv3CkptPrefix     = "opv3:ckpt:"
	opv3SnapPrefix     = "opv3:snap:"
	opv3LedgerPrefix   = "opv3:ledger:"
	opv3IntentPrefix   = "opv3:intent:"
	opv3ItemPrefix     = "opv3:item:"
	opv3LogPrefix      = "opv3:log:"
	opv3MarkPrefix     = "opv3:mark:"
	opv3SchedPrefix    = "opv3:sched:"
	opv3DefPrefix      = "opv3:def:"
	opv3MetaCountsKey  = "opv3:meta:counts"

	// opv3ItemCap is the per-run cap on stored item outcomes.
	opv3ItemCap = 10000
	// opv3RunSchemaVersion is RunRecord.SchemaVersion.
	opv3RunSchemaVersion = 3
	// opv3DefaultPage is the page size of the list readers when the caller
	// passes no limit.
	opv3DefaultPage = 100
)

// ── key builders ────────────────────────────────────────────────────────────

func opv3RunKey(id string) []byte  { return []byte(opv3RunPrefix + id) }
func opv3ProgKey(id string) []byte { return []byte(opv3ProgPrefix + id) }

// opv3QueueKey uses opv2QueueKey's encoding: priority DESC, queued_at ASC.
func opv3QueueKey(priority int, queuedAt *time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%s%03d:%020d:%s", opv3QueuePrefix, 999-priority, opv3Nanos(queuedAt), id))
}

func opv3IxStateKey(s state.V3, id string) []byte {
	return []byte(opv3IxStatePrefix + string(s) + ":" + id)
}

func opv3IxDefKey(defID string, queuedAt *time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%s%s:%020d:%s", opv3IxDefPrefix, defID, opv3Nanos(queuedAt), id))
}

func opv3IxDoneKey(completedAt time.Time, id string) []byte {
	return []byte(fmt.Sprintf("%s%020d:%s", opv3IxDonePrefix, completedNanos(completedAt), id))
}

func opv3IxParentKey(parentID, id string) []byte {
	return []byte(opv3IxParentPrefix + parentID + ":" + id)
}

func opv3EvtKey(id string, ts time.Time, seq uint64) []byte {
	return []byte(fmt.Sprintf("%s%s:%020d:%06d", opv3EvtPrefix, id, completedNanos(ts), seq))
}

func opv3CkptKey(id string) []byte   { return []byte(opv3CkptPrefix + id) }
func opv3LedgerKey(id string) []byte { return []byte(opv3LedgerPrefix + id) }

// opv3SnapRunPrefix covers every snapshot page and the chunk-offsets key of
// one run (opv3:snap:{id}:chunks sorts inside it).
func opv3SnapRunPrefix(id string) []byte { return []byte(opv3SnapPrefix + id + ":") }
func opv3SnapKey(id string, page int) []byte {
	return []byte(fmt.Sprintf("%s%s:%06d", opv3SnapPrefix, id, page))
}
func opv3SnapChunksKey(id string) []byte { return []byte(opv3SnapPrefix + id + ":chunks") }

func opv3IntentKey(id string, seq uint64) []byte {
	return []byte(fmt.Sprintf("%s%s:%010d", opv3IntentPrefix, id, seq))
}

func opv3ItemKey(id, outcome, key string) []byte {
	return []byte(opv3ItemPrefix + id + ":" + outcome + ":" + key)
}

// opv3MarkRunPrefix is a run's opv3:mark: family. A mark is
// opv3:mark:{id}:{name}\x00{key}: names never hold a NUL, item keys may hold
// anything else, so the key splits on its first NUL.
func opv3MarkRunPrefix(id string) []byte { return []byte(opv3MarkPrefix + id + ":") }
func opv3MarkKey(id, name, key string) []byte {
	return []byte(opv3MarkPrefix + id + ":" + name + "\x00" + key)
}

func opv3LogKey(id string, ts time.Time, seq int64) []byte {
	return []byte(fmt.Sprintf("%s%s:%020d:%010d", opv3LogPrefix, id, completedNanos(ts), seq))
}

// opv3RunScopedPrefixes are the per-run families a discard deletes whole.
func opv3RunScopedPrefixes(id string) [][]byte {
	return [][]byte{
		[]byte(opv3EvtPrefix + id + ":"),
		opv3SnapRunPrefix(id),
		[]byte(opv3IntentPrefix + id + ":"),
		[]byte(opv3ItemPrefix + id + ":"),
		[]byte(opv3LogPrefix + id + ":"),
		opv3MarkRunPrefix(id),
		opv3RowMarkRunPrefix(id),
	}
}

// opv3Nanos is completedNanos for an optional time (nil sorts first).
func opv3Nanos(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return completedNanos(*t)
}

// opv3IndexKeys is every index key a record should have. put stages the
// difference between prev's and next's sets.
func opv3IndexKeys(r *RunRecord) []string {
	if r == nil {
		return nil
	}
	keys := []string{
		string(opv3IxStateKey(r.State, r.ID)),
		string(opv3IxDefKey(r.DefID, r.QueuedAt, r.ID)),
	}
	if r.CompletedAt != nil {
		keys = append(keys, string(opv3IxDoneKey(*r.CompletedAt, r.ID)))
	}
	if r.ParentID != "" {
		keys = append(keys, string(opv3IxParentKey(r.ParentID, r.ID)))
	}
	if r.State == state.V3Queued && r.WaitReason == "" {
		keys = append(keys, string(opv3QueueKey(r.Priority, r.QueuedAt, r.ID)))
	}
	return keys
}

// ── mirror switch ───────────────────────────────────────────────────────────

// opsV2MirrorEnabled reports whether native v3 writes also stage the opv2:
// mirror row. On by default (the zero value of opsV2MirrorOff); 05-PR16 turns
// it off after the D25 soak, with the production setter it needs (today only
// the tests flip it, through setOpsV2MirrorEnabled in a _test.go file). It is
// deliberately not a user setting. The v2 façade methods always write opv2:
// (that is what they are).
func (p *PebbleStore) opsV2MirrorEnabled() bool { return !p.opsV2MirrorOff.Load() }

// ── staging ─────────────────────────────────────────────────────────────────

// opsV3Stage accumulates one batch's v3 writes. Every user holds p.opsMu from
// before the first read of a record it stages until after the commit, so the
// counter read-modify-write in flush cannot lose a concurrent change.
type opsV3Stage struct {
	p       *PebbleStore
	b       *GatedBatch
	byState map[state.V3]int64
	byDef   map[string]int64
	total   int64
	dirty   bool
}

func (p *PebbleStore) newOpsV3Stage(b *GatedBatch) *opsV3Stage {
	return &opsV3Stage{p: p, b: b, byState: map[state.V3]int64{}, byDef: map[string]int64{}}
}

func (s *opsV3Stage) count(r *RunRecord, d int64) {
	s.byState[r.State] += d
	s.byDef[r.DefID] += d
	s.total += d
	s.dirty = true
}

// put stages next (and its index keys against prev's) and, when tr is not
// nil, one opv3:evt: row. prev is the stored record (nil: none). On a move
// into a terminal state the run's snapshot pages, chunk offsets, ledger and
// marks are deleted in the same batch (the final counters live on in
// RunRecord.Progress; the marks were handed to Finish before the run ended).
func (s *opsV3Stage) put(prev, next *RunRecord, tr *Transition) error {
	if next.ID == "" {
		return errors.New("opv3: run record has no id")
	}
	next.SchemaVersion = opv3RunSchemaVersion
	if tr != nil {
		next.EventSeq++
		tr.Seq = next.EventSeq
		tr.Attempt = next.Attempt
		tr.FenceEpoch = next.FenceEpoch
		data, err := json.Marshal(tr)
		if err != nil {
			return err
		}
		if err := s.b.Set(opv3EvtKey(next.ID, tr.At, tr.Seq), data, nil); err != nil {
			return err
		}
	}
	if err := s.stageRecord(prev, next); err != nil {
		return err
	}
	if err := s.stageIndexDiff(opv3IndexKeys(prev), opv3IndexKeys(next)); err != nil {
		return err
	}
	if prev == nil || prev.State != next.State || prev.DefID != next.DefID {
		if prev != nil {
			s.count(prev, -1)
		}
		s.count(next, +1)
	}
	if state.ClassifyV3(next.State).Terminal && (prev == nil || !state.ClassifyV3(prev.State).Terminal) {
		snap := opv3SnapRunPrefix(next.ID)
		if err := s.b.DeleteRange(snap, prefixEnd(snap), nil); err != nil {
			return err
		}
		if err := s.b.Delete(opv3LedgerKey(next.ID), nil); err != nil {
			return err
		}
		marks := opv3MarkRunPrefix(next.ID)
		if err := s.b.DeleteRange(marks, prefixEnd(marks), nil); err != nil {
			return err
		}
		// A Fixer row's apply markers are resume evidence like the ledger:
		// a terminal run is never resumed, so they go on the same edge.
		rowMarks := opv3RowMarkRunPrefix(next.ID)
		if err := s.b.DeleteRange(rowMarks, prefixEnd(rowMarks), nil); err != nil {
			return err
		}
	}
	return nil
}

// del stages the removal of a stored run and everything keyed by its id
// under opv3:.
func (s *opsV3Stage) del(prev *RunRecord) error {
	id := prev.ID
	if err := s.stageIndexDiff(opv3IndexKeys(prev), nil); err != nil {
		return err
	}
	for _, k := range [][]byte{opv3RunKey(id), opv3ProgKey(id), opv3CkptKey(id), opv3LedgerKey(id)} {
		if err := s.b.Delete(k, nil); err != nil {
			return err
		}
	}
	for _, pfx := range opv3RunScopedPrefixes(id) {
		if err := s.b.DeleteRange(pfx, prefixEnd(pfx), nil); err != nil {
			return err
		}
	}
	s.count(prev, -1)
	return nil
}

// runHot is the opv3:prog:{id} value: the part of a run that a progress,
// phase or checkpoint tick changes, and the hash of the opv2:op: row the run
// was last synced with (which every v2 write changes). See "RUN RECORD
// SPLIT" at the top of this file.
type runHot struct {
	Progress         ProgressSnapshot `json:"progress"`
	LastProgressAt   *time.Time       `json:"last_progress_at,omitempty"`
	LastCheckpointAt *time.Time       `json:"last_checkpoint_at,omitempty"`
	V2RowHash        string           `json:"v2_row_hash,omitempty"`
}

func hotOf(r *RunRecord) runHot {
	return runHot{Progress: r.Progress, LastProgressAt: r.LastProgressAt, LastCheckpointAt: r.LastCheckpointAt, V2RowHash: r.V2RowHash}
}

// runCoreJSON encodes a RunRecord without its runHot fields: each shadowing
// field below is shallower than the embedded one of the same JSON name, so it
// wins, and being nil it is omitted.
type runCoreJSON struct {
	*RunRecord
	Progress         *struct{} `json:"progress,omitempty"`
	LastProgressAt   *struct{} `json:"last_progress_at,omitempty"`
	LastCheckpointAt *struct{} `json:"last_checkpoint_at,omitempty"`
	V2RowHash        *struct{} `json:"v2_row_hash,omitempty"`
}

func encodeRunCore(r *RunRecord) ([]byte, error) { return json.Marshal(runCoreJSON{RunRecord: r}) }

func encodeRunHot(r *RunRecord) ([]byte, error) {
	h := hotOf(r)
	return json.Marshal(&h)
}

// stageRecord stages next's opv3:run: and opv3:prog: values, each only when
// it differs from prev's (prev nil: both), so a progress tick rewrites only
// the small key. The comparison is reflect.DeepEqual on the decoded halves,
// not on their encodings, so a tick never marshals the Params and Result
// blobs: equal values always encode equally, and the only thing DeepEqual can
// get "wrong" (a time that is the same instant in another representation) is
// one redundant write.
func (s *opsV3Stage) stageRecord(prev, next *RunRecord) error {
	if prev == nil || !reflect.DeepEqual(runCoreOf(prev), runCoreOf(next)) {
		data, err := encodeRunCore(next)
		if err != nil {
			return err
		}
		if err := s.b.Set(opv3RunKey(next.ID), data, nil); err != nil {
			return err
		}
	}
	if prev == nil || !reflect.DeepEqual(hotOf(prev), hotOf(next)) {
		data, err := encodeRunHot(next)
		if err != nil {
			return err
		}
		if err := s.b.Set(opv3ProgKey(next.ID), data, nil); err != nil {
			return err
		}
	}
	return nil
}

// runCoreOf is r without its runHot fields: what opv3:run: stores.
func runCoreOf(r *RunRecord) RunRecord {
	c := *r
	c.Progress, c.LastProgressAt, c.LastCheckpointAt, c.V2RowHash = ProgressSnapshot{}, nil, nil, ""
	return c
}

func (s *opsV3Stage) stageIndexDiff(prev, next []string) error {
	want := make(map[string]bool, len(next))
	for _, k := range next {
		want[k] = true
	}
	had := make(map[string]bool, len(prev))
	for _, k := range prev {
		had[k] = true
		if !want[k] {
			if err := s.b.Delete([]byte(k), nil); err != nil {
				return err
			}
		}
	}
	for _, k := range next {
		if !had[k] {
			if err := s.b.Set([]byte(k), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// flush stages opv3:meta:counts with this batch's delta applied. Call it once,
// last, before the commit.
func (s *opsV3Stage) flush() error {
	if !s.dirty {
		return nil
	}
	c, err := s.p.readOpCountsV3()
	if err != nil {
		return err
	}
	for k, d := range s.byState {
		c.ByState[k] += d
	}
	for k, d := range s.byDef {
		c.ByDef[k] += d
	}
	c.Total += s.total
	return s.p.stageOpCounts(s.b, c)
}

// stageOpCounts writes c with zero entries dropped, so a recomputation of the
// same counts is byte-identical.
func (p *PebbleStore) stageOpCounts(b *GatedBatch, c OpCounts) error {
	for k, v := range c.ByState {
		if v == 0 {
			delete(c.ByState, k)
		}
	}
	for k, v := range c.ByDef {
		if v == 0 {
			delete(c.ByDef, k)
		}
	}
	data, err := json.Marshal(&c)
	if err != nil {
		return err
	}
	return b.Set([]byte(opv3MetaCountsKey), data, nil)
}

func (p *PebbleStore) readOpCountsV3() (OpCounts, error) {
	c := OpCounts{ByState: map[state.V3]int64{}, ByDef: map[string]int64{}}
	if err := p.pebbleGetJSON([]byte(opv3MetaCountsKey), &c); err != nil {
		return c, err
	}
	if c.ByState == nil {
		c.ByState = map[state.V3]int64{}
	}
	if c.ByDef == nil {
		c.ByDef = map[string]int64{}
	}
	return c, nil
}

// errOpsV3Undecodable is wrapped by every error that says stored bytes (an
// opv3 run record half, or an opv2:op: row) do not decode, as opposed to a
// failed read. The migration and the boot reconcile count and skip a run
// whose bytes do not decode, and stop on any other error.
var errOpsV3Undecodable = errors.New("does not decode")

// isJSONDecodeError reports whether err is json.Unmarshal rejecting the
// bytes (as opposed to the read that fetched them failing).
func isJSONDecodeError(err error) bool {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	return errors.As(err, &syn) || errors.As(err, &typ)
}

// loadRunV3 reads a stored record. A missing record is (nil, nil). A record
// that does not decode is reported as an error wrapping errOpsV3Undecodable:
// overwriting it would orphan its index keys, whose state is unknown.
func (p *PebbleStore) loadRunV3(id string) (rec *RunRecord, err error) {
	defer recoverPebbleClosed("loadRunV3", &err)
	val, closer, err := p.db.Get(opv3RunKey(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r RunRecord
	uerr := json.Unmarshal(val, &r)
	_ = closer.Close()
	if uerr != nil {
		return nil, fmt.Errorf("opv3: run record %s %w: %v", id, errOpsV3Undecodable, uerr)
	}
	if r.ID == "" {
		return nil, fmt.Errorf("opv3: run record %s %w: it has no id", id, errOpsV3Undecodable)
	}
	var hot runHot
	found, err := p.getJSONFound(opv3ProgKey(id), &hot)
	if isJSONDecodeError(err) {
		return nil, fmt.Errorf("opv3: progress of run %s %w: %v", id, errOpsV3Undecodable, err)
	}
	if err != nil {
		return nil, err
	}
	if found {
		r.Progress, r.LastProgressAt, r.LastCheckpointAt, r.V2RowHash = hot.Progress, hot.LastProgressAt, hot.LastCheckpointAt, hot.V2RowHash
	}
	return &r, nil
}

// ── the v2 façade projection ────────────────────────────────────────────────

var (
	// opsV3UnmappedLogged / opsV3OffTableLogged log an unmapped v2 status or
	// an off-table v2 move once per process per value.
	opsV3UnmappedLogged sync.Map
	opsV3OffTableLogged sync.Map
)

// stageRunV3FromV2 stages the v3 projection of one opv2:op: row write into s.
// data is the exact value staged under opv2:op:{row.ID}; its hash becomes the
// record's V2RowHash, which is how the boot reconcile (ReconcileOpsV3WithV2)
// knows the record reflects this row. Caller holds opsMu. A record that does
// not decode is left alone (logged): the v2 write must not fail on v3
// trouble, and overwriting it would orphan its index keys.
func (p *PebbleStore) stageRunV3FromV2(s *opsV3Stage, row *OperationV2Row, data []byte) error {
	cur, err := p.loadRunV3(row.ID)
	if err != nil {
		opsV3Log.Warn("opv3: not mirroring a v2 write onto an unreadable run record (op %s): %v", row.ID, err)
		return nil
	}
	// An interrupted run with a chunk ledger mirrors as interrupted_dropped
	// (ToV2), so the "same status family" test below must know about the
	// ledger, or the next v2 write on that row would read as a drop.
	curHasLedger := false
	if cur != nil && cur.State == state.V3Interrupted {
		if curHasLedger, err = p.hasKey(opv3LedgerKey(cur.ID)); err != nil {
			return err
		}
	}
	next, tr := projectRunFromV2(cur, curHasLedger, row, time.Now().UTC())
	next.V2RowHash = opsV3RowHash(data)
	return s.put(cur, next, tr)
}

// stageRunV3DeleteForV2 stages the removal of the v3 record of a v2 row being
// deleted (DeleteOperationV2, SweepHollowOperationsV2).
func (p *PebbleStore) stageRunV3DeleteForV2(s *opsV3Stage, id string) error {
	cur, err := p.loadRunV3(id)
	if err != nil {
		if !errors.Is(err, errOpsV3Undecodable) {
			return err
		}
		// Every key named by the id alone goes: both record halves, the
		// checkpoint, the ledger and the per-run families. The index keys
		// (ix:state, ix:def, ix:done, ix:parent, q) and the counters embed
		// fields only the record knew (state, def, queued/completed time,
		// parent, priority), so they cannot be derived and are left; the
		// listings skip an index key whose record is gone.
		opsV3Log.Warn("opv3: deleting an unreadable run record with its v2 row (op %s); its index keys cannot be derived and stay: %v", id, err)
		for _, k := range [][]byte{opv3RunKey(id), opv3ProgKey(id), opv3CkptKey(id), opv3LedgerKey(id)} {
			if derr := s.b.Delete(k, nil); derr != nil {
				return derr
			}
		}
		for _, pfx := range opv3RunScopedPrefixes(id) {
			if derr := s.b.DeleteRange(pfx, prefixEnd(pfx), nil); derr != nil {
				return derr
			}
		}
		return nil
	}
	if cur == nil {
		return nil
	}
	return s.del(cur)
}

// v2StatusOf is the v2 status a stored record mirrors as (hasLedger: the run
// has an opv3:ledger: key).
func v2StatusOf(r *RunRecord, hasLedger bool) string {
	st, _ := state.ToV2(r.State, r.WaitReason, r.InterruptedWhy, r.SuccessorID != "", hasLedger, r.Error)
	return st
}

// projectRunFromV2 returns the record a v2 row write leaves, and the audit
// row for it (nil when the v3 state did not change). cur is the stored record
// (nil: none); curHasLedger says whether it has a chunk ledger.
//
// When the v2 status is the one cur already mirrors as, cur's v3 state is
// kept: a stopping zombie's progress tick still reads "running" in v2, a
// timed_out run's row reads "canceled", and neither may be overwritten by the
// forward map.
func projectRunFromV2(cur *RunRecord, curHasLedger bool, row *OperationV2Row, now time.Time) (*RunRecord, *Transition) {
	var next RunRecord
	if cur != nil {
		next = *cur
	}
	fillRunFromV2Row(&next, row)
	if cur != nil && v2StatusOf(cur, curHasLedger) == row.Status {
		return &next, nil
	}

	from := state.V3("")
	if cur != nil {
		from = cur.State
	}
	target, why, ok := state.FromV2Live(row.Status, next.Error)
	next.State = target
	next.WaitReason, next.InterruptedWhy = "", ""
	switch target {
	case state.V3Queued:
		next.WaitReason = why
	case state.V3Interrupted:
		next.InterruptedWhy = why
	}
	next.UnmappedV2Status = ""
	if !ok {
		next.UnmappedV2Status = row.Status
		if _, seen := opsV3UnmappedLogged.LoadOrStore(row.Status, true); !seen {
			opsV3Log.Warn("opv3: v2 status %q is not in the v2->v3 mapping table; recording the run as failed (logged once per status)", row.Status)
		}
	}
	if target != state.V3Stopping {
		next.Zombie = false
		// A stop reason describes stopping, and survives into the state the
		// stop ended in (canceled, timed_out, interrupted) as its cause.
		if from != state.V3Stopping || !isStopOutcome(target) {
			next.StopReason = ""
		}
	}
	if target == state.V3Running && from != state.V3Running && from != state.V3Stopping {
		next.Attempt++
		next.FenceEpoch++
	}
	if target != state.V3Dropped {
		next.SuccessorID = ""
	}

	if from == target {
		// Same state, different reason. Only queued{reason} -> queued{} is a
		// transition (wait_cleared); any other reason change is recorded on
		// the record without an event.
		if target == state.V3Queued && cur.WaitReason != "" && next.WaitReason == "" {
			return &next, &Transition{From: from, To: target, Event: string(state.EvWaitCleared), Reason: cur.WaitReason, At: now}
		}
		return &next, nil
	}
	ev, stopReason, found := state.InferEvent(from, target)
	if !found {
		ev = state.EvV2Write
		pair := string(from) + "->" + string(target)
		if _, seen := opsV3OffTableLogged.LoadOrStore(pair, true); !seen {
			opsV3Log.Info("opv3: v2 write moved a run %s, which no single v3 event does; recorded as %s (logged once per pair)", pair, state.EvV2Write)
		}
	}
	reason := stopReason
	switch target {
	case state.V3Queued:
		reason = next.WaitReason
	case state.V3Interrupted:
		reason = next.InterruptedWhy
	}
	if from == state.V3Stopping && isStopOutcome(target) && cur.StopReason != "" {
		reason = cur.StopReason
	}
	return &next, &Transition{From: from, To: target, Event: string(ev), Reason: reason, At: now}
}

// isStopOutcome reports whether s is a state a stopping run can end in.
func isStopOutcome(s state.V3) bool {
	return s == state.V3Canceled || s == state.V3TimedOut || s == state.V3Interrupted
}

// fillRunFromV2Row copies every field the v2 row carries onto r (not the
// state, which projectRunFromV2 decides). Typed progress beyond what v2 has
// (phases, chunks, workers, counters) is left as it was, and so are Done,
// Total and Current once the chunk ledger owns them (Chunks set).
func fillRunFromV2Row(r *RunRecord, row *OperationV2Row) {
	r.SchemaVersion = opv3RunSchemaVersion
	r.ID = row.ID
	r.DefID = row.DefID
	r.ParentID = derefStr(row.ParentID)
	r.Actor.UserID = derefStr(row.ActorUserID)
	r.Actor.AuthMethod = row.ActorAuthMethod
	r.Params = jsonOrString(row.Params)
	r.Priority = row.Priority
	r.TraceID = row.TraceID
	r.SpanID = row.SpanID
	if row.QueuedAt.IsZero() {
		r.QueuedAt = nil
	} else {
		q := row.QueuedAt
		r.QueuedAt = &q
	}
	r.StartedAt = copyTime(row.StartedAt)
	r.CompletedAt = copyTime(row.CompletedAt)
	r.LastProgressAt = copyTime(row.LastProgressAt)
	r.LastCheckpointAt = copyTime(row.LastCheckpointAt)
	r.Error = derefStr(row.ErrorMessage)
	if row.ResultData != nil {
		r.Result = jsonOrString(*row.ResultData)
	} else {
		r.Result = nil
	}
	r.Progress.Phase = derefStr(row.CurrentPhase)
	if r.Progress.Chunks != nil {
		// A chunk-ledger run (05-PR5 Batch runner): its progress has ONE
		// writer, the runner's ledger goroutine (CommitLedgerV3). The v2
		// row's numbers lag it (the v2 reporter flushes lazily), so a v2
		// write must not move Done, Total or Current back.
		return
	}
	r.Progress.Done = int64(row.ProgressCurrent)
	if row.ProgressTotal > 0 {
		t := int64(row.ProgressTotal)
		r.Progress.Total = &t
	} else {
		r.Progress.Total = nil
	}
	r.Progress.Current = row.ProgressMessage
	if row.LastProgressAt != nil {
		r.Progress.UpdatedAt = *row.LastProgressAt
	}
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func copyTime(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	c := *t
	return &c
}

// jsonOrString keeps s as raw JSON when it is valid JSON, and encodes it as a
// JSON string otherwise, so a malformed v2 blob can never make the record
// unmarshalable. "" is nil.
func jsonOrString(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(append([]byte(nil), s...))
	}
	b, _ := json.Marshal(s)
	return b
}

// ── the mirror (native writes → opv2:) ──────────────────────────────────────

// mirrorRowFromRecord builds the opv2:op: row a native write leaves. prev is
// the stored v2 row (nil: none); its fields the record does not own (plugin,
// resume counters, requirements, progress for a run the v2 registry reports)
// are kept.
func mirrorRowFromRecord(rec *RunRecord, prev *OperationV2Row, hasLedger bool, now time.Time) OperationV2Row {
	var row OperationV2Row
	if prev != nil {
		row = *prev
	} else {
		row = OperationV2Row{
			ID:              rec.ID,
			DefID:           rec.DefID,
			TraceID:         rec.TraceID,
			SpanID:          rec.SpanID,
			Priority:        rec.Priority,
			Params:          string(rec.Params),
			ActorAuthMethod: rec.Actor.AuthMethod,
			ProgressCurrent: int(rec.Progress.Done),
			ProgressMessage: rec.Progress.Current,
		}
		if rec.Progress.Total != nil {
			row.ProgressTotal = int(*rec.Progress.Total)
		}
		if rec.Progress.Phase != "" {
			ph := rec.Progress.Phase
			row.CurrentPhase = &ph
		}
		if rec.QueuedAt != nil {
			row.QueuedAt = *rec.QueuedAt
		}
		if rec.ParentID != "" {
			pid := rec.ParentID
			row.ParentID = &pid
		}
		if rec.Actor.UserID != "" {
			uid := rec.Actor.UserID
			row.ActorUserID = &uid
		}
	}
	status, msg := state.ToV2(rec.State, rec.WaitReason, rec.InterruptedWhy, rec.SuccessorID != "", hasLedger, rec.Error)
	row.Status = status
	if msg == "" {
		row.ErrorMessage = nil
	} else {
		row.ErrorMessage = &msg
	}
	row.StartedAt = copyTime(rec.StartedAt)
	row.CompletedAt = copyTime(rec.CompletedAt)
	if rec.Result != nil {
		rd := string(rec.Result)
		row.ResultData = &rd
	}
	// The v2 completed_at rules: terminal and interrupted_* rows carry a
	// stamp (every v2 write site of those statuses passes one), live ones do
	// not.
	switch {
	case status == string(state.Queued) || status == string(state.WaitingDeps) || status == string(state.Running):
		row.CompletedAt = nil
	case row.CompletedAt == nil && (state.IsTerminal(status) || state.IsInterrupted(status)):
		t := now
		row.CompletedAt = &t
	}
	return row
}

// stageV2QueueAct stages the opv2:q: and opv2:act: delta between prev and
// next the way the v2 status writers maintain them: a queue key exactly for a
// queued row, an active key exactly for a queued or running row (the meaning
// ListActiveOperationsV2 documents).
func stageV2QueueAct(b *GatedBatch, prev *OperationV2Row, next *OperationV2Row) error {
	prevQ := prev != nil && prev.Status == string(state.Queued)
	nextQ := next.Status == string(state.Queued)
	var prevKey []byte
	if prevQ {
		prevKey = opv2QueueKey(prev.Priority, prev.QueuedAt, prev.ID)
	}
	nextKey := opv2QueueKey(next.Priority, next.QueuedAt, next.ID)
	if prevQ && (!nextQ || !bytes.Equal(prevKey, nextKey)) {
		if err := b.Delete(prevKey, nil); err != nil {
			return err
		}
	}
	if nextQ && (!prevQ || !bytes.Equal(prevKey, nextKey)) {
		if err := b.Set(nextKey, []byte(next.ID), nil); err != nil {
			return err
		}
	}
	if nextQ || next.Status == string(state.Running) {
		return b.Set(opv2ActKey(next.ID), nil, nil)
	}
	return b.Delete(opv2ActKey(next.ID), nil)
}

// syncedV2Row returns the opv2:op: row a native write with the mirror on is
// about to replace or delete (nil: none), after checking that rec, the stored
// record, was last synced with exactly those bytes (rec.V2RowHash; "" for a
// run that has no row). A mismatch means a write the record never saw changed
// or removed the row -- a previous binary after a rollback, with the boot
// reconcile (ReconcileOpsV3WithV2) failed or not yet run -- so the record's
// state, timestamps, error and result are stale. Mirroring them would
// overwrite the newer row, re-create a row that binary deleted, or discard a
// run by a state it has left, so the write is refused with
// ErrStaleTransition and nothing is staged; every caller falls back to the
// v2 write path, which decides from the row. In steady state the check never
// fires: every writer stamps the hash of the bytes it stores
// (TestReconcileOpsV3NoWritesAfterEveryWriter). Caller holds opsMu.
func (p *PebbleStore) syncedV2Row(rec *RunRecord) (*OperationV2Row, error) {
	row, raw, err := p.getOpV2Raw(rec.ID)
	if err != nil && raw == nil {
		return nil, err // the read itself failed
	}
	have := ""
	if raw != nil {
		have = opsV3RowHash(raw)
	}
	if have != rec.V2RowHash {
		what := "was changed"
		if raw == nil {
			what = "was deleted"
		}
		return nil, fmt.Errorf("%w: run %s: its opv2 row %s by a write its opv3 record did not see (the boot reconcile has not repaired it); refusing to mirror a stale record over it",
			ErrStaleTransition, rec.ID, what)
	}
	if err != nil {
		// Unreachable while the hashes agree: no writer stamps the hash of
		// bytes it did not marshal from a row.
		return nil, err
	}
	return row, nil
}

// stageMirror stages the opv2: row for rec (and its queue/active/timeline
// keys) when the mirror is on, and sets rec.V2RowHash to the hash of the row
// bytes it staged, so call it before staging rec, while rec.V2RowHash is
// still the stored record's (syncedV2Row checks it). With the mirror off rec
// keeps the hash it had: the opv2 row it names is unchanged. Caller holds
// opsMu.
func (p *PebbleStore) stageMirror(b *GatedBatch, rec *RunRecord, now time.Time) error {
	if !p.opsV2MirrorEnabled() {
		return nil
	}
	prev, err := p.syncedV2Row(rec)
	if err != nil {
		return err
	}
	hasLedger, err := p.hasKey(opv3LedgerKey(rec.ID))
	if err != nil {
		return err
	}
	row := mirrorRowFromRecord(rec, prev, hasLedger, now)
	data, err := stageOpV2RowWrite(b, prev, &row)
	if err != nil {
		return err
	}
	rec.V2RowHash = opsV3RowHash(data)
	return stageV2QueueAct(b, prev, &row)
}

func (p *PebbleStore) hasKey(key []byte) (ok bool, err error) {
	defer recoverPebbleClosed("hasKey", &err)
	_, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_ = closer.Close()
	return true, nil
}

// ── native writers ──────────────────────────────────────────────────────────

// InsertRunV3 creates a queued run (event enqueue), its indexes, counts and,
// with the mirror on, its opv2: row, in one batch. An existing run or v2 row
// with the same id is refused.
func (p *PebbleStore) InsertRunV3(rec RunRecord) (err error) {
	defer recoverPebbleClosed("InsertRunV3", &err)
	if rec.ID == "" {
		return errors.New("opv3: InsertRunV3: run has no id")
	}
	if rec.State == "" {
		rec.State = state.V3Queued
	}
	if to, err := state.Next("", state.EvEnqueue, ""); err != nil || rec.State != to {
		return fmt.Errorf("opv3: InsertRunV3: a run is created queued, not %q", rec.State)
	}
	if err := validWaitReason(rec.WaitReason); err != nil {
		return err
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(rec.ID)
	if err != nil {
		return err
	}
	if cur != nil {
		return fmt.Errorf("opv3: InsertRunV3: run %s already exists", rec.ID)
	}
	if v2, err := p.GetOperationV2(rec.ID); err != nil || v2 != nil {
		return fmt.Errorf("opv3: InsertRunV3: an opv2 row %s already exists (err=%v)", rec.ID, err)
	}
	now := time.Now().UTC()
	if rec.QueuedAt == nil {
		rec.QueuedAt = &now
	}
	rec.CompletedAt, rec.StartedAt = nil, nil
	rec.EventSeq = 0
	b := p.db.NewBatch()
	defer b.Close()
	s := p.newOpsV3Stage(b)
	tr := &Transition{From: "", To: state.V3Queued, Event: string(state.EvEnqueue), Reason: rec.WaitReason, Actor: actorPtr(rec.Actor), At: now}
	rec.V2RowHash = ""
	if err := p.stageMirror(b, &rec, now); err != nil { // before put: it sets rec.V2RowHash
		return err
	}
	if err := s.put(nil, &rec, tr); err != nil {
		return err
	}
	if err := s.flush(); err != nil {
		return err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

func actorPtr(a Actor) *Actor {
	if a == (Actor{}) {
		return nil
	}
	return &a
}

func validWaitReason(w string) error {
	switch w {
	case "", state.WaitDeps, state.WaitReady, state.WaitPause:
		return nil
	}
	return fmt.Errorf("opv3: unknown wait reason %q", w)
}

// TransitionRunV3 moves one run through the closed transition table as a
// compare-and-set on (State, Attempt, FenceEpoch). See OpsV3RunWriter.
func (p *PebbleStore) TransitionRunV3(req TransitionRequest) (out *RunRecord, err error) {
	defer recoverPebbleClosed("TransitionRunV3", &err)
	if req.ID == "" {
		return nil, errors.New("opv3: TransitionRunV3: no id")
	}
	if req.Event == state.EvEnqueue {
		return nil, errors.New("opv3: TransitionRunV3: enqueue creates a run; use InsertRunV3")
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(req.ID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("%w: %s", ErrRunNotFoundV3, req.ID)
	}
	if cur.State != req.ExpectState || cur.Attempt != req.ExpectAttempt || cur.FenceEpoch != req.ExpectFenceEpoch {
		return nil, fmt.Errorf("%w: run %s is %s attempt %d epoch %d, request expected %s attempt %d epoch %d",
			ErrStaleTransition, req.ID, cur.State, cur.Attempt, cur.FenceEpoch, req.ExpectState, req.ExpectAttempt, req.ExpectFenceEpoch)
	}
	to, err := state.Next(cur.State, req.Event, req.StopReason)
	if err != nil {
		return nil, err
	}
	switch req.Event {
	case state.EvWaitCleared:
		if cur.WaitReason == "" {
			return nil, fmt.Errorf("%w: run %s is queued with no wait reason", state.ErrTransition, req.ID)
		}
	case state.EvDispatch:
		if cur.WaitReason != "" {
			return nil, fmt.Errorf("%w: run %s waits (%s) and cannot be dispatched", state.ErrTransition, req.ID, cur.WaitReason)
		}
	}
	if err := validWaitReason(req.WaitReason); err != nil {
		return nil, err
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	next := *cur
	next.State = to
	reason := ""
	switch to {
	case state.V3Queued:
		next.WaitReason = req.WaitReason
		if req.Event == state.EvWaitCleared {
			reason = cur.WaitReason
			next.WaitReason = ""
		} else {
			reason = next.WaitReason
		}
		next.CompletedAt = nil
		next.StartedAt = nil
		next.Error = ""
		next.InterruptedWhy, next.StopReason, next.SuccessorID = "", "", ""
		next.Zombie = false
	case state.V3Running:
		next.WaitReason = ""
		next.Attempt++
		next.FenceEpoch++
		next.StartedAt = &now
		next.CompletedAt = nil
	case state.V3Stopping:
		next.StopReason = req.StopReason
		next.Zombie = req.Zombie
		reason = req.StopReason
	default:
		// Interrupted, awaiting_decision and every terminal state.
		next.Zombie = false
		if next.CompletedAt == nil {
			next.CompletedAt = &now
		}
		if req.Error != "" {
			next.Error = req.Error
		}
		if req.Result != nil {
			next.Result = req.Result
		}
		switch to {
		case state.V3Interrupted:
			why := req.InterruptedWhy
			if why == "" {
				switch {
				case req.Event == state.EvBootSweep:
					why = state.WhyCrash
				case req.Event == state.EvGoroutineReturned:
					why = state.WhyQuiesced
				}
			}
			next.InterruptedWhy = why
			reason = why
		case state.V3Dropped:
			next.SuccessorID = req.SuccessorID
		}
		if req.Event == state.EvGoroutineReturned {
			reason = cur.StopReason
		}
	}
	if req.Approval != nil {
		next.Approval = req.Approval
	}
	tr := &Transition{From: cur.State, To: to, Event: string(req.Event), Reason: reason, Actor: req.Actor, At: now}

	b := p.db.NewBatch()
	defer b.Close()
	s := p.newOpsV3Stage(b)
	if err := p.stageMirror(b, &next, now); err != nil { // before put: it sets next.V2RowHash
		return nil, err
	}
	if err := s.put(cur, &next, tr); err != nil {
		return nil, err
	}
	if err := s.flush(); err != nil {
		return nil, err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return nil, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return nil, err
	}
	return &next, nil
}

// DeleteRunV3 discards a run. See OpsV3RunWriter.
func (p *PebbleStore) DeleteRunV3(id string, allowed []state.V3) (deleted bool, err error) {
	defer recoverPebbleClosed("DeleteRunV3", &err)
	if id == "" {
		return false, nil
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(id)
	if err != nil || cur == nil {
		return false, err
	}
	ok := false
	for _, s := range allowed {
		if s == cur.State {
			ok = true
			break
		}
	}
	if !ok {
		return false, nil
	}
	b := p.db.NewBatch()
	defer b.Close()
	s := p.newOpsV3Stage(b)
	if err := s.del(cur); err != nil {
		return false, err
	}
	if p.opsV2MirrorEnabled() {
		// allowed was checked against the record's state; refuse when the
		// row has moved on without it (syncedV2Row), or the discard could
		// delete a run a previous binary has since re-run.
		row, gerr := p.syncedV2Row(cur)
		if gerr != nil {
			return false, fmt.Errorf("opv3: DeleteRunV3: %w", gerr)
		}
		if row != nil {
			if err := stageOpV2RowDelete(b, row); err != nil {
				return false, err
			}
		}
	}
	if err := s.flush(); err != nil {
		return false, err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return false, err
	}
	if err := b.Commit(pebble.Sync); err != nil {
		return false, err
	}
	return true, nil
}

// ── readers ─────────────────────────────────────────────────────────────────

// GetRunV3 returns one run, or nil for an unknown id.
func (p *PebbleStore) GetRunV3(id string) (*RunRecord, error) {
	return p.loadRunV3(id)
}

// OpCountsV3 returns opv3:meta:counts.
func (p *PebbleStore) OpCountsV3() (OpCounts, error) {
	return p.readOpCountsV3()
}

// scanKeys calls fn for every key in [lower, upper) in order until fn returns
// false. Values are not decoded.
func (p *PebbleStore) scanKeys(lower, upper []byte, fn func(k []byte) bool) (err error) {
	defer recoverPebbleClosed("scanKeys", &err)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return err
	}
	for iter.First(); iter.Valid(); iter.Next() {
		if !fn(iter.Key()) {
			break
		}
	}
	iterErr := iter.Error()
	if cerr := iter.Close(); cerr != nil && iterErr == nil {
		iterErr = cerr
	}
	return iterErr
}

// lastSegment is the text after the last ':' (the op id of an index key).
func lastSegment(k []byte) string {
	if i := bytes.LastIndexByte(k, ':'); i >= 0 {
		return string(k[i+1:])
	}
	return string(k)
}

// runsFromIndex pages the records named by the last segment of each index
// key under prefix, after cursor (an index key), up to limit. Total is exact:
// with no keep filter it is the number of keys under prefix (a key-only
// walk); with one it is the number of records keep accepts, which needs every
// record under prefix read.
func (p *PebbleStore) runsFromIndex(prefix []byte, cursor string, limit int, keep func(*RunRecord) bool) (RunPage, error) {
	if limit <= 0 {
		limit = opv3DefaultPage
	}
	var page RunPage
	if cursor != "" && !strings.HasPrefix(cursor, string(prefix)) {
		return page, fmt.Errorf("opv3: cursor %q does not belong to this listing", cursor)
	}
	type ref struct{ key, id string }
	var refs []ref
	if err := p.scanKeys(prefix, prefixEnd(prefix), func(k []byte) bool {
		refs = append(refs, ref{string(k), lastSegment(k)})
		return true
	}); err != nil {
		return page, err
	}
	var lastKey string
	for _, r := range refs {
		inPage := cursor == "" || r.key > cursor
		if keep == nil {
			page.Total++
			if !inPage || page.Next != "" {
				continue
			}
			if len(page.Runs) == limit {
				page.Next = lastKey
				continue
			}
		}
		rec, err := p.loadRunV3(r.id)
		if err != nil {
			return page, err
		}
		if rec == nil {
			continue
		}
		if keep != nil {
			if !keep(rec) {
				continue
			}
			page.Total++
			if !inPage || page.Next != "" {
				continue
			}
			if len(page.Runs) == limit {
				page.Next = lastKey
				continue
			}
		}
		page.Runs = append(page.Runs, *rec)
		lastKey = r.key
	}
	return page, nil
}

// ListRunsV3 is the census listing. With State it walks opv3:ix:state:, with
// DefID opv3:ix:def:, with neither every opv3:ix:state: key; with both it
// walks the def's index and keeps the runs in that state. Total is the exact
// number of matching runs, never the page length.
func (p *PebbleStore) ListRunsV3(q RunQuery) (RunPage, error) {
	switch {
	case q.DefID != "":
		// A def id containing ':' would also match the keys of a longer id
		// that extends it, so the def is checked on the record as well then.
		var keep func(*RunRecord) bool
		if q.State != "" || strings.Contains(q.DefID, ":") {
			keep = func(r *RunRecord) bool {
				return r.DefID == q.DefID && (q.State == "" || r.State == q.State)
			}
		}
		return p.runsFromIndex([]byte(opv3IxDefPrefix+q.DefID+":"), q.Cursor, q.Limit, keep)
	case q.State != "":
		return p.runsFromIndex([]byte(opv3IxStatePrefix+string(q.State)+":"), q.Cursor, q.Limit, nil)
	default:
		return p.runsFromIndex([]byte(opv3IxStatePrefix), q.Cursor, q.Limit, nil)
	}
}

// ListQueuedRunsV3 returns dispatchable runs (queued, no wait reason) in
// dispatch order.
func (p *PebbleStore) ListQueuedRunsV3(limit int) ([]RunRecord, error) {
	var ids []string
	prefix := []byte(opv3QueuePrefix)
	if err := p.scanKeys(prefix, prefixEnd(prefix), func(k []byte) bool {
		if limit > 0 && len(ids) >= limit {
			return false
		}
		ids = append(ids, lastSegment(k))
		return true
	}); err != nil {
		return nil, err
	}
	out := make([]RunRecord, 0, len(ids))
	for _, id := range ids {
		r, err := p.loadRunV3(id)
		if err != nil {
			return nil, err
		}
		if r != nil && r.State == state.V3Queued && r.WaitReason == "" {
			out = append(out, *r)
		}
	}
	return out, nil
}

// ListDoneRunsSinceV3 returns runs completed at or after since, oldest first.
func (p *PebbleStore) ListDoneRunsSinceV3(since time.Time, limit int) ([]RunRecord, bool, error) {
	if limit <= 0 {
		limit = opv3DefaultPage
	}
	lower := []byte(fmt.Sprintf("%s%020d", opv3IxDonePrefix, completedNanos(since)))
	prefix := []byte(opv3IxDonePrefix)
	var ids []string
	complete := true
	if err := p.scanKeys(lower, prefixEnd(prefix), func(k []byte) bool {
		if len(ids) == limit {
			complete = false
			return false
		}
		ids = append(ids, lastSegment(k))
		return true
	}); err != nil {
		return nil, false, err
	}
	out := make([]RunRecord, 0, len(ids))
	for _, id := range ids {
		r, err := p.loadRunV3(id)
		if err != nil {
			return nil, false, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, complete, nil
}

// ListChildRunsV3 returns a pipeline's children.
func (p *PebbleStore) ListChildRunsV3(parentID string) ([]RunRecord, error) {
	prefix := []byte(opv3IxParentPrefix + parentID + ":")
	var ids []string
	if err := p.scanKeys(prefix, prefixEnd(prefix), func(k []byte) bool {
		ids = append(ids, lastSegment(k))
		return true
	}); err != nil {
		return nil, err
	}
	var out []RunRecord
	for _, id := range ids {
		r, err := p.loadRunV3(id)
		if err != nil {
			return nil, err
		}
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

// iterJSON decodes every value in [prefix, prefixEnd) into a fresh T, in key
// order, or the last `last` of them when last > 0.
func iterJSON[T any](p *PebbleStore, prefix []byte, last int, withKey func(*T, []byte)) (out []T, err error) {
	defer recoverPebbleClosed("iterJSON", &err)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	if last > 0 {
		for iter.Last(); iter.Valid() && len(out) < last; iter.Prev() {
			var v T
			if err := json.Unmarshal(iter.Value(), &v); err != nil {
				return nil, fmt.Errorf("opv3: %s does not decode: %w", iter.Key(), err)
			}
			if withKey != nil {
				withKey(&v, iter.Key())
			}
			out = append(out, v)
		}
		for a, b := 0, len(out)-1; a < b; a, b = a+1, b-1 {
			out[a], out[b] = out[b], out[a]
		}
		return out, iter.Error()
	}
	for iter.First(); iter.Valid(); iter.Next() {
		var v T
		if err := json.Unmarshal(iter.Value(), &v); err != nil {
			return nil, fmt.Errorf("opv3: %s does not decode: %w", iter.Key(), err)
		}
		if withKey != nil {
			withKey(&v, iter.Key())
		}
		out = append(out, v)
	}
	return out, iter.Error()
}

// ListRunEventsV3 returns a run's transitions in order; with limit > 0 the
// last limit of them.
func (p *PebbleStore) ListRunEventsV3(id string, limit int) ([]Transition, error) {
	return iterJSON[Transition](p, []byte(opv3EvtPrefix+id+":"), limit, nil)
}

// ── checkpoint, snapshot, ledger ────────────────────────────────────────────

// SaveCheckpointV3 replaces a run's checkpoint and stamps the record's
// last_checkpoint_at, in one batch.
func (p *PebbleStore) SaveCheckpointV3(id string, ck Checkpoint) (err error) {
	defer recoverPebbleClosed("SaveCheckpointV3", &err)
	if ck.SavedAt.IsZero() {
		ck.SavedAt = time.Now().UTC()
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: %s", ErrRunNotFoundV3, id)
	}
	data, err := json.Marshal(&ck)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(opv3CkptKey(id), data, nil); err != nil {
		return err
	}
	next := *cur
	at := ck.SavedAt
	next.LastCheckpointAt = &at
	if err := p.newOpsV3Stage(b).put(cur, &next, nil); err != nil {
		return err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// GetCheckpointV3 returns a run's checkpoint, or nil.
func (p *PebbleStore) GetCheckpointV3(id string) (*Checkpoint, error) {
	var ck Checkpoint
	found, err := p.getJSONFound(opv3CkptKey(id), &ck)
	if err != nil || !found {
		return nil, err
	}
	return &ck, nil
}

// getJSONFound is pebbleGetJSON that also reports whether the key exists.
func (p *PebbleStore) getJSONFound(key []byte, dst any) (found bool, err error) {
	defer recoverPebbleClosed("getJSONFound", &err)
	val, closer, err := p.db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer closer.Close()
	return true, json.Unmarshal(val, dst)
}

// openRunV3 refuses a per-run write (snapshot pages, chunk offsets, intents)
// for a run that does not exist or is terminal. AppendLogV3 does not use it:
// a log line is accepted on a terminal run. The caller holds opsMu and keeps holding it through the write, so
// neither a terminal transition (which deletes the run's snapshot and ledger
// in its batch, under opsMu) nor a discard (DeleteRunV3, which also deletes
// its intents and log) can land between this check and the write and leave a
// per-run key no pass ever cleans up. Intents are deliberately NOT deleted at
// a terminal transition: an intent left by a crashed write is the "may be
// partially applied" evidence and must outlive the run's end until a discard.
// Same errors as CommitLedgerV3.
func (p *PebbleStore) openRunV3(id string) error {
	cur, err := p.loadRunV3(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: %s", ErrRunNotFoundV3, id)
	}
	if state.ClassifyV3(cur.State).Terminal {
		return fmt.Errorf("%w: run %s is %s; its per-run keys are closed", ErrStaleTransition, id, cur.State)
	}
	return nil
}

// PutSnapshotPageV3 stores one page of frozen item keys. Refused for a
// missing or terminal run (openRunV3).
func (p *PebbleStore) PutSnapshotPageV3(id string, page int, keys []string) (err error) {
	defer recoverPebbleClosed("PutSnapshotPageV3", &err)
	if keys == nil {
		keys = []string{}
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if err := p.openRunV3(id); err != nil {
		return err
	}
	return p.pebbleSetJSON(opv3SnapKey(id, page), keys)
}

// GetSnapshotPageV3 returns one page of frozen item keys.
func (p *PebbleStore) GetSnapshotPageV3(id string, page int) ([]string, bool, error) {
	var keys []string
	found, err := p.getJSONFound(opv3SnapKey(id, page), &keys)
	return keys, found, err
}

// PutChunkOffsetsV3 stores the chunk start offsets (only when PartitionBy
// adjusted them). Refused for a missing or terminal run (openRunV3).
func (p *PebbleStore) PutChunkOffsetsV3(id string, offsets []int32) (err error) {
	defer recoverPebbleClosed("PutChunkOffsetsV3", &err)
	if offsets == nil {
		offsets = []int32{}
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if err := p.openRunV3(id); err != nil {
		return err
	}
	return p.pebbleSetJSON(opv3SnapChunksKey(id), offsets)
}

// GetChunkOffsetsV3 returns the stored chunk start offsets.
func (p *PebbleStore) GetChunkOffsetsV3(id string) ([]int32, bool, error) {
	var offsets []int32
	found, err := p.getJSONFound(opv3SnapChunksKey(id), &offsets)
	return offsets, found, err
}

// ListMarksV3 returns the run's opv3:mark: keys by name, in key order.
func (p *PebbleStore) ListMarksV3(id string) (marks map[string][]string, err error) {
	defer recoverPebbleClosed("ListMarksV3", &err)
	pfx := opv3MarkRunPrefix(id)
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: pfx, UpperBound: prefixEnd(pfx)})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	marks = map[string][]string{}
	for iter.First(); iter.Valid(); iter.Next() {
		name, key, ok := strings.Cut(string(iter.Key()[len(pfx):]), "\x00")
		if !ok {
			return nil, fmt.Errorf("opv3: malformed mark key %q", iter.Key())
		}
		marks[name] = append(marks[name], key)
	}
	return marks, iter.Error()
}

// GetLedgerV3 returns a run's ledger, or nil.
func (p *PebbleStore) GetLedgerV3(id string) (*Ledger, error) {
	var l Ledger
	found, err := p.getJSONFound(opv3LedgerKey(id), &l)
	if err != nil || !found {
		return nil, err
	}
	return &l, nil
}

// CommitLedgerV3 writes the ledger, the lease table (inside the checkpoint),
// the run's progress snapshot and any item outcomes in ONE batch. A run that
// is already terminal is refused: its ledger was deleted with the terminal
// transition and must not come back.
func (p *PebbleStore) CommitLedgerV3(id string, lb LedgerBatch) (err error) {
	defer recoverPebbleClosed("CommitLedgerV3", &err)
	now := time.Now().UTC()
	// Encode before taking opsMu: the ledger is the caller's own copy, and
	// every other opv3 writer waits on the lock.
	if lb.Ledger.UpdatedAt.IsZero() {
		lb.Ledger.UpdatedAt = now
	}
	ledger, err := json.Marshal(&lb.Ledger)
	if err != nil {
		return err
	}
	for name := range lb.Marks {
		if name == "" || strings.ContainsRune(name, 0) {
			return fmt.Errorf("opv3: mark name %q is empty or holds a NUL", name)
		}
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: %s", ErrRunNotFoundV3, id)
	}
	if state.ClassifyV3(cur.State).Terminal {
		return fmt.Errorf("%w: run %s is %s; its ledger is closed", ErrStaleTransition, id, cur.State)
	}
	var ck Checkpoint
	if _, err := p.getJSONFound(opv3CkptKey(id), &ck); err != nil {
		return err
	}
	ck.Leases = lb.Leases
	ck.SavedAt = now
	ckData, err := json.Marshal(&ck)
	if err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := b.Set(opv3LedgerKey(id), ledger, nil); err != nil {
		return err
	}
	if err := b.Set(opv3CkptKey(id), ckData, nil); err != nil {
		return err
	}
	if lb.ResetMarks {
		// Before the Sets below: within one batch the later Set wins over
		// the range deletion.
		marks := opv3MarkRunPrefix(id)
		if err := b.DeleteRange(marks, prefixEnd(marks), nil); err != nil {
			return err
		}
	}
	for name, keys := range lb.Marks {
		for _, k := range keys {
			if err := b.Set(opv3MarkKey(id, name, k), nil, nil); err != nil {
				return err
			}
		}
	}
	next := *cur
	// The runner owns the numbers; the phase is the run's (rc.Phase writes
	// it through the v2 row), so a ledger commit without one keeps it.
	prog := lb.Progress
	if prog.Phase == "" {
		prog.Phase = cur.Progress.Phase
	}
	if prog.Phases == nil {
		prog.Phases = cur.Progress.Phases
	}
	next.Progress = prog
	next.LastProgressAt = &now
	if err := p.stageItemOutcomes(b, &next, lb.ItemRows); err != nil {
		return err
	}
	if err := p.newOpsV3Stage(b).put(cur, &next, nil); err != nil {
		return err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// ── intents ─────────────────────────────────────────────────────────────────

// PutIntentV3 writes a write-ahead intent. Refused for a missing or terminal
// run (openRunV3).
func (p *PebbleStore) PutIntentV3(id string, in Intent) (err error) {
	defer recoverPebbleClosed("PutIntentV3", &err)
	if in.WrittenAt.IsZero() {
		in.WrittenAt = time.Now().UTC()
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if err := p.openRunV3(id); err != nil {
		return err
	}
	return p.pebbleSetJSON(opv3IntentKey(id, in.Seq), &in)
}

// ClearIntentV3 deletes a committed intent. It does not sync. The Writer
// (internal/operations/opswriter) calls it only after the write committed and
// every history row describing it was recorded; any other outcome leaves the
// intent. Those writes are synced, and Pebble's WAL is ordered, so a crash
// that loses this delete only leaves an intent behind, and a leftover intent
// makes recovery report the subject as "may be partially applied" --
// cautious, never wrong in the direction that loses data. Skipping the fsync
// halves the synced writes of every Writer call.
func (p *PebbleStore) ClearIntentV3(id string, seq uint64) (err error) {
	defer recoverPebbleClosed("ClearIntentV3", &err)
	return p.db.Delete(opv3IntentKey(id, seq), pebble.NoSync)
}

// ListIntentsV3 returns a run's outstanding intents in seq order.
func (p *PebbleStore) ListIntentsV3(id string) ([]Intent, error) {
	return iterJSON[Intent](p, []byte(opv3IntentPrefix+id+":"), 0, nil)
}

// ── item outcomes ───────────────────────────────────────────────────────────

// stageItemOutcomes stages rows under opv3:item:{id}: and keeps the counts on
// r. Past the per-run cap a new row is counted as dropped instead of stored;
// re-putting an existing key overwrites it. Caller holds opsMu and stages r.
func (p *PebbleStore) stageItemOutcomes(b *GatedBatch, r *RunRecord, rows []ItemOutcome) error {
	if len(rows) == 0 {
		return nil
	}
	counts := ItemOutcomeCounts{}
	if r.ItemOutcomes != nil {
		counts = *r.ItemOutcomes
	}
	staged := map[string]bool{}
	for _, row := range rows {
		if row.Outcome == "" || row.Key == "" {
			return fmt.Errorf("opv3: item outcome needs a key and an outcome (got %+v)", row)
		}
		key := opv3ItemKey(r.ID, row.Outcome, row.Key)
		exists := staged[string(key)]
		if !exists {
			var err error
			if exists, err = p.hasKey(key); err != nil {
				return err
			}
		}
		if !exists && counts.Stored >= opv3ItemCap {
			counts.Dropped++
			continue
		}
		data, err := json.Marshal(&row)
		if err != nil {
			return err
		}
		if err := b.Set(key, data, nil); err != nil {
			return err
		}
		if !exists {
			counts.Stored++
			staged[string(key)] = true
		}
	}
	r.ItemOutcomes = &counts
	return nil
}

// PutItemOutcomesV3 stores failed and skipped items for a run.
func (p *PebbleStore) PutItemOutcomesV3(id string, rows []ItemOutcome) (err error) {
	defer recoverPebbleClosed("PutItemOutcomesV3", &err)
	if len(rows) == 0 {
		return nil
	}
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: %s", ErrRunNotFoundV3, id)
	}
	b := p.db.NewBatch()
	defer b.Close()
	next := *cur
	if err := p.stageItemOutcomes(b, &next, rows); err != nil {
		return err
	}
	if err := p.newOpsV3Stage(b).put(cur, &next, nil); err != nil {
		return err
	}
	if err := p.runOpsV2BeforeCommitHook(); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

// ListItemOutcomesV3 returns one page of a run's item outcomes.
func (p *PebbleStore) ListItemOutcomesV3(id string, q ItemQuery) (page ItemPage, err error) {
	defer recoverPebbleClosed("ListItemOutcomesV3", &err)
	limit := q.Limit
	if limit <= 0 {
		limit = opv3DefaultPage
	}
	prefix := []byte(opv3ItemPrefix + id + ":" + q.Outcome)
	if err := p.scanKeys(prefix, prefixEnd(prefix), func([]byte) bool {
		page.Total++
		return true
	}); err != nil {
		return page, err
	}
	if r, err := p.loadRunV3(id); err != nil {
		return page, err
	} else if r != nil && r.ItemOutcomes != nil {
		page.Truncated = r.ItemOutcomes.Dropped > 0
	}
	lower := prefix
	if q.Cursor != "" {
		if !strings.HasPrefix(q.Cursor, string(prefix)) {
			return page, fmt.Errorf("opv3: cursor %q does not belong to this listing", q.Cursor)
		}
		lower = append([]byte(q.Cursor), 0)
	}
	iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: prefixEnd(prefix)})
	if err != nil {
		return page, err
	}
	defer iter.Close()
	var last string
	for iter.First(); iter.Valid(); iter.Next() {
		if len(page.Rows) == limit {
			page.Next = last
			break
		}
		var row ItemOutcome
		if err := json.Unmarshal(iter.Value(), &row); err != nil {
			return page, fmt.Errorf("opv3: %s does not decode: %w", iter.Key(), err)
		}
		page.Rows = append(page.Rows, row)
		last = string(iter.Key())
	}
	return page, iter.Error()
}

// ── log ─────────────────────────────────────────────────────────────────────

// AppendLogV3 appends one log line to a run. Refused for a missing run (one
// never created, or discarded): a line landing after a discard would
// re-create a log key nothing deletes. A terminal run is accepted, unlike the
// other per-run writers (openRunV3): the terminal transition keeps the log
// (it deletes only the snapshot, chunk offsets and ledger), a discard is what
// deletes it, and a line written as a run finishes -- its last summary, a
// flush that lands just after the transition, a migrated v2 run -- is part
// of that run's record and must not be lost. opsMu is held through the write,
// so a discard cannot land between the check and the write.
func (p *PebbleStore) AppendLogV3(id string, line LogLine) (err error) {
	defer recoverPebbleClosed("AppendLogV3", &err)
	if line.At.IsZero() {
		line.At = time.Now().UTC()
	}
	line.Key = ""
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	cur, err := p.loadRunV3(id)
	if err != nil {
		return err
	}
	if cur == nil {
		return fmt.Errorf("%w: %s", ErrRunNotFoundV3, id)
	}
	return p.pebbleSetJSON(opv3LogKey(id, line.At, p.opsLogSeq.Add(1)), &line)
}

// logCursor is a position in the merged v2+v3 log order: (ts, family, seq),
// v2 lines (rank 0) before v3 lines (rank 1) at the same nanosecond.
type logCursor struct {
	ns   int64
	rank int
	seq  int64
}

// parseLogKey reads (ts, seq) from an opv2:log: or opv3:log: key of run id.
func parseLogKey(k, id string) (logCursor, bool) {
	var rest string
	var c logCursor
	switch {
	case strings.HasPrefix(k, "opv2:log:"+id+":"):
		rest = strings.TrimPrefix(k, "opv2:log:"+id+":")
	case strings.HasPrefix(k, opv3LogPrefix+id+":"):
		rest = strings.TrimPrefix(k, opv3LogPrefix+id+":")
		c.rank = 1
	default:
		return c, false
	}
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return c, false
	}
	ns, err1 := strconv.ParseInt(parts[0], 10, 64)
	seq, err2 := strconv.ParseInt(parts[1], 10, 64)
	if err1 != nil || err2 != nil {
		return c, false
	}
	c.ns, c.seq = ns, seq
	return c, true
}

func (a logCursor) less(b logCursor) bool {
	if a.ns != b.ns {
		return a.ns < b.ns
	}
	if a.rank != b.rank {
		return a.rank < b.rank
	}
	return a.seq < b.seq
}

// logSource is one family of a run's log, positioned for the merge.
type logSource struct {
	iter *GatedIter
	id   string
	rank int
	ok   bool
	cur  logCursor
}

func (ls *logSource) settle() {
	for ls.ok = ls.iter.Valid(); ls.ok; ls.ok = ls.iter.Valid() {
		if c, good := parseLogKey(string(ls.iter.Key()), ls.id); good {
			ls.cur = c
			return
		}
		ls.iter.Next()
	}
}

// ListLogV3 returns up to limit log lines after afterKey (limit <= 0: all),
// merging the run's opv2:log: lines (read-through: migration 065 does not
// copy them, and the v2 registry still logs there) with its opv3:log: lines
// in (time, v2-before-v3, seq) order. Each family is read from a seek
// position, so a page of a 300k-line scan log costs the page, not the log.
func (p *PebbleStore) ListLogV3(id string, limit int, afterKey string) (lines []LogLine, err error) {
	defer recoverPebbleClosed("ListLogV3", &err)
	prefixes := [2]string{"opv2:log:" + id + ":", opv3LogPrefix + id + ":"}
	lowers := [2]string{prefixes[0], prefixes[1]}
	if afterKey != "" {
		c, ok := parseLogKey(afterKey, id)
		if !ok {
			return nil, fmt.Errorf("opv3: log cursor %q does not belong to run %s", afterKey, id)
		}
		// The first key of each family that sorts after c in the merged order.
		at := func(rank int, ns, seq int64) string { return fmt.Sprintf("%s%020d:%010d", prefixes[rank], ns, seq) }
		if c.rank == 0 {
			lowers[0] = at(0, c.ns, c.seq+1)
			lowers[1] = at(1, c.ns, 0)
		} else {
			lowers[0] = at(0, c.ns+1, 0)
			lowers[1] = at(1, c.ns, c.seq+1)
		}
	}
	var srcs [2]*logSource
	for rank := range srcs {
		upper := prefixEnd([]byte(prefixes[rank]))
		iter, err := p.db.NewIter(&pebble.IterOptions{LowerBound: []byte(lowers[rank]), UpperBound: upper})
		if err != nil {
			return nil, err
		}
		defer iter.Close()
		srcs[rank] = &logSource{iter: iter, id: id, rank: rank}
		iter.First()
		srcs[rank].settle()
	}
	for limit <= 0 || len(lines) < limit {
		var s *logSource
		switch {
		case srcs[0].ok && srcs[1].ok:
			s = srcs[0]
			if srcs[1].cur.less(srcs[0].cur) {
				s = srcs[1]
			}
		case srcs[0].ok:
			s = srcs[0]
		case srcs[1].ok:
			s = srcs[1]
		}
		if s == nil {
			break
		}
		var line LogLine
		decoded := true
		if s.rank == 0 {
			var v2 OpLogV2Row
			if json.Unmarshal(s.iter.Value(), &v2) == nil {
				line = LogLine{At: v2.CreatedAt, Level: v2.Level, Message: v2.Message, Fields: attrsToFields(v2.Attrs)}
			} else {
				decoded = false
			}
		} else if json.Unmarshal(s.iter.Value(), &line) != nil {
			decoded = false
		}
		if decoded {
			line.Key = string(s.iter.Key())
			lines = append(lines, line)
		}
		s.iter.Next()
		s.settle()
	}
	for _, s := range srcs {
		if err := s.iter.Error(); err != nil {
			return nil, err
		}
	}
	return lines, nil
}

// attrsToFields flattens a v2 log row's JSON attrs into strings.
func attrsToFields(attrs string) map[string]string {
	if attrs == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(attrs), &m) != nil || len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
			continue
		}
		b, _ := json.Marshal(v)
		out[k] = string(b)
	}
	return out
}
