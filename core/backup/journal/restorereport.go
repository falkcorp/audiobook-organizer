// file: core/backup/journal/restorereport.go
// version: 0.1.0
// guid: 91c3e5a7-2d48-4b06-8f1a-6e0d7b24c953
// last-edited: 2026-11-21

package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// restoreReportKey is c/backup/restore_report: what the restore hooks of the
// last run reported (a plain write, like restore_hooked/<seq>). The hooks'
// report is the source of the health tokens restore_last_admin_kept and
// restore_admins_review (05-health 5H.3.1), which stay until an interactive
// admin acknowledges them, so a report that lived only in memory would clear on
// the first restart.
const restoreReportKey = "restore_report"

// RestoreReport is the accounts the restore hooks flagged: the users whose last
// core:admin entry was kept (restricted) and, after a restore with no pre-swap
// store, every admin holder to review. User IDs only.
type RestoreReport struct {
	Kept   []string `json:"kept,omitempty"`
	Review []string `json:"review,omitempty"`
}

// Empty reports whether the report flags nobody.
func (r RestoreReport) Empty() bool { return len(r.Kept) == 0 && len(r.Review) == 0 }

type restoreReportRecord struct {
	RestoreReport
	At time.Time `json:"at"`
}

// PutRestoreReport records rep as the report of the last hook run, replacing
// the previous one; an empty report deletes it. It is synchronous, and the
// caller commits it before the record that says the hooks completed
// (MarkHooked, ClearHookRun): a crash between the two re-runs the hooks, which
// write the report again.
func PutRestoreReport(ctx context.Context, ks storage.Keyspace, rep RestoreReport, at time.Time) (err error) {
	b := ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if rep.Empty() {
		if err := b.In(ks).Delete([]byte(restoreReportKey)); err != nil {
			return err
		}
	} else {
		raw, err := json.Marshal(restoreReportRecord{RestoreReport: rep, At: at.UTC()})
		if err != nil {
			return err
		}
		if err := b.In(ks).Set([]byte(restoreReportKey), raw); err != nil {
			return err
		}
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("journal: record %s: %w", restoreReportKey, err)
	}
	return nil
}

// ReadRestoreReport is the report PutRestoreReport recorded, empty when none is
// recorded. A record that is present and does not decode is an error: a report
// that cannot be read must not read as "nothing to review".
func ReadRestoreReport(ctx context.Context, ks storage.Reader) (RestoreReport, error) {
	raw, err := ks.Get(ctx, []byte(restoreReportKey))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return RestoreReport{}, nil
	case err != nil:
		return RestoreReport{}, fmt.Errorf("journal: read %s: %w", restoreReportKey, err)
	}
	var rec restoreReportRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return RestoreReport{}, fmt.Errorf("journal: %s does not decode: %w", restoreReportKey, err)
	}
	return rec.RestoreReport, nil
}

// ErrNotInReview is Acknowledge of an account the stored report does not list:
// no report is recorded, the report has no review list, or the account is not
// on it (never listed, or already acknowledged).
var ErrNotInReview = errors.New("journal: the account is not listed for review")

// ReviewAcks removes accounts from the report's review list, one at a time,
// as an interactive admin acknowledges them (05-rbac 7.1: "until an
// interactive core:admin session acknowledges each one").
//
// c/backup/restore_report is a plain write, not a Serialized key, so a batch
// cannot carry an Expect on it and its commit cannot fail on a concurrent
// change. The read-modify-write is therefore made exclusive instead: mu is held
// from the read to the Commit, so two acknowledgements never both decide on the
// same record (the second would otherwise write the record back with the
// first's account still on it). The only other writer is the boot's hook run
// (PutRestoreReport), which finishes before any route is mounted. Making the key
// Serialized would make that check the store's, but PutRestoreReport's plain
// write would then need an Expect and the c/backup/ owner spec would change.
type ReviewAcks struct {
	ks storage.Keyspace
	mu sync.Mutex

	// beforeCommit runs after the new record is staged and before Commit: the
	// tests' seam. Nil outside tests.
	beforeCommit func()
}

// NewReviewAcks is the acknowledgements over ks, the c/backup/ keyspace.
func NewReviewAcks(ks storage.Keyspace) *ReviewAcks { return &ReviewAcks{ks: ks} }

// Acknowledge removes userID from the stored report's review list. The report
// is read again under the lock, so an account another acknowledgement just
// removed is ErrNotInReview, not removed twice. The Kept list is not touched:
// restore_last_admin_kept clears by recovery, not by review. A report left with
// nobody on either list is deleted (PutRestoreReport's replace rule); otherwise
// the record is rewritten with its hook run's time.
//
// stage runs after the record's write is staged, with the same batch, so what it
// stages (the audit record) commits with the removal or not at all; a stage
// error discards both. A report that cannot be read or does not decode is an
// error that is not ErrNotInReview: an unreadable report is not an unlisted
// account.
func (r *ReviewAcks) Acknowledge(ctx context.Context, userID string, stage func(*storage.Batch) error) (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := r.ks.Get(ctx, []byte(restoreReportKey))
	switch {
	case errors.Is(err, storage.ErrNotFound):
		return ErrNotInReview
	case err != nil:
		return fmt.Errorf("journal: read %s: %w", restoreReportKey, err)
	}
	var rec restoreReportRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return fmt.Errorf("journal: %s does not decode: %w", restoreReportKey, err)
	}
	if !slices.Contains(rec.Review, userID) {
		return ErrNotInReview
	}
	rec.Review = slices.DeleteFunc(slices.Clone(rec.Review), func(id string) bool { return id == userID })
	b := r.ks.NewBatch()
	defer func() { err = errors.Join(err, b.Close()) }()
	if rec.Empty() {
		err = b.In(r.ks).Delete([]byte(restoreReportKey))
	} else {
		var next []byte
		if next, err = json.Marshal(rec); err == nil {
			err = b.In(r.ks).Set([]byte(restoreReportKey), next)
		}
	}
	if err != nil {
		return fmt.Errorf("journal: stage %s: %w", restoreReportKey, err)
	}
	if stage != nil {
		if err := stage(b); err != nil {
			return err
		}
	}
	if r.beforeCommit != nil {
		r.beforeCommit()
	}
	if err := b.Commit(ctx, storage.Sync); err != nil {
		return fmt.Errorf("journal: record %s: %w", restoreReportKey, err)
	}
	return nil
}
