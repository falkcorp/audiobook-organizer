// file: internal/audiobooks/revert_history_test.go
// version: 1.4.0
// guid: 829118c5-b507-4c65-8bb6-eaf346c88634
// last-edited: 2026-10-07

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

// TestRevertSettle_NeverWritesAnITunesMember: the settle pass's crown of an
// original would demote a nil-flag iTunes copy (a write to an iTunes book's
// primary flag). It refuses the whole crown, so the group is left as it
// stands and reported in SettleSkipped: not a failure, not owed for retry.
func TestRevertSettle_NeverWritesAnITunesMember(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, no, pid := "vg-itunes", false, "0123456789ABCDEF"
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	x, err := s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, ITunesPersistentID: &pid})
	require.NoError(t, err)
	result := &RevertResult{}
	msgs := rs.settleGroups("op-itunes", settleInput{touched: []settleTouch{
		{bookID: o.ID, changeType: undo.ChangeTypeBookPrimaryDemote, oldValue: "true"},
	}}, result)
	require.Empty(t, msgs)
	require.Empty(t, result.HandOffFailed)
	require.Len(t, result.SettleSkipped, 1)
	require.Contains(t, result.SettleSkipped[0], "iTunes copy "+x.ID)
	require.True(t, result.Partial())
	require.Contains(t, result.Summary(), "left unsettled to protect an iTunes book")
	xb, err := s.GetBookByID(x.ID)
	require.NoError(t, err)
	require.Nil(t, xb.IsPrimaryVersion, "the iTunes copy's flag is never written")
	ob, err := s.GetBookByID(o.ID)
	require.NoError(t, err)
	require.NotNil(t, ob.IsPrimaryVersion)
	require.False(t, *ob.IsPrimaryVersion, "the crown wrote nothing")
	require.False(t, rs.hasSettleOwed("op-itunes"), "not recorded for retry")
}

// B1: an ambiguous "crowned:<id>" note (2026-10-02..06) may name a member
// the hand-off only kept. It never puts that member past the iTunes guard:
// an explicit-true iTunes X the operation never wrote is not demoted, the
// group is left as it stands and reported.
func TestRevertSettle_AmbiguousCrownedNoteNeverUnlocksAnITunesMember(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes, pid := "vg-legacy", true, "0123456789ABCDEF"
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	x, err := s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, IsPrimaryVersion: &yes, ITunesPersistentID: &pid})
	require.NoError(t, err)
	ev := handOffEvidenceOf([]*database.OperationChange{{BookID: o.ID, ChangeType: undo.ChangeTypeBookPrimaryHandoff,
		FieldName: "version_group_id", OldValue: undo.HandOffCrownedValue(x.ID), NewValue: gid}})
	require.Equal(t, []string{x.ID}, ev[o.ID].crowned)
	require.Empty(t, ev[o.ID].wrote, "the ambiguous form is never proof of a write")
	result := &RevertResult{}
	msgs := rs.settleGroups("op-legacy", settleInput{touched: []settleTouch{
		{bookID: o.ID, changeType: undo.ChangeTypeBookPrimaryDemote, oldValue: "true"},
	}, crowned: ev}, result)
	require.Empty(t, msgs)
	require.Len(t, result.SettleSkipped, 1, "%+v", result)
	xb, err := s.GetBookByID(x.ID)
	require.NoError(t, err)
	require.NotNil(t, xb.IsPrimaryVersion)
	require.True(t, *xb.IsPrimaryVersion, "the iTunes member keeps the flag the operation never wrote")
	ob, err := s.GetBookByID(o.ID)
	require.NoError(t, err)
	require.NotNil(t, ob.IsPrimaryVersion)
	require.False(t, *ob.IsPrimaryVersion, "the refused crown leaves no second primary beside the iTunes member")

	// The unambiguous "wrote:" form is the operation's own write: put back.
	ev = handOffEvidenceOf([]*database.OperationChange{{BookID: o.ID, ChangeType: undo.ChangeTypeBookPrimaryHandoff,
		FieldName: "version_group_id", OldValue: undo.HandOffNoteValue(x.ID, true), NewValue: gid}})
	require.Equal(t, []string{x.ID}, ev[o.ID].wrote)
	result = &RevertResult{}
	msgs = rs.settleGroups("op-wrote", settleInput{touched: []settleTouch{
		{bookID: o.ID, changeType: undo.ChangeTypeBookPrimaryDemote, oldValue: "true"},
	}, crowned: ev}, result)
	require.Empty(t, msgs)
	require.Empty(t, result.SettleSkipped)
	xb, err = s.GetBookByID(x.ID)
	require.NoError(t, err)
	require.False(t, *xb.IsPrimaryVersion, "the operation wrote X's true, so its revert puts X back")
}

// Round 2: the crown back is refused because an iTunes member X turned
// writable (nil) after the operation, whose hand-off wrote C's true. The
// demote revert has already restored the original O's true: the refused
// crown must not leave O true beside C (two primaries). O yields (its flag
// is the operation's own write), C keeps the true the operation wrote, X is
// never written, and the group is reported as left unsettled.
func TestRevertSettle_RefusedCrownLeavesNoSecondPrimary(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes, pid := "vg-refused-crown", true, "FEDCBA9876543210"
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	c, err := s.CreateBook(&database.Book{Title: "C", FilePath: "/x/c", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	x, err := s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid, ITunesPersistentID: &pid})
	require.NoError(t, err)
	ev := handOffEvidenceOf([]*database.OperationChange{{BookID: o.ID, ChangeType: undo.ChangeTypeBookPrimaryHandoff,
		FieldName: "version_group_id", OldValue: undo.HandOffNoteValue(c.ID, true), NewValue: gid}})
	result := &RevertResult{}
	msgs := rs.settleGroups("op-refused-crown", settleInput{touched: []settleTouch{
		{bookID: o.ID, changeType: undo.ChangeTypeBookPrimaryDemote, oldValue: "true"},
	}, crowned: ev}, result)
	require.Empty(t, msgs)
	require.Empty(t, result.HandOffFailed)
	require.Len(t, result.SettleSkipped, 1, "%+v", result)
	require.Contains(t, result.SettleSkipped[0], "iTunes copy "+x.ID)
	require.True(t, result.Partial())
	require.False(t, rs.hasSettleOwed("op-refused-crown"), "not recorded for retry")
	want := map[string]*bool{o.ID: new(bool), c.ID: &yes, x.ID: nil}
	for id, w := range want {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		if w == nil {
			require.Nil(t, b.IsPrimaryVersion, "the iTunes copy's flag is never written")
			continue
		}
		require.NotNil(t, b.IsPrimaryVersion, id)
		require.Equal(t, *w, *b.IsPrimaryVersion, id)
	}
}

// S1: the evidence is the group's, not one original's: a kept note on the
// SECOND original still makes the first one yield to the kept incumbent.
func TestRevertSettle_EvidenceIsUnionedAcrossOriginals(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(s)
	gid, yes := "vg-union", true
	o1, err := s.CreateBook(&database.Book{Title: "O1", FilePath: "/x/o1", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	o2, err := s.CreateBook(&database.Book{Title: "O2", FilePath: "/x/o2", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	k, err := s.CreateBook(&database.Book{Title: "K", FilePath: "/x/k", VersionGroupID: &gid, IsPrimaryVersion: &yes})
	require.NoError(t, err)
	ev := handOffEvidenceOf([]*database.OperationChange{{BookID: o2.ID, ChangeType: undo.ChangeTypeBookPrimaryHandoff,
		FieldName: "version_group_id", OldValue: undo.HandOffNoteValue(k.ID, false), NewValue: gid}})
	_, err = rs.settleGroup("op-union", gid, []string{o1.ID, o2.ID}, nil, ev)
	require.NoError(t, err)
	for id, want := range map[string]bool{o1.ID: false, o2.ID: false, k.ID: true} {
		b, err := s.GetBookByID(id)
		require.NoError(t, err)
		require.NotNil(t, b.IsPrimaryVersion)
		require.Equal(t, want, *b.IsPrimaryVersion, id)
	}
}

// B3: a failed read in the iTunes guard is a failure, not a refusal: the
// group goes to HandOffFailed and is owed a retry, never SettleSkipped.
func TestRevertSettle_GuardReadErrorIsRetriedNotSkipped(t *testing.T) {
	s := newRevertPebble(t)
	rs := NewRevertService(&extFailStore{PebbleStore: s})
	gid, no := "vg-readerr", false
	o, err := s.CreateBook(&database.Book{Title: "O", FilePath: "/x/o", VersionGroupID: &gid, IsPrimaryVersion: &no})
	require.NoError(t, err)
	_, err = s.CreateBook(&database.Book{Title: "X", FilePath: "/x/x", VersionGroupID: &gid})
	require.NoError(t, err)
	result := &RevertResult{}
	msgs := rs.settleGroups("op-readerr", settleInput{touched: []settleTouch{
		{bookID: o.ID, changeType: undo.ChangeTypeBookPrimaryDemote, oldValue: "true"},
	}}, result)
	require.Len(t, msgs, 1)
	require.Empty(t, result.SettleSkipped)
	require.Len(t, result.HandOffFailed, 1)
	require.Contains(t, result.HandOffFailed[0], "external ids unreadable")
	require.True(t, rs.hasSettleOwed("op-readerr"), "owed a retry")
}

// extFailStore fails every external-id read (the iTunes guard's).
type extFailStore struct{ *database.PebbleStore }

func (s *extFailStore) GetExternalIDsForBook(string) ([]database.ExternalIDMapping, error) {
	return nil, errors.New("external ids unreadable")
}
