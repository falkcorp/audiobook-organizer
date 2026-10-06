// file: internal/plugins/maintenance/version_twin_metadata_review_test.go
// version: 1.0.0
// guid: 3c9e5b71-2a84-4f06-b1d3-8e7a6c40f259
// last-edited: 2026-10-06

package maintenance

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// The regression tests for the first review of #3797. Each is one of the
// reviewer's probes (P1-P5) or a should-fix, turned into a test. All fixture
// rows are synthetic (public repo).

// vtFresh plans and re-plans one group and returns the fresh row Apply
// takes, failing unless it is applicable.
func (l *vtLib) vtFresh(group string) repairs.Row {
	l.t.Helper()
	_, rows := l.plan()
	row, ok := rows[group]
	require.True(l.t, ok, "group %s is a row", group)
	fresh, err := l.fixer.Replan(context.Background(), nil, row, &fakeReporter{})
	require.NoError(l.t, err)
	require.True(l.t, fresh.Applicable(), "%s: %s %s", group, fresh.Skipped, fresh.SkipReason)
	return fresh
}

// vtPreflightSafe: the op revert's preflight (the "Undo N changes?"
// confirmation) predicts the one journaled row restorable, as the revert
// then finds it.
func vtPreflightSafe(t *testing.T, l *vtLib) {
	t.Helper()
	rep, err := undo.PreflightUndoConflicts(l.st, vtTestOpID)
	require.NoError(t, err)
	require.Equal(t, 1, rep.Safe, "preflight %+v", rep)
	require.Zero(t, rep.NotRestorable, "preflight %+v", rep)
	require.Empty(t, rep.CheckFailed, "preflight %+v", rep)
	require.Empty(t, rep.ContentChanged, "preflight %+v", rep)
}

func (l *vtLib) writer() *repairs.Writer {
	return repairs.NewWriter(l.st, l.st, l.fixer.ID(), "bulk_update", "repairs-").WithJournal(l.st, l.st, vtTestOpID)
}

// P1: an iTunes id that lands on the primary between the re-plan and the
// write is caught inside the write: the apply is refused changed_since_plan
// and nothing is written. Covers the book PID, a live iTunes external id
// and a book_file under an iTunes path (the framework path guard is off for
// this fixer, so the fixer's own check is the only one).
func TestVersionTwinFixer_Review_P1_ITunesAfterReplanRefusesApply(t *testing.T) {
	cases := map[string]func(l *vtLib, id string){
		"book pid": func(l *vtLib, id string) {
			_, err := l.st.ModifyBook(id, func(b *database.Book) error { b.ITunesPersistentID = vtPtr("SYNTHPID00000001"); return nil })
			require.NoError(l.t, err)
		},
		"external id": func(l *vtLib, id string) {
			require.NoError(l.t, l.st.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes",
				ExternalID: "SYNTHPID00000002", BookID: id}))
		},
		"book_file under the iTunes library": func(l *vtLib, id string) {
			// No iTunes id and no iTunes path field: only the path guard
			// (itunesCopyWhy's GuardBookPathsWith) can tell.
			files, err := l.st.GetBookFiles(id)
			require.NoError(l.t, err)
			_, err = l.st.ModifyBookFile(id, files[0].ID, func(f *database.BookFile) error {
				f.FilePath = "/media/books/itunes/Synthetic Author A/Synthetic Saga/01.m4b"
				return nil
			})
			require.NoError(l.t, err)
		},
		"book_file itunes path": func(l *vtLib, id string) {
			files, err := l.st.GetBookFiles(id)
			require.NoError(l.t, err)
			_, err = l.st.ModifyBookFile(id, files[0].ID, func(f *database.BookFile) error {
				f.ITunesPath = "file://localhost/Synthetic/iTunes Media/Audiobooks/01.m4b"
				return nil
			})
			require.NoError(l.t, err)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			l := newVTLib(t)
			l.book("p", "g1", true, nil)
			l.appliedTwin("t", "g1", vtSaga(), nil)
			fresh := l.vtFresh("g1")
			mutate(l, l.ids["p"])

			err := l.fixer.Apply(context.Background(), l.writer(), fresh)
			require.Error(t, err)
			require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
			p := l.get("p")
			require.Nil(t, p.MetadataReviewStatus, "an iTunes-linked primary is never written")
			require.Empty(t, dcStr(p.Narrator))
			require.Empty(t, dcStr(p.MetadataSourceHash))
		})
	}
}

// P2: the candidates class re-checks iTunes inside the copy too: a book_file
// iTunes id on the primary after the re-plan refuses the copy.
func TestVersionTwinFixer_Review_P2_ITunesAfterReplanRefusesCandidateCopy(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g2", true, nil)
	tid := l.book("t", "g2", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtSaga()}, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
	fresh := l.vtFresh("g2")

	files, err := l.st.GetBookFiles(l.ids["p"])
	require.NoError(t, err)
	_, err = l.st.ModifyBookFile(l.ids["p"], files[0].ID, func(f *database.BookFile) error {
		f.ITunesPersistentID = "SYNTHPID00000003"
		return nil
	})
	require.NoError(t, err)

	err = l.fixer.Apply(context.Background(), l.writer(), fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	entry, err := l.st.GetMetadataCache(l.ids["p"])
	require.NoError(t, err)
	require.Nil(t, entry, "no candidates are copied onto an iTunes-linked primary")
}

// P3: an applied_twin apply is attributable and revertible from its op: the
// history rows carry the op's batch id and the fixer as source, one
// metadata_apply row is journaled under the op after the write, and the op
// revert restores the primary. Undo-last-apply after the op revert finds the
// apply already undone.
func TestVersionTwinFixer_Review_P3_AppliedTwinJournaledAndOpRevertible(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g3", true, nil)
	l.appliedTwin("t", "g3", vtSaga(), nil)
	flags := l.primaryFlags()
	fresh := l.vtFresh("g3")

	require.NoError(t, l.fixer.Apply(context.Background(), l.writer(), fresh))
	p := l.get("p")
	require.True(t, database.MetadataApplied(p.MetadataReviewStatus))

	hist, err := l.st.GetBookChangeHistory(l.ids["p"], 100)
	require.NoError(t, err)
	require.NotEmpty(t, hist)
	for _, h := range hist {
		require.True(t, strings.HasPrefix(h.BatchID, vtTestOpID+":"), "history batch %q is the op's", h.BatchID)
		require.Contains(t, h.Source, versionTwinFixerID, "history source names the fixer")
	}
	changes, err := l.st.GetOperationChanges(vtTestOpID)
	require.NoError(t, err)
	require.Len(t, changes, 1, "one journal row for the apply")
	require.Equal(t, l.ids["p"], changes[0].BookID)
	require.Equal(t, hist[0].BatchID, changes[0].NewValue, "the journal row names the history batch")

	vtPreflightSafe(t, l)
	res, err := audiobooks.NewRevertService(l.st).RevertOperation(vtTestOpID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored, "result %+v", res)
	p = l.get("p")
	require.Empty(t, dcStr(p.MetadataReviewStatus), "the op revert undoes the match stamp")
	require.Empty(t, dcStr(p.Narrator), "the op revert undoes the narrator fill")
	require.Empty(t, dcStr(p.MetadataSourceHash))
	require.Equal(t, flags, l.primaryFlags())

	_, err = metafetch.NewService(l.st).UndoLastApply(l.ids["p"])
	require.ErrorIs(t, err, metafetch.ErrApplyAlreadyUndone, "undo-last-apply sees the op revert's undo")
}

// P3, the other order: undo-last-apply first, then the op revert finds the
// apply already restored and writes nothing.
func TestVersionTwinFixer_Review_P3_UndoLastApplyThenOpRevert(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g3b", true, nil)
	l.appliedTwin("t", "g3b", vtSaga(), nil)
	fresh := l.vtFresh("g3b")
	require.NoError(t, l.fixer.Apply(context.Background(), l.writer(), fresh))

	_, err := metafetch.NewService(l.st).UndoLastApply(l.ids["p"])
	require.NoError(t, err)
	// A later edit the op revert must not touch.
	_, err = l.st.ModifyBook(l.ids["p"], func(b *database.Book) error { b.Narrator = vtPtr("Synthetic Narrator Edit"); return nil })
	require.NoError(t, err)

	res, err := audiobooks.NewRevertService(l.st).RevertOperation(vtTestOpID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored, "already undone counts restored: %+v", res)
	require.Equal(t, "Synthetic Narrator Edit", dcStr(l.get("p").Narrator))
}

// P4: narrator and ASIN are not filled without same-edition evidence: a
// primary with no narrator and a runtime 2% off the twin's gets the match
// and the other fields, never the twin's narrator or ASIN.
func TestVersionTwinFixer_Review_P4_NoEditionEvidenceNoNarratorOrASIN(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g4", true, func(b *database.Book) { b.Duration = vtPtr(74000) })
	cand := vtSagaASIN("B0SYNTH004")
	cand.Narrator = "Synthetic Narrator B"
	l.appliedTwin("t", "g4", cand, nil)

	_, rows := l.plan()
	row := rows["g4"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	// Display keys are proposed unchanged; a fill would propose a value.
	require.Empty(t, row.Proposed["primary_narrator"], "no narrator proposal without evidence: %v", row.Proposed)
	require.Empty(t, row.Proposed["primary_asin"], "no ASIN proposal without evidence: %v", row.Proposed)

	fresh := l.vtFresh("g4")
	require.NoError(t, l.fixer.Apply(context.Background(), l.writer(), fresh))
	p := l.get("p")
	require.True(t, database.MetadataApplied(p.MetadataReviewStatus))
	require.Empty(t, dcStr(p.Narrator), "the narrator is not copied without evidence")
	require.Empty(t, dcStr(p.ASIN), "the ASIN is not copied without evidence")
}

// P4, the positive side: runtimes within 1% are evidence, and the narrator
// and ASIN are filled.
func TestVersionTwinFixer_Review_P4_RuntimeEvidenceFillsNarratorAndASIN(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "g4b", true, func(b *database.Book) { b.Duration = vtPtr(75400) })
	l.appliedTwin("t", "g4b", vtSagaASIN("B0SYNTH005"), nil)
	_, rows := l.plan()
	row := rows["g4b"]
	require.True(t, row.Applicable(), "%s: %s", row.Skipped, row.SkipReason)
	require.Equal(t, "B0SYNTH005", row.Proposed["primary_asin"])
	require.Equal(t, "Synthetic Narrator A", row.Proposed["primary_narrator"])
}

// P4, ASIN elsewhere: a live book outside the group already carries the
// record's ASIN, so the record may be that book's; the row is held.
func TestVersionTwinFixer_Review_P4_ASINOnBookOutsideGroupHolds(t *testing.T) {
	l := newVTLib(t)
	l.book("out", "g-other", true, func(b *database.Book) { b.ASIN = vtPtr("B0SYNTH006"); b.Title = "Synthetic Other" })
	l.book("p", "g4c", true, nil)
	l.appliedTwin("t", "g4c", vtSagaASIN("B0SYNTH006"), nil)
	_, rows := l.plan()
	row := rows["g4c"]
	require.Equal(t, vtHoldASINElsewhere, row.Skipped, row.SkipReason)
	require.Contains(t, row.SkipReason, l.ids["out"])
}

// P5: a book outside the group that gains the twin's record after the
// re-plan refuses the apply before the commit, and the post-commit MATCH-4
// election never runs for this fixer: no member of the group (the iTunes
// twin included) and no outside book is demoted or merged.
func TestVersionTwinFixer_Review_P5_OutsideHashAfterReplanNoElection(t *testing.T) {
	l := newVTLib(t)
	cand := vtSagaASIN("B0SYNTH007")
	// Created first, with more files and an iTunes id: it would win MATCH-4.
	l.book("out", "g-out", true, func(b *database.Book) { b.ITunesPersistentID = vtPtr("SYNTHPID00000005") })
	for i := 2; i <= 4; i++ {
		require.NoError(t, l.st.CreateBookFile(&database.BookFile{BookID: l.ids["out"],
			FilePath: "/lib/Synthetic Author A/out/0" + string(rune('0'+i)) + ".m4b", Format: "m4b"}))
	}
	l.book("p", "g5", true, nil)
	l.appliedTwin("t", "g5", cand, func(b *database.Book) { b.ITunesPersistentID = vtPtr("SYNTHPID00000006") })
	flags := l.primaryFlags()
	fresh := l.vtFresh("g5")

	_, err := l.st.ModifyBook(l.ids["out"], func(b *database.Book) error {
		b.MetadataReviewStatus = vtPtr("matched")
		b.MetadataSourceHash = vtPtr(metafetch.CandidateSourceHash(cand))
		return nil
	})
	require.NoError(t, err)
	flags["out"] = l.primaryFlags()["out"]

	err = l.fixer.Apply(context.Background(), l.writer(), fresh)
	require.Error(t, err)
	require.True(t, errors.Is(err, repairs.ErrChangedSincePlan), err.Error())
	require.Equal(t, flags, l.primaryFlags(), "no primary flag or merge target moved")
	require.Nil(t, l.get("p").MetadataReviewStatus)
}

// S2: the candidate copy is journaled with the primary's prior cache state,
// and the op revert removes the copy (no prior row) again.
func TestVersionTwinFixer_Review_S2_CandidateCopyOpRevertible(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs2", true, nil)
	tid := l.book("t", "gs2", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtSaga()}, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
	fresh := l.vtFresh("gs2")
	require.NoError(t, l.fixer.Apply(context.Background(), l.writer(), fresh))
	entry, err := l.st.GetMetadataCache(l.ids["p"])
	require.NoError(t, err)
	require.NotNil(t, entry)

	vtPreflightSafe(t, l)
	res, err := audiobooks.NewRevertService(l.st).RevertOperation(vtTestOpID)
	require.NoError(t, err)
	require.Equal(t, 1, res.Restored, "result %+v", res)
	entry, err = l.st.GetMetadataCache(l.ids["p"])
	require.NoError(t, err)
	require.Nil(t, entry, "the op revert removes the copied candidates")
}

// S2: a primary whose cache row holds a fetch verdict (no candidates, but a
// search fingerprint: "searched, nothing found") is not overwritten.
func TestVersionTwinFixer_Review_S2_VerdictRowIsNotOverwritten(t *testing.T) {
	l := newVTLib(t)
	pid := l.book("p", "gs2b", true, nil)
	tid := l.book("t", "gs2b", false, nil)
	l.putCache(tid, []metafetch.MetadataCandidate{vtSaga()}, metafetch.BatchSourceHash(tid, "Synthetic Saga", "Synthetic Author A"))
	require.NoError(t, l.st.PutMetadataCache(&database.MetadataCandidateCache{BookID: pid, SearchFingerprint: "v2:synthetic"}))

	_, rows := l.plan()
	row, ok := rows["gs2b"]
	require.True(t, ok)
	require.Equal(t, vtHoldPrimaryVerdict, row.Skipped, row.SkipReason)
}

// S3: Doctor Who / Big Finish / Torchwood is judged on the record too: a
// twin's record naming a DW series is held even when the books' own titles
// do not say so.
func TestVersionTwinFixer_Review_S3_OwnerManualOnlyRecordHolds(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs3", true, nil)
	cand := vtSagaASIN("B0SYNTH008")
	cand.Series = "Doctor Who: Synthetic Range"
	l.appliedTwin("t", "gs3", cand, nil)

	l.book("cp", "gs3c", true, nil)
	ctid := l.book("ct", "gs3c", false, nil)
	cc := vtSagaASIN("B0SYNTH009")
	cc.Publisher = "Big Finish"
	l.putCache(ctid, []metafetch.MetadataCandidate{vtSagaASIN("B0SYNTH010"), cc},
		metafetch.BatchSourceHash(ctid, "Synthetic Saga", "Synthetic Author A"))

	_, rows := l.plan()
	require.Equal(t, vtHoldOwnerManual, rows["gs3"].Skipped, rows["gs3"].SkipReason)
	require.Equal(t, vtHoldOwnerManual, rows["gs3c"].Skipped, rows["gs3c"].SkipReason)
}

// S4: the fixer's apply adds no provenance or category tags and records no
// fetched-value provenance: everything it writes is in the history batch the
// revert undoes.
func TestVersionTwinFixer_Review_S4_NoTagsOrProvenance(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs4", true, nil)
	cand := vtSaga()
	cand.CategoryTags = []string{"genre:synthetic"}
	l.appliedTwin("t", "gs4", cand, nil)
	fresh := l.vtFresh("gs4")
	require.NoError(t, l.fixer.Apply(context.Background(), l.writer(), fresh))

	tags, err := l.st.GetBookTags(l.ids["p"])
	require.NoError(t, err)
	require.Empty(t, tags, "no tags are added")
	states, err := l.st.GetMetadataFieldStates(l.ids["p"])
	require.NoError(t, err)
	require.Empty(t, states, "no fetched-value provenance is recorded")
}

// The framework's owner-manual guard still holds a group whose record is
// clean when a member is Doctor Who / Big Finish / Torchwood by its path.
func TestVersionTwinFixer_Review_S3_FrameworkGuardStillHoldsMember(t *testing.T) {
	l := newVTLib(t)
	l.book("p", "gs3f", true, nil)
	l.appliedTwin("t", "gs3f", vtSagaASIN("B0SYNTH011"), func(b *database.Book) {
		b.FilePath = "/lib/Big Finish/Synthetic Saga"
	})
	_, rows := l.plan()
	require.Equal(t, repairs.SkipOwnerManual, rows["gs3f"].Skipped, rows["gs3f"].SkipReason)
}
