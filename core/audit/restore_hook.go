// file: core/audit/restore_hook.go
// version: 1.1.0
// guid: e19562d3-4a41-481f-afb2-7fdc551fac2c
// last-edited: 2026-11-12

package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"slices"
	"strconv"

	"github.com/falkcorp/mediacore/backup/journal"
	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/keys"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// restoreOwner is the owner whose keyspace rc.Previous opens for this hook.
const restoreOwner storage.Owner = "core/audit"

// restoreCopyBatch is how many rows one copy batch carries before it commits.
const restoreCopyBatch = 512

// Counts keys the hook returns.
const (
	CountRecordsCopied   = "audit_records_copied"
	CountRecordsAppended = "audit_records_appended"
)

// RestoreDeps is what the kernel hands audit's restore hook.
type RestoreDeps struct {
	Store *Store // the live (just restored) audit store
	// RestoreSeq is the seq of the revocation journal's restore line this run
	// belongs to (0 when the journal could not take one). Unless RunKey is set it
	// is the idempotency key of the records the hook appends, and 0 is never a
	// key: two restores that both had no line must not share one.
	RestoreSeq uint64
	// RunKey identifies this run when there is no restore line: a value that is
	// the same for every re-run of one restore (hooks re-run after a crash) and
	// differs between restores. The kernel records one durably with the run
	// (journal.EnsureHookRun). When set it is the idempotency key and RestoreSeq
	// is only reported; the hook fails before writing anything when both are
	// unset.
	RunKey string
	Log    *slog.Logger // nil discards
}

// restoreTargetID is the id of the core.restore target the run's records are
// keyed on: the run key when there is one, else the restore line's seq. A run
// with neither has nothing to tell it from another, and deduplicating on a zero
// seq would silently drop the records of every restore after the first.
func (d RestoreDeps) restoreTargetID() (string, error) {
	switch {
	case d.RunKey != "":
		return d.RunKey, nil
	case d.RestoreSeq != 0:
		return strconv.FormatUint(d.RestoreSeq, 10), nil
	}
	return "", errors.New("audit: restore hook: no restore line and no run key to tell this restore from another")
}

// RestoreHook returns audit's journal.RestoreHook (run third, after auth and
// rbac; 05-rbac §7.1 "audit's hook", SEC-17, SEC2-12, DATA2-16).
//
// With rc.Previous set it copies every pre-swap record whose seq is above
// rc.AuditHighWater, with its index rows, keys and values unchanged, into the
// live store; the boundary is the sequence high-water mark, never a clock.
// Before the copy it advances the live store's audit sequence past the
// highest copied seq (its own Sync commit), so the next allocation can never
// overwrite a copied record and a crash between the two only skips IDs. It
// then appends core.restore.stage, core.restore.revoke and core.restore.apply
// records, each only when none exists yet for this restore (hooks re-run
// after a crash, DATA3-6).
func RestoreHook(d RestoreDeps) journal.RestoreHook {
	return func(ctx context.Context, rc journal.RestoreContext) (map[string]int, error) {
		if d.Store == nil {
			return nil, errors.New("audit: restore hook: RestoreDeps.Store is nil")
		}
		target, err := d.restoreTargetID()
		if err != nil {
			return nil, err
		}
		log := d.Log
		if log == nil {
			log = slog.New(slog.DiscardHandler)
		}
		copied := 0
		if rc.Previous != nil {
			n, err := copyForward(ctx, d.Store, rc)
			if err != nil {
				return nil, err
			}
			copied = n
		}
		appended, err := appendRestoreRecords(ctx, d, target, rc, copied)
		if err != nil {
			return nil, err
		}
		log.Info("audit restore hook done", "copied", copied, "appended", appended, "restore_seq", d.RestoreSeq, "run", target)
		return map[string]int{CountRecordsCopied: copied, CountRecordsAppended: appended}, nil
	}
}

// copyForward copies the pre-swap records above rc.AuditHighWater and their
// index rows into s, returning the number of records copied.
//
// This loop is deliberately sequential (CLAUDE.md concurrency rule: ordered
// and exclusive). It runs once per restore, the copy must be ordered by seq,
// and it is the only writer of these keys: nothing else touches the live audit
// keyspace until the hook returns.
func copyForward(ctx context.Context, s *Store, rc journal.RestoreContext) (_ int, err error) {
	hw := rc.AuditHighWater
	if hw == math.MaxUint64 {
		return 0, nil
	}
	prev, err := rc.Previous(restoreOwner)
	if err != nil {
		return 0, fmt.Errorf("audit: restore: open the pre-swap audit view: %w", err)
	}
	recRange := storage.ScanOptions{Start: recKey(hw + 1), End: keys.PrefixEnd(recPrefix())}

	// H, the highest seq to copy.
	top := recRange
	top.Reverse, top.KeysOnly, top.Limit = true, true, 1
	var high uint64
	found := false
	for kv, err := range prev.Scan(ctx, top) {
		if err != nil {
			return 0, fmt.Errorf("audit: restore: find the highest pre-swap record: %w", err)
		}
		if high, err = seqOfIndexKey(kv.Key); err != nil {
			return 0, err
		}
		found = true
	}
	if !found {
		return 0, nil
	}

	// Advance first: a crash after this and before the copy only skips IDs.
	if err := s.seq.AdvancePast(ctx, high); err != nil {
		return 0, fmt.Errorf("audit: restore: advance the sequence past %d: %w", high, err)
	}

	b := s.ks.NewBatch()
	pending := 0
	commit := func() error {
		if pending == 0 {
			return nil
		}
		if err := b.Commit(ctx, storage.Sync); err != nil {
			return fmt.Errorf("audit: restore: commit copy batch: %w", err)
		}
		if err := b.Close(); err != nil {
			return fmt.Errorf("audit: restore: close copy batch: %w", err)
		}
		b, pending = s.ks.NewBatch(), 0
		return nil
	}
	// The last, unused batch (and the one open at a failure) is closed here.
	defer func() { err = errors.Join(err, b.Close()) }()
	put := func(kv storage.KV) error {
		if err := b.Set(slices.Clone(kv.Key), slices.Clone(kv.Value)); err != nil {
			return fmt.Errorf("audit: restore: stage copy: %w", err)
		}
		pending++
		if pending >= restoreCopyBatch {
			return commit()
		}
		return nil
	}

	records := 0
	for kv, err := range prev.Scan(ctx, recRange) {
		if err != nil {
			return 0, fmt.Errorf("audit: restore: read pre-swap records: %w", err)
		}
		if err := put(kv); err != nil {
			return 0, err
		}
		records++
	}
	if err := commit(); err != nil {
		return 0, err
	}
	// Index rows: every row whose last segment is a copied seq. Rows of
	// records at or below the high-water mark are the archive's own.
	idx := keys.Prefix(keys.Str("idx"))
	for kv, err := range prev.Scan(ctx, storage.ScanOptions{Start: idx, End: keys.PrefixEnd(idx)}) {
		if err != nil {
			return 0, fmt.Errorf("audit: restore: read pre-swap index rows: %w", err)
		}
		seq, err := seqOfIndexKey(kv.Key)
		if err != nil {
			return 0, err
		}
		if seq <= hw || seq > high {
			continue
		}
		if err := put(kv); err != nil {
			return 0, err
		}
	}
	if err := commit(); err != nil {
		return 0, err
	}
	return records, nil
}

// appendRestoreRecords writes the stage, revoke and apply records through the
// store's normal Record path, skipping any that already exist for this
// restore. It returns how many it wrote.
func appendRestoreRecords(ctx context.Context, d RestoreDeps, targetID string, rc journal.RestoreContext, copied int) (int, error) {
	target := entity.Ref{Module: "core", Kind: "restore", ID: targetID}
	kind := restoreKindName(rc.Kind)
	appended := 0
	write := func(action string, details map[string]any) error {
		have, err := hasRestoreRecord(ctx, d.Store, action, target)
		if err != nil {
			return err
		}
		if have {
			return nil
		}
		err = d.Store.Record(ctx, Event{Action: action, Outcome: Success, Target: target, Details: details})
		if err != nil {
			return fmt.Errorf("audit: restore: record %s: %w", action, err)
		}
		appended++
		return nil
	}

	if st, ok := stagedRecord(rc.Journal); ok {
		if err := write("core.restore.stage", map[string]any{
			"kind": st.Note, "archive_id": rc.ArchiveID, "foreign": rc.Foreign,
		}); err != nil {
			return appended, err
		}
	}
	if len(rc.Counts) > 0 {
		// A list of {family, n} and not the map itself: the family names
		// (api_keys_revoked, passwords_blanked) match the redaction's key
		// pattern, which would replace every count with "[redacted]".
		fams := make([]any, 0, len(rc.Counts))
		for _, name := range slices.Sorted(maps.Keys(rc.Counts)) {
			fams = append(fams, map[string]any{"family": name, "n": rc.Counts[name]})
		}
		if err := write("core.restore.revoke", map[string]any{"counts": fams}); err != nil {
			return appended, err
		}
	}
	err := write("core.restore.apply", map[string]any{
		"kind": kind, "boundary": rc.Boundary, "copied": copied,
		"restore_seq": d.RestoreSeq, "foreign": rc.Foreign,
	})
	return appended, err
}

// hasRestoreRecord reports whether a record of action already exists for target.
func hasRestoreRecord(ctx context.Context, s *Store, action string, target entity.Ref) (bool, error) {
	for rec, err := range s.Query(ctx, Filter{ActionPrefix: action, Target: target, Limit: 1}) {
		if err != nil {
			return false, fmt.Errorf("audit: restore: look for an earlier %s: %w", action, err)
		}
		if rec.Action == action {
			return true, nil
		}
	}
	return false, nil
}

// stagedRecord is the "staged" record of the restore journal, if any.
func stagedRecord(j []restorejournal.Record) (restorejournal.Record, bool) {
	for _, r := range j {
		if r.Step == "staged" {
			return r, true
		}
	}
	return restorejournal.Record{}, false
}

// restoreKindName is the audit label of a hook-side restore kind.
func restoreKindName(k journal.RestoreKind) string {
	switch k {
	case journal.KindArchive:
		return "archive"
	case journal.KindRevert:
		return "revert"
	case journal.KindCheckpoint:
		return "checkpoint"
	case journal.KindExternal:
		return "external"
	}
	return "unknown"
}
