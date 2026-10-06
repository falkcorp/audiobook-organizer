// file: internal/metafetch/apply_repair_options_test.go
// version: 1.0.0
// guid: 0d7f3a92-6c15-4e8b-9a24-b51e8c6d3f07
// last-edited: 2026-10-06

package metafetch

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The ApplyOptions a Repairs fixer (maintenance.version-twin-metadata) applies
// with, and the per-batch undo its op revert uses. Synthetic rows only.

func repairOptsCand() MetadataCandidate {
	return MetadataCandidate{Title: "Synthetic Saga", Author: "Synthetic Author A", Narrator: "Synthetic Narrator A",
		ASIN: "B0SYNTH002", Source: "audible", CategoryTags: []string{"genre:synthetic"}}
}

// mkBook creates a book carrying the shared author, optionally in a version
// group.
func mkRepairOptsBook(t *testing.T, st *database.PebbleStore, authorID int, group string) string {
	t.Helper()
	b := &database.Book{Title: "Synthetic Saga", Format: "m4b", FilePath: "/lib/Synthetic Saga " + group, AuthorID: &authorID}
	if group != "" {
		b.VersionGroupID = &group
	}
	created, err := st.CreateBook(b)
	require.NoError(t, err)
	return created.ID
}

func repairOptsStore(t *testing.T) (*database.PebbleStore, int) {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	a, err := st.CreateAuthor("Synthetic Author A")
	require.NoError(t, err)
	return st, a.ID
}

// SkipHashElection: a book in another group carrying the record is not
// merged and the applied book is not demoted. Without it (the control) the
// election flags one of the two.
func TestApplyOptions_SkipHashElection(t *testing.T) {
	for _, skip := range []bool{true, false} {
		st, aid := repairOptsStore(t)
		cand := repairOptsCand()
		out := mkRepairOptsBook(t, st, aid, "g-out")
		_, err := st.ModifyBook(out, func(b *database.Book) error {
			h := CandidateSourceHash(cand)
			b.MetadataSourceHash = &h
			return nil
		})
		require.NoError(t, err)
		target := mkRepairOptsBook(t, st, aid, "g-target")

		_, err = NewService(st).ApplyMetadataCandidateWithOptions(target, cand, []string{"narrator"},
			ApplyOptions{FillOnly: true, SkipHashElection: skip})
		require.NoError(t, err)
		merged := 0
		for _, id := range []string{out, target} {
			b, err := st.GetBookByID(id)
			require.NoError(t, err)
			if b.MergedIntoBookID != nil {
				merged++
			}
		}
		if skip {
			require.Zero(t, merged, "SkipHashElection: no book is flagged")
		} else {
			require.Equal(t, 1, merged, "control: the election flags one book")
		}
	}
}

// BookRowOnly: no system or category tags and no fetched-value provenance;
// HistorySource labels every history row; BatchID is honoured.
func TestApplyOptions_BookRowOnlyAndHistorySource(t *testing.T) {
	st, aid := repairOptsStore(t)
	id := mkRepairOptsBook(t, st, aid, "")
	_, err := NewService(st).ApplyMetadataCandidateWithOptions(id, repairOptsCand(), []string{"narrator"},
		ApplyOptions{FillOnly: true, BookRowOnly: true, HistorySource: "maintenance.synthetic", BatchID: "op-1:" + id})
	require.NoError(t, err)
	tags, err := st.GetBookTags(id)
	require.NoError(t, err)
	require.Empty(t, tags)
	states, err := st.GetMetadataFieldStates(id)
	require.NoError(t, err)
	require.Empty(t, states)
	hist, err := st.GetBookChangeHistory(id, 50)
	require.NoError(t, err)
	require.NotEmpty(t, hist)
	for _, h := range hist {
		require.Equal(t, "maintenance.synthetic", h.Source)
		require.Equal(t, "op-1:"+id, h.BatchID)
	}

	// Control: without BookRowOnly the tags land.
	id2 := mkRepairOptsBook(t, st, aid, "")
	_, err = NewService(st).ApplyMetadataCandidateWithOptions(id2, repairOptsCand(), []string{"narrator"}, ApplyOptions{FillOnly: true})
	require.NoError(t, err)
	tags, err = st.GetBookTags(id2)
	require.NoError(t, err)
	require.NotEmpty(t, tags)
}

// failingHistoryStore fails every change-history write.
type failingHistoryStore struct{ *database.PebbleStore }

func (failingHistoryStore) RecordMetadataChange(*database.MetadataChangeRecord) error {
	return errors.New("synthetic history failure")
}

// RequireHistory: a failed history write comes back as an error WITH the
// response (the write stands). Without it the apply reports success.
func TestApplyOptions_RequireHistory(t *testing.T) {
	st, aid := repairOptsStore(t)
	svc := NewService(failingHistoryStore{st})
	id := mkRepairOptsBook(t, st, aid, "")
	resp, err := svc.ApplyMetadataCandidateWithOptions(id, repairOptsCand(), []string{"narrator"},
		ApplyOptions{FillOnly: true, RequireHistory: true})
	require.Error(t, err)
	require.NotNil(t, resp)
	require.NotNil(t, resp.Book)

	id2 := mkRepairOptsBook(t, st, aid, "")
	_, err = svc.ApplyMetadataCandidateWithOptions(id2, repairOptsCand(), []string{"narrator"}, ApplyOptions{FillOnly: true})
	require.NoError(t, err, "control: an ordinary automatic apply swallows the history failure")
}

// UndoApplyBatch undoes exactly the named batch even when a later apply
// followed it, leaves a field edited since, and refuses a second undo.
func TestUndoApplyBatch(t *testing.T) {
	st, aid := repairOptsStore(t)
	svc := NewService(st)
	id := mkRepairOptsBook(t, st, aid, "")
	_, err := svc.ApplyMetadataCandidateWithOptions(id, repairOptsCand(), []string{"narrator", "publisher"},
		ApplyOptions{FillOnly: true, BatchID: "batch-a"})
	require.NoError(t, err)
	_, err = st.ModifyBook(id, func(b *database.Book) error {
		e := "Synthetic Narrator Edit"
		b.Narrator = &e
		return nil
	})
	require.NoError(t, err)

	_, err = svc.UndoApplyBatch(id, "batch-missing")
	require.ErrorIs(t, err, ErrNoApplyToUndo)

	res, err := svc.UndoApplyBatch(id, "batch-a")
	require.NoError(t, err)
	require.Contains(t, res.ChangedSince, "narrator")
	require.Contains(t, res.Reverted, "metadata_review_status")
	b, err := st.GetBookByID(id)
	require.NoError(t, err)
	require.Equal(t, "Synthetic Narrator Edit", *b.Narrator, "an edit since is left")
	require.False(t, database.MetadataApplied(b.MetadataReviewStatus))

	_, err = svc.UndoApplyBatch(id, "batch-a")
	require.ErrorIs(t, err, ErrApplyAlreadyUndone)
	_, err = svc.UndoLastApply(id)
	require.ErrorIs(t, err, ErrApplyAlreadyUndone)
}
