// file: internal/database/sql_activity_wal_truncate.go
// version: 2.2.0
// guid: 3f1d8a24-6c05-4b9e-8d72-51ac07e4b6f3
// last-edited: 2026-10-18

// Package database — the post-VACUUM WAL truncate of the SQLite activity store.
//
// In WAL mode a VACUUM writes the rebuilt database THROUGH the WAL, and SQLite's
// automatic checkpoint is PASSIVE: it recycles the WAL in place at its
// high-water mark and never shrinks the file. So the space a VACUUM frees stays
// held by the -wal until an explicit wal_checkpoint(TRUNCATE) lands. Measured on
// prod 2026-09-08: the main file fell 22,898,438,144 -> 11,447,480,320 while the
// WAL held at 11,514,065,152. truncateWALAfterVacuum is the PASSIVE-converge-
// then-TRUNCATE sequence that returns that space; the background checkpointer's
// idle tick (checkpointAndMaybeTruncate) runs the same sequence.
//
// VacuumActivity is the entry point. It is the only VACUUM in the codebase
// (auto_vacuum is never set and db-optimize only compacts Pebble), and the
// maintenance.vacuum-activity-db op is its only caller. This file used to also
// hold the retroactive summary clamp that POST /api/v1/activity/clamp-summaries
// drove; that pass finished in prod and was removed in 01-P81g with the route.
// The write-time clamp (clampActivitySummary) is unchanged.
package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/diskstats"
)

// ActivityVacuumStats is a point-in-time measurement of the SQLite activity
// database's file layout.
//
// ReclaimableBytes is freelist_count * page_size: the pages inside the main file
// that hold no data and that a VACUUM would hand back to the filesystem. It is an
// estimate of what a run frees, not a promise: VACUUM also repacks partly full
// pages, and in WAL mode the rebuilt database passes through the -wal before the
// post-vacuum truncate returns it.
type ActivityVacuumStats struct {
	FileBytes        int64 `json:"file_bytes"`
	WALBytes         int64 `json:"wal_bytes"`
	PageSize         int64 `json:"page_size"`
	PageCount        int64 `json:"page_count"`
	FreelistCount    int64 `json:"freelist_count"`
	ReclaimableBytes int64 `json:"reclaimable_bytes"`

	// Preflight: whether a live VACUUM has the disk space it needs. FreeBytes is
	// the space available to this process on the volume holding the database (and,
	// because pinTempDirToDatabase points SQLite's temp directory there, the
	// volume that holds VACUUM's temp copy too). RequiredBytes is
	// vacuumRequiredBytes. WouldRefuse is true when a live run would refuse to
	// start; PreflightNote says why when free space could not be read.
	FreeBytes     int64  `json:"free_bytes"`
	RequiredBytes int64  `json:"required_bytes"`
	WouldRefuse   bool   `json:"would_refuse"`
	PreflightNote string `json:"preflight_note,omitempty"`
}

// vacuumMarginFloor is the smallest safety margin added to a VACUUM's space
// estimate: the estimate is of live data only and VACUUM also allocates page
// slack, the freelist rewrite and the WAL's own growth.
const vacuumMarginFloor = int64(1) << 30

// vacuumRequiredBytes is the free space a live VACUUM needs on the database's
// volume: TWICE the compacted size, plus a margin of max(1 GiB, 10%).
//
// Why twice. VACUUM first builds the compacted copy in a temp database (about
// the live data size; the temp file is pinned to this volume), then copies it back
// into the original through the WAL, which holds roughly another compacted size
// until the post-vacuum TRUNCATE returns it. The original's own bytes are
// released only at the end, so the peak EXTRA space is about 2x the compacted
// size, not 1x the file size. The compacted size is FileBytes minus the free
// pages (ReclaimableBytes); repacking partly full pages can only make it smaller,
// so this is an upper-ish estimate, not an exact one.
func vacuumRequiredBytes(st ActivityVacuumStats) int64 {
	compacted := st.FileBytes - st.ReclaimableBytes
	if compacted < 0 {
		compacted = 0
	}
	base := 2 * compacted
	margin := base / 10
	if margin < vacuumMarginFloor {
		margin = vacuumMarginFloor
	}
	return base + margin
}

// freeBytesFunc reports the bytes available to this process on the filesystem
// holding path. Production uses diskstats.Stats; tests inject a fake.
type freeBytesFunc func(path string) (uint64, error)

func diskstatsFreeBytes(path string) (uint64, error) {
	_, free, err := diskstats.Stats(path)
	return free, err
}

// compile-time proof that the SQLite store is the vacuumer: without it a rename
// of either method would silently turn FindActivityVacuumer into "unsupported".
var _ ActivityVacuumer = (*SQLActivityStore)(nil)

// DiskBytes is the main file plus its -wal: what the activity database holds on
// disk right now.
func (st ActivityVacuumStats) DiskBytes() int64 { return st.FileBytes + st.WALBytes }

// VacuumStats measures the activity database without writing anything.
func (s *SQLActivityStore) VacuumStats(ctx context.Context) (ActivityVacuumStats, error) {
	var st ActivityVacuumStats
	for _, q := range []struct {
		pragma string
		dst    *int64
	}{
		{"page_size", &st.PageSize},
		{"page_count", &st.PageCount},
		{"freelist_count", &st.FreelistCount},
	} {
		if err := s.reader.QueryRowContext(ctx, "PRAGMA "+q.pragma).Scan(q.dst); err != nil {
			return st, fmt.Errorf("sql_activity: PRAGMA %s: %w", q.pragma, err)
		}
	}
	st.ReclaimableBytes = st.FreelistCount * st.PageSize
	if fi, err := os.Stat(s.path); err == nil {
		st.FileBytes = fi.Size()
	}
	if fi, err := os.Stat(s.path + "-wal"); err == nil {
		st.WALBytes = fi.Size()
	}

	st.RequiredBytes = vacuumRequiredBytes(st)
	freeFn := s.freeBytes
	if freeFn == nil {
		freeFn = diskstatsFreeBytes
	}
	free, ferr := freeFn(filepath.Dir(s.path))
	switch {
	case ferr != nil:
		// Fail closed: an unreadable free-space figure is not proof of enough space.
		st.WouldRefuse = true
		st.PreflightNote = fmt.Sprintf("cannot read free space: %v", ferr)
	default:
		st.FreeBytes = int64(free)
		st.WouldRefuse = st.FreeBytes < st.RequiredBytes
	}
	return st, nil
}

// ActivityVacuumer is the surface the vacuum op needs. Only the SQLite store
// implements it: the Pebble activity keyspace is block-compressed and has no
// freelist to give back.
type ActivityVacuumer interface {
	VacuumStats(ctx context.Context) (ActivityVacuumStats, error)
	VacuumActivity(ctx context.Context) (time.Duration, error)
}

// FindActivityVacuumer digs the SQLite store out from behind the migration
// wrapper, returning false when the active backend has no SQLite side at all
// (ActivityBackend=pebble). A type assertion on the outermost value is not
// enough: in the normal configuration the caller holds a
// *MigratingActivityStore, which deliberately does not forward this surface,
// because a VACUUM is a repair of one backend's file, not an activity operation
// that should fan out to every backend.
func FindActivityVacuumer(s ActivityStorer) (ActivityVacuumer, bool) {
	switch v := s.(type) {
	case ActivityVacuumer:
		return v, true
	case *MigratingActivityStore:
		if c, ok := FindActivityVacuumer(v.Primary()); ok {
			return c, true
		}
		return FindActivityVacuumer(v.Secondary())
	}
	return nil, false
}

// ActivityVacuumReport is the outcome of RunActivityVacuum.
type ActivityVacuumReport struct {
	// Supported is false when the backend has no SQLite activity database, so
	// there was nothing to vacuum. Reported explicitly, as in
	// ActivityOptimizeResult, so "nothing to do here" is not read as success.
	Supported bool                `json:"supported"`
	DryRun    bool                `json:"dry_run"`
	Before    ActivityVacuumStats `json:"before"`
	// After is zero on a dry run.
	After ActivityVacuumStats `json:"after"`
	// BytesReclaimed is Before.DiskBytes() - After.DiskBytes() (file plus -wal),
	// zero on a dry run. Negative if the database grew while the run was going.
	BytesReclaimed int64         `json:"bytes_reclaimed"`
	Duration       time.Duration `json:"duration"`
}

// RunActivityVacuum previews or performs a VACUUM of the SQLite activity
// database. A dry run measures and writes nothing. A live run measures, runs
// VacuumActivity (VACUUM plus the WAL truncate that returns the freed space),
// measures again, and reports the difference. If VacuumActivity fails the report
// is returned alongside the error, with After filled in, so the caller can say
// how far it got (the VACUUM may have committed even when the truncate gave up).
func RunActivityVacuum(ctx context.Context, store ActivityStorer, dryRun bool) (ActivityVacuumReport, error) {
	rep := ActivityVacuumReport{DryRun: dryRun}
	v, ok := FindActivityVacuumer(store)
	if !ok {
		return rep, nil
	}
	rep.Supported = true
	before, err := v.VacuumStats(ctx)
	if err != nil {
		return rep, err
	}
	rep.Before = before
	if dryRun {
		return rep, nil
	}
	d, vacErr := v.VacuumActivity(ctx)
	rep.Duration = d
	// Measure even after a failure: a VACUUM that committed and a truncate that
	// gave up still changed the file.
	if after, err := v.VacuumStats(ctx); err == nil {
		rep.After = after
		rep.BytesReclaimed = before.DiskBytes() - after.DiskBytes()
	}
	return rep, vacErr
}

// VacuumActivity reclaims the file space a clamp pass freed.
//
// Clamping shortens values but leaves the pages allocated, so the .sqlite file
// does not shrink until this runs — reporting "reclaimed 5 GB" without it would
// be false from the user's point of view, since df would not move.
//
// VACUUM cannot run inside a transaction and rewrites the whole database, so it
// is deliberately a separate call rather than part of the pass: it needs a
// window where its cost (about twice the compacted size in free space on the
// database's volume, and the IO) is acceptable; VacuumStats' preflight checks it. It is issued on the writer connection because it takes an
// exclusive lock.
func (s *SQLActivityStore) VacuumActivity(ctx context.Context) (time.Duration, error) {
	start := time.Now()

	// FAIL CLOSED on the temp directory. VACUUM's temp copy goes to SQLite's temp
	// directory, which defaults to TMPDIR, which on this deployment is the ROOT
	// pool (the 2026-09-09 outage: see pinTempDirToDatabase). Open pinned it
	// best-effort; here a failure is fatal, because a VACUUM of a database this
	// size that spills to the root pool takes production down.
	if err := s.pinTempDirToDatabase(ctx); err != nil {
		return time.Since(start), fmt.Errorf("sql_activity: refusing to vacuum, cannot keep its temp copy off TMPDIR: %w", err)
	}

	// Free-space preflight on the database's own volume.
	st, err := s.VacuumStats(ctx)
	if err != nil {
		return time.Since(start), err
	}
	if st.WouldRefuse {
		reason := fmt.Sprintf("%d bytes free, %d required", st.FreeBytes, st.RequiredBytes)
		if st.PreflightNote != "" {
			reason = st.PreflightNote
		}
		return time.Since(start), fmt.Errorf("sql_activity: refusing to vacuum, not enough disk space (%s; 2x the compacted size plus margin is needed on %s)",
			reason, filepath.Dir(s.path))
	}

	if _, err := s.writer.ExecContext(ctx, `VACUUM`); err != nil {
		return time.Since(start), fmt.Errorf("sql_activity: vacuum: %w", err)
	}

	// VACUUM alone does not finish the job in WAL mode: it writes the rebuilt
	// database THROUGH the WAL, so the bytes it "freed" are still occupying the
	// -wal file afterwards. SQLite's automatic checkpoint is PASSIVE, which
	// recycles the WAL in place at its high-water mark and never shrinks the
	// file — so without an explicit TRUNCATE the space is never returned and no
	// amount of waiting fixes it.
	//
	// Measured on prod 2026-09-08, first run of this pass: the main file fell
	// 22,898,438,144 → 11,447,480,320 (10.66 GB freed) while the WAL sat at
	// 11,514,065,152 and did not move for the next three minutes. Net reclaim
	// was therefore approximately zero until the service restarted.
	//
	// Failure here is not fatal to the vacuum, which has already committed:
	// report it so the caller can say the space is still held, rather than
	// discarding a successful multi-GB rebuild. "Still held" means held for
	// now, not stranded: once any checkpoint (this one's last PASSIVE, or the
	// background loop's next tick) has copied every frame, the first commit
	// that then finds no reader on the WAL restarts it from frame 1 and,
	// because journal_size_limit is set (sqlActJournalSizeLimit, 64 MiB), cuts
	// the -wal file down to that size. The error names which of the reasons in
	// walNotResetReason stopped the reset.
	if err := s.truncateWALAfterVacuum(ctx); err != nil {
		return time.Since(start), fmt.Errorf("sql_activity: vacuum succeeded but WAL truncate failed (space still held): %w", err)
	}
	return time.Since(start), nil
}

// The post-vacuum truncate has two retry budgets, because the two ways an
// attempt can fail have different causes and need different waits.
//
//   - NOT READY (vacuumTruncateAttempts, exponential backoff from 10 ms up to
//     vacuumTruncateMaxBackoff): no TRUNCATE was issued because the PASSIVEs
//     could not get the WAL fully and recently copied — a reader's snapshot
//     held PASSIVE short (walReasonReaderHeld), the checkpoint lock was taken
//     (walReasonCheckpointBusy), or writes outpaced the copy
//     (walReasonDidNotConverge). These clear on the scale of a reader's
//     lifetime, so the wait grows. One attempt is one run of passiveUntilShort:
//     a single PASSIVE when it comes back incomplete or busy, up to
//     vacuumPassiveConvergeRounds of them when each is complete but long. The
//     backoff alone sums to 10+20+40+80+160+320+500 ms = 1.13 s across 8
//     attempts; the PASSIVE copies come on top of that.
//   - BUSY TRUNCATE (vacuumTruncateBusyAttempts, a fixed
//     vacuumTruncateBusyRetryPause): a TRUNCATE was issued after a converged
//     PASSIVE, copied every frame, and could not reset the file because a
//     Record held the write lock or a reader was on the WAL past its own
//     vacuumTruncateResetWaitMS wait. That clears on the scale of one Record,
//     so it is retried promptly.
//
// They used to share one budget. Under a steady stream of Records (one every
// millisecond in TestVacuumTruncate_ReaderReleasedMidTruncateNeverTruncatesUnderTheCopy)
// a reader held for 100 ms used four of the eight attempts on backoff, and
// with a 0 ms busy wait each converged TRUNCATE had to land in a gap between
// two Records; the four left were often not enough, and the vacuum reported
// the space still held with the WAL fully copied (15 of 135 loaded runs).
//
// Giving up does not strand the space. The background checkpointer's idle tick
// (checkpointAndMaybeTruncate) runs the same PASSIVE-then-TRUNCATE sequence
// (TestVacuumTruncate_IdleTickReclaimsWhatAGivenUpTruncateLeft), and even
// without it, once every frame is copied the first commit that finds no reader
// on the WAL restarts it and cuts the file to journal_size_limit (64 MiB).
//
// Vars so a test can shorten them.
var (
	vacuumTruncateAttempts   = 8
	vacuumTruncateMaxBackoff = 500 * time.Millisecond

	vacuumTruncateBusyAttempts   = 8
	vacuumTruncateBusyRetryPause = 10 * time.Millisecond

	// vacuumTruncateResetWaitMS is the busy_timeout the post-vacuum TRUNCATE,
	// and only it, runs with (truncateWithResetWait). The checkpoint
	// connection's resting value stays 0 (sqlActCkptBusyTimeoutMS).
	//
	// WHY A WAIT AT ALL, given that the 0 resting value exists so a TRUNCATE
	// never stalls writers: SQLite's TRUNCATE spends its busy handler in two
	// places, and only one of them stalls writers.
	//
	//  1. Acquiring the WAL write lock. While it waits here it holds only the
	//     checkpoint lock, which no Record needs, so Records keep committing.
	//     With 0 it gave up the moment a Record was mid-commit, degraded to a
	//     PASSIVE and reported busy=1 with every frame copied: the
	//     {Busy:1 Log:2153 Checkpointed:2153} rows that failed the test above.
	//     A Record's commit is about a millisecond, so a short wait outlasts it.
	//  2. Waiting, WHILE HOLDING the write lock, for readers still on the WAL to
	//     finish, before it resets the file. Every Record queues behind this.
	//     This is the stall the 0 resting value was introduced against: at
	//     1000 ms a late reader made each attempt hold the lock for a full
	//     second and Records stalled 2.5 s.
	//
	// One busy handler covers both, and its budget is per statement, so 25 ms
	// bounds the second wait, and therefore the extra time any Record can be
	// held behind one TRUNCATE, to 25 ms on top of the copy (which convergence
	// already bounds to the frames written during one short PASSIVE plus those
	// committed during this wait). Records' own busy_timeout is 10,000 ms, 400
	// times that, so none fails. SQLite's default handler polls at 1, 2, 5, 10
	// ms intervals, so 25 ms is five chances for a 1 ms Record to finish.
	vacuumTruncateResetWaitMS = 25

	// vacuumTruncateMaxLeftoverFrames bounds the frames the TRUNCATE may have
	// to copy under the WAL write lock: it is issued only after a complete
	// PASSIVE that itself had at most this many frames left to copy (see
	// passiveNewFrames). 256 frames is about 1 MiB at the 4 KiB page size.
	vacuumTruncateMaxLeftoverFrames = 256
	// vacuumPassiveConvergeRounds caps the back-to-back PASSIVEs one attempt
	// runs waiting for one to come in under vacuumTruncateMaxLeftoverFrames.
	// Hitting it means writes outpace the copy; the attempt is then not ready
	// (walReasonDidNotConverge, no TRUNCATE) and retried after the backoff.
	vacuumPassiveConvergeRounds = 32
)

// walNotResetReason says why an attempt to reset the WAL did not succeed. It is
// carried into the error truncateWALAfterVacuum returns and the log line, so a
// "space still held" report names its actual cause instead of always blaming
// a reader.
type walNotResetReason string

const (
	// walReasonReaderHeld: a PASSIVE came back incomplete (Checkpointed < Log):
	// a reader's snapshot predates frames it would have to overwrite.
	walReasonReaderHeld walNotResetReason = "reader-held"
	// walReasonCheckpointBusy: a PASSIVE returned busy=1 with log and
	// checkpointed of -1. SQLite's checkpoint fills in the counts only after
	// it has taken the checkpoint lock and read the WAL index header, so this
	// row means one of those was busy: another connection was checkpointing,
	// or a commit was updating the header and SQLite could not get a stable
	// read of it. It copied nothing and its counts say nothing about the WAL.
	walReasonCheckpointBusy walNotResetReason = "checkpoint-busy"
	// walReasonDidNotConverge: every PASSIVE in vacuumPassiveConvergeRounds was
	// complete but had more than vacuumTruncateMaxLeftoverFrames new frames,
	// so writes were outpacing the copy and no TRUNCATE was issued. The last
	// counts look complete; the WAL was still not reset.
	walReasonDidNotConverge walNotResetReason = "did-not-converge"
	// walReasonBusyTruncate: a TRUNCATE was issued after a converged PASSIVE,
	// copied every frame (busy=1, log == checkpointed) and could not reset the
	// file within vacuumTruncateResetWaitMS, because a writer kept the write
	// lock or a reader stayed on the WAL.
	walReasonBusyTruncate walNotResetReason = "busy-truncate"
)

// describe is the human-readable cause used in the error and the log.
func (r walNotResetReason) describe() string {
	switch r {
	case walReasonReaderHeld:
		return "a reader's snapshot kept PASSIVE from copying every frame, so no TRUNCATE was issued"
	case walReasonCheckpointBusy:
		return "PASSIVE could not take the checkpoint lock or read the WAL index header, so it copied nothing " +
			"(its counts are -1)"
	case walReasonDidNotConverge:
		return fmt.Sprintf("writes outpaced the copy: no PASSIVE in %d rounds had at most %d new frames, "+
			"so no TRUNCATE was issued (the counts below are from a complete PASSIVE, but the WAL was not reset)",
			vacuumPassiveConvergeRounds, vacuumTruncateMaxLeftoverFrames)
	case walReasonBusyTruncate:
		return fmt.Sprintf("TRUNCATE copied every frame but could not reset the WAL within its %d ms wait: "+
			"a writer kept the write lock or a reader stayed on the WAL", vacuumTruncateResetWaitMS)
	}
	return string(r)
}

// walGeneration identifies one generation of the -wal file: SQLite bumps the
// checkpoint sequence number and salt-1 in the WAL header (bytes 12-19) every
// time it restarts the log from frame 1, and writes the new header with the
// first frame of the restarted log.
type walGeneration struct {
	ckptSeq, salt1, salt2 uint32
}

// readWALGeneration reads the -wal header. ok is false when the file is
// missing, short, or does not start with a WAL magic number; callers treat
// that as "a restart may have happened", the safe direction.
//
// Reading the -wal on a separate descriptor is safe: SQLite takes no POSIX
// locks on the -wal (WAL locks live in the -shm), so closing this descriptor
// cannot drop a lock SQLite holds. A read that races a restart can return a
// torn header; that reads as a different generation, which again only makes
// the caller count more frames as new.
func readWALGeneration(walPath string) (walGeneration, bool) {
	f, err := os.Open(walPath)
	if err != nil {
		return walGeneration{}, false
	}
	var hdr [24]byte
	_, rerr := io.ReadFull(f, hdr[:])
	if cerr := f.Close(); rerr != nil || cerr != nil {
		return walGeneration{}, false
	}
	if magic := binary.BigEndian.Uint32(hdr[0:4]); magic != 0x377f0682 && magic != 0x377f0683 {
		return walGeneration{}, false
	}
	return walGeneration{
		ckptSeq: binary.BigEndian.Uint32(hdr[12:16]),
		salt1:   binary.BigEndian.Uint32(hdr[16:20]),
		salt2:   binary.BigEndian.Uint32(hdr[20:24]),
	}, true
}

// passiveTracker carries what passiveNewFrames needs from one PASSIVE to the
// next, across attempts: the previous PASSIVE's Checkpointed and the WAL
// generation read just BEFORE that PASSIVE started.
type passiveTracker struct {
	walPath          string
	have             bool // a previous PASSIVE with counts exists
	prevCheckpointed int
	prevGen          walGeneration
	prevGenOK        bool
}

// passiveNewFrames is how many frames a checkpoint had to copy that the
// previous one in the same sequence had not: the frames written between the
// start of the previous checkpoint and the start of this one. prevCheckpointed
// is the previous result's Checkpointed (0 for the first in a sequence, so the
// first PASSIVE always counts the whole WAL).
//
// If the WAL was restarted from frame 1 in between, every frame in it is new.
// restarted says so when the WAL header's generation changed anywhere between
// just before the previous PASSIVE and just after this one (passiveTracker), or
// could not be read; that window covers every point a restart could make
// Log - prevCheckpointed wrong, and every uncertainty in it resolves to
// counting all of Log. Log below prevCheckpointed is also a restart, kept as a
// second signal. Overcounting only costs another PASSIVE round, since after a
// restart Log is the true number of new frames; undercounting would let the
// TRUNCATE copy a long PASSIVE's worth of writes under the write lock.
func passiveNewFrames(prevCheckpointed int, res walCheckpointResult, restarted bool) int {
	if restarted || res.Log < prevCheckpointed {
		return res.Log
	}
	return res.Log - prevCheckpointed
}

// truncateWALAfterVacuum empties the -wal VACUUM just filled: a PASSIVE
// checkpoint copies the frames, and wal_checkpoint(TRUNCATE) resets the file
// once nothing is left to copy, retrying until both succeed or a budget runs
// out (see vacuumTruncateAttempts and vacuumTruncateBusyAttempts).
//
// TRUNCATE ONLY AFTER A COMPLETE PASSIVE. TRUNCATE holds the WAL write lock for
// the whole time it copies frames into the database file, and while it waits
// for readers. After a VACUUM the frames are the entire rebuilt database (11 GB
// on prod on 2026-09-08), and every foreground Record waiting on that lock
// gives up after its busy_timeout(10000) with SQLITE_BUSY: a review probe on a
// 66 MB WAL took 33.8 s to copy and failed 3 Records. PASSIVE copies the same
// frames without the write lock, so writers keep committing. It stops short
// while a reader still holds an older snapshot; issuing TRUNCATE then would copy
// the rest UNDER the lock (measured: ~16k frames when the reader let go 300 ms
// in, and a 2.78 s Record when it never did). So TRUNCATE is issued only when
// the PASSIVE before it reports complete().
//
// AND ONLY AFTER A SHORT ONE. complete() means every frame that was in the WAL
// when that PASSIVE STARTED is copied; the frames Records commit while it runs
// are not, and the TRUNCATE copies them under the lock. After a long copy that
// is a lot: on a loaded CI runner (run 37453234558) a complete 6.13 s PASSIVE
// was followed by a 3.79 s TRUNCATE and a 3.84 s Record behind it, and on prod
// an 11 GB PASSIVE would leave minutes of writes. So each attempt repeats
// PASSIVE back to back until one is complete and had at most
// vacuumTruncateMaxLeftoverFrames frames of its own to copy (passiveNewFrames).
// Such a PASSIVE was short, so the frames written during it, which are most of
// what the TRUNCATE copies, are few. If vacuumPassiveConvergeRounds pass
// without that, writes are outpacing the copy: the attempt logs the counts,
// issues no TRUNCATE, and is retried after the backoff like any other
// not-ready attempt. The background loop's checkpointAndMaybeTruncate needs no
// such loop: it truncates only on an idle tick, when no Record committed since
// the last one.
//
// A busy TRUNCATE is not an SQL error. It returns an ordinary row with busy=1
// and leaves the -wal file as it was. When it could not take the write lock it
// carries on as a PASSIVE, so the row has real counts and usually
// log == checkpointed (every frame copied, file not reset; observed
// {Busy:1 Log:1880 Checkpointed:1880}); only when it could not take the
// checkpoint lock or read the WAL index header are log and checkpointed -1. This used to go through
// ExecContext, which discards that row, so a TRUNCATE that lost the race with
// the background checkpointer was reported as success with the WAL still
// holding the space VACUUM freed. Every checkpoint here runs on the checkpoint
// connection, the single-connection handle the background loop also uses, so
// the two queue behind each other instead of colliding.
//
// ctx is checked between attempts and interrupts a checkpoint's copy loop. It
// does not cut short the TRUNCATE's busy-handler wait, which is at most
// vacuumTruncateResetWaitMS; a PASSIVE never waits in the busy handler.
func (s *SQLActivityStore) truncateWALAfterVacuum(ctx context.Context) error {
	tr := passiveTracker{walPath: s.path + "-wal"}
	backoff := 10 * time.Millisecond
	var res walCheckpointResult
	var reason walNotResetReason
	notReady, busyTruncates := 0, 0
	for {
		out, err := s.passiveUntilShort(ctx, &tr)
		if err != nil {
			return fmt.Errorf("passive checkpoint before truncate: %w", err)
		}
		if out.notWAL {
			return nil // not in WAL mode: there is no -wal to reset
		}
		res = out.res
		var wait time.Duration
		if out.converged {
			res, err = s.truncateWithResetWait(ctx)
			if err != nil {
				return err
			}
			if res.complete() {
				return nil
			}
			reason = walReasonBusyTruncate
			busyTruncates++
			if busyTruncates >= vacuumTruncateBusyAttempts {
				break
			}
			wait = vacuumTruncateBusyRetryPause
		} else {
			reason = out.reason
			notReady++
			if notReady >= vacuumTruncateAttempts {
				break
			}
			wait = backoff
			backoff = min(2*backoff, vacuumTruncateMaxBackoff)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("WAL not yet reset (%s; last checkpoint busy=%d wal_frames=%d checkpointed=%d) "+
				"when cancelled: %w", reason, res.Busy, res.Log, res.Checkpointed, ctx.Err())
		case <-timer.C:
		}
	}
	ckptLog.Warn("post-vacuum WAL reset of %s gave up (%s) after %d not-ready attempts and %d busy TRUNCATEs "+
		"(last checkpoint busy=%d wal_frames=%d checkpointed=%d); the background checkpointer retries on its "+
		"next idle tick, and the first commit after a complete checkpoint that finds no reader on the WAL cuts "+
		"the -wal to journal_size_limit (%d bytes)", s.path, reason, notReady, busyTruncates,
		res.Busy, res.Log, res.Checkpointed, sqlActJournalSizeLimit)
	return fmt.Errorf("WAL not reset (%s): %s; %d of %d not-ready attempts, %d of %d busy TRUNCATEs "+
		"(last checkpoint busy=%d wal_frames=%d checkpointed=%d)",
		reason, reason.describe(), notReady, vacuumTruncateAttempts, busyTruncates, vacuumTruncateBusyAttempts,
		res.Busy, res.Log, res.Checkpointed)
}

// truncateWithResetWait runs the post-vacuum TRUNCATE with a busy_timeout of
// vacuumTruncateResetWaitMS (see its comment for why that bound keeps writers
// moving) instead of the checkpoint connection's resting 0. It pins the
// checkpoint handle's one connection for the three statements so nothing else
// can run on it while the wait is raised, and restores the resting value before
// releasing it. If the restore fails the connection is discarded rather than
// returned to the pool: the background loop's TRUNCATE must never inherit the
// wait.
func (s *SQLActivityStore) truncateWithResetWait(ctx context.Context) (walCheckpointResult, error) {
	conn, err := s.ckpt.Conn(ctx)
	if err != nil {
		return walCheckpointResult{}, fmt.Errorf("pin checkpoint connection for truncate: %w", err)
	}
	defer func() {
		// context.Background, not ctx: a cancelled ctx must still restore.
		_, rerr := conn.ExecContext(context.Background(),
			fmt.Sprintf("PRAGMA busy_timeout = %d", sqlActCkptBusyTimeoutMS))
		if rerr != nil {
			// Returning driver.ErrBadConn from Raw makes database/sql close the
			// driver connection instead of pooling it, so the next checkpoint
			// opens a fresh one at the DSN's busy_timeout(0). Raw hands that
			// same ErrBadConn back; anything else means the discard failed.
			// Logged, not returned: the TRUNCATE's own result is still true,
			// and reporting a reset WAL as "space still held" would be false.
			if derr := conn.Raw(func(any) error { return driver.ErrBadConn }); derr != nil &&
				!errors.Is(derr, driver.ErrBadConn) {
				ckptLog.Error("could not restore busy_timeout on the checkpoint connection of %s (%v) nor discard "+
					"it (%v): background TRUNCATEs may wait up to %d ms for readers", s.path, rerr, derr,
					vacuumTruncateResetWaitMS)
			} else {
				ckptLog.Warn("could not restore busy_timeout on the checkpoint connection of %s (%v); "+
					"discarded it", s.path, rerr)
			}
		}
		// After a discard the Conn is already closed and Close reports
		// ErrConnDone; that is the expected outcome, not a failure.
		if cerr := conn.Close(); cerr != nil && !errors.Is(cerr, sql.ErrConnDone) {
			ckptLog.Warn("release of the checkpoint connection of %s after truncate failed: %v", s.path, cerr)
		}
	}()
	if _, err := conn.ExecContext(ctx, fmt.Sprintf("PRAGMA busy_timeout = %d", vacuumTruncateResetWaitMS)); err != nil {
		return walCheckpointResult{}, fmt.Errorf("set truncate busy_timeout: %w", err)
	}
	return s.walCheckpointOn(ctx, conn, "TRUNCATE")
}

// passiveOutcome is what one run of passiveUntilShort found.
type passiveOutcome struct {
	res walCheckpointResult // the last PASSIVE's result
	// converged: the last PASSIVE was complete and had at most
	// vacuumTruncateMaxLeftoverFrames new frames, so a TRUNCATE may follow.
	converged bool
	// notWAL: the database is not in WAL mode (busy=0, log=checkpointed=-1);
	// there is nothing to truncate.
	notWAL bool
	// reason is why it did not converge (unset when converged or notWAL).
	reason walNotResetReason
}

// classifyPassive maps one PASSIVE result and its new-frame count to what the
// round loop does next: stop with an outcome, or (done=false) run another round.
func classifyPassive(res walCheckpointResult, newFrames int) (out passiveOutcome, done bool) {
	out.res = res
	switch {
	case res.Busy == 0 && res.Log < 0:
		// (0,-1,-1) is what wal_checkpoint returns for a database that is not
		// in WAL mode. It was treated as complete before the convergence loop
		// existed and must still be: retrying it can never change it.
		out.notWAL = true
		return out, true
	case res.Log < 0:
		// (1,-1,-1): SQLite could not take the checkpoint lock or read the WAL
		// index header, so this PASSIVE neither copied nor counted anything.
		out.reason = walReasonCheckpointBusy
		return out, true
	case !res.complete():
		out.reason = walReasonReaderHeld
		return out, true
	case newFrames <= vacuumTruncateMaxLeftoverFrames:
		out.converged = true
		return out, true
	}
	return out, false
}

// passiveUntilShort runs PASSIVE checkpoints back to back until one is complete
// and had at most vacuumTruncateMaxLeftoverFrames new frames to copy, which is
// when a TRUNCATE may follow it (converged). It stops early, not converged, at
// the first PASSIVE that is not complete (walReasonReaderHeld) or could not
// take the checkpoint lock (walReasonCheckpointBusy): only the caller's backoff
// helps with those. It also stops, not converged, after
// vacuumPassiveConvergeRounds (walReasonDidNotConverge). tr carries the previous
// PASSIVE's Checkpointed and WAL generation across calls, so the first PASSIVE
// of a retry is measured against the last one of the attempt before.
func (s *SQLActivityStore) passiveUntilShort(ctx context.Context, tr *passiveTracker) (passiveOutcome, error) {
	var res walCheckpointResult
	for round := 1; round <= vacuumPassiveConvergeRounds; round++ {
		if err := ctx.Err(); err != nil {
			return passiveOutcome{res: res}, err
		}
		preGen, preOK := readWALGeneration(tr.walPath)
		var err error
		res, err = s.walCheckpoint(ctx, "PASSIVE")
		if err != nil {
			return passiveOutcome{res: res}, err
		}
		newFrames := res.Log
		if res.Log >= 0 {
			postGen, postOK := readWALGeneration(tr.walPath)
			if tr.have {
				restarted := !tr.prevGenOK || !postOK || tr.prevGen != postGen
				newFrames = passiveNewFrames(tr.prevCheckpointed, res, restarted)
			}
			tr.have, tr.prevCheckpointed, tr.prevGen, tr.prevGenOK = true, res.Checkpointed, preGen, preOK
		}
		if out, done := classifyPassive(res, newFrames); done {
			return out, nil
		}
	}
	ckptLog.Warn("post-vacuum PASSIVE of %s did not converge in %d rounds (last: wal_frames=%d checkpointed=%d); "+
		"writes are outpacing the copy, so no TRUNCATE this attempt", s.path, vacuumPassiveConvergeRounds,
		res.Log, res.Checkpointed)
	return passiveOutcome{res: res, reason: walReasonDidNotConverge}, nil
}
