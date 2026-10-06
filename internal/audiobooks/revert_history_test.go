// file: internal/audiobooks/revert_history_test.go
// version: 1.1.1
// guid: 829118c5-b507-4c65-8bb6-eaf346c88634
// last-edited: 2026-10-06

package audiobooks

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// histSpyStore is a real Pebble store whose history writes can be made to
// fail. Embedding the concrete store keeps every capability the revert
// asserts (repair_book_create's checks, the path index).
type histSpyStore struct {
	*database.PebbleStore
	fail bool
}

func (s *histSpyStore) RecordMetadataChange(r *database.MetadataChangeRecord) error {
	if s.fail {
		return errors.New("history store down")
	}
	return s.PebbleStore.RecordMetadataChange(r)
}

// revertRows returns the operation_revert history rows of book id for opID,
// by field.
func revertRows(t *testing.T, s *database.PebbleStore, id, opID string) map[string]database.MetadataChangeRecord {
	t.Helper()
	rows, err := s.GetBookChangeHistory(id, 1<<20)
	require.NoError(t, err)
	out := map[string]database.MetadataChangeRecord{}
	for _, r := range rows {
		if r.Source != RevertHistorySource || r.BatchID != opID {
			continue
		}
		require.Equal(t, "undo", r.ChangeType, "field %s", r.Field)
		out[r.Field] = r
	}
	return out
}

func fieldsOf(m map[string]database.MetadataChangeRecord) []string {
	out := make([]string, 0, len(m))
	for f := range m {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// TestRevertHistory_EverySiteRecords: every revert path that restores a book
// column records history rows for exactly the columns it restored, with
// Source operation_revert, the operation id as BatchID and change type undo.
func TestRevertHistory_EverySiteRecords(t *testing.T) {
	type site struct {
		name string
		// run sets up the book, reverts, and returns the book id and the
		// fields the history must hold.
		run func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string)
	}
	sites := []site{
		{"metadata_update series_id", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			a, err := s.CreateSeries("Old Series", nil)
			require.NoError(t, err)
			b2, err := s.CreateSeries("New Series", nil)
			require.NoError(t, err)
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/b", SeriesID: &b2.ID})
			require.NoError(t, err)
			require.NoError(t, rs.revertMetadataUpdate(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: "metadata_update", FieldName: "series_id", OldValue: itoaT(a.ID), NewValue: itoaT(b2.ID)}, nil))
			return bk.ID, []string{database.HistoryFieldSeries}
		}},
		{"merged_into", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			surv := "survivor"
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/b", MergedIntoBookID: &surv})
			require.NoError(t, err)
			require.NoError(t, rs.revertBookMergedInto(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeBookMergedInto, OldValue: "", NewValue: surv}))
			return bk.ID, []string{"merged_into_book_id"}
		}},
		{"path update", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/new"})
			require.NoError(t, err)
			require.NoError(t, rs.revertBookPathUpdate(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeBookPathUpdate, OldValue: "/x/old", NewValue: "/x/new"}))
			return bk.ID, []string{"file_path"}
		}},
		{"primary demote", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			no := false
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/b", IsPrimaryVersion: &no})
			require.NoError(t, err)
			require.NoError(t, rs.revertBookPrimaryDemote(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeBookPrimaryDemote, OldValue: "", NewValue: "false"}))
			return bk.ID, []string{"is_primary_version"}
		}},
		{"soft delete", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/b"})
			require.NoError(t, err)
			_, err = s.ModifyBook(bk.ID, func(b *database.Book) error {
				yes, now := true, time.Now()
				b.MarkedForDeletion, b.MarkedForDeletionAt = &yes, &now
				return nil
			})
			require.NoError(t, err)
			require.NoError(t, rs.revertBookSoftDelete(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeBookSoftDelete}, undo.SoftDeleteStamps{}))
			// The restore also puts library_state back.
			return bk.ID, []string{"library_state", "marked_for_deletion", "marked_for_deletion_at"}
		}},
		{"file move", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			dir := t.TempDir()
			oldPath, newPath := filepath.Join(dir, "old", "b.m4b"), filepath.Join(dir, "new", "b.m4b")
			require.NoError(t, os.MkdirAll(filepath.Dir(newPath), 0o755))
			require.NoError(t, os.WriteFile(newPath, []byte("audio"), 0o644))
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: newPath})
			require.NoError(t, err)
			require.NoError(t, rs.revertFileMove(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: "file_move", FieldName: "file_path", OldValue: oldPath, NewValue: newPath}))
			return bk.ID, []string{"file_path"}
		}},
		{"junk-author primary", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			junk, err := s.CreateAuthor("Demon Cycle")
			require.NoError(t, err)
			real, err := s.CreateAuthor("Peter V. Brett")
			require.NoError(t, err)
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/b", AuthorID: &real.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetBookAuthors(bk.ID, []database.BookAuthor{{BookID: bk.ID, AuthorID: real.ID, Role: "author"}}))
			snap := undo.TitleRelinkCreditsSnapshot{AuthorID: &junk.ID,
				Credits: []database.BookAuthor{{BookID: bk.ID, AuthorID: junk.ID, Role: "author"}}}
			after := []database.BookAuthor{{BookID: bk.ID, AuthorID: real.ID, Role: "author"}}
			move := undo.JunkAuthorCreditsMove{FromAuthorID: junk.ID, IntoAuthorID: real.ID, PrimaryAfter: &real.ID,
				PrimaryChanged: true, CreditsAfter: &after}
			oldJSON, _ := json.Marshal(snap)
			newJSON, _ := json.Marshal(move)
			require.NoError(t, rs.revertJunkAuthorCredits(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeJunkAuthorCredits, FieldName: "book_authors",
				OldValue: string(oldJSON), NewValue: string(newJSON)}))
			return bk.ID, []string{database.HistoryFieldAuthor}
		}},
		{"title-relink", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			title, err := s.CreateAuthor("Arcane Chef 2")
			require.NoError(t, err)
			real, err := s.CreateAuthor("Brand New Writer")
			require.NoError(t, err)
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/b", AuthorID: &real.ID})
			require.NoError(t, err)
			require.NoError(t, s.SetBookAuthors(bk.ID, []database.BookAuthor{{BookID: bk.ID, AuthorID: real.ID, Role: "author"}}))
			snap := undo.TitleRelinkCreditsSnapshot{AuthorID: &title.ID,
				Credits: []database.BookAuthor{{BookID: bk.ID, AuthorID: title.ID, Role: "author"}}}
			oldJSON, _ := json.Marshal(snap)
			newJSON, _ := json.Marshal(undo.TitleRelinkCreditsMove{FromAuthorID: title.ID, IntoAuthorID: real.ID})
			require.NoError(t, rs.revertTitleRelinkCredits(&database.OperationChange{OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeTitleRelinkCredits, FieldName: "book_authors",
				OldValue: string(oldJSON), NewValue: string(newJSON)}))
			return bk.ID, []string{database.HistoryFieldAuthor}
		}},
		{"repair_book_create", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/created"})
			require.NoError(t, err)
			require.NoError(t, rs.revertRepairBookCreate(&database.OperationChange{ID: "rc1", OperationID: opID, BookID: bk.ID,
				ChangeType: undo.ChangeTypeRepairBookCreate, NewValue: bk.ID}))
			return bk.ID, []string{"marked_for_deletion", "marked_for_deletion_at"}
		}},
		{"settle crown", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			gid, no, yes := "vg-crown", false, true
			o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &no})
			require.NoError(t, err)
			_, err = s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &yes})
			require.NoError(t, err)
			_, err = rs.settleGroup(opID, gid, []string{o.ID}, nil, nil)
			require.NoError(t, err)
			return o.ID, []string{"is_primary_version"}
		}},
		{"settle demote", func(t *testing.T, s *database.PebbleStore, rs *RevertService, opID string) (string, []string) {
			// The original yields to a member made primary after the
			// operation (laterPick): explicit false on the original.
			gid, yes := "vg-yield", true
			o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &yes})
			require.NoError(t, err)
			_, err = s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &yes})
			require.NoError(t, err)
			_, err = rs.settleGroup(opID, gid, []string{o.ID}, nil, map[string]handOffEvidence{o.ID: {crowned: []string{"someone-else"}, recorded: true}})
			require.NoError(t, err)
			b, err := s.GetBookByID(o.ID)
			require.NoError(t, err)
			require.NotNil(t, b.IsPrimaryVersion)
			require.False(t, *b.IsPrimaryVersion)
			return o.ID, []string{"is_primary_version"}
		}},
	}
	for _, tc := range sites {
		t.Run(tc.name, func(t *testing.T) {
			s := newRevertPebble(t)
			rs := NewRevertService(&histSpyStore{PebbleStore: s})
			opID := "op-" + tc.name
			id, want := tc.run(t, s, rs, opID)
			sort.Strings(want)
			got := revertRows(t, s, id, opID)
			require.Equal(t, want, fieldsOf(got))
		})
	}
}

// TestRevertHistory_SettleCrownRecordsTheDemotedMemberToo: Crown writes the
// other member's explicit false through the recorded store as well.
func TestRevertHistory_SettleCrownRecordsTheDemotedMemberToo(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(&histSpyStore{PebbleStore: s})
	gid, no, yes := "vg", false, true
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	x, err := s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	_, err = rs.settleGroup("op", gid, []string{o.ID}, nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"is_primary_version"}, fieldsOf(revertRows(t, s, x.ID, "op")))
}

// TestRevertHistory_RefusedWriteRecordsNothing: a compare-and-set refusal
// writes nothing and records nothing; so does an already-restored row and a
// repair_book_create whose book is gone.
func TestRevertHistory_RefusedWriteRecordsNothing(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(&histSpyStore{PebbleStore: s})
	bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/current"})
	require.NoError(t, err)
	err = rs.revertBookPathUpdate(&database.OperationChange{OperationID: "op", BookID: bk.ID,
		ChangeType: undo.ChangeTypeBookPathUpdate, OldValue: "/x/old", NewValue: "/x/not-current"})
	require.Error(t, err)
	// Already restored: OldValue is what the book holds (the caller marks
	// the row reverted on this error).
	require.ErrorIs(t, rs.revertBookPathUpdate(&database.OperationChange{OperationID: "op", BookID: bk.ID,
		ChangeType: undo.ChangeTypeBookPathUpdate, OldValue: "/x/current", NewValue: "/x/elsewhere"}), undo.ErrAlreadyRestored)
	require.Empty(t, revertRows(t, s, bk.ID, "op"))

	// repair_book_create of a book that is gone: nothing to retire, no row.
	require.NoError(t, rs.revertRepairBookCreate(&database.OperationChange{ID: "rc", OperationID: "op", BookID: "gone-book",
		ChangeType: undo.ChangeTypeRepairBookCreate, NewValue: "gone-book"}))
	require.Empty(t, revertRows(t, s, "gone-book", "op"))
}

// TestRevertHistory_HistoryFailureDoesNotFailTheRevert: the restore has
// committed when history is written; a history failure is logged, not
// returned.
func TestRevertHistory_HistoryFailureDoesNotFailTheRevert(t *testing.T) {
	s := newRevertPebble(t)
	spy := &histSpyStore{PebbleStore: s, fail: true}
	rs := NewRevertService(spy)
	bk, err := s.CreateBook(&database.Book{Title: "B", FilePath: "/x/new"})
	require.NoError(t, err)
	require.NoError(t, rs.revertBookPathUpdate(&database.OperationChange{OperationID: "op", BookID: bk.ID,
		ChangeType: undo.ChangeTypeBookPathUpdate, OldValue: "/x/old", NewValue: "/x/new"}))
	got, err := s.GetBookByID(bk.ID)
	require.NoError(t, err)
	require.Equal(t, "/x/old", got.FilePath)
	require.Empty(t, revertRows(t, s, bk.ID, "op"))
}

func itoaT(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}
