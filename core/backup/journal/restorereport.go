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
