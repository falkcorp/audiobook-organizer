// file: internal/undo/metadata_apply_test.go
// version: 1.1.0
// guid: 4a8c1e63-9f27-4b05-8d3e-e6b2a0c79f18
// last-edited: 2026-10-10

package undo

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The classifier, the revert and the preflight must agree on the two rows
// maintenance.version-twin-metadata journals: a well-formed row is
// restorable, a malformed one is labelled not restorable.
func TestMetadataApplyRows_Classified(t *testing.T) {
	ok := []*database.OperationChange{
		{ChangeType: ChangeTypeMetadataApply, FieldName: MetadataApplyField, NewValue: "op:book"},
		{ChangeType: ChangeTypeMetadataCacheCopy, FieldName: MetadataCacheField, OldValue: MetadataCacheAbsent, NewValue: "h|t|1|||"},
		{ChangeType: ChangeTypeMetadataCacheCopy, FieldName: MetadataCacheField, OldValue: `{"book_id":"b"}`, NewValue: "h|t|1|||"},
	}
	for _, c := range ok {
		require.True(t, IsRestorable(c), "%+v", c)
	}
	bad := []*database.OperationChange{
		{ChangeType: ChangeTypeMetadataApply, FieldName: MetadataApplyField},
		{ChangeType: ChangeTypeMetadataApply, FieldName: "other", NewValue: "op:book"},
		{ChangeType: ChangeTypeMetadataCacheCopy, FieldName: MetadataCacheField, OldValue: "not json", NewValue: "h"},
		{ChangeType: ChangeTypeMetadataCacheCopy, FieldName: MetadataCacheField, OldValue: MetadataCacheAbsent, NewValue: MetadataCacheAbsent},
	}
	for _, c := range bad {
		require.False(t, IsRestorable(c), "%+v", c)
	}
}

// CheckMetadataCacheCopy: restore while the row carries the copy's stamp,
// already restored when it is back to the prior state, refused otherwise.
func TestCheckMetadataCacheCopy(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	copied := &database.MetadataCandidateCache{BookID: "b", SourceHash: "h", FetchedAt: at,
		Candidates: []json.RawMessage{json.RawMessage(`{}`)}}
	c := &database.OperationChange{BookID: "b", ChangeType: ChangeTypeMetadataCacheCopy, FieldName: MetadataCacheField,
		OldValue: MetadataCacheAbsent, NewValue: MetadataCacheStamp(copied)}
	require.NoError(t, CheckMetadataCacheCopy(copied, c))
	require.ErrorIs(t, CheckMetadataCacheCopy(nil, c), ErrAlreadyRestored)
	refetched := *copied
	refetched.FetchedAt = at.Add(time.Hour)
	require.Equal(t, ReasonChangedSince, RefusalReason(CheckMetadataCacheCopy(&refetched, c)))

	prior := &database.MetadataCandidateCache{BookID: "b", FetchedAt: at.Add(-time.Hour)}
	old, err := EncodeMetadataCacheOld(prior)
	require.NoError(t, err)
	c.OldValue = old
	require.NoError(t, CheckMetadataCacheCopy(copied, c))
	require.ErrorIs(t, CheckMetadataCacheCopy(prior, c), ErrAlreadyRestored)
	require.Equal(t, ReasonChangedSince, RefusalReason(CheckMetadataCacheCopy(nil, c)))
}

// A retitle marks the copied row Stale in place (same time, same candidates):
// the revert refuses it as changed-since rather than restoring the prior row
// unmarked. An unmarked row's stamp keeps its pre-mark format, so a journal
// row written before the mark existed still matches.
func TestCheckMetadataCacheCopy_StaleRowIsChangedSince(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	copied := &database.MetadataCandidateCache{BookID: "b", SourceHash: "h", FetchedAt: at,
		Candidates: []json.RawMessage{json.RawMessage(`{}`)}, SearchFingerprint: "fp", FetchedForASIN: "B00OLDASIN"}
	require.Equal(t, "h|"+at.Format(time.RFC3339Nano)+"|1|fp|B00OLDASIN|", MetadataCacheStamp(copied),
		"an unmarked stamp is the pre-mark format")
	c := &database.OperationChange{BookID: "b", ChangeType: ChangeTypeMetadataCacheCopy, FieldName: MetadataCacheField,
		OldValue: MetadataCacheAbsent, NewValue: MetadataCacheStamp(copied)}
	require.NoError(t, CheckMetadataCacheCopy(copied, c))

	marked := *copied
	marked.Stale, marked.StaleQuestionFP = true, "fp"
	require.NotEqual(t, MetadataCacheStamp(copied), MetadataCacheStamp(&marked))
	require.Equal(t, ReasonChangedSince, RefusalReason(CheckMetadataCacheCopy(&marked, c)))
}
