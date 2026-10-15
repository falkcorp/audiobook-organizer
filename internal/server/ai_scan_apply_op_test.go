// file: internal/server/ai_scan_apply_op_test.go
// version: 1.2.0
// guid: 6b3d1e97-0c45-4a28-9f7e-d2a8c5416b03
// last-edited: 2026-10-14

package server

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/aiscan"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/server/handlers"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// applyReporter is the relink reporter plus a result slot and a cancel switch.
type applyReporter struct {
	relinkOpReporter
	result   any
	canceled bool
	// onProgress, when set, runs on every progress update: the hook a test uses to
	// make something happen while the run is part way through.
	onProgress func()
	id         string
}

func (r *applyReporter) OpID() string { return r.id }
func (r *applyReporter) UpdateProgress(_, _ int, _ string) error {
	if r.onProgress != nil {
		r.onProgress()
	}
	return nil
}

func (r *applyReporter) SetResult(v any) error { r.result = v; return nil }
func (r *applyReporter) IsCanceled() bool      { return r.canceled }

func (r *applyReporter) report(t *testing.T) scanApplyReport {
	t.Helper()
	got, ok := r.result.(scanApplyReport)
	require.True(t, ok, "the op stores its per-result report; got %T", r.result)
	return got
}

type applyFixture struct {
	t         *testing.T
	st        *database.PebbleStore
	scanStore *database.AIScanStore
	s         *Server
	scan      *database.Scan
}

func newApplyFixture(t *testing.T) *applyFixture {
	t.Helper()
	st := newRelinkOpStore(t)
	scanStore, err := database.NewAIScanStore(t.TempDir() + "/aiscan.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = scanStore.Close() })
	s := newRelinkOpServer(st)
	s.aiScanStore = scanStore
	s.pipelineManager = aiscan.NewPipelineManager(scanStore, st, nil)
	scan, err := scanStore.CreateScan("realtime", nil, 0)
	require.NoError(t, err)
	require.NoError(t, scanStore.UpdateScanStatus(scan.ID, "complete"))
	return &applyFixture{t: t, st: st, scanStore: scanStore, s: s, scan: scan}
}

func aiscanManagerFor(f *applyFixture) *aiscan.PipelineManager {
	return aiscan.NewPipelineManager(f.scanStore, f.st, nil)
}

func (f *applyFixture) author(name string) *database.Author {
	f.t.Helper()
	a, err := f.st.CreateAuthor(name)
	require.NoError(f.t, err)
	return a
}

// result stores a merge result and returns its id.
func (f *applyFixture) result(sug database.ScanSuggestion) int {
	f.t.Helper()
	if sug.Action == "" {
		sug.Action = "merge"
	}
	r := &database.ScanResult{ScanID: f.scan.ID, Agreement: "agreed", Suggestion: sug}
	require.NoError(f.t, f.scanStore.SaveScanResult(r))
	return r.ID
}

func (f *applyFixture) run(dryRun *bool, ids ...int) (*applyReporter, error) {
	f.t.Helper()
	return f.runWith(&applyReporter{id: "op-apply-test"}, dryRun, ids...)
}

func (f *applyFixture) runWith(rep *applyReporter, dryRun *bool, ids ...int) (*applyReporter, error) {
	f.t.Helper()
	def := relinkOpDef(f.t, f.s.RegisterAIScanApplyOp, handlers.AIScanApplyDefID)
	p := handlers.AIScanApplyOpParams{ScanID: f.scan.ID, ResultIDs: ids, DryRun: dryRun}
	raw, err := json.Marshal(p)
	require.NoError(f.t, err)
	return rep, def.Run(context.Background(), raw, rep)
}

func live() *bool { f := false; return &f }

func (f *applyFixture) applied(id int) bool {
	f.t.Helper()
	rs, err := f.scanStore.GetScanResults(f.scan.ID)
	require.NoError(f.t, err)
	for _, r := range rs {
		if r.ID == id {
			return r.Applied
		}
	}
	f.t.Fatalf("result %d not found", id)
	return false
}

func TestAIScanApplyOp_MergesTheGroupAndMarksOnlyWhatSucceeded(t *testing.T) {
	f := newApplyFixture(t)
	keepA, fromA := f.author("Apply Keep A"), f.author("Apply From A")
	keepB, fromB := f.author("Apply Keep B"), f.author("Apply From B")
	bookA := mkRelinkOpBook(t, f.st, "apply-a", fromA.ID, false)
	lockedB := mkRelinkOpBook(t, f.st, "apply-b-locked", fromB.ID, false)
	lockAuthorName(t, f.st, lockedB)

	good := f.result(database.ScanSuggestion{CanonicalName: keepA.Name, KeepID: keepA.ID, AuthorIDs: []int{keepA.ID, fromA.ID}, Source: "groups_scan"})
	held := f.result(database.ScanSuggestion{CanonicalName: keepB.Name, KeepID: keepB.ID, AuthorIDs: []int{keepB.ID, fromB.ID}, Source: "groups_scan"})

	rep, err := f.run(live(), good, held)

	// The run says it did not finish: one result was held back by a lock.
	require.Error(t, err)
	require.Contains(t, err.Error(), "1 of 2 result(s) applied, 1 not applied")
	require.Contains(t, err.Error(), "result "+strconv.Itoa(held)+": ")
	require.Contains(t, err.Error(), "user-locked")

	require.Equal(t, []int{keepA.ID}, junctionIDs(t, f.st, bookA), "the merge really moved the book")
	requireSourceDeleted(t, f.st, fromA.ID)
	require.True(t, f.applied(good), "a merged result is marked")

	require.Equal(t, []int{fromB.ID}, junctionIDs(t, f.st, lockedB), "the locked book kept its author")
	still, gerr := f.st.GetAuthorByID(fromB.ID)
	require.NoError(t, gerr)
	require.Equal(t, fromB.ID, still.ID)
	require.False(t, f.applied(held), "a result whose merge was held back is NOT marked")

	rpt := rep.report(t)
	require.False(t, rpt.DryRun)
	require.Len(t, rpt.Results, 2)
	require.Equal(t, []int{fromA.ID}, rpt.Results[0].Merged)
	require.True(t, rpt.Results[0].Applied)
	require.False(t, rpt.Results[1].Applied)
}

func TestAIScanApplyOp_OmittedModeIsAPreviewThatWritesAndMarksNothing(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Preview Keep"), f.author("Preview From")
	book := mkRelinkOpBook(t, f.st, "preview-book", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})

	rep, err := f.run(nil, id) // no dry_run key at all

	require.NoError(t, err)
	require.Equal(t, []int{from.ID}, junctionIDs(t, f.st, book), "a preview moves nothing")
	still, gerr := f.st.GetAuthorByID(from.ID)
	require.NoError(t, gerr)
	require.Equal(t, from.ID, still.ID)
	require.False(t, f.applied(id))
	rpt := rep.report(t)
	require.True(t, rpt.DryRun)
	require.Equal(t, []int{from.ID}, rpt.Results[0].Merged, "the preview says what it would merge")
}

func TestAIScanApplyOp_APreviewNamesTheLockThatWouldHoldAnAuthorBack(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Preview Lock Keep"), f.author("Preview Lock From")
	book := mkRelinkOpBook(t, f.st, "preview-lock-book", from.ID, false)
	lockAuthorName(t, f.st, book)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})

	rep, err := f.run(nil, id)

	require.NoError(t, err, "a preview reports; it does not fail")
	rpt := rep.report(t)
	require.False(t, rpt.Results[0].Applied)
	require.Empty(t, rpt.Results[0].Merged, "a preview does not call an author merged when a lock would hold it back")
	require.NotEmpty(t, rpt.Results[0].Errors)
	require.Contains(t, rpt.Results[0].Errors[0], "user-locked")
}

func TestAIScanApplyOp_ARefusedRowDoesNotStopTheOthers(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Refuse Keep"), f.author("Refuse From")
	mkRelinkOpBook(t, f.st, "refuse-book", from.ID, false)
	alias := f.result(database.ScanSuggestion{Action: "alias", CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}})
	good := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})

	_, err := f.run(live(), alias, good)

	require.Error(t, err)
	require.Contains(t, err.Error(), "result "+strconv.Itoa(alias))
	require.Contains(t, err.Error(), "only merge suggestions")
	require.False(t, f.applied(alias))
	require.True(t, f.applied(good))
	requireSourceDeleted(t, f.st, from.ID)
}

func TestAIScanApplyOp_ASupersededScanIsRefusedBeforeAnythingMerges(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Super Keep"), f.author("Super From")
	book := mkRelinkOpBook(t, f.st, "super-book", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})
	newer, err := f.scanStore.CreateScan("realtime", nil, 0)
	require.NoError(t, err)
	ok, err := f.scanStore.SupersedeIfUnapplied(f.scan.ID, newer.ID)
	require.NoError(t, err)
	require.True(t, ok)

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "superseded by scan "+strconv.Itoa(newer.ID))
	require.Equal(t, []int{from.ID}, junctionIDs(t, f.st, book))
}

func TestAIScanApplyOp_AnAlreadyAppliedResultIsNotMergedAgain(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Again Keep"), f.author("Again From")
	mkRelinkOpBook(t, f.st, "again-book", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})

	_, err := f.run(live(), id)
	require.NoError(t, err)
	rep, err := f.run(live(), id)

	require.NoError(t, err, "the repeat is a no-op, not a failure")
	require.Equal(t, "already applied", rep.report(t).Results[0].Skipped)
}

// Two results of one scan can share authors. They are applied in order, each
// merge reading the store as the last one left it, so the second finds its keep
// author already folded into another and fails by name instead of merging into a
// deleted row.
func TestAIScanApplyOp_ResultsSharingAnAuthorAreAppliedInOrder(t *testing.T) {
	f := newApplyFixture(t)
	a, b, c := f.author("Shared A"), f.author("Shared B"), f.author("Shared C")
	mkRelinkOpBook(t, f.st, "shared-b", b.ID, false)
	mkRelinkOpBook(t, f.st, "shared-c", c.ID, false)
	first := f.result(database.ScanSuggestion{CanonicalName: a.Name, KeepID: a.ID, AuthorIDs: []int{a.ID, b.ID}, Source: "groups_scan"})
	second := f.result(database.ScanSuggestion{CanonicalName: b.Name, KeepID: b.ID, AuthorIDs: []int{b.ID, c.ID}, Source: "groups_scan"})

	_, err := f.run(live(), first, second)

	require.Error(t, err)
	require.True(t, f.applied(first))
	require.False(t, f.applied(second))
	require.Contains(t, err.Error(), "keep author "+strconv.Itoa(b.ID))
	still, gerr := f.st.GetAuthorByID(c.ID)
	require.NoError(t, gerr)
	require.Equal(t, c.ID, still.ID, "nothing was merged into a keep author that was already gone")
}

// R1 folds B into A. R2 asks to fold B and D into C. B is gone, but it went into A,
// not C, so R2 must not be marked applied (A and C are still separate), while D is
// still merged.
func TestAIScanApplyOp_AnAuthorMergedIntoSomeoneElseDoesNotCountAsMerged(t *testing.T) {
	f := newApplyFixture(t)
	a, b, c, d := f.author("Elsewhere A"), f.author("Elsewhere B"), f.author("Elsewhere C"), f.author("Elsewhere D")
	mkRelinkOpBook(t, f.st, "elsewhere-b", b.ID, false)
	bookD := mkRelinkOpBook(t, f.st, "elsewhere-d", d.ID, false)
	r1 := f.result(database.ScanSuggestion{CanonicalName: a.Name, KeepID: a.ID, AuthorIDs: []int{a.ID, b.ID}, Source: "groups_scan"})
	r2 := f.result(database.ScanSuggestion{CanonicalName: c.Name, KeepID: c.ID, AuthorIDs: []int{c.ID, b.ID, d.ID}, Source: "groups_scan"})

	_, err := f.run(live(), r1, r2)

	require.Error(t, err)
	require.True(t, f.applied(r1))
	require.False(t, f.applied(r2), "B went into A, so A and C are still separate: R2 is not done")
	require.Contains(t, err.Error(), "already merged into author "+strconv.Itoa(a.ID)+", not author "+strconv.Itoa(c.ID))
	require.Equal(t, []int{c.ID}, junctionIDs(t, f.st, bookD), "D, which was free to merge, still merged")
	requireSourceDeleted(t, f.st, d.ID)
}

func TestAIScanApplyOp_AFlaggedNarratorAlreadyMergedIntoAnAuthorIsNotReclassified(t *testing.T) {
	f := newApplyFixture(t)
	a, v, keep := f.author("Narr Elsewhere A"), f.author("Narr Elsewhere Voice"), f.author("Narr Elsewhere Keep")
	mkRelinkOpBook(t, f.st, "narr-elsewhere-voice", v.ID, false)
	mkRelinkOpBook(t, f.st, "narr-elsewhere-keep", keep.ID, false)
	r1 := f.result(database.ScanSuggestion{CanonicalName: a.Name, KeepID: a.ID, AuthorIDs: []int{a.ID, v.ID}, Source: "groups_scan"})
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": v.Name, "ids": []int{v.ID}}})
	require.NoError(t, err)
	r2 := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, v.ID, f.author("Narr Elsewhere Extra").ID},
		Roles: roles, Source: "full_scan",
	})

	_, err = f.run(live(), r1, r2)

	require.Error(t, err)
	require.True(t, f.applied(r1))
	require.False(t, f.applied(r2))
	require.Contains(t, err.Error(), "cannot be reclassified as a narrator")
}

func TestAIScanApplyOp_ACancelStopsBetweenResultsAndKeepsWhatMerged(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Cancel Keep"), f.author("Cancel From")
	mkRelinkOpBook(t, f.st, "cancel-book", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})

	def := relinkOpDef(t, f.s.RegisterAIScanApplyOp, handlers.AIScanApplyDefID)
	raw, err := json.Marshal(handlers.AIScanApplyOpParams{ScanID: f.scan.ID, ResultIDs: []int{id}, DryRun: live()})
	require.NoError(t, err)
	err = def.Run(context.Background(), raw, &applyReporter{canceled: true})

	require.Error(t, err)
	require.Contains(t, err.Error(), "cancelled")
	require.False(t, f.applied(id))
}

func TestAIScanApplyOp_AFullScanNarratorIsReclassifiedNotMerged(t *testing.T) {
	f := newApplyFixture(t)
	keep, variant, voice := f.author("Role Keep"), f.author("Role Variant"), f.author("Role Voice")
	variantBook := mkRelinkOpBook(t, f.st, "role-variant", variant.ID, false)
	voiceBook := mkRelinkOpBook(t, f.st, "role-voice", voice.ID, false)
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": voice.Name, "ids": []int{voice.ID}}})
	require.NoError(t, err)
	id := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, variant.ID, voice.ID},
		Roles: roles, Source: "full_scan",
	})

	rep, err := f.run(live(), id)

	require.NoError(t, err)
	require.True(t, f.applied(id))
	require.Equal(t, []int{keep.ID}, junctionIDs(t, f.st, variantBook), "the variant was merged")
	requireSourceDeleted(t, f.st, voice.ID)
	narrators, nerr := f.st.GetBookNarrators(voiceBook)
	require.NoError(t, nerr)
	require.Len(t, narrators, 1, "the flagged voice became the book's narrator")
	require.Equal(t, []int{voice.ID}, rep.report(t).Results[0].Reclassified)
	// And it was NOT folded into the keep author's credits.
	credits, cerr := f.st.GetBookAuthors(voiceBook)
	require.NoError(t, cerr)
	for _, ba := range credits {
		require.NotEqual(t, keep.ID, ba.AuthorID, "a flagged narrator's book must not be credited to the keep author")
	}
}

func TestAIScanApplyOp_AFlaggedNarratorWithALockedBookKeepsThatBookAndIsReported(t *testing.T) {
	f := newApplyFixture(t)
	keep, variant, voice := f.author("Lockrole Keep"), f.author("Lockrole Variant"), f.author("Lockrole Voice")
	mkRelinkOpBook(t, f.st, "lockrole-variant", variant.ID, false)
	lockedVoiceBook := mkRelinkOpBook(t, f.st, "lockrole-voice", voice.ID, false)
	lockAuthorName(t, f.st, lockedVoiceBook)
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": voice.Name, "ids": []int{voice.ID}}})
	require.NoError(t, err)
	id := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, variant.ID, voice.ID},
		Roles: roles, Source: "full_scan",
	})

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "user-locked")
	require.Equal(t, []int{voice.ID}, junctionIDs(t, f.st, lockedVoiceBook), "the locked book keeps its author")
	still, gerr := f.st.GetAuthorByID(voice.ID)
	require.NoError(t, gerr)
	require.Equal(t, voice.ID, still.ID)
	require.False(t, f.applied(id), "the merge happened but the narrator step did not: not marked")
}

func TestAIScanApplyOp_ANarratorFlagItCannotMatchRefusesTheResult(t *testing.T) {
	f := newApplyFixture(t)
	keep, variant := f.author("Pos Keep"), f.author("Pos Variant")
	book := mkRelinkOpBook(t, f.st, "pos-book", variant.ID, false)
	// A groups-scan answer names the narrator by POSITION in the group, which
	// cannot be mapped to an author id.
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": "N", "ids": []int{1}}})
	require.NoError(t, err)
	id := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, variant.ID},
		Roles: roles, Source: "groups_scan",
	})

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "narrator by position")
	require.Equal(t, []int{variant.ID}, junctionIDs(t, f.st, book), "refused means untouched")
	require.False(t, f.applied(id))
}

func TestPlanScanMerge(t *testing.T) {
	role := func(kind string, ids ...int) json.RawMessage {
		b, _ := json.Marshal(map[string]any{kind: map[string]any{"name": "X", "ids": ids}})
		return b
	}
	base := database.ScanSuggestion{Action: "merge", KeepID: 2, AuthorIDs: []int{1, 2, 3, 3, 0}, Source: "full_scan"}

	plan, err := planScanMerge(database.ScanResult{Suggestion: base}, aiscan.RawRoleFlags{})
	require.NoError(t, err)
	require.Equal(t, scanApplyPlan{KeepID: 2, MergeIDs: []int{1, 3}}, plan, "duplicates and non-ids are dropped, the keep is not merged")

	withNarrator := base
	withNarrator.Roles = role("narrator", 3)
	plan, err = planScanMerge(database.ScanResult{Suggestion: withNarrator}, aiscan.RawRoleFlags{})
	require.NoError(t, err)
	require.Equal(t, scanApplyPlan{KeepID: 2, MergeIDs: []int{1}, NarratorIDs: []int{3}}, plan)

	// An agreed row whose roles came from the full scan names authors by id: its
	// narrator is reclassified, as on a full-scan row.
	agreedByID := withNarrator
	agreedByID.Source, agreedByID.RolesSource = "cross_validate", "full_scan"
	plan, err = planScanMerge(database.ScanResult{Suggestion: agreedByID}, aiscan.RawRoleFlags{})
	require.NoError(t, err)
	require.Equal(t, scanApplyPlan{KeepID: 2, MergeIDs: []int{1}, NarratorIDs: []int{3}}, plan)

	for name, mutate := range map[string]func(*database.ScanSuggestion){
		"not a merge":                              func(s *database.ScanSuggestion) { s.Action = "split" },
		"no keep":                                  func(s *database.ScanSuggestion) { s.KeepID = 0 },
		"keep not in the group":                    func(s *database.ScanSuggestion) { s.KeepID = 9 },
		"narrator is the keep":                     func(s *database.ScanSuggestion) { s.Roles = role("narrator", 2) },
		"narrator not in group":                    func(s *database.ScanSuggestion) { s.Roles = role("narrator", 8) },
		"narrator by position":                     func(s *database.ScanSuggestion) { s.Roles = role("narrator", 1); s.Source = "groups_scan" },
		"agreed row's narrator, source unrecorded": func(s *database.ScanSuggestion) { s.Roles = role("narrator", 1); s.Source = "cross_validate" },
		"agreed row's narrator by position": func(s *database.ScanSuggestion) {
			s.Roles, s.Source, s.RolesSource = role("narrator", 1), "cross_validate", "groups_scan"
		},
		"publisher flagged": func(s *database.ScanSuggestion) { s.Roles = role("publisher", 1) },
		"unreadable roles":  func(s *database.ScanSuggestion) { s.Roles = json.RawMessage(`{"narrator":`) },
		"nothing to merge":  func(s *database.ScanSuggestion) { s.AuthorIDs = []int{2} },
	} {
		t.Run(name, func(t *testing.T) {
			sug := base
			mutate(&sug)
			_, err := planScanMerge(database.ScanResult{Suggestion: sug}, aiscan.RawRoleFlags{})
			require.Error(t, err)
		})
	}
}

// The batch prompts ask the model ONLY for is_narrator / is_publisher, so for every
// batch and nightly scan that list is the whole flag. Recording such a scan used
// to drop it and the apply merged the narrator into the keep author.
func TestAIScanApplyOp_ADeprecatedIsNarratorFlagIsHonouredNotMerged(t *testing.T) {
	f := newApplyFixture(t)
	keep, variant, voice := f.author("Flag Keep"), f.author("Flag Variant"), f.author("Flag Voice")
	variantBook := mkRelinkOpBook(t, f.st, "flag-variant", variant.ID, false)
	voiceBook := mkRelinkOpBook(t, f.st, "flag-voice", voice.ID, false)

	scanID, err := aiscan.RecordFullScanResults(f.scanStore, "night-flag", []ai.AuthorDiscoverySuggestion{{
		AuthorIDs: []int{keep.ID, variant.ID, voice.ID}, Action: "merge", CanonicalName: keep.Name,
		Confidence: "high", IsNarrator: []int{voice.ID},
	}}, f.st)
	require.NoError(t, err)
	f.scan, err = f.scanStore.GetScan(scanID)
	require.NoError(t, err)
	rows, err := f.scanStore.GetScanResults(scanID)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	rep, err := f.run(live(), rows[0].ID)

	require.NoError(t, err)
	require.Equal(t, []int{keep.ID}, junctionIDs(t, f.st, variantBook), "the variant merged")
	credits, cerr := f.st.GetBookAuthors(voiceBook)
	require.NoError(t, cerr)
	for _, ba := range credits {
		require.NotEqual(t, keep.ID, ba.AuthorID, "the flagged narrator's book must not be credited to the keep author")
	}
	narrators, nerr := f.st.GetBookNarrators(voiceBook)
	require.NoError(t, nerr)
	require.Len(t, narrators, 1, "the flagged voice became the narrator of its book")
	require.Equal(t, []int{voice.ID}, rep.report(t).Results[0].Reclassified)
}

// A scan stored before the converters kept the flag has no flag on its result; the
// stored phase output is the only place the answer survives. The apply refuses the
// result, and merges nothing, rather than folding the flagged author in.
func TestAIScanApplyOp_AFlagOnlyInTheStoredPhaseOutputRefusesTheResult(t *testing.T) {
	f := newApplyFixture(t)
	keep, voice := f.author("Old Keep"), f.author("Old Voice")
	voiceBook := mkRelinkOpBook(t, f.st, "old-voice", voice.ID, false)
	_, err := f.scanStore.CreatePhase(f.scan.ID, "full_scan", "m")
	require.NoError(t, err)
	output, err := json.Marshal([]ai.AuthorDiscoverySuggestion{{
		AuthorIDs: []int{keep.ID, voice.ID}, Action: "merge", CanonicalName: keep.Name, IsNarrator: []int{voice.ID},
	}})
	require.NoError(t, err)
	require.NoError(t, f.scanStore.SavePhaseData(f.scan.ID, "full_scan", nil, output, json.RawMessage("[]")))
	// The stored result as the old converter wrote it: no roles at all.
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, voice.ID}, Source: "full_scan"})

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "flags author "+strconv.Itoa(voice.ID))
	require.Equal(t, []int{voice.ID}, junctionIDs(t, f.st, voiceBook), "refused means untouched")
	require.False(t, f.applied(id))
}

func TestAIScanApplyOp_AnUnreadableStoredPhaseOutputFailsTheRunClosed(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Bad Keep"), f.author("Bad From")
	book := mkRelinkOpBook(t, f.st, "bad-book", from.ID, false)
	_, err := f.scanStore.CreatePhase(f.scan.ID, "full_scan", "m")
	require.NoError(t, err)
	require.NoError(t, f.scanStore.SavePhaseData(f.scan.ID, "full_scan", nil, json.RawMessage(`{"not":"a list"}`), json.RawMessage("[]")))
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "full_scan"})

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Equal(t, []int{from.ID}, junctionIDs(t, f.st, book), "an unreadable answer is not 'nothing flagged'")
}

// A nightly run supersedes a scan whose results nobody has applied. If that lands
// while an apply is part way through, the run's merges would be orphaned (the
// results cannot be marked on a superseded scan). The run claims the scan first, so
// the supersede is refused instead.
func TestAIScanApplyOp_ASupersedeThatLandsMidRunIsRefusedByTheClaim(t *testing.T) {
	f := newApplyFixture(t)
	keepA, fromA := f.author("Claim Keep A"), f.author("Claim From A")
	keepB, fromB := f.author("Claim Keep B"), f.author("Claim From B")
	mkRelinkOpBook(t, f.st, "claim-a", fromA.ID, false)
	mkRelinkOpBook(t, f.st, "claim-b", fromB.ID, false)
	r1 := f.result(database.ScanSuggestion{CanonicalName: keepA.Name, KeepID: keepA.ID, AuthorIDs: []int{keepA.ID, fromA.ID}, Source: "groups_scan"})
	r2 := f.result(database.ScanSuggestion{CanonicalName: keepB.Name, KeepID: keepB.ID, AuthorIDs: []int{keepB.ID, fromB.ID}, Source: "groups_scan"})
	newer, err := f.scanStore.CreateScan("realtime", nil, 0)
	require.NoError(t, err)

	superseded := make([]bool, 0, 2)
	rep := &applyReporter{id: "op-claim", onProgress: func() {
		// The reporter is called at the start of each result, before its merge.
		ok, serr := f.scanStore.SupersedeIfUnapplied(f.scan.ID, newer.ID)
		require.NoError(t, serr)
		superseded = append(superseded, ok)
	}}
	_, err = f.runWith(rep, live(), r1, r2)

	require.NoError(t, err)
	require.NotEmpty(t, superseded)
	for _, ok := range superseded {
		require.False(t, ok, "the scan cannot be superseded while a run holds it")
	}
	require.True(t, f.applied(r1))
	require.True(t, f.applied(r2))
	got, err := f.scanStore.GetScan(f.scan.ID)
	require.NoError(t, err)
	require.Equal(t, "complete", got.Status)
	require.Empty(t, got.ApplyingOpID, "the claim is released when the run ends")
}

func TestAIScanApplyOp_RefusesAScanThatIsNotComplete(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Status Keep"), f.author("Status From")
	book := mkRelinkOpBook(t, f.st, "status-book", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})

	preview := true
	for _, status := range []string{"scanning", "failed", "canceled", "pending"} {
		require.NoError(t, f.scanStore.UpdateScanStatus(f.scan.ID, status))
		// A live run is refused by the claim as well; a preview claims nothing, so the
		// op's own check is the only thing that stops it.
		for name, dry := range map[string]*bool{"live": live(), "preview": &preview} {
			_, err := f.run(dry, id)
			require.Error(t, err, "%s %s", status, name)
			require.Contains(t, err.Error(), "only a finished scan's results can be applied", "%s %s", status, name)
			require.Equal(t, []int{from.ID}, junctionIDs(t, f.st, book), "nothing merged for a %s scan (%s)", status, name)
		}
	}
}

func TestAIScanApplyOp_AnUnknownAuthorIsNotFoundAndTheResultIsNotMarked(t *testing.T) {
	f := newApplyFixture(t)
	keep := f.author("Unknown Keep")
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, 987654}, Source: "groups_scan"})

	_, err := f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "author 987654 was not found")
	require.False(t, f.applied(id), "an author that never existed is not 'already merged'")
}

func TestAIScanApplyOp_AFlaggedNarratorWithALockedNarratorKeepsItsBook(t *testing.T) {
	f := newApplyFixture(t)
	keep, voice := f.author("NarrLock Keep"), f.author("NarrLock Voice")
	mkRelinkOpBook(t, f.st, "narrlock-keep", keep.ID, false)
	voiceBook := mkRelinkOpBook(t, f.st, "narrlock-voice", voice.ID, false)
	require.NoError(t, f.st.UpsertMetadataFieldState(&database.MetadataFieldState{
		BookID: voiceBook, Field: database.FieldKeyNarrator, OverrideLocked: true,
	}))
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": voice.Name, "ids": []int{voice.ID}}})
	require.NoError(t, err)
	id := f.result(database.ScanSuggestion{
		CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, voice.ID},
		Roles: roles, Source: "full_scan",
	})

	_, err = f.run(live(), id)

	require.Error(t, err)
	require.Contains(t, err.Error(), "narrator is user-locked")
	require.Equal(t, []int{voice.ID}, junctionIDs(t, f.st, voiceBook), "the book keeps its author")
	narrators, nerr := f.st.GetBookNarrators(voiceBook)
	require.NoError(t, nerr)
	require.Empty(t, narrators, "and gets no narrator the owner locked")
	require.False(t, f.applied(id))
}

// End to end through CrossValidate: the groups scan agreed on the merge without
// a role, the full scan flagged the narrator by author id. The agreed row carries
// the full scan's flag and the apply reclassifies the narrator instead of
// refusing the row (or merging the narrator into the keep).
func TestPlanScanMerge_AnAgreedRowWithTheFullScansNarratorReclassifiesIt(t *testing.T) {
	roles, err := json.Marshal(map[string]any{"narrator": map[string]any{"name": "Synthetic Voice", "ids": []int{30}}})
	require.NoError(t, err)
	results := aiscan.CrossValidate(1,
		[]database.ScanSuggestion{{Action: "merge", CanonicalName: "Synthetic Keep", AuthorIDs: []int{10, 20, 30}, KeepID: 10, Source: "groups_scan"}},
		[]database.ScanSuggestion{{Action: "merge", CanonicalName: "Synthetic Keep", AuthorIDs: []int{10, 20, 30}, Roles: roles, Source: "full_scan"}})
	require.Len(t, results, 1)

	plan, err := planScanMerge(results[0], aiscan.RawRoleFlags{Authors: map[int]bool{30: true}})
	require.NoError(t, err)
	require.Equal(t, scanApplyPlan{KeepID: 10, MergeIDs: []int{20}, NarratorIDs: []int{30}}, plan)

	// The same rows where the groups scan flagged the narrator by position: refused.
	positional := results[0]
	positional.Suggestion.Roles, positional.Suggestion.RolesSource = json.RawMessage(`{"narrator":{"name":"N","ids":[2]}}`), "groups_scan"
	_, err = planScanMerge(positional, aiscan.RawRoleFlags{})
	require.ErrorContains(t, err, "narrator by position")
}

// supersedeAfterNoteStore supersedes the scan the moment the apply records a
// result's undo note, so the MarkResultApplied that follows is refused.
type supersedeAfterNoteStore struct {
	*database.PebbleStore
	scans  *database.AIScanStore
	scanID int
}

func (s supersedeAfterNoteStore) CreateOperationChange(c *database.OperationChange) error {
	if err := s.PebbleStore.CreateOperationChange(c); err != nil {
		return err
	}
	if c.ChangeType == undo.ChangeTypeAIScanResultApplied && !c.Voided {
		return s.scans.UpdateScanStatus(s.scanID, "superseded")
	}
	return nil
}

// The note that lets an undo reopen a result is written before the result is
// marked applied. A mark that is refused voids the note, so an undo never
// "reopens" a result this run did not mark.
func TestAIScanApplyOp_ARefusedMarkVoidsTheResultsUndoNote(t *testing.T) {
	f := newApplyFixture(t)
	keep, from := f.author("Note Keep"), f.author("Note From")
	mkRelinkOpBook(t, f.st, "note-from", from.ID, false)
	id := f.result(database.ScanSuggestion{CanonicalName: keep.Name, KeepID: keep.ID, AuthorIDs: []int{keep.ID, from.ID}, Source: "groups_scan"})
	f.s.store = supersedeAfterNoteStore{PebbleStore: f.st, scans: f.scanStore, scanID: f.scan.ID}

	_, err := f.run(live(), id)

	require.Error(t, err, "the result is reported not applied")
	require.False(t, f.applied(id))
	changes, err := f.st.GetOperationChanges("op-apply-test")
	require.NoError(t, err)
	notes := 0
	for _, c := range changes {
		if c.ChangeType == undo.ChangeTypeAIScanResultApplied {
			notes++
			require.True(t, c.Voided, "the note of a result that was not marked is voided")
		}
	}
	require.Equal(t, 1, notes, "the note was written before the mark")
}
