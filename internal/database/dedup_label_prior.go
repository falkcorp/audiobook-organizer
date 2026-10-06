// file: internal/database/dedup_label_prior.go
// version: 1.0.0
// guid: 5a1c7e93-0b2d-4f64-8e35-9c7d2b1f4a08
// last-edited: 2026-10-06

package database

import (
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

// dedupLabelPriorPfx keeps the labeled example a filter-scoped bulk dismiss
// replaced, so undoing that dismiss puts the earlier verdict back instead of
// leaving the bulk "not_dup" (or nothing) in its place. Outside the
// dedup:label: prefix on purpose: label scans must never see these rows.
const dedupLabelPriorPfx = "dedup:lblprior:"

func dedupLabelPriorKey(candidateID int64) []byte {
	return []byte(fmt.Sprintf("%s%016x", dedupLabelPriorPfx, uint64(candidateID)))
}

// SaveLabelBeforeBulk records candidateID's current labeled example as the one
// a bulk dismiss is about to replace. With no current label it clears any
// stale saved one, so a later restore cannot resurrect an older verdict.
func (s *EmbeddingStore) SaveLabelBeforeBulk(candidateID int64) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	cur, err := s.GetLabeledExample(candidateID)
	if err != nil {
		return err
	}
	if cur == nil {
		return s.db.Delete(dedupLabelPriorKey(candidateID), pebble.Sync)
	}
	data, err := json.Marshal(cur)
	if err != nil {
		return fmt.Errorf("marshal prior label %d: %w", candidateID, err)
	}
	return s.db.Set(dedupLabelPriorKey(candidateID), data, pebble.Sync)
}

// RestoreLabelAfterBulkRevert undoes SaveLabelBeforeBulk: the saved example
// becomes the candidate's label again, or, when none was saved, the label is
// removed. The saved row is deleted either way. restored reports whether an
// earlier label was put back.
func (s *EmbeddingStore) RestoreLabelAfterBulkRevert(candidateID int64) (restored bool, err error) {
	if err := s.checkClosed(); err != nil {
		return false, err
	}
	val, closer, gerr := s.db.Get(dedupLabelPriorKey(candidateID))
	switch {
	case gerr == pebble.ErrNotFound:
		return false, s.DeleteLabeledExample(candidateID)
	case gerr != nil:
		return false, fmt.Errorf("get prior label %d: %w", candidateID, gerr)
	}
	var prior LabeledExample
	uerr := json.Unmarshal(val, &prior)
	if cerr := closer.Close(); cerr != nil && uerr == nil {
		uerr = cerr
	}
	if uerr != nil {
		return false, fmt.Errorf("unmarshal prior label %d: %w", candidateID, uerr)
	}
	if err := s.UpsertLabeledExample(prior); err != nil {
		return false, err
	}
	return true, s.db.Delete(dedupLabelPriorKey(candidateID), pebble.Sync)
}
