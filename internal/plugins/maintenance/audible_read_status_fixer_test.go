// file: internal/plugins/maintenance/audible_read_status_fixer_test.go
// version: 1.0.0
// guid: 569aec15-d845-4f39-9a8e-63ba7e93162a
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Every title, author, ASIN and user here is synthetic.

const (
	arsTestApplyOp = "op-ars-apply"
	arsTestPlanOp  = "op-ars-plan"
)

// arsAudibleTS is Audible's listening time in the fixtures.
var arsAudibleTS = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

type arsLib struct {
	t     *testing.T
	store *database.PebbleStore
	p     *Plugin
	fixer *audibleReadStatusFixer
	user  string
	ids   map[string]string
	items []map[string]any
}

func newARSLib(t *testing.T) *arsLib {
	t.Helper()
	st, err := database.NewPebbleStore(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	u, err := st.CreateUser("listener", "listener@example.invalid", "argon2id", "x", []string{"user"}, "active")
	require.NoError(t, err)
	p := &Plugin{deps: fakeDeps{store: st}, standDownWait: noWait}
	return &arsLib{t: t, store: st, p: p, fixer: newAudibleReadStatusFixer(p), user: u.ID, ids: map[string]string{}}
}

type arsBook struct {
	title, author, asin, vg string
	primary                 *bool
	minutes                 int
}

func (l *arsLib) book(key string, b arsBook) string {
	l.t.Helper()
	nb := &database.Book{Title: b.title, Format: "m4b", FilePath: "/lib/" + key}
	if b.asin != "" {
		nb.ASIN = &b.asin
	}
	if b.author != "" {
		a, err := l.store.GetAuthorByName(b.author)
		require.NoError(l.t, err)
		if a == nil {
			a, err = l.store.CreateAuthor(b.author)
			require.NoError(l.t, err)
		}
		nb.AuthorID = &a.ID
	}
	if b.vg != "" {
		nb.VersionGroupID = &b.vg
		nb.IsPrimaryVersion = b.primary
	}
	created, err := l.store.CreateBook(nb)
	require.NoError(l.t, err)
	require.NoError(l.t, l.store.CreateBookFile(&database.BookFile{BookID: created.ID, FilePath: "/lib/" + key + "/01.m4b",
		Format: "m4b", Duration: b.minutes * 60}))
	l.ids[key] = created.ID
	return created.ID
}

// item adds an export item. status: "finished", "progress" or "none".
func (l *arsLib) item(asin, title, author string, minutes int, status string, pct float64, ts *time.Time) {
	it := map[string]any{"asin": asin, "title": title, "runtime_length_min": minutes,
		"authors": []map[string]string{{"name": author, "asin": "AUTHOR0001"}}}
	ls := map[string]any{}
	switch status {
	case "finished":
		it["is_finished"] = true
		it["percent_complete"] = 99.0
	case "progress":
		it["is_finished"] = false
		it["percent_complete"] = pct
		ls["time_remaining_seconds"] = float64(minutes*60) * (100 - pct) / 100
	default:
		it["is_finished"] = false
		it["percent_complete"] = 0
	}
	if ts != nil {
		ls["finished_at_timestamp"] = ts.Format("2006-01-02T15:04:05.000Z")
	}
	it["listening_status"] = ls
	l.items = append(l.items, it)
}

func (l *arsLib) params() json.RawMessage {
	l.t.Helper()
	b, err := json.Marshal(map[string]any{"target_user_id": l.user, "export": map[string]any{"items": l.items}})
	require.NoError(l.t, err)
	return b
}

func (l *arsLib) plan() (*repairs.PlanResult, map[string]repairs.Row) {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	res, err := repairs.RunPlan(context.Background(), l.fixer, l.params(), l.p.repairsPlanDeps(l.store, series), &fakeReporter{})
	require.NoError(l.t, err)
	raw, err := json.Marshal(res)
	require.NoError(l.t, err)
	var stored repairs.PlanResult
	require.NoError(l.t, json.Unmarshal(raw, &stored))
	byID := map[string]repairs.Row{}
	for _, r := range stored.Rows {
		byID[r.RowID] = r
	}
	return &stored, byID
}

func (l *arsLib) apply(plan *repairs.PlanResult, rowIDs []string) map[string]repairs.RowResult {
	l.t.Helper()
	series, err := l.store.GetAllSeries()
	require.NoError(l.t, err)
	w := repairs.NewWriter(l.store, l.store, l.fixer.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, arsTestApplyOp).WithUserState(l.p.deps.UserReadStateStore())
	res, err := repairs.RunApply(context.Background(), l.fixer, plan, arsTestPlanOp, rowIDs, false,
		repairs.ApplyDeps{Guard: l.store, Tags: l.p.repairsGuardTags(), Series: repairs.SeriesNamesFrom(series), Writer: w, OpID: arsTestApplyOp}, &fakeReporter{})
	require.NoError(l.t, err)
	out := map[string]repairs.RowResult{}
	for _, r := range res.Rows {
		out[r.RowID] = r
	}
	return out
}

func (l *arsLib) state(key string) *database.UserBookState {
	l.t.Helper()
	s, err := l.store.GetUserBookState(l.user, l.ids[key])
	require.NoError(l.t, err)
	return s
}

func (l *arsLib) setState(key string, s database.UserBookState) {
	l.t.Helper()
	s.UserID, s.BookID = l.user, l.ids[key]
	require.NoError(l.t, l.store.SetUserBookState(&s))
}

func (l *arsLib) positions(key string) []database.UserPosition {
	l.t.Helper()
	p, err := l.store.ListUserPositionsForBook(l.user, l.ids[key])
	require.NoError(l.t, err)
	return p
}

func ptrTime(t time.Time) *time.Time { return &t }

func TestAudibleReadStatus_TargetUserRequired(t *testing.T) {
	l := newARSLib(t)
	l.book("a", arsBook{title: "Synthetic One", author: "Pat Example", asin: "B0TEST0001", minutes: 600})
	l.item("B0TEST0001", "Synthetic One", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))
	items := l.items
	cases := map[string]any{
		"no params":        nil,
		"no target user":   map[string]any{"items": items},
		"blank user":       map[string]any{"target_user_id": "  ", "items": items},
		"unknown user":     map[string]any{"target_user_id": "01NOSUCHUSER0000000000000", "items": items},
		"iTunes sync user": map[string]any{"target_user_id": arsLocalUserID, "items": items},
		"no items":         map[string]any{"target_user_id": l.user},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			var raw json.RawMessage
			if p != nil {
				b, err := json.Marshal(p)
				require.NoError(t, err)
				raw = b
			}
			_, err := l.fixer.Plan(context.Background(), raw, &fakeReporter{})
			require.Error(t, err)
		})
	}
	// The flat form works as well as the export form.
	raw, err := json.Marshal(map[string]any{"target_user_id": l.user, "items": items})
	require.NoError(t, err)
	rows, err := l.fixer.Plan(context.Background(), raw, &fakeReporter{})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, arsWouldFinish, rows[0].Class)
}

func TestAudibleReadStatus_PlanClasses(t *testing.T) {
	l := newARSLib(t)
	yes, no := true, false
	older, newer := arsAudibleTS.Add(-48*time.Hour), arsAudibleTS.Add(48*time.Hour)

	l.book("fresh", arsBook{title: "Fresh Finish", author: "Pat Example", asin: "B0TEST0001", minutes: 600})
	l.item("B0TEST0001", "Fresh Finish", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("done", arsBook{title: "Done Already", author: "Pat Example", asin: "B0TEST0002", minutes: 600})
	l.setState("done", database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: older})
	l.item("B0TEST0002", "Done Already", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	// In progress on Audible, finished here: never un-finished.
	l.book("doneprog", arsBook{title: "Finished Here", author: "Pat Example", asin: "B0TEST0022", minutes: 600})
	l.setState("doneprog", database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: older})
	l.item("B0TEST0022", "Finished Here", "Pat Example", 600, "progress", 20, ptrTime(newer))

	l.book("newer", arsBook{title: "Newer Here", author: "Pat Example", asin: "B0TEST0003", minutes: 600})
	l.setState("newer", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 40, LastActivityAt: older})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["newer"], "abs", 900, newer))
	l.item("B0TEST0003", "Newer Here", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("partial", arsBook{title: "Partial Older", author: "Pat Example", asin: "B0TEST0004", minutes: 600})
	l.setState("partial", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: older,
		HideFromContinueListening: true})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["partial"], "abs", 600, older))
	l.item("B0TEST0004", "Partial Older", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("prog", arsBook{title: "Halfway There", author: "Pat Example", asin: "B0TEST0005", minutes: 600})
	l.item("B0TEST0005", "Halfway There", "Pat Example", 600, "progress", 40, ptrTime(arsAudibleTS))

	l.book("haspos", arsBook{title: "Has Position", author: "Pat Example", asin: "B0TEST0006", minutes: 600})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["haspos"], "abs", 60, older))
	l.item("B0TEST0006", "Has Position", "Pat Example", 600, "progress", 40, ptrTime(arsAudibleTS))

	l.book("quit", arsBook{title: "Gave Up", author: "Pat Example", asin: "B0TEST0007", minutes: 600})
	l.setState("quit", database.UserBookState{Status: database.UserBookStatusAbandoned, StatusManual: true, LastActivityAt: older})
	l.item("B0TEST0007", "Gave Up", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("idle", arsBook{title: "Not Yet", author: "Pat Example", asin: "B0TEST0008", minutes: 600})
	l.item("B0TEST0008", "Not Yet", "Pat Example", 600, "none", 0, nil)

	l.book("twinA", arsBook{title: "Twin Copy", author: "Pat Example", asin: "B0TEST0009", vg: "vg-a", primary: &yes, minutes: 600})
	l.book("twinB", arsBook{title: "Twin Copy", author: "Pat Example", asin: "B0TEST0009", vg: "vg-b", primary: &yes, minutes: 610})
	l.item("B0TEST0009", "Twin Copy", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("rtA", arsBook{title: "Settled By Runtime", author: "Pat Example", asin: "B0TEST0010", vg: "vg-c", primary: &yes, minutes: 600})
	l.book("rtB", arsBook{title: "Settled By Runtime", author: "Pat Example", asin: "B0TEST0010", vg: "vg-d", primary: &yes, minutes: 200})
	l.item("B0TEST0010", "Settled By Runtime", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	// A non-primary member carries the ASIN; its group's primary is the target.
	l.book("member", arsBook{title: "Grouped", author: "Pat Example", asin: "B0TEST0011", vg: "vg-e", primary: &no, minutes: 600})
	l.book("primary", arsBook{title: "Grouped", author: "Pat Example", vg: "vg-e", primary: &yes, minutes: 600})
	l.item("B0TEST0011", "Grouped", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("short", arsBook{title: "Short Copy", author: "Pat Example", asin: "B0TEST0012", minutes: 300})
	l.item("B0TEST0012", "Short Copy", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("junk", arsBook{title: "Read by Sam Voice", author: "Pat Example", asin: "B0TEST0013", minutes: 600})
	l.item("B0TEST0013", "Real Name Of It", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("bytitle", arsBook{title: "Synthetic Saga 2", author: "Robin Q. Sample", minutes: 600})
	l.item("B0TEST0014", "Synthetic Saga: Book 2 (Unabridged)", "Robin Sample", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("otherauthor", arsBook{title: "Lonely Title", author: "Someone Else", minutes: 600})
	l.item("B0TEST0015", "Lonely Title", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.item("B0TEST0016", "Nowhere To Be Found", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("swapped", arsBook{title: "Read by Sam Voice", author: "Lost Title Here", minutes: 600})
	l.item("B0TEST0017", "Lost Title Here", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	// Two items on one book: one by ASIN, one by title.
	l.book("claimed", arsBook{title: "Claimed Twice", author: "Pat Example", asin: "B0TEST0018", minutes: 600})
	l.item("B0TEST0018", "Claimed Twice", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))
	l.item("B0TEST0019", "Claimed Twice", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	// The same ASIN twice in the export.
	l.item("B0TEST0001", "Fresh Finish", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	l.book("nots", arsBook{title: "No Stamp", author: "Pat Example", asin: "B0TEST0020", minutes: 600})
	l.item("B0TEST0020", "No Stamp", "Pat Example", 600, "finished", 0, nil)

	l.book("dw", arsBook{title: "Doctor Who: Synthetic Adventure", author: "Pat Example", asin: "B0TEST0021", minutes: 600})
	l.item("B0TEST0021", "Doctor Who: Synthetic Adventure", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	plan, rows := l.plan()
	want := map[string]string{
		"asin:B0TEST0001":   arsWouldFinish,
		"asin:B0TEST0002":   arsSkipAlreadyFinished,
		"asin:B0TEST0003":   arsSkipNewerLocal,
		"asin:B0TEST0004":   arsWouldFinish,
		"asin:B0TEST0005":   arsWouldProgress,
		"asin:B0TEST0006":   arsSkipLocalProgress,
		"asin:B0TEST0007":   arsSkipAbandoned,
		"asin:B0TEST0008":   arsSkipNotStarted,
		"asin:B0TEST0009":   arsReviewAmbiguous,
		"asin:B0TEST0010":   arsWouldFinish,
		"asin:B0TEST0011":   arsWouldFinish,
		"asin:B0TEST0012":   arsReviewRuntimeMismatch,
		"asin:B0TEST0013":   arsReviewJunkTitle,
		"asin:B0TEST0014":   arsWouldFinish,
		"asin:B0TEST0015":   arsUnmatchedAuthorMismatch,
		"asin:B0TEST0016":   arsUnmatchedNoCandidate,
		"asin:B0TEST0017":   arsReviewSwappedTitle,
		"asin:B0TEST0018":   arsReviewDuplicateTarget,
		"asin:B0TEST0019":   arsReviewDuplicateTarget,
		"asin:B0TEST0001#2": arsUnmatchedDuplicateItem,
		"asin:B0TEST0020":   arsSkipNoTimestamp,
		"asin:B0TEST0021":   arsWouldFinish,
		"asin:B0TEST0022":   arsSkipAlreadyFinished,
	}
	require.Len(t, rows, len(want))
	for id, class := range want {
		require.Contains(t, rows, id)
		require.Equal(t, class, rows[id].Class, "%s: %s", id, rows[id].Reason)
	}
	// Only the would_* rows are applicable, and the Doctor Who one is held
	// by the framework guard under its class.
	for id, r := range rows {
		applicable := (r.Class == arsWouldFinish || r.Class == arsWouldProgress) && id != "asin:B0TEST0021"
		require.Equal(t, applicable, r.Applicable(), "%s: %s %s", id, r.Skipped, r.SkipReason)
		if !applicable && r.Class != arsWouldFinish {
			require.Equal(t, r.Class, r.Skipped, id)
		}
	}
	require.Equal(t, repairs.SkipOwnerManual, rows["asin:B0TEST0021"].Skipped)
	require.Equal(t, []string{l.ids["rtA"]}, rows["asin:B0TEST0010"].BookIDs)
	require.Equal(t, []string{l.ids["primary"]}, rows["asin:B0TEST0011"].BookIDs)
	require.Equal(t, []string{l.ids["bytitle"]}, rows["asin:B0TEST0014"].BookIDs)
	require.ElementsMatch(t, []string{l.ids["twinA"], l.ids["twinB"]}, rows["asin:B0TEST0009"].BookIDs)

	// Every count is a filter of the rows endpoint.
	require.Equal(t, 6, plan.ByClass[arsWouldFinish])
	require.Equal(t, 1, plan.SkippedByKind[arsSkipNewerLocal])
	page, err := plan.Page(arsTestPlanOp, repairs.FilterApplicable, arsWouldFinish, 0, 50)
	require.NoError(t, err)
	require.Equal(t, 5, page.Total)
	page, err = plan.Page(arsTestPlanOp, repairs.FilterSkippedKindPrefix+arsReviewDuplicateTarget, "", 0, 50)
	require.NoError(t, err)
	require.Equal(t, 2, page.Total)
	require.Equal(t, 6, plan.Applicable, "five finishes and one progress")

	// The plan wrote nothing.
	require.Nil(t, l.state("fresh"))
	require.Empty(t, l.positions("prog"))
}

func TestAudibleReadStatus_ApplyAndRevert(t *testing.T) {
	l := newARSLib(t)
	older := arsAudibleTS.Add(-48 * time.Hour)
	l.book("fresh", arsBook{title: "Fresh Finish", author: "Pat Example", asin: "B0TEST0001", minutes: 600})
	l.item("B0TEST0001", "Fresh Finish", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))
	l.book("partial", arsBook{title: "Partial Older", author: "Pat Example", asin: "B0TEST0004", minutes: 600})
	l.setState("partial", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 30, LastActivityAt: older,
		HideFromContinueListening: true, LastSegmentID: "abs", TotalListenedSeconds: 600})
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["partial"], "abs", 600, older))
	l.item("B0TEST0004", "Partial Older", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))
	l.book("prog", arsBook{title: "Halfway There", author: "Pat Example", asin: "B0TEST0005", minutes: 500})
	l.item("B0TEST0005", "Halfway There", "Pat Example", 500, "progress", 40, ptrTime(arsAudibleTS))
	l.book("done", arsBook{title: "Done Already", author: "Pat Example", asin: "B0TEST0002", minutes: 600})
	l.setState("done", database.UserBookState{Status: database.UserBookStatusFinished, ProgressPct: 100, LastActivityAt: older})
	l.item("B0TEST0002", "Done Already", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))

	doneBefore := l.state("done")
	plan, rows := l.plan()
	require.Equal(t, 3, plan.Applicable)
	res := l.apply(plan, []string{"asin:B0TEST0001", "asin:B0TEST0004", "asin:B0TEST0005", "asin:B0TEST0002"})
	require.Equal(t, repairs.OutcomeApplied, res["asin:B0TEST0001"].Outcome, "%+v", res)
	require.Equal(t, repairs.OutcomeApplied, res["asin:B0TEST0004"].Outcome, "%+v", res)
	require.Equal(t, repairs.OutcomeApplied, res["asin:B0TEST0005"].Outcome, "%+v", res)
	require.Equal(t, repairs.OutcomeNotApplicable, res["asin:B0TEST0002"].Outcome, "never re-finishes a finished book")
	_ = rows

	// Finished: manual, 100%, Audible's finish time as finished_at and last
	// activity (never now).
	s := l.state("fresh")
	require.NotNil(t, s)
	require.Equal(t, database.UserBookStatusFinished, s.Status)
	require.True(t, s.StatusManual)
	require.Equal(t, 100, s.ProgressPct)
	require.NotNil(t, s.FinishedAt)
	require.True(t, s.FinishedAt.Equal(arsAudibleTS), "finished_at %v", s.FinishedAt)
	require.True(t, s.LastActivityAt.Equal(arsAudibleTS))
	// A finished book with no position gets one at its end, at Audible's
	// time: the ABS progress list is built from positions.
	pos := l.positions("fresh")
	require.Len(t, pos, 1)
	require.Equal(t, "abs", pos[0].SegmentID)
	require.InDelta(t, 600*60, pos[0].PositionSeconds, 0.1)
	require.True(t, pos[0].UpdatedAt.Equal(arsAudibleTS))

	// The partial book keeps its hide flag and its position.
	s = l.state("partial")
	require.Equal(t, database.UserBookStatusFinished, s.Status)
	require.True(t, s.StatusManual)
	require.True(t, s.HideFromContinueListening)
	require.True(t, s.FinishedAt.Equal(arsAudibleTS))
	require.Len(t, l.positions("partial"), 1)

	// In progress: 40% of the local 500 min, on the ABS segment, at
	// Audible's time.
	s = l.state("prog")
	require.Equal(t, database.UserBookStatusInProgress, s.Status)
	require.False(t, s.StatusManual)
	require.Equal(t, 40, s.ProgressPct)
	require.True(t, s.LastActivityAt.Equal(arsAudibleTS))
	pos = l.positions("prog")
	require.Len(t, pos, 1)
	require.Equal(t, "abs", pos[0].SegmentID)
	require.InDelta(t, 500*60*0.4, pos[0].PositionSeconds, 1)
	require.True(t, pos[0].UpdatedAt.Equal(arsAudibleTS))

	// The finished book was not touched.
	s = l.state("done")
	require.True(t, undo.SameUserBookState(doneBefore, s))
	require.True(t, s.UpdatedAt.Equal(doneBefore.UpdatedAt), "not even rewritten")

	changes, err := l.store.GetOperationChanges(arsTestApplyOp)
	require.NoError(t, err)
	require.Len(t, changes, 3)
	for _, c := range changes {
		require.Equal(t, undo.ChangeTypeUserBookStateSet, c.ChangeType)
		require.Equal(t, undo.UserStateField(l.user), c.FieldName)
		require.Empty(t, undo.NotRestorableLabel(c))
	}

	// Re-planning after the apply: nothing left to do.
	plan2, rows2 := l.plan()
	require.Equal(t, 0, plan2.Applicable)
	require.Equal(t, arsSkipAlreadyFinished, rows2["asin:B0TEST0001"].Class)
	require.Equal(t, arsSkipAlreadyFinished, rows2["asin:B0TEST0004"].Class)
	require.Equal(t, arsSkipLocalProgress, rows2["asin:B0TEST0005"].Class)

	// A device listens to the in-progress book after the import: its revert
	// is refused and the listen is kept; the other two are restored.
	later := arsAudibleTS.Add(time.Hour)
	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["prog"], "abs", 13000, later))
	rr, err := audiobooks.NewRevertService(l.store).RevertOperation(arsTestApplyOp)
	require.ErrorContains(t, err, "positions changed after the import")
	require.Equal(t, 2, rr.Restored, "%+v", rr)
	require.Equal(t, 1, rr.Failed, "%+v", rr)

	require.Nil(t, l.state("fresh"), "a row the import created is deleted")
	require.Empty(t, l.positions("fresh"), "and so is the position it wrote")
	s = l.state("partial")
	require.Equal(t, database.UserBookStatusInProgress, s.Status)
	require.False(t, s.StatusManual)
	require.Equal(t, 30, s.ProgressPct)
	require.Nil(t, s.FinishedAt)
	require.True(t, s.HideFromContinueListening)
	require.True(t, s.LastActivityAt.Equal(older))
	require.Len(t, l.positions("partial"), 1)

	pos = l.positions("prog")
	require.Len(t, pos, 1)
	require.InDelta(t, 13000, pos[0].PositionSeconds, 0.1, "the later listen is the user's")
	require.Equal(t, database.UserBookStatusInProgress, l.state("prog").Status)

	finished, err := l.store.ListUserBookStatesByStatus(l.user, database.UserBookStatusFinished, 10, 0)
	require.NoError(t, err)
	require.Len(t, finished, 1, "only the untouched finished book is listed finished")
}

// A local listen between the plan and the apply wins: the row is refused as
// changed_since_plan and nothing is written.
func TestAudibleReadStatus_NewerLocalWinsAtApply(t *testing.T) {
	l := newARSLib(t)
	l.book("fresh", arsBook{title: "Fresh Finish", author: "Pat Example", asin: "B0TEST0001", minutes: 600})
	l.item("B0TEST0001", "Fresh Finish", "Pat Example", 600, "finished", 0, ptrTime(arsAudibleTS))
	plan, rows := l.plan()
	require.True(t, rows["asin:B0TEST0001"].Applicable())

	require.NoError(t, l.store.SetUserPositionAt(l.user, l.ids["fresh"], "abs", 120, arsAudibleTS.Add(time.Hour)))
	l.setState("fresh", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 1,
		LastActivityAt: arsAudibleTS.Add(time.Hour), LastSegmentID: "abs"})
	res := l.apply(plan, []string{"asin:B0TEST0001"})
	require.Equal(t, repairs.OutcomeChangedSincePlan, res["asin:B0TEST0001"].Outcome)
	require.Equal(t, arsSkipNewerLocal, res["asin:B0TEST0001"].Skipped)
	require.Equal(t, database.UserBookStatusInProgress, l.state("fresh").Status)
	changes, err := l.store.GetOperationChanges(arsTestApplyOp)
	require.NoError(t, err)
	require.Empty(t, changes)
}

// The Writer re-reads the state right before the write: a change after the
// re-plan is still refused, and nothing is journaled.
func TestAudibleReadStatus_WriterRefusesChangedState(t *testing.T) {
	l := newARSLib(t)
	id := l.book("fresh", arsBook{title: "Fresh Finish", author: "Pat Example", asin: "B0TEST0001", minutes: 600})
	w := repairs.NewWriter(l.store, l.store, l.fixer.ID(), "bulk_update", "repairs-").
		WithJournal(l.store, l.store, arsTestApplyOp).WithUserState(l.p.deps.UserReadStateStore())
	l.setState("fresh", database.UserBookState{Status: database.UserBookStatusInProgress, ProgressPct: 5})
	next := &database.UserBookState{UserID: l.user, BookID: id, Status: database.UserBookStatusFinished, StatusManual: true, ProgressPct: 100}
	err := w.SetUserState(l.user, id, undo.UserStateSnapshot{}, undo.UserStateSnapshot{State: next})
	require.ErrorIs(t, err, repairs.ErrChangedSincePlan)
	changes, err := l.store.GetOperationChanges(arsTestApplyOp)
	require.NoError(t, err)
	require.Empty(t, changes)
	require.Equal(t, database.UserBookStatusInProgress, l.state("fresh").Status)
}

func TestAudibleReadStatus_Registered(t *testing.T) {
	l := newARSLib(t)
	f, ok := l.p.Repairs().Get(audibleReadStatusFixerID)
	require.True(t, ok, "maintenance.audible-read-status not registered")
	require.False(t, repairs.AllowsBookTagsOnly(f), "the framework guard must run")
	require.False(t, repairs.AllowsITunesDatabaseOnly(f), "the iTunes path guard must run")
	require.NotNil(t, l.p.deps.UserReadStateStore())
}

func TestAudibleReadStatus_ExportShapes(t *testing.T) {
	var it arsItem
	require.NoError(t, json.Unmarshal([]byte(`{"asin":"B0TEST0001","title":"T","authors":"A One, B Two",
		"runtime_length_min":"600","is_finished":"True","percent_complete":null,
		"listening_status":{"finished_at_timestamp":"2026-09-01T12:00:00Z","time_remaining_seconds":null}}`), &it))
	require.Equal(t, []string{"A One", "B Two"}, []string(it.Authors))
	require.Equal(t, 36000.0, it.runtimeSeconds())
	require.Equal(t, arsAudibleFinished, it.status())
	ts, ok := it.timestamp()
	require.True(t, ok)
	require.True(t, ts.Equal(arsAudibleTS))

	require.NoError(t, json.Unmarshal([]byte(`{"authors":[{"name":"C Three"},"D Four"],"percent_complete":12.5}`), &it))
	require.Equal(t, []string{"C Three", "D Four"}, []string(it.Authors))

	require.True(t, arsTitlesMatch(arsKeyOf("The Synthetic Saga: Book 03"), arsKeyOf("Synthetic Saga 3")))
	require.True(t, arsTitlesMatch(arsKeyOf("Synthetic Saga: A Subtitle"), arsKeyOf("Synthetic Saga")))
	require.False(t, arsTitlesMatch(arsKeyOf("Synthetic Saga: One"), arsKeyOf("Synthetic Saga: Two")))
	require.False(t, arsTitlesMatch(arsKeyOf("Synthetic Saga 2"), arsKeyOf("Synthetic Saga 3")))
	require.False(t, arsTitlesMatch(arsKeyOf("Synthetic Saga: Book 2"), arsKeyOf("Synthetic Saga")))
}
