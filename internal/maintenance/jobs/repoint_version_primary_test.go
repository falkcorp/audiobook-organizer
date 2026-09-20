// file: internal/maintenance/jobs/repoint_version_primary_test.go
// version: 1.2.0
// guid: 8c3f1b52-6a04-4de7-9b18-2f7a05c9d6e1
// last-edited: 2026-09-20

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
)

// rvpRun runs repoint-version-primary and returns its structured result.
func rvpRun(t *testing.T, store maintenance.JobStore, params string, dryRun bool) repointVersionPrimaryResult {
	t.Helper()
	res, err := rvpTryRun(t, store, params, dryRun)
	if err != nil {
		t.Fatalf("repoint-version-primary Run: %v", err)
	}
	return res
}

func rvpTryRun(t *testing.T, store maintenance.JobStore, params string, dryRun bool) (repointVersionPrimaryResult, error) {
	t.Helper()
	var got any
	ctx := maintenance.WithOperationID(context.Background(), "op-rvp-test")
	ctx = maintenance.WithRawParams(ctx, json.RawMessage(params))
	ctx = maintenance.WithResultSetter(ctx, func(v any) error { got = v; return nil })
	runErr := (&repointVersionPrimaryJob{}).Run(ctx, store, ddJobReporter{}, dryRun)
	if got == nil {
		return repointVersionPrimaryResult{}, runErr
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var res repointVersionPrimaryResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res, runErr
}

func rvpStr(s string) *string { return &s }
func rvpBool(b bool) *bool    { return &b }

// rvpSeedPair creates the prod shape: an imported, explicitly non-primary
// single-file chapter record in dir, paired in a 2-member version group with an
// organized single-file twin that holds the explicit primary flag and lives
// ALONE in its own directory (so it is below min_files and no detector run can
// ever group it — the measured reason the blocker's advice is unfollowable).
func rvpSeedPair(t *testing.T, s *database.PebbleStore, dir, name string, i int, vgid string) (imported, twin *database.Book) {
	t.Helper()
	author := 1
	ipath := fmt.Sprintf("%s/%s - %d.mp3", dir, name, i)
	imported = ddMustBook(t, s, &database.Book{
		Title: fmt.Sprint(i), FilePath: ipath, Duration: chIntP(1200), AuthorID: &author,
		IsPrimaryVersion: rvpBool(false), VersionGroupID: rvpStr(vgid), LibraryState: rvpStr("imported"),
	})
	ddMustFile(t, s, &database.BookFile{BookID: imported.ID, FilePath: ipath, Duration: 1200})

	tpath := fmt.Sprintf("/lib/org/%s %d/%s %d.mp3", name, i, name, i)
	twin = ddMustBook(t, s, &database.Book{
		Title: fmt.Sprintf("%s %d", name, i), FilePath: tpath, Duration: chIntP(1200), AuthorID: &author,
		IsPrimaryVersion: rvpBool(true), VersionGroupID: rvpStr(vgid), LibraryState: rvpStr("organized"),
	})
	ddMustFile(t, s, &database.BookFile{BookID: twin.ID, FilePath: tpath, Duration: 1200})
	return imported, twin
}

// rvpSeedRun seeds a whole n-member chapter run of paired records.
func rvpSeedRun(t *testing.T, s *database.PebbleStore, dir, name string, n int, vgPrefix string) (imported, twins []*database.Book) {
	t.Helper()
	for i := n; i >= 1; i-- {
		im, tw := rvpSeedPair(t, s, dir, name, i, fmt.Sprintf("%s-%d", vgPrefix, i))
		imported = append(imported, im)
		twins = append(twins, tw)
	}
	return imported, twins
}

func rvpFlag(t *testing.T, s *database.PebbleStore, id string) string {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID %s: %v", id, err)
	}
	return priorFlag(b.IsPrimaryVersion)
}

func rvpGroupID(t *testing.T, s *database.PebbleStore, id string) string {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID %s: %v", id, err)
	}
	return strPtr(b.VersionGroupID)
}

// TestRepointVersionPrimary_QualifyingPairRepoints is the whole point: a
// detected chapter run whose ONLY blocker is that every member is a non-primary
// version has its flag moved onto the imported side, and the version link
// survives — the owner rejected dissolving it because the link is the only
// record that the two sides are copies of each other.
func TestRepointVersionPrimary_QualifyingPairRepoints(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")

	// Dry run: decisions reported, nothing written.
	dry := rvpRun(t, s, `{}`, true)
	if dry.GroupsSoleNonPrimary != 1 {
		t.Fatalf("want 1 sole-non-primary group, got %d (%+v)", dry.GroupsSoleNonPrimary, dry.Counts)
	}
	if dry.Counts[bucketWouldRepoint] != 3 || dry.Counts[bucketRepointed] != 0 {
		t.Fatalf("dry run buckets wrong: %+v", dry.Counts)
	}
	for i := range imported {
		if got := rvpFlag(t, s, imported[i].ID); got != "false" {
			t.Fatalf("dry run WROTE to imported %s: flag=%s", imported[i].ID, got)
		}
		if got := rvpFlag(t, s, twins[i].ID); got != "true" {
			t.Fatalf("dry run WROTE to twin %s: flag=%s", twins[i].ID, got)
		}
	}
	// apply:true alone, with the dispatcher still in dry_run, must not write.
	half := rvpRun(t, s, `{"apply":true}`, true)
	if half.Counts[bucketRepointed] != 0 || rvpFlag(t, s, imported[0].ID) != "false" {
		t.Fatalf("apply:true with dry_run:true wrote: %+v", half.Counts)
	}

	res := rvpRun(t, s, `{"apply":true}`, false)
	if res.Counts[bucketRepointed] != 3 || res.Counts[bucketWouldRepoint] != 0 {
		t.Fatalf("apply buckets wrong: %+v", res.Counts)
	}
	for i := range imported {
		if got := rvpFlag(t, s, imported[i].ID); got != "true" {
			t.Fatalf("imported %s not promoted: flag=%s", imported[i].ID, got)
		}
		if got := rvpFlag(t, s, twins[i].ID); got != "false" {
			t.Fatalf("twin %s not demoted: flag=%s", twins[i].ID, got)
		}
		// Explicit on BOTH sides: neither EffectiveIsPrimaryVersion (nil =>
		// primary) nor countsAsPrimary (nil => not primary) has to guess.
		if rvpGroupID(t, s, imported[i].ID) == "" || rvpGroupID(t, s, twins[i].ID) == "" {
			t.Fatalf("version link dissolved on pair %d; the owner rejected that", i)
		}
	}

	// Idempotent: the run is no longer blocked on non-primary members, so it is
	// no longer in the sole-non-primary population at all.
	again := rvpRun(t, s, `{"apply":true}`, false)
	if again.GroupsSoleNonPrimary != 0 || again.Counts[bucketRepointed] != 0 {
		t.Fatalf("second apply was not inert: sole=%d counts=%+v", again.GroupsSoleNonPrimary, again.Counts)
	}
}

// TestRepointVersionPrimary_RefusesWhenGroupWouldLosePrimary covers the
// zero-primary case the two helpers disagree about. countsAsPrimary
// (reconcile/elect_primaries.go:203) does not count a soft-deleted row or a
// merge loser, so writing an explicit true on one would leave the group reading
// primary to every listing and ZERO primaries to elect-missing-primaries.
//
// It is asserted on classify with a doctored snapshot rather than end-to-end,
// because the detector drops non-live rows before this job ever sees them: the
// check is defence in depth against a future detector that does not, and the
// only way to exercise it is to hand it the row the detector would have
// dropped.
func TestRepointVersionPrimary_RefusesWhenGroupWouldLosePrimary(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")

	det, snap, err := detectChapterGroupsForRunWithBooks(context.Background(), s, chapterGroupParams{MinFiles: 2, MaxPerFileDuration: 600})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	idx := newRepointIndex(snap, det)
	none := func(*database.BookCore) string { return "" }
	job := &repointVersionPrimaryJob{}

	// Control: it qualifies before the row is doctored.
	if d := job.classify(s, idx, none, imported[0].ID, nil, false); d.Bucket != bucketWouldRepoint {
		t.Fatalf("control did not qualify: %+v", d)
	}
	// A merge loser: countsAsPrimary would not count it, so promoting it leaves
	// the group with ZERO primaries by elect-missing-primaries' reckoning.
	idx.byID[imported[0].ID].MergedIntoBookID = rvpStr("other-book")
	if d := job.classify(s, idx, none, imported[0].ID, nil, false); d.Bucket != bucketNotElectable {
		t.Fatalf("merge loser not refused: %+v", d)
	}
	// A trashed row: same reckoning.
	idx.byID[imported[1].ID].MarkedForDeletion = rvpBool(true)
	if d := job.classify(s, idx, none, imported[1].ID, nil, false); d.Bucket != bucketNotElectable {
		t.Fatalf("trashed member not refused: %+v", d)
	}
	if rvpFlag(t, s, imported[0].ID) != "false" || rvpFlag(t, s, twins[0].ID) != "true" {
		t.Fatalf("classify wrote: imported=%s twin=%s",
			rvpFlag(t, s, imported[0].ID), rvpFlag(t, s, twins[0].ID))
	}
}

// TestRepointVersionPrimary_TwinWithoutFlagOrInARunRefused: a twin that holds no
// explicit primary flag has nothing to move (electing one belongs to
// reconcile's elect-missing-primaries), and a twin that is itself in a chapter
// run would only have the blocker moved onto it — either flip could end with two
// primaries or none, so both are refused.
func TestRepointVersionPrimary_TwinWithoutFlagOrInARunRefused(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")

	// twins[0]: no explicit flag at all (nil).
	if _, err := s.ModifyBook(twins[0].ID, func(b *database.Book) error {
		b.IsPrimaryVersion = nil
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	res := rvpRun(t, s, `{"apply":true}`, false)
	if res.Counts[bucketTwinNotPrimary] != 1 {
		t.Fatalf("twin with a nil flag not refused: %+v", res.Counts)
	}
	if rvpFlag(t, s, imported[0].ID) != "false" {
		t.Fatalf("member of the nil-flag pair was promoted anyway")
	}

	// A second run whose twins are themselves a chapter run: both sides in runs.
	s2 := ddRealStore(t)
	im2, _ := rvpSeedRun(t, s2, "/lib/imported/Wyrm", "Wyrm", 3, "wg")
	// Re-home each twin into ONE shared directory with sequence titles so the
	// detector groups them too.
	for i := 1; i <= 3; i++ {
		gid := fmt.Sprintf("wg-%d", i)
		books, err := s2.GetAllBooksCore(0, 0)
		if err != nil {
			t.Fatalf("GetAllBooksCore: %v", err)
		}
		for _, b := range books {
			if strPtr(b.VersionGroupID) != gid || strPtr(b.LibraryState) != "organized" {
				continue
			}
			p := fmt.Sprintf("/lib/org/Wyrm/Wyrm - %d.mp3", i)
			if _, err := s2.ModifyBook(b.ID, func(x *database.Book) error {
				x.FilePath, x.Title = p, fmt.Sprint(i)
				return nil
			}); err != nil {
				t.Fatalf("ModifyBook: %v", err)
			}
		}
	}
	res2 := rvpRun(t, s2, `{"apply":true}`, false)
	if res2.Counts[bucketTwinInChapterRun] != 3 {
		t.Fatalf("twins that are themselves a chapter run not refused: %+v", res2.Counts)
	}
	if rvpFlag(t, s2, im2[0].ID) != "false" {
		t.Fatalf("a both-sides-in-a-run pair was written")
	}
}

// TestRepointVersionPrimary_NonChapterRunPairLeftAlone: a non-primary book that
// is not a member of any detected chapter run is never considered. "Member of a
// chapter run" comes from the SAME detection the consolidator uses, not from a
// parallel heuristic here.
func TestRepointVersionPrimary_NonChapterRunPairLeftAlone(t *testing.T) {
	s := ddRealStore(t)
	// One lone imported non-primary book with an organized primary twin — a
	// perfectly ordinary duplicate pair, no sequence run anywhere.
	im, tw := rvpSeedPair(t, s, "/lib/imported/Lonely", "Lonely", 1, "vg-lonely")
	if _, err := s.ModifyBook(im.ID, func(b *database.Book) error {
		b.Title = "A Lonely Book"
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}

	res := rvpRun(t, s, `{"apply":true}`, false)
	if res.GroupsSoleNonPrimary != 0 || len(res.Groups) != 0 {
		t.Fatalf("non-run pair was considered: %+v", res)
	}
	if rvpFlag(t, s, im.ID) != "false" || rvpFlag(t, s, tw.ID) != "true" {
		t.Fatalf("non-run pair was written: %s / %s", rvpFlag(t, s, im.ID), rvpFlag(t, s, tw.ID))
	}
}

// TestRepointVersionPrimary_ExcludesManualOnlyAndITunes.
//
// The manual-only side goes through the whole job: applygate.IsOwnerManualOnly
// reads the path, so a Doctor Who run is excluded by the SHARED detection hook
// (chapterExcluder) before this job sees it at all — the strongest form of
// "excluded", and the assertion is that nothing is proposed.
//
// The iTunes side is asserted on classify directly with a stub protector,
// because the real one reads config.Snapshot().ITunes. This is the check that
// matters here: the twin is a lone single-chapter record BELOW min_files, so
// detection never saw it and the detector's own protector never looked at it.
func TestRepointVersionPrimary_ExcludesManualOnlyAndITunes(t *testing.T) {
	s := ddRealStore(t)
	rvpSeedRun(t, s, "/lib/imported/Doctor Who/Series 5", "Doctor Who", 3, "dw")

	res := rvpRun(t, s, `{}`, true)
	if res.GroupsSoleNonPrimary != 0 || res.Counts[bucketWouldRepoint] != 0 {
		t.Fatalf("owner-manual-only run was proposed: %+v", res)
	}

	// iTunes: the twin is protected, so the pair is refused even though the
	// detector's own protector never inspected it.
	s2 := ddRealStore(t)
	im, tw := rvpSeedRun(t, s2, "/lib/imported/Saga", "Saga", 3, "vg")
	books, err := s2.GetAllBooksCore(0, 0)
	if err != nil {
		t.Fatalf("GetAllBooksCore: %v", err)
	}
	det, snap, err := detectChapterGroupsForRunWithBooks(context.Background(), s2, chapterGroupParams{MinFiles: 2, MaxPerFileDuration: 600})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	_ = books
	idx := newRepointIndex(snap, det)
	protect := func(b *database.BookCore) string {
		if b.ID == tw[0].ID {
			return "in the active iTunes library"
		}
		return ""
	}
	d := (&repointVersionPrimaryJob{}).classify(s2, idx, protect, im[0].ID, nil, false)
	if d.Bucket != bucketITunesProtected {
		t.Fatalf("iTunes-protected twin not refused: %+v", d)
	}
	// And with no protection the same member qualifies, so the assertion above
	// is about the protector and not about some unrelated rejection.
	d = (&repointVersionPrimaryJob{}).classify(s2, idx, func(*database.BookCore) string { return "" }, im[0].ID, nil, false)
	if d.Bucket != bucketWouldRepoint {
		t.Fatalf("unprotected control did not qualify: %+v", d)
	}
}

// TestRepointVersionPrimary_StateMismatchIsNamed: a zero-candidate run must say
// WHY it found nothing. A twin that is not `organized` (the scanner reverts
// library_state on every rescan — PR #3097) lands in its own bucket with the
// observed value in the reason, not in a silent catch-all.
func TestRepointVersionPrimary_StateMismatchIsNamed(t *testing.T) {
	s := ddRealStore(t)
	_, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
	for _, tw := range twins {
		if _, err := s.ModifyBook(tw.ID, func(b *database.Book) error {
			b.LibraryState = rvpStr("imported")
			return nil
		}); err != nil {
			t.Fatalf("ModifyBook: %v", err)
		}
	}
	res := rvpRun(t, s, `{"apply":true}`, false)
	if res.Counts[bucketStateMismatch] != 3 || res.Counts[bucketRepointed] != 0 {
		t.Fatalf("state mismatch not named: %+v", res.Counts)
	}
	if !strings.Contains(res.Groups[0].Pairs[0].Reason, "organized") {
		t.Fatalf("reason does not name the wanted state: %q", res.Groups[0].Pairs[0].Reason)
	}
}

// TestRepointVersionPrimary_GroupIDsTouchOnlyThose.
func TestRepointVersionPrimary_GroupIDsTouchOnlyThose(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")

	res := rvpRun(t, s, `{"apply":true,"group_ids":["vg-2"]}`, false)
	if res.Counts[bucketRepointed] != 1 || res.Counts[bucketNotSelected] != 2 {
		t.Fatalf("group_ids run touched the wrong set: %+v", res.Counts)
	}
	for i := range imported {
		want := "false"
		if rvpGroupID(t, s, imported[i].ID) == "vg-2" {
			want = "true"
		}
		if got := rvpFlag(t, s, imported[i].ID); got != want {
			t.Fatalf("imported %s flag=%s want=%s", imported[i].ID, got, want)
		}
		wantTwin := "true"
		if want == "true" {
			wantTwin = "false"
		}
		if got := rvpFlag(t, s, twins[i].ID); got != wantTwin {
			t.Fatalf("twin %s flag=%s want=%s", twins[i].ID, got, wantTwin)
		}
	}

	// The camelCase alias is accepted; both spellings disagreeing is an error.
	if _, err := parseRepointVersionPrimaryParams(json.RawMessage(`{"groupIds":["a"]}`)); err != nil {
		t.Fatalf("groupIds alias rejected: %v", err)
	}
	if _, err := parseRepointVersionPrimaryParams(json.RawMessage(`{"group_ids":["a"],"groupIds":["b"]}`)); err == nil {
		t.Fatal("group_ids and groupIds disagreeing was accepted")
	}
}

// TestRepointVersionPrimary_PartiallyRepointedGroupStaysInScope: a group does
// NOT leave the population because some of its members have already been
// repointed. Requiring EVERY member to be non-primary would strand any group
// with one drifted or state-mismatched member — still blocked from
// consolidation, and now invisible to the op that exists to unblock it — and
// would also miss the 10 "K of N" groups the 2026-09-19 measurement found.
func TestRepointVersionPrimary_PartiallyRepointedGroupStaysInScope(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
	// Member 0's twin is not organized, so that pair cannot repoint this run.
	if _, err := s.ModifyBook(twins[0].ID, func(b *database.Book) error {
		b.LibraryState = rvpStr("imported")
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}

	first := rvpRun(t, s, `{"apply":true}`, false)
	if first.Counts[bucketRepointed] != 2 || first.Counts[bucketStateMismatch] != 1 {
		t.Fatalf("first pass: %+v", first.Counts)
	}

	// The group is now MIXED. It must still be in scope.
	second := rvpRun(t, s, `{"apply":true}`, false)
	if second.GroupsSoleNonPrimary != 1 {
		t.Fatalf("partially repointed group left the population: sole=%d other=%d",
			second.GroupsSoleNonPrimary, second.GroupsOtherBlockers)
	}
	if second.Counts[bucketStateMismatch] != 1 || second.Counts[bucketAlreadyPrimary] != 2 {
		t.Fatalf("second pass buckets: %+v", second.Counts)
	}

	// Repair the twin and the last member repoints.
	if _, err := s.ModifyBook(twins[0].ID, func(b *database.Book) error {
		b.LibraryState = rvpStr("organized")
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	third := rvpRun(t, s, `{"apply":true}`, false)
	if third.Counts[bucketRepointed] != 1 {
		t.Fatalf("third pass did not finish the group: %+v", third.Counts)
	}
	if rvpFlag(t, s, imported[0].ID) != "true" || rvpFlag(t, s, twins[0].ID) != "false" {
		t.Fatalf("last pair not repointed: %s / %s",
			rvpFlag(t, s, imported[0].ID), rvpFlag(t, s, twins[0].ID))
	}
}

// rvpPrimaryCount counts the version group's primaries BOTH ways, because the
// two helpers disagree about nil: effective (nil = primary) and strict
// (elect_primaries' countsAsPrimary, nil = not primary). A correct outcome is
// exactly one by each.
func rvpPrimaryCount(t *testing.T, s *database.PebbleStore, gid string) (effective, strict int) {
	t.Helper()
	books, err := s.GetAllBooksCore(0, 0)
	if err != nil {
		t.Fatalf("GetAllBooksCore: %v", err)
	}
	for i := range books {
		b := &books[i]
		if strPtr(b.VersionGroupID) != gid || b.IsSoftDeleted() {
			continue
		}
		if database.EffectiveIsPrimaryVersion(b.IsPrimaryVersion) {
			effective++
		}
		if b.IsPrimaryVersion != nil && *b.IsPrimaryVersion && repointElectable(b) {
			strict++
		}
	}
	return effective, strict
}

// rvpTwinStore subverts the TWIN's write the way a concurrent writer would:
// ErrSkipBookWrite territory (already demoted / left the group) or a row that no
// longer exists. Both return (row-or-nil, nil) from ModifyBook — NOT an error —
// so a recovery path that cannot tell "skipped" from "failed" reverts a
// promotion that should have stood.
type rvpTwinStore struct {
	maintenance.JobStore
	inner  *database.PebbleStore
	twinID string
	// before runs against the twin just before the job's ModifyBook reaches it.
	before func(*database.Book)
	// deleteTwin makes the twin's row vanish instead.
	deleteTwin bool
	done       bool
}

func (f *rvpTwinStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return f.inner.ListActiveOperationsV2()
}

func (f *rvpTwinStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == f.twinID && !f.done {
		f.done = true
		if f.deleteTwin {
			return nil, nil // the row is gone: ModifyBook's (nil, nil)
		}
		if _, err := f.inner.ModifyBook(id, func(b *database.Book) error { f.before(b); return nil }); err != nil {
			return nil, err
		}
	}
	return f.inner.ModifyBook(id, fn)
}

// TestRepointVersionPrimary_SkippedDemoteIsNotAFailedDemote is the regression
// for the revert path. ModifyBook answers "wrote", "skipped" and "no such row"
// all with a nil error. In the skip cases the target state ALREADY holds —
// the promoted member is the group's one primary — and reverting there would
// leave the group with ZERO primaries, hiding both books from the library list
// and from ABS. The revert must fire only on a genuine error.
func TestRepointVersionPrimary_SkippedDemoteIsNotAFailedDemote(t *testing.T) {
	fls := false
	cases := []struct {
		name   string
		before func(*database.Book)
		gone   bool
	}{
		{"twin already demoted", func(b *database.Book) { b.IsPrimaryVersion = &fls }, false},
		{"twin left the version group", func(b *database.Book) { b.VersionGroupID = rvpStr("vg-elsewhere") }, false},
		{"twin row is gone", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := ddRealStore(t)
			imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 2, "vg")
			fs := &rvpTwinStore{JobStore: s, inner: s, twinID: twins[0].ID, before: tc.before, deleteTwin: tc.gone}

			res := rvpRun(t, fs, `{"apply":true}`, false)
			if res.Counts[bucketRepointed] != 2 {
				t.Fatalf("a SKIPPED demote was treated as a failure: %+v", res.Counts)
			}
			// The promotion must STAND: reverting it would empty the group.
			if got := rvpFlag(t, s, imported[0].ID); got != "true" {
				t.Fatalf("promotion was reverted over a skipped demote: flag=%s", got)
			}
			eff, strict := rvpPrimaryCount(t, s, "vg-1")
			if eff != 1 || strict != 1 {
				t.Fatalf("version group vg-1 has effective=%d strict=%d primaries, want 1/1", eff, strict)
			}
		})
	}
}

// TestRepointVersionPrimary_TwinFlagWentNilIsReportedNotReverted: a twin whose
// flag became nil reads as primary to EffectiveIsPrimaryVersion, so with the
// member explicit true the group has TWO effective primaries. Reverting would
// swap that for zero by countsAsPrimary. Both are hand fixes; only one hides the
// books, so this is reported and the run does not complete green.
func TestRepointVersionPrimary_TwinFlagWentNilIsReportedNotReverted(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 2, "vg")
	fs := &rvpTwinStore{JobStore: s, inner: s, twinID: twins[0].ID,
		before: func(b *database.Book) { b.IsPrimaryVersion = nil }}

	res, err := rvpTryRun(t, fs, `{"apply":true}`, false)
	if err == nil {
		t.Fatal("a pair left with an ambiguous twin completed green")
	}
	if res.Counts[bucketLeftDoublePrimary] != 1 {
		t.Fatalf("ambiguous twin not reported: %+v", res.Counts)
	}
	if got := rvpFlag(t, s, imported[0].ID); got != "true" {
		t.Fatalf("promotion was reverted into a zero-primary group: flag=%s", got)
	}
}

// TestRepointVersionPrimary_AlreadyPromotedMemberStillDemotesItsTwin: if the
// member is already explicitly true when the write lands, returning early would
// leave TWO primaries — and a re-run could not heal it, because classify stops
// such a member at already_primary and never reaches applyPair. So the demote
// still runs.
func TestRepointVersionPrimary_AlreadyPromotedMemberStillDemotesItsTwin(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 2, "vg")
	tru := true
	fs := &rvpTwinStore{JobStore: s, inner: s, twinID: imported[0].ID,
		before: func(b *database.Book) { b.IsPrimaryVersion = &tru }}

	res := rvpRun(t, fs, `{"apply":true}`, false)
	if res.Counts[bucketRepointed] != 2 {
		t.Fatalf("already-promoted member did not finish its pair: %+v", res.Counts)
	}
	if rvpFlag(t, s, twins[0].ID) != "false" {
		t.Fatalf("twin left primary beside an already-promoted member: flag=%s", rvpFlag(t, s, twins[0].ID))
	}
	eff, strict := rvpPrimaryCount(t, s, "vg-1")
	if eff != 1 || strict != 1 {
		t.Fatalf("version group vg-1 has effective=%d strict=%d primaries, want 1/1", eff, strict)
	}
}

// TestRepointVersionPrimary_TwinDurationMustCorroborate: nothing else in the
// predicate distinguishes a copy of THIS CHAPTER from a complete single-file
// book that happens to be version-linked to one chapter. Demoting the latter
// would hide a whole book while a lone chapter became primary.
func TestRepointVersionPrimary_TwinDurationMustCorroborate(t *testing.T) {
	s := ddRealStore(t)
	_, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
	// twins[0] is really a whole book; twins[1] never had its duration measured.
	if _, err := s.ModifyBook(twins[0].ID, func(b *database.Book) error {
		b.Duration = chIntP(1200 * 12)
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	if _, err := s.ModifyBook(twins[1].ID, func(b *database.Book) error {
		b.Duration = nil
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}

	res := rvpRun(t, s, `{"apply":true}`, false)
	if res.Counts[bucketDurationMismatch] != 2 || res.Counts[bucketRepointed] != 1 {
		t.Fatalf("duration gate: %+v", res.Counts)
	}
	if rvpFlag(t, s, twins[0].ID) != "true" || rvpFlag(t, s, twins[1].ID) != "true" {
		t.Fatalf("an uncorroborated twin was demoted")
	}
	// A couple of seconds of rounding slack still qualifies.
	if _, err := s.ModifyBook(twins[0].ID, func(b *database.Book) error {
		b.Duration = chIntP(1201)
		return nil
	}); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	again := rvpRun(t, s, `{"apply":true}`, false)
	if again.Counts[bucketRepointed] != 1 || again.Counts[bucketDurationMismatch] != 1 {
		t.Fatalf("1 s of rounding slack was refused: %+v", again.Counts)
	}
}

// rvpBlindStore is a JobStore that cannot answer "is a library.scan running?".
type rvpBlindStore struct{ maintenance.JobStore }

// TestRepointVersionPrimary_RefusesWithoutScanProof: the guard fails CLOSED. A
// scan is what reverts library_state organized->imported (PR #3097), the very
// field this job's predicate keys on, so "cannot tell" must mean "do not write".
// A dry run is unaffected — it writes nothing.
func TestRepointVersionPrimary_RefusesWithoutScanProof(t *testing.T) {
	s := ddRealStore(t)
	imported, _ := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 2, "vg")
	blind := &rvpBlindStore{JobStore: s}

	if _, err := rvpTryRun(t, blind, `{"apply":true}`, false); err == nil ||
		!strings.Contains(err.Error(), "library.scan") {
		t.Fatalf("apply without scan proof was allowed: %v", err)
	}
	if got := rvpFlag(t, s, imported[0].ID); got != "false" {
		t.Fatalf("refused run still wrote: flag=%s", got)
	}
	if dry := rvpRun(t, blind, `{}`, true); dry.Counts[bucketWouldRepoint] != 2 {
		t.Fatalf("dry run should not need scan proof: %+v", dry.Counts)
	}
}

// rvpFailingStore fails or subverts ModifyBook for one book id, so the
// half-written-pair paths can be exercised against a real store.
type rvpFailingStore struct {
	maintenance.JobStore
	inner   *database.PebbleStore
	failID  string
	failErr error
	driftID string
	drifted bool
	// blockSecondCallTo fails the SECOND ModifyBook for that id — the
	// compensating revert, which is the only second write this job makes to a
	// promoted row.
	blockSecondCallTo string
	calls             map[string]int
}

// ListActiveOperationsV2 must be forwarded explicitly: maintenance.JobStore does
// not declare it, so a wrapper that only embeds the interface loses it and the
// scan guard refuses the write. That is the guard failing CLOSED, and
// TestRepointVersionPrimary_RefusesWithoutScanProof pins it.
func (f *rvpFailingStore) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return f.inner.ListActiveOperationsV2()
}

func (f *rvpFailingStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[id]++
	if f.failID != "" && id == f.failID {
		return nil, f.failErr
	}
	if id == f.blockSecondCallTo && f.calls[id] > 1 {
		return nil, fmt.Errorf("revert refused by the test")
	}
	if f.driftID != "" && id == f.driftID && !f.drifted {
		f.drifted = true
		// Something else committed between detection and this write.
		if _, err := f.inner.ModifyBook(id, func(b *database.Book) error {
			b.IsPrimaryVersion = nil
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return f.inner.ModifyBook(id, fn)
}

// TestRepointVersionPrimary_RowChangedUnderneathIsSkipped: ModifyBook re-checks
// the precondition on the row it re-read under the write lock and returns
// ErrSkipBookWrite when it no longer holds, so a row that changed underneath is
// skipped and REPORTED, never clobbered.
func TestRepointVersionPrimary_RowChangedUnderneathIsSkipped(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
	fs := &rvpFailingStore{JobStore: s, inner: s, driftID: imported[0].ID}

	res := rvpRun(t, fs, `{"apply":true}`, false)
	if res.Counts[bucketDrifted] != 1 || res.Counts[bucketRepointed] != 2 {
		t.Fatalf("drift not skipped-and-reported: %+v", res.Counts)
	}
	// The drifted row keeps the value the other writer left, and its twin keeps
	// its primary flag: the pair is untouched, not half-written.
	if got := rvpFlag(t, s, imported[0].ID); got != "nil" {
		t.Fatalf("drifted row clobbered: flag=%s", got)
	}
	if got := rvpFlag(t, s, twins[0].ID); got != "true" {
		t.Fatalf("twin of the drifted row was demoted: flag=%s", got)
	}
}

// TestRepointVersionPrimary_FailedDemoteRevertsOrReportsDoublePrimary pins the
// write ordering: promote first, demote second, and if the demote does not land
// the promotion is reverted. If the revert ALSO fails the pair is reported as
// left_double_primary — with both ids and the version group id — and the op
// does not complete green.
func TestRepointVersionPrimary_FailedDemoteRevertsOrReportsDoublePrimary(t *testing.T) {
	s := ddRealStore(t)
	imported, twins := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 2, "vg")
	boom := fmt.Errorf("demote exploded")

	// Revert succeeds: the pair ends exactly where it started, and the other
	// pair in the same run still repoints.
	fs := &rvpFailingStore{JobStore: s, inner: s, failID: twins[0].ID, failErr: boom}
	res := rvpRun(t, fs, `{"apply":true}`, false)
	if res.Counts[bucketDrifted] != 1 || res.Counts[bucketRepointed] != 1 {
		t.Fatalf("failed demote with a good revert: %+v", res.Counts)
	}
	if rvpFlag(t, s, imported[0].ID) != "false" || rvpFlag(t, s, twins[0].ID) != "true" {
		t.Fatalf("revert did not restore the pair: %s / %s",
			rvpFlag(t, s, imported[0].ID), rvpFlag(t, s, twins[0].ID))
	}

	// Revert also fails: reported as left_double_primary AND the run errors.
	// A fresh store, because the run above already repointed one pair and a run
	// with an effective-primary member is no longer sole-non-primary-blocked.
	s2 := ddRealStore(t)
	im2, tw2 := rvpSeedRun(t, s2, "/lib/imported/Saga", "Saga", 2, "vg")
	fs2 := &rvpFailingStore{JobStore: s2, inner: s2, failID: tw2[0].ID, failErr: boom, blockSecondCallTo: im2[0].ID}
	res2, err := rvpTryRun(t, fs2, `{"apply":true}`, false)
	if err == nil {
		t.Fatal("a pair left double-primary completed green")
	}
	if res2.Counts[bucketLeftDoublePrimary] != 1 {
		t.Fatalf("double primary not reported: %+v", res2.Counts)
	}
	p := res2.Groups[0].Pairs[0]
	if p.PromoteBookID == "" || p.DemoteBookID == "" || p.VersionGroupID == "" ||
		p.PromotePriorFlag != "false" || p.DemotePriorFlag != "true" {
		t.Fatalf("double-primary record is not hand-fixable: %+v", p)
	}
}
