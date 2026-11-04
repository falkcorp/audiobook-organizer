// file: internal/plugins/maintenance/folder_books_owner_link_test.go
// version: 1.3.1
// guid: 5b8e2f41-9c07-4d6a-b3e5-1a7d40c92f68
// last-edited: 2026-11-03

package maintenance

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The owner apply links a duplicate-title orphan into the version group of the
// live book that already is the work (Q2): a non-primary version, never the
// other way round. The row is the mixed-shelf shape of the PR-B tests, with one
// library work (Arkham Asylum) that a live twin already names.

// fbTwin creates a live "Arkham Asylum" by the row's author, filed elsewhere:
// the book the orphan group duplicates.
func (f *fragFixture) fbTwin(t *testing.T, role string) string {
	t.Helper()
	author, err := f.s.GetAuthorByName("Alfred Pennyworth")
	require.NoError(t, err)
	b, err := f.s.CreateBook(&database.Book{Title: "Arkham Asylum", AuthorID: &author.ID,
		FilePath: f.path("elsewhere/" + role + ".m4b")})
	require.NoError(t, err)
	f.ids[role] = b.ID
	return b.ID
}

// linkRow seeds the owner row and its twin and plans it.
func linkRow(t *testing.T, twinGroup string) (*fragFixture, repairs.Row, string) {
	t.Helper()
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}})
	twin := f.fbTwin(t, "twin")
	if twinGroup != "" {
		f.setVG(t, twin, twinGroup, true)
	}
	row := f.fbSingleRow(t, "op-plan")
	return f, row, twin
}

func (f *fragFixture) fbGet(t *testing.T, id string) *database.Book {
	t.Helper()
	b, err := f.s.GetBookByID(id)
	require.NoError(t, err)
	require.NotNil(t, b)
	return b
}

func fbRowState(t *testing.T, row repairs.Row) fbState {
	t.Helper()
	var st fbState
	require.NoError(t, json.Unmarshal(row.State, &st))
	return st
}

// linked is the live "Arkham Asylum" the apply created at the library folder.
func (f *fragFixture) linked(t *testing.T) *database.Book {
	t.Helper()
	b := f.liveAtDir(t, filepath.Join("Alfred Pennyworth", "Arkham Asylum"))
	require.NotNil(t, b)
	return b
}

func TestFolderBooksOwnerLink_JoinsAnExistingGroupAsANonPrimaryVersion(t *testing.T) {
	f, _, twin := linkRow(t, "vg-arkham")
	sibling := f.fbTwin(t, "sibling")
	f.setVG(t, sibling, "vg-arkham", false)
	row := f.fbSingleRow(t, "op-plan-b")

	st := fbRowState(t, row)
	require.True(t, st.DupSingle)
	require.Len(t, st.Links, 1)
	require.Equal(t, "vg-arkham", st.Links[0].Group)
	require.Empty(t, st.Links[0].Mint)
	require.Equal(t, twin, st.Links[0].Target, "the smallest id of the group's matching members")
	require.True(t, row.OwnerApplicable, "%s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, "1", row.Proposed["linked_versions"])
	require.Equal(t, "0", row.Proposed["minted_groups"])
	require.Contains(t, row.OwnerApplyReason, "non-primary version")
	require.Contains(t, row.BookIDs, twin)

	out := f.fbOwnerApply(t, "op-plan-b", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	b := f.linked(t)
	require.NotNil(t, b.VersionGroupID)
	require.Equal(t, "vg-arkham", *b.VersionGroupID)
	require.NotNil(t, b.IsPrimaryVersion)
	require.False(t, *b.IsPrimaryVersion, "the new version is explicitly non-primary")
	tw := f.fbGet(t, twin)
	require.True(t, tw.IsPrimaryVersion != nil && *tw.IsPrimaryVersion, "the group's primary is unchanged")
	require.False(t, f.fbGet(t, sibling).IsPrimaryVersion != nil && *f.fbGet(t, sibling).IsPrimaryVersion)

	changes, err := f.s.GetOperationChanges("op-owner")
	require.NoError(t, err)
	var create *database.OperationChange
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeRepairVersionGroupJoin, c.ChangeType, "an existing group needs no mint")
		if c.ChangeType == undo.ChangeTypeRepairBookCreate && c.BookID == b.ID {
			create = c
		}
	}
	require.NotNil(t, create)
	v, _, ok := undo.ParseRepairBookCreateValue(b.ID, create.NewValue)
	require.True(t, ok)
	require.Equal(t, "vg-arkham", v.VersionGroupID)

	pre, err := preflightUndoConflicts(f.s, "op-owner")
	require.NoError(t, err)
	require.Zero(t, preRefused(pre), "%+v", pre)

	res, err := audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	require.True(t, f.fbGet(t, b.ID).IsSoftDeleted())
	require.Equal(t, "vg-arkham", *f.fbGet(t, twin).VersionGroupID)
	require.True(t, *f.fbGet(t, twin).IsPrimaryVersion)
	require.True(t, f.live(t, "fb"))
}

func TestFolderBooksOwnerLink_MintsAGroupForAnUngroupedTargetAndRevertsExactly(t *testing.T) {
	f, row, twin := linkRow(t, "")
	before := undo.StateOfBook(f.fbGet(t, twin))
	require.Nil(t, before.Group)
	require.Nil(t, before.Primary)

	st := fbRowState(t, row)
	require.Len(t, st.Links, 1)
	mint := fbMintID(row.RowID, twin)
	require.Equal(t, mint, st.Links[0].Mint)
	require.Empty(t, st.Links[0].Group)
	require.Equal(t, "1", row.Proposed["minted_groups"])
	require.True(t, row.OwnerApplicable, "%s: %s", row.Skipped, row.SkipReason)

	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)

	tw := f.fbGet(t, twin)
	require.Equal(t, mint, *tw.VersionGroupID)
	require.True(t, *tw.IsPrimaryVersion, "the live book is the new group's primary")
	b := f.linked(t)
	require.Equal(t, mint, *b.VersionGroupID)
	require.False(t, *b.IsPrimaryVersion)

	// The mint is journaled before the create row, so a newest-first revert
	// hides the book before it un-mints the target.
	changes, err := f.s.GetOperationChanges("op-owner")
	require.NoError(t, err)
	joinAt, createAt := -1, -1
	for i, c := range changes {
		switch {
		case c.ChangeType == undo.ChangeTypeRepairVersionGroupJoin:
			joinAt = i
			require.Equal(t, twin, c.BookID)
			require.Equal(t, fbFixerID, c.Source)
		case c.ChangeType == undo.ChangeTypeRepairBookCreate && c.BookID == b.ID:
			createAt = i
		}
	}
	require.GreaterOrEqual(t, joinAt, 0)
	require.Greater(t, createAt, joinAt)

	pre, err := preflightUndoConflicts(f.s, "op-owner")
	require.NoError(t, err)
	require.Zero(t, preRefused(pre), "%+v", pre)

	res, err := audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	require.Equal(t, before, undo.StateOfBook(f.fbGet(t, twin)), "group and primary flag back exactly, nil stays nil")
	require.True(t, f.fbGet(t, b.ID).IsSoftDeleted())
}

func TestFolderBooksOwnerLink_RevertRefusesAfterTheCreatedBookWasCrowned(t *testing.T) {
	f, row, twin := linkRow(t, "")
	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	mint := fbMintID(row.RowID, twin)
	b := f.linked(t)
	// Someone made the new version the primary since.
	f.setVG(t, b.ID, mint, true)
	f.setVG(t, twin, mint, false)

	pre, err := preflightUndoConflicts(f.s, "op-owner")
	require.NoError(t, err)
	require.Positive(t, preRefused(pre), "%+v", pre)
	_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.ErrorContains(t, err, "changed since")
	require.False(t, f.fbGet(t, b.ID).IsSoftDeleted(), "a changed created book is not hidden")
	require.Equal(t, mint, *f.fbGet(t, twin).VersionGroupID, "the group is not taken off a target whose group changed")
}

func TestFolderBooksOwnerLink_RevertRefusesWhenAnotherBookJoinedTheMintedGroup(t *testing.T) {
	f, row, twin := linkRow(t, "")
	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	mint := fbMintID(row.RowID, twin)
	other := f.fbTwin(t, "later")
	f.setVG(t, other, mint, false)

	_, err := audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.ErrorContains(t, err, "changed since")
	require.Equal(t, mint, *f.fbGet(t, twin).VersionGroupID, "the minted group is not dissolved under a later member")
}

func TestFolderBooksOwnerLink_HardDuplicatesStayHolds(t *testing.T) {
	t.Run("two groups match", func(t *testing.T) {
		f, _, _ := linkRow(t, "vg-arkham")
		other := f.fbTwin(t, "other")
		f.setVG(t, other, "vg-arkham-two", true)
		row := f.fbSingleRow(t, "op-plan-b")
		st := fbRowState(t, row)
		require.False(t, st.DupSingle)
		require.Empty(t, st.Links)
		require.False(t, row.OwnerApplicable)
		require.Equal(t, fbSkipMixedShelf, row.Skipped)
	})
	t.Run("two ungrouped books match", func(t *testing.T) {
		f, _, _ := linkRow(t, "")
		f.fbTwin(t, "other")
		row := f.fbSingleRow(t, "op-plan-b")
		require.False(t, row.OwnerApplicable)
		require.False(t, fbRowState(t, row).DupSingle)
	})
	t.Run("a live book already sits at the group's folder", func(t *testing.T) {
		f, _, _ := linkRow(t, "")
		author, err := f.s.GetAuthorByName("Alfred Pennyworth")
		require.NoError(t, err)
		_, err = f.s.CreateBook(&database.Book{Title: "Arkham Asylum, Retitled", AuthorID: &author.ID,
			FilePath: f.path(filepath.Join("Alfred Pennyworth", "Arkham Asylum"))})
		require.NoError(t, err)
		row := f.fbSingleRow(t, "op-plan-b")
		require.False(t, row.OwnerApplicable)
		require.Empty(t, fbRowState(t, row).Links)
	})
	t.Run("the only match has no author", func(t *testing.T) {
		f, _, twin := linkRow(t, "")
		_, err := f.s.ModifyBook(twin, func(b *database.Book) error { b.AuthorID = nil; return nil })
		require.NoError(t, err)
		row := f.fbSingleRow(t, "op-plan-b")
		require.False(t, row.OwnerApplicable, "a title alone does not say it is the same work")
		require.Empty(t, fbRowState(t, row).Links)
	})
}

func TestFolderBooksOwnerLink_ARetargetedLinkFailsTheFingerprint(t *testing.T) {
	for _, how := range []string{"retitled", "other author", "moved to a group", "deleted"} {
		t.Run(how, func(t *testing.T) {
			f, row, twin := linkRow(t, "")
			switch how {
			case "retitled":
				_, err := f.s.ModifyBook(twin, func(b *database.Book) error { b.Title = "Arkham Asylum Revisited"; return nil })
				require.NoError(t, err)
			case "other author":
				other, err := f.s.CreateAuthor("Selina Kyle")
				require.NoError(t, err)
				_, err = f.s.ModifyBook(twin, func(b *database.Book) error { b.AuthorID = &other.ID; return nil })
				require.NoError(t, err)
			case "moved to a group":
				f.setVG(t, twin, "vg-elsewhere", true)
			case "deleted":
				yes := true
				_, err := f.s.ModifyBook(twin, func(b *database.Book) error { b.MarkedForDeletion = &yes; return nil })
				require.NoError(t, err)
			}
			fresh, err := newFolderBooksFixer(f.p).Replan(t.Context(), nil, row, nil)
			require.NoError(t, err)
			require.NotEqual(t, row.Fingerprint, fresh.Fingerprint)
			require.False(t, fresh.OwnerApplicable)

			out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
			require.Zero(t, out.Applied, "%+v", out.Rows)
			require.Equal(t, "changed_since_plan", fbOutcome(out, row.RowID).Outcome, "%+v", out.Rows)
			require.True(t, f.live(t, "fb"))
			require.Nil(t, f.liveAtDir(t, filepath.Join("Alfred Pennyworth", "Arkham Asylum")), "nothing was created")
		})
	}
}

func TestFolderBooksOwnerLink_APlainApplyNeverLinks(t *testing.T) {
	f, row, twin := linkRow(t, "vg-arkham")
	out := f.fbApply(t, "op-plan", "op-plain", []string{row.RowID})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "fb"))
	require.Equal(t, "vg-arkham", *f.fbGet(t, twin).VersionGroupID)
	require.Nil(t, f.liveAtDir(t, filepath.Join("Alfred Pennyworth", "Arkham Asylum")))
}

// A plain (single-shelf) row keeps skipped_duplicate_title, and its preview
// names the book the owner's apply would link to.
func TestFolderBooksOwnerLink_APlainRowNamesTheWouldBeTarget(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes})
	author, err := f.s.GetAuthorByName("Alfred Pennyworth")
	require.NoError(t, err)
	twin, err := f.s.CreateBook(&database.Book{Title: "Dark Victory", AuthorID: &author.ID, FilePath: f.path("elsewhere/Dark Victory.m4b")})
	require.NoError(t, err)
	row := f.fbSingleRow(t, "op-plan")
	require.Equal(t, fbSkipDupTitle, row.Skipped, "%s", row.SkipReason)
	require.False(t, row.OwnerApplicable)
	require.Equal(t, []string{fbFlagSingleShelfOwnerResolvable}, row.Flags)
	found := false
	for _, e := range row.Evidence {
		if strings.Contains(e, twin.ID) {
			found = true
		}
	}
	require.True(t, found, "evidence %v names the target %s", row.Evidence, twin.ID)
	require.Equal(t, "1", row.Proposed["linked_versions"])
}

// A group whose primary is the folder-book itself: the new version joins it,
// the folder-book retires, the other member is crowned, and the new version
// stays non-primary through the hand-off; the revert restores every flag.
func TestFolderBooksOwnerLink_GroupOfTheFolderBookItself(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	fb := f.seedOwnerRow(t, ownerShelf{base: fbITunes, libWorks: []string{"Arkham Asylum"}})
	author, err := f.s.GetAuthorByName("Alfred Pennyworth")
	require.NoError(t, err)
	twin := f.fbBook(t, "twin", "Arkham Asylum", f.path("elsewhere/Arkham Asylum.m4b"), 3600,
		f.file(t, "elsewhere/Arkham Asylum.m4b", 10))
	_, err = f.s.ModifyBook(twin, func(b *database.Book) error { b.AuthorID = &author.ID; return nil })
	require.NoError(t, err)
	f.organized(t, twin)
	f.setVG(t, fb, "vg-pennyworth", true)
	f.setVG(t, twin, "vg-pennyworth", false)
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.OwnerApplicable, "%s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, "vg-pennyworth", fbRowState(t, row).Links[0].Group)

	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"))
	primaries := 0
	members, err := f.s.GetBooksByVersionGroup("vg-pennyworth")
	require.NoError(t, err)
	for _, m := range members {
		if m.IsSoftDeleted() {
			continue
		}
		if m.IsPrimaryVersion == nil || *m.IsPrimaryVersion {
			primaries++
			require.Equal(t, twin, m.ID, "the elected heir is crowned, not the new version")
		}
	}
	require.Equal(t, 1, primaries)
	b := f.linked(t)
	require.False(t, *b.IsPrimaryVersion)

	pre, err := preflightUndoConflicts(f.s, "op-owner")
	require.NoError(t, err)
	require.Zero(t, preRefused(pre), "%+v", pre)
	res, err := audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	require.True(t, f.live(t, "fb"))
	require.True(t, *f.fbGet(t, fb).IsPrimaryVersion, "the folder-book is the primary again")
	require.False(t, *f.fbGet(t, twin).IsPrimaryVersion)
	require.True(t, f.fbGet(t, b.ID).IsSoftDeleted())
}

// A cut-off owner apply leaves a hidden book (and maybe a minted group). The
// next operation adopts it on the strength of the first one's journal: no
// second book, no second mint, and one revert undoes everything exactly once.
func TestFolderBooksOwnerLink_AnAdoptingApplyFinishesACutOffOne(t *testing.T) {
	for _, tc := range []struct {
		stage string
		// when: "" crashes at the first call of the stage; "hidden" only while
		// the linked group's book is the hidden one being made; "live" once
		// that book has been un-hidden (the claim stage: a live book, which a
		// fresh plan must still see as the row's own and adopt). A resumed run
		// never applies owner rows, so the next owner apply is a new operation.
		when string
	}{{"mint", ""}, {"book", "hidden"}, {"reindex", "hidden"}, {"row", "hidden"}, {"claim", "live"}} {
		t.Run(tc.stage, func(t *testing.T) {
			f, row, twin := linkRow(t, "")
			before := undo.StateOfBook(f.fbGet(t, twin))
			mint := fbMintID(row.RowID, twin)
			// The group that duplicates the twin is the one whose created
			// book must be the hidden one when the apply is cut off.
			plan, ok := row.Detail.(*fbPlan)
			require.True(t, ok)
			linkedID := ""
			for _, g := range plan.Groups {
				if g.Title == "Arkham Asylum" {
					linkedID = fbCreatedID(row.RowID, f.ids["fb"], g.Key)
				}
			}
			require.NotEmpty(t, linkedID)
			now := func() string {
				b, err := f.s.GetBookByID(linkedID)
				require.NoError(t, err)
				switch {
				case b == nil:
					return "none"
				case b.IsSoftDeleted():
					return "hidden"
				}
				return "live"
			}
			fired := false
			fbCrashHook = func(stage string, _ int) error {
				if !fired && stage == tc.stage && (tc.when == "" || now() == tc.when) {
					fired = true
					return fmt.Errorf("injected stop at %s", stage)
				}
				return nil
			}
			t.Cleanup(func() { fbCrashHook = nil })
			cut := f.fbOwnerApply(t, "op-plan", "op-owner-1", []string{row.RowID})
			fbCrashHook = nil
			require.True(t, fired)
			require.Zero(t, cut.Applied, "%+v", cut.Rows)
			require.True(t, f.live(t, "fb"))

			again := f.fbSingleRow(t, "op-plan-2")
			require.True(t, again.OwnerApplicable, "%s: %s (the cut-off book must be adoptable)", again.Skipped, again.SkipReason)
			out := f.fbOwnerApply(t, "op-plan-2", "op-owner-2", []string{again.RowID})
			require.Equal(t, 1, out.Applied, "%+v", out.Rows)

			// One live Arkham Asylum at the folder, in the minted group, and
			// the target is still its primary.
			b := f.linked(t)
			require.Equal(t, mint, *b.VersionGroupID)
			require.False(t, *b.IsPrimaryVersion)
			require.True(t, *f.fbGet(t, twin).IsPrimaryVersion)
			live := 0
			members, err := f.s.GetBooksByVersionGroup(mint)
			require.NoError(t, err)
			for _, m := range members {
				if !m.IsSoftDeleted() {
					live++
				}
			}
			require.Equal(t, 2, live, "the target and exactly one new version")

			// The second operation owns the join and the create; the first
			// operation's open rows were handed over.
			second, err := f.s.GetOperationChanges("op-owner-2")
			require.NoError(t, err)
			var adopted bool
			joins := 0
			for _, c := range second {
				if c.ChangeType == undo.ChangeTypeRepairVersionGroupJoin {
					joins++
				}
				if c.ChangeType == undo.ChangeTypeRepairBookCreate && c.BookID == b.ID {
					v, _, ok := undo.ParseRepairBookCreateValue(b.ID, c.NewValue)
					require.True(t, ok)
					if tc.stage != "mint" {
						require.Equal(t, "op-owner-1", v.AdoptedFrom)
					}
					adopted = adopted || v.AdoptedFrom != ""
				}
			}
			require.Equal(t, 1, joins, "the join is journaled by the operation that finishes the row")
			if tc.stage != "mint" {
				require.True(t, adopted)
			}
			first, err := f.s.GetOperationChanges("op-owner-1")
			require.NoError(t, err)
			for _, c := range first {
				if c.ChangeType == undo.ChangeTypeRepairVersionGroupJoin || (c.ChangeType == undo.ChangeTypeRepairBookCreate && c.BookID == b.ID) {
					require.NotNil(t, c.RevertedAt, "%s of op-owner-1 was handed to op-owner-2", c.ChangeType)
				}
			}

			pre, err := preflightUndoConflicts(f.s, "op-owner-2")
			require.NoError(t, err)
			require.Zero(t, preRefused(pre), "%+v", pre)
			res, err := audiobooks.NewRevertService(f.s).RevertOperation("op-owner-2")
			require.NoError(t, err)
			require.Zero(t, res.Failed, "%+v", res)
			require.Equal(t, before, undo.StateOfBook(f.fbGet(t, twin)), "the target's group state is back exactly")
			require.True(t, f.fbGet(t, b.ID).IsSoftDeleted())
			// Reverting the cut-off operation afterwards does nothing twice.
			// Every open row of it was handed over, so there is nothing left
			// to undo; the revert says so rather than writing a second time.
			_, err = audiobooks.NewRevertService(f.s).RevertOperation("op-owner-1")
			if err != nil {
				require.ErrorContains(t, err, "already been reverted")
			}
			require.Equal(t, before, undo.StateOfBook(f.fbGet(t, twin)))
			require.True(t, f.fbGet(t, b.ID).IsSoftDeleted())
		})
	}
}

// A plain (no link) apply cut off at the claim stage leaves live books of its
// own. The fresh plan sees them as the row's own, not as proper holders, so
// its row is the one the apply will see, and the next apply adopts the books.
func TestFolderBooksOwnerLink_APlainClaimStageCutOffIsAdoptedByAFreshPlan(t *testing.T) {
	f := newGlobalRootFragFixture(t)
	f.seedOwnerRow(t, ownerShelf{base: fbITunes})
	row := f.fbSingleRow(t, "op-plan")
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	plan, ok := row.Detail.(*fbPlan)
	require.True(t, ok)
	require.NotEmpty(t, plan.Groups)
	hit := false
	fbCrashHook = func(stage string, n int) error {
		if stage == "claim" && n == 1 && !hit {
			hit = true
			return fmt.Errorf("injected stop at claim")
		}
		return nil
	}
	t.Cleanup(func() { fbCrashHook = nil })
	cut := f.fbApply(t, "op-plan", "op-apply", []string{row.RowID})
	fbCrashHook = nil
	require.True(t, hit)
	require.Zero(t, cut.Applied, "%+v", cut.Rows)
	require.True(t, f.live(t, "fb"))

	again := f.fbSingleRow(t, "op-plan-2")
	require.True(t, again.Applicable(), "%s: %s", again.Skipped, again.SkipReason)
	out := f.fbApply(t, "op-plan-2", "op-apply-2", []string{again.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	require.False(t, f.live(t, "fb"))
	changes, err := f.s.GetOperationChanges("op-apply-2")
	require.NoError(t, err)
	adopted := 0
	for _, c := range changes {
		if c.ChangeType != undo.ChangeTypeRepairBookCreate {
			continue
		}
		v, _, ok := undo.ParseRepairBookCreateValue(c.BookID, c.NewValue)
		require.True(t, ok)
		if v.AdoptedFrom == "op-apply" {
			adopted++
			require.False(t, f.fbGet(t, c.BookID).IsSoftDeleted())
		}
	}
	require.Equal(t, 1, adopted, "the live book of the cut-off apply is adopted, not duplicated")
}

// A live book of a claim-stage cut is adopted only on the same evidence as a
// hidden one: the journal says the cut-off operation created exactly the book
// the re-plan would. A target that moved to another group since the cut makes
// the re-plan a different book, so the row is not offered, and an apply of it
// writes nothing (it would otherwise refuse after creating earlier groups).
func TestFolderBooksOwnerLink_AClaimStageCutOffWhoseTargetMovedIsNotOffered(t *testing.T) {
	f, row, twin := linkRow(t, "")
	plan, ok := row.Detail.(*fbPlan)
	require.True(t, ok)
	linkedID := ""
	for _, g := range plan.Groups {
		if g.Title == "Arkham Asylum" {
			linkedID = fbCreatedID(row.RowID, f.ids["fb"], g.Key)
		}
	}
	require.NotEmpty(t, linkedID)
	fired := false
	fbCrashHook = func(stage string, _ int) error {
		if fired || stage != "claim" {
			return nil
		}
		if b, err := f.s.GetBookByID(linkedID); err != nil || b == nil || b.IsSoftDeleted() {
			return nil
		}
		fired = true
		return fmt.Errorf("injected stop at %s", stage)
	}
	t.Cleanup(func() { fbCrashHook = nil })
	cut := f.fbOwnerApply(t, "op-plan", "op-owner-1", []string{row.RowID})
	fbCrashHook = nil
	require.True(t, fired)
	require.Zero(t, cut.Applied, "%+v", cut.Rows)

	f.setVG(t, twin, "vg-elsewhere", true)
	again := f.fbSingleRow(t, "op-plan-2")
	require.False(t, again.Applicable(), "%s", again.Reason)
	require.False(t, again.OwnerApplicable)
	require.Equal(t, fbSkipCutOffLive, again.Skipped, "%s", again.SkipReason)

	out := f.fbOwnerApply(t, "op-plan-2", "op-owner-2", []string{again.RowID})
	require.Zero(t, out.Applied, "%+v", out.Rows)
	require.True(t, f.live(t, "fb"))
	changes, err := f.s.GetOperationChanges("op-owner-2")
	require.NoError(t, err)
	for _, c := range changes {
		require.NotEqual(t, undo.ChangeTypeRepairBookCreate, c.ChangeType, "the refused row wrote a book")
	}
}

// A hidden book whose creating operation was reverted is not adoptable: its
// files stay "previously resolved", for a link row exactly as for any other.
func TestFolderBooksOwnerLink_ARevertedApplyIsNotOfferedAgain(t *testing.T) {
	f, row, _ := linkRow(t, "vg-arkham")
	out := f.fbOwnerApply(t, "op-plan", "op-owner", []string{row.RowID})
	require.Equal(t, 1, out.Applied, "%+v", out.Rows)
	res, err := audiobooks.NewRevertService(f.s).RevertOperation("op-owner")
	require.NoError(t, err)
	require.Zero(t, res.Failed, "%+v", res)
	again := f.fbPlan(t, fbFixerID, "op-plan-2")
	require.Len(t, again.Rows, 1, "the folder-book is live again and still listed")
	for _, r := range again.Rows {
		require.False(t, r.OwnerApplicable, "a reverted apply is offered again: %s", r.OwnerApplyReason)
	}
	require.Zero(t, again.OwnerApplicable)
	st := fbRowState(t, again.Rows[0])
	require.Len(t, st.Links, 1, "the row still carries the link the owner asked for")
	require.True(t, st.DupSingle)
}

// preRefused counts the rows the preflight says the revert would refuse.
func preRefused(r *undo.UndoConflictReport) int {
	return len(r.ContentChanged) + len(r.CheckFailed) + len(r.BookMissing)
}
