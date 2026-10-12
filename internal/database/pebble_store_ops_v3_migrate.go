// file: internal/database/pebble_store_ops_v3_migrate.go
// version: 1.0.0
// guid: e3dede70-3460-4dd5-8185-a94a66839551
// last-edited: 2026-10-11

// Migration 065's engine: build opv3: run records from opv2: operation rows
// (state-and-persistence.md sections 3 and 4). Every opv2: key is left in
// place; no op id changes.

package database

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/pebble/v2"

	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
)

const (
	// opsV3MigrationBatch is migration 065's rows per Pebble batch.
	opsV3MigrationBatch = 500
	// opsV3MigrationLogEvery is how often (in rows) the migration logs progress.
	opsV3MigrationLogEvery = 10000
	// opsV3SuccessorWindow is how close a newer run's queued_at must be to a
	// requeued row's completed_at to be taken as its successor.
	opsV3SuccessorWindow = time.Second
)

// OpsV3MigrationReport is what MigrateOpsV2ToV3 did.
type OpsV3MigrationReport struct {
	Scanned     int // decodable opv2:op: rows read
	Written     int // opv3:run: records written (new or re-derived)
	Skipped     int // already migrated with the same SourceHash
	SkippedLive int // already written by the live v2 façade (newer than this row)
	Undecodable int // opv2:op: rows (or opv3:run: records) that do not decode
	Batches     int // Pebble batches committed by the main pass
	// PerStatus counts rows by original v2 status; Unmapped counts the ones
	// the mapping table does not know ("anything else": recorded as failed).
	PerStatus map[string]int
	Unmapped  map[string]int
	// SuccessorsFilled / SuccessorsMissing: requeued rows whose successor was
	// (not) found by the 1 s queued_at heuristic.
	SuccessorsFilled  int
	SuccessorsMissing int
}

// opsV3SourceHash is the hash of a v2 row as the store encodes it: equal for
// an unchanged row, so a second pass skips it.
func opsV3SourceHash(row *OperationV2Row) (string, error) {
	data, err := json.Marshal(row)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// migratedRunRecord is the record migration 065 builds for row. cur is a
// record an earlier pass wrote for the same id from an older version of the
// row (re-derived; its attempt, fence epoch, event seq and successor carry
// over).
func migratedRunRecord(cur *RunRecord, row *OperationV2Row, hash string) (*RunRecord, string, bool) {
	var rec RunRecord
	if cur != nil {
		rec = *cur
	}
	fillRunFromV2Row(&rec, row)
	s, why, ok := state.FromV2(row.Status, rec.Error)
	rec.State = s
	rec.WaitReason, rec.InterruptedWhy, rec.StopReason, rec.Zombie = "", "", "", false
	switch s {
	case state.V3Queued:
		rec.WaitReason = why
	case state.V3Interrupted:
		rec.InterruptedWhy = why
	}
	if s != state.V3Dropped {
		rec.SuccessorID = ""
	}
	rec.UnmappedV2Status = ""
	if !ok {
		rec.UnmappedV2Status = row.Status
	}
	rec.FromV2 = &V2Origin{
		Status:      row.Status,
		SourceHash:  hash,
		HighWater:   int64(row.HighWaterProgress),
		ResumeCount: row.ResumeCount,
		ErrMessage:  rec.Error,
	}
	return &rec, why, ok
}

// MigrateOpsV2ToV3 builds an opv3:run: record, its indexes and one synthetic
// migrate event for every opv2:op: row, batchSize rows per Pebble batch, and
// leaves every opv2: key in place.
//
// Idempotent: a run already migrated from the same row (same SourceHash) is
// skipped, a run the live v2 façade has written since (no FromV2) is newer
// than the row and is skipped, and a run migrated from an older version of
// the row is re-derived. opv2:state: is copied into opv3:ckpt: as
// legacy_state; opv2:log: is not copied (ListLogV3 reads through).
//
// CONCURRENCY: deliberately one streaming writer in key order, not a worker
// pool (repo CLAUDE.md "Concurrency" rule, ordering case). Each batch is a
// read-then-write of run records and the shared opv3:meta:counts key under
// opsMu, the same lock every live ops writer takes, so a second writer would
// only queue on that lock; and the migration runs once, at boot, before the
// registry starts. The bounded cost is the 500-row batch: opsMu is held for
// one batch at a time and released between them, so a live writer is never
// stalled for the whole pass. Each page is read with a fresh key-only
// iterator that is closed before the batch commits.
func (p *PebbleStore) MigrateOpsV2ToV3(batchSize int, progress func(done, total int)) (rep OpsV3MigrationReport, err error) {
	defer recoverPebbleClosed("MigrateOpsV2ToV3", &err)
	if batchSize <= 0 {
		batchSize = opsV3MigrationBatch
	}
	rep.PerStatus = map[string]int{}
	rep.Unmapped = map[string]int{}

	// Key-only count for progress, page by page like the main pass.
	total := 0
	for after := ""; ; {
		ids, err := p.listOpV2IDsPage(after, opsV3MigrationLogEvery)
		if err != nil {
			return rep, err
		}
		if len(ids) == 0 {
			break
		}
		total += len(ids)
		after = ids[len(ids)-1]
	}
	opsV3Log.Info("opv3 migration: building run records from %d opv2 operation rows (%d per batch)", total, batchSize)

	var requeued []string
	done := 0
	nextLog := opsV3MigrationLogEvery
	for after := ""; ; {
		ids, err := p.listOpV2IDsPage(after, batchSize)
		if err != nil {
			return rep, err
		}
		if len(ids) == 0 {
			break
		}
		after = ids[len(ids)-1]
		more, err := p.migrateOpsV3Batch(ids, &rep)
		if err != nil {
			return rep, err
		}
		requeued = append(requeued, more...)
		rep.Batches++
		done += len(ids)
		if progress != nil {
			progress(done, total)
		}
		if done >= nextLog {
			opsV3Log.Info("opv3 migration: %d/%d rows (%d written, %d skipped)", done, total, rep.Written, rep.Skipped+rep.SkippedLive)
			nextLog += opsV3MigrationLogEvery
		}
	}

	if err := p.fillMigratedSuccessors(requeued, batchSize, &rep); err != nil {
		return rep, err
	}
	if err := p.rebuildOpCountsV3(); err != nil {
		return rep, err
	}
	opsV3Log.Info("opv3 migration: done: scanned %d, written %d, skipped %d (same row) + %d (live), undecodable %d, batches %d; successors filled %d, not found %d",
		rep.Scanned, rep.Written, rep.Skipped, rep.SkippedLive, rep.Undecodable, rep.Batches, rep.SuccessorsFilled, rep.SuccessorsMissing)
	opsV3Log.Info("opv3 migration: v2 status counts %v; anything else (recorded as failed) %v", rep.PerStatus, rep.Unmapped)
	return rep, nil
}

// migrateOpsV3Batch migrates one page of ids in one batch under opsMu and
// returns the ids of requeued rows still missing a successor.
func (p *PebbleStore) migrateOpsV3Batch(ids []string, rep *OpsV3MigrationReport) (requeued []string, err error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	now := time.Now().UTC()
	b := p.db.NewBatch()
	defer b.Close()
	s := p.newOpsV3Stage(b)
	for _, id := range ids {
		// Re-read under the lock: the page was listed without it.
		row, gerr := p.GetOperationV2(id)
		if gerr != nil {
			rep.Undecodable++
			opsV3Log.Warn("opv3 migration: opv2 row %s does not decode; not migrated: %v", id, gerr)
			continue
		}
		if row == nil {
			continue // deleted since the page was listed
		}
		rep.Scanned++
		rep.PerStatus[row.Status]++
		hash, err := opsV3SourceHash(row)
		if err != nil {
			return nil, err
		}
		cur, err := p.loadRunV3(id)
		if err != nil {
			rep.Undecodable++
			opsV3Log.Warn("opv3 migration: run record %s does not decode; left alone: %v", id, err)
			continue
		}
		switch {
		case cur != nil && cur.FromV2 == nil:
			rep.SkippedLive++
			continue
		case cur != nil && cur.FromV2.SourceHash == hash:
			rep.Skipped++
			continue
		}
		rec, why, ok := migratedRunRecord(cur, row, hash)
		if !ok {
			rep.Unmapped[row.Status]++
		}
		from := state.V3("")
		if cur != nil {
			from = cur.State
		}
		reason := why
		if why == "" && !ok {
			reason = "v2 status " + row.Status
		}
		tr := &Transition{From: from, To: rec.State, Event: string(state.EvMigrate), Reason: reason, At: now}
		if err := s.put(cur, rec, tr); err != nil {
			return nil, err
		}
		if err := p.stageLegacyCheckpoint(b, id, now); err != nil {
			return nil, err
		}
		rep.Written++
		if rec.State == state.V3Dropped && why == state.WhyRequeued && rec.SuccessorID == "" {
			requeued = append(requeued, id)
		}
	}
	if err := s.flush(); err != nil {
		return nil, err
	}
	return requeued, b.Commit(pebble.Sync)
}

// stageLegacyCheckpoint copies opv2:state:{id} into opv3:ckpt:{id} as
// legacy_state bytes, keeping any v3 checkpoint fields already there.
func (p *PebbleStore) stageLegacyCheckpoint(b *GatedBatch, id string, now time.Time) error {
	raw, closer, err := p.db.Get(opv2StateKey(id))
	if errors.Is(err, pebble.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	legacy := append([]byte(nil), raw...)
	_ = closer.Close()
	var ck Checkpoint
	if _, err := p.getJSONFound(opv3CkptKey(id), &ck); err != nil {
		return err
	}
	ck.LegacyState = legacy
	if ck.SchemaVersion == 0 {
		ck.SchemaVersion = 1
	}
	if ck.SavedAt.IsZero() {
		ck.SavedAt = now
	}
	data, err := json.Marshal(&ck)
	if err != nil {
		return err
	}
	return b.Set(opv3CkptKey(id), data, nil)
}

// fillMigratedSuccessors sets SuccessorID on migrated requeue rows. The v2
// requeue wrote the successor's id only to a log line (resume.go), so the
// successor is the newer run of the same def queued within 1 s of this row's
// completed_at; a row with no such run keeps an empty SuccessorID and is
// counted.
func (p *PebbleStore) fillMigratedSuccessors(ids []string, batchSize int, rep *OpsV3MigrationReport) error {
	for start := 0; start < len(ids); start += batchSize {
		end := min(start+batchSize, len(ids))
		if err := p.fillSuccessorBatch(ids[start:end], rep); err != nil {
			return err
		}
	}
	return nil
}

func (p *PebbleStore) fillSuccessorBatch(ids []string, rep *OpsV3MigrationReport) error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	b := p.db.NewBatch()
	defer b.Close()
	s := p.newOpsV3Stage(b)
	for _, id := range ids {
		rec, err := p.loadRunV3(id)
		if err != nil {
			return err
		}
		if rec == nil || rec.State != state.V3Dropped || rec.SuccessorID != "" {
			continue
		}
		succ, err := p.findSuccessorV3(rec)
		if err != nil {
			return err
		}
		if succ == "" {
			rep.SuccessorsMissing++
			continue
		}
		next := *rec
		next.SuccessorID = succ
		if err := s.put(rec, &next, nil); err != nil {
			return err
		}
		rep.SuccessorsFilled++
	}
	return b.Commit(pebble.Sync)
}

// parseIxDefKey splits an opv3:ix:def:{def}:{queued_ns}:{id} key. The def id
// may itself contain ':', so the two fixed fields are taken from the right.
func parseIxDefKey(k []byte) (defID string, queuedNs int64, id string, ok bool) {
	rest := strings.TrimPrefix(string(k), opv3IxDefPrefix)
	if len(rest) == len(k) {
		return "", 0, "", false
	}
	i := strings.LastIndexByte(rest, ':')
	if i < 0 {
		return "", 0, "", false
	}
	id = rest[i+1:]
	rest = rest[:i]
	j := strings.LastIndexByte(rest, ':')
	if j < 0 {
		return "", 0, "", false
	}
	ns, err := strconv.ParseInt(rest[j+1:], 10, 64)
	if err != nil {
		return "", 0, "", false
	}
	return rest[:j], ns, id, true
}

// findSuccessorV3 returns the id of the run of rec's def queued closest to
// rec's completed_at within opsV3SuccessorWindow and newer than rec, or "".
func (p *PebbleStore) findSuccessorV3(rec *RunRecord) (string, error) {
	if rec.CompletedAt == nil {
		return "", nil
	}
	at := completedNanos(*rec.CompletedAt)
	w := int64(opsV3SuccessorWindow)
	lo := max(at-w, 0)
	lower := []byte(fmt.Sprintf("%s%s:%020d", opv3IxDefPrefix, rec.DefID, lo))
	upper := []byte(fmt.Sprintf("%s%s:%020d", opv3IxDefPrefix, rec.DefID, at+w+1))
	best, bestGap := "", int64(-1)
	err := p.scanKeys(lower, upper, func(k []byte) bool {
		def, ns, id, ok := parseIxDefKey(k)
		if !ok || def != rec.DefID || id == rec.ID || id <= rec.ID {
			return true
		}
		if rec.QueuedAt != nil && ns <= completedNanos(*rec.QueuedAt) {
			return true // not newer than the row it would replace
		}
		gap := ns - at
		if gap < 0 {
			gap = -gap
		}
		if bestGap < 0 || gap < bestGap {
			best, bestGap = id, gap
		}
		return true
	})
	return best, err
}

// rebuildOpCountsV3 recomputes opv3:meta:counts from the ix:state and ix:def
// indexes under opsMu. It is exact and idempotent: run twice over the same
// indexes it writes the same bytes.
func (p *PebbleStore) rebuildOpCountsV3() error {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	c := OpCounts{ByState: map[state.V3]int64{}, ByDef: map[string]int64{}}
	stPrefix := []byte(opv3IxStatePrefix)
	if err := p.scanKeys(stPrefix, prefixEnd(stPrefix), func(k []byte) bool {
		rest := strings.TrimPrefix(string(k), opv3IxStatePrefix)
		if i := strings.IndexByte(rest, ':'); i > 0 {
			c.ByState[state.V3(rest[:i])]++
			c.Total++
		}
		return true
	}); err != nil {
		return err
	}
	defPrefix := []byte(opv3IxDefPrefix)
	if err := p.scanKeys(defPrefix, prefixEnd(defPrefix), func(k []byte) bool {
		if def, _, _, ok := parseIxDefKey(k); ok {
			c.ByDef[def]++
		}
		return true
	}); err != nil {
		return err
	}
	b := p.db.NewBatch()
	defer b.Close()
	if err := p.stageOpCounts(b, c); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}
