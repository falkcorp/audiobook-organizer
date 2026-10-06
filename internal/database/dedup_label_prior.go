// file: internal/database/dedup_label_prior.go
// version: 2.0.0
// guid: 5a1c7e93-0b2d-4f64-8e35-9c7d2b1f4a08
// last-edited: 2026-10-06

package database

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// LabelReasonUserBulkDismiss is the label_reason of the not_dup gold label a
// filter-scoped bulk dismiss records. One definition, shared by the store
// (which must never save such a label as a "prior") and the dedup handler
// (which writes it).
const LabelReasonUserBulkDismiss = "user_bulk_dismiss"

// ErrNoBulkDismissRecord is returned when a candidate carries no bulk-dismiss
// undo record: a bulk dismiss did not dismiss it, or its revert already
// finished. The revert endpoint leaves such a row alone.
var ErrNoBulkDismissRecord = errors.New("no bulk-dismiss record for this candidate")

// ErrPriorIsBulkDismiss is returned by SaveLabelBeforeBulk when the label it
// would save as the "earlier verdict" is itself a bulk-dismiss label with no
// undo record behind it. Saving it would make a revert restore the bulk
// verdict it is meant to undo.
var ErrPriorIsBulkDismiss = errors.New("current label is a bulk-dismiss label, not an earlier verdict")

// ErrLabelChangedSinceBulk is returned by RestoreLabelAfterBulkRevert when the
// candidate's label is no longer the one the bulk dismiss left (someone
// re-labelled it since): that verdict is not the bulk dismiss's to undo.
var ErrLabelChangedSinceBulk = errors.New("label changed since the bulk dismiss")

// dedupLabelPriorPfx keys the undo record a filter-scoped bulk dismiss writes
// for EVERY row it dismisses: the labeled example it replaced, or an explicit
// "no earlier label" marker. The revert is keyed off this record, not off the
// label reason, because the not_dup capture is best-effort and a dismissed
// row whose capture failed must still be undoable. Outside the dedup:label:
// prefix on purpose: label scans must never see these rows.
const dedupLabelPriorPfx = "dedup:lblprior:"

// bulkPriorRecordVersion tags the current record shape. A record without it
// is the bare LabeledExample the first version wrote (#3783), still decoded.
const bulkPriorRecordVersion = 2

type bulkPriorRecord struct {
	V int `json:"v"`
	// Prior is the label the bulk dismiss replaced; nil = there was none.
	Prior *LabeledExample `json:"prior,omitempty"`
}

// BulkDismissPrior is a decoded bulk-dismiss undo record.
type BulkDismissPrior struct {
	// Prior is the label the bulk dismiss replaced; nil = there was none, so a
	// revert removes the label.
	Prior *LabeledExample
}

// Owns reports whether cur -- the candidate's current label -- is still what
// the bulk dismiss left behind: no label or the earlier one (the not_dup
// capture failed or a partial revert already restored it), or the bulk
// not_dup itself. Anything else is a later verdict the revert must not touch.
func (p *BulkDismissPrior) Owns(cur *LabeledExample) bool {
	if cur == nil || cur.LabelReason == LabelReasonUserBulkDismiss {
		return true
	}
	return p.Prior != nil && sameVerdict(*p.Prior, *cur)
}

func sameVerdict(a, b LabeledExample) bool {
	return a.Label == b.Label && a.LabelSource == b.LabelSource &&
		a.LabelReason == b.LabelReason && a.DecidedAt == b.DecidedAt
}

func dedupLabelPriorKey(candidateID int64) []byte {
	return []byte(fmt.Sprintf("%s%016x", dedupLabelPriorPfx, uint64(candidateID)))
}

// readBulkPriorRaw returns the stored record, or found=false.
func (s *EmbeddingStore) readBulkPriorRaw(candidateID int64) (rec *BulkDismissPrior, found bool, err error) {
	val, closer, gerr := s.db.Get(dedupLabelPriorKey(candidateID))
	if gerr == pebble.ErrNotFound {
		return nil, false, nil
	}
	if gerr != nil {
		return nil, false, fmt.Errorf("get bulk-dismiss record %d: %w", candidateID, gerr)
	}
	defer func() { _ = closer.Close() }()
	var r bulkPriorRecord
	if err := json.Unmarshal(val, &r); err != nil {
		return nil, false, fmt.Errorf("unmarshal bulk-dismiss record %d: %w", candidateID, err)
	}
	if r.V >= bulkPriorRecordVersion {
		return &BulkDismissPrior{Prior: r.Prior}, true, nil
	}
	// Legacy (#3783): the bare LabeledExample that was replaced.
	var legacy LabeledExample
	if err := json.Unmarshal(val, &legacy); err != nil {
		return nil, false, fmt.Errorf("unmarshal legacy bulk-dismiss record %d: %w", candidateID, err)
	}
	return &BulkDismissPrior{Prior: &legacy}, true, nil
}

// GetBulkDismissPrior returns candidateID's bulk-dismiss undo record, or
// ErrNoBulkDismissRecord.
//
// Legacy rows: the first version (#3783) wrote no record when the row had no
// earlier label, and marked its dismissals only by the label reason. A row
// with no record whose current label is a bulk-dismiss label is therefore
// read as a "no earlier label" record, so those dismissals stay undoable.
func (s *EmbeddingStore) GetBulkDismissPrior(candidateID int64) (*BulkDismissPrior, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	rec, found, err := s.readBulkPriorRaw(candidateID)
	if err != nil {
		return nil, err
	}
	if found {
		return rec, nil
	}
	cur, err := s.GetLabeledExample(candidateID)
	if err != nil {
		return nil, err
	}
	if cur != nil && cur.LabelReason == LabelReasonUserBulkDismiss {
		return &BulkDismissPrior{}, nil
	}
	return nil, ErrNoBulkDismissRecord
}

// SaveLabelBeforeBulk writes the undo record for a bulk dismiss of
// candidateID: its current labeled example, or a "no earlier label" marker.
// The record is ALWAYS written (or kept), because the revert is keyed off it.
//
// A current label that is itself a bulk-dismiss label is never saved as the
// earlier verdict: when an undo record already exists (an earlier bulk
// dismiss whose revert has not finished) that record is kept as-is;
// otherwise ErrPriorIsBulkDismiss is returned and the caller must not
// proceed with the dismiss.
func (s *EmbeddingStore) SaveLabelBeforeBulk(candidateID int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	cur, err := s.GetLabeledExample(candidateID)
	if err != nil {
		return err
	}
	if cur != nil && cur.LabelReason == LabelReasonUserBulkDismiss {
		_, found, rerr := s.readBulkPriorRaw(candidateID)
		if rerr != nil {
			return rerr
		}
		if found {
			return nil
		}
		return fmt.Errorf("save prior label %d: %w", candidateID, ErrPriorIsBulkDismiss)
	}
	data, err := json.Marshal(bulkPriorRecord{V: bulkPriorRecordVersion, Prior: cur})
	if err != nil {
		return fmt.Errorf("marshal bulk-dismiss record %d: %w", candidateID, err)
	}
	return s.db.Set(dedupLabelPriorKey(candidateID), data, pebble.Sync)
}

// DiscardBulkDismissRecord deletes candidateID's undo record. Used when the
// dismiss the record was written for is rolled back. A no-op when absent.
func (s *EmbeddingStore) DiscardBulkDismissRecord(candidateID int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	return s.db.Delete(dedupLabelPriorKey(candidateID), pebble.Sync)
}

// RestoreLabelAfterBulkRevert undoes a bulk dismiss's label change: the saved
// example becomes the candidate's label again, or, when the record says there
// was none, the label is removed. The record is deleted only after the label
// write succeeded, so a failed restore can be retried. restored reports
// whether an earlier label was put back.
//
// Returns ErrNoBulkDismissRecord when there is no record, and
// ErrLabelChangedSinceBulk (touching nothing) when the current label is a
// later verdict rather than what the bulk dismiss left.
func (s *EmbeddingStore) RestoreLabelAfterBulkRevert(candidateID int64) (restored bool, err error) {
	rec, err := s.GetBulkDismissPrior(candidateID)
	if err != nil {
		return false, err
	}
	cur, err := s.GetLabeledExample(candidateID)
	if err != nil {
		return false, err
	}
	if !rec.Owns(cur) {
		return false, fmt.Errorf("restore label %d: %w", candidateID, ErrLabelChangedSinceBulk)
	}
	if rec.Prior != nil {
		if err := s.UpsertLabeledExample(*rec.Prior); err != nil {
			return false, err
		}
	} else if cur != nil {
		if err := s.DeleteLabeledExample(candidateID); err != nil {
			return false, err
		}
	}
	return rec.Prior != nil, s.db.Delete(dedupLabelPriorKey(candidateID), pebble.Sync)
}
