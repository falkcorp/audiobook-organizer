// file: internal/maintenance/jobs/chapter_groups_test.go
// version: 1.6.0
// guid: 24b634b3-fd8d-4f7f-8809-0843e63141c8
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
)

// chRun runs job against store with the given raw params and returns the
// structured result the job persisted.
func chRun(t *testing.T, job maintenance.MaintenanceJob, store maintenance.JobStore, params string, dryRun bool) chapterGroupsResult {
	t.Helper()
	var got any
	ctx := maintenance.WithOperationID(context.Background(), "op-chapter-test")
	ctx = maintenance.WithRawParams(ctx, json.RawMessage(params))
	ctx = maintenance.WithResultSetter(ctx, func(v any) error { got = v; return nil })
	if err := job.Run(ctx, store, ddJobReporter{}, dryRun); err != nil {
		t.Fatalf("%s Run: %v", job.ID(), err)
	}
	if got == nil {
		t.Fatalf("%s persisted no structured result", job.ID())
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var res chapterGroupsResult
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	return res
}

func chIntP(v int) *int { return &v }

// chRunRaw runs job and returns the persisted result as raw JSON, or the error.
func chRunRaw(t *testing.T, job maintenance.MaintenanceJob, store maintenance.JobStore, params string, dryRun bool) ([]byte, error) {
	t.Helper()
	var got any
	ctx := maintenance.WithOperationID(context.Background(), "op-chapter-test")
	ctx = maintenance.WithRawParams(ctx, json.RawMessage(params))
	ctx = maintenance.WithResultSetter(ctx, func(v any) error { got = v; return nil })
	if err := job.Run(ctx, store, ddJobReporter{}, dryRun); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return raw, nil
}

// chReviewedParams turns a dry-run preview (raw JSON) into the params of a
// real merge of exactly its would_merge groups -- what the card sends.
func chReviewedParams(t *testing.T, preview []byte) string {
	t.Helper()
	var r struct {
		Groups []struct {
			PrimaryBookID string   `json:"primary_book_id"`
			BookIDs       []string `json:"book_ids"`
			Fingerprint   string   `json:"fingerprint"`
			Status        string   `json:"status"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(preview, &r); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	type sel struct {
		PrimaryBookID string   `json:"primary_book_id"`
		BookIDs       []string `json:"book_ids"`
		Fingerprint   string   `json:"fingerprint"`
	}
	var groups []sel
	for _, g := range r.Groups {
		if g.Status == "would_merge" {
			groups = append(groups, sel{g.PrimaryBookID, g.BookIDs, g.Fingerprint})
		}
	}
	raw, _ := json.Marshal(map[string]any{"dry_run": false, "groups": groups})
	return string(raw)
}

func chPreviewThenApply(t *testing.T, s maintenance.JobStore) chapterGroupsResult {
	t.Helper()
	preview, err := chRunRaw(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	return chRun(t, &mergeChapterGroupsJob{}, s, chReviewedParams(t, preview), false)
}

// chSeedGroup creates n chapter books under dir, each owning one file, created
// in REVERSE chapter order so store order is not chapter order.
func chSeedGroup(t *testing.T, s *database.PebbleStore, dir, title string, n, dur int) []*database.Book {
	t.Helper()
	out := make([]*database.Book, n)
	for i := n; i >= 1; i-- {
		path := dir + "/" + []string{"", "01", "02", "03", "04", "05"}[i] + " - " + title + ".mp3"
		b := ddMustBook(t, s, &database.Book{Title: []string{"", "01", "02", "03", "04", "05"}[i] + " - " + title, FilePath: path, Duration: chIntP(dur)})
		ddMustFile(t, s, &database.BookFile{BookID: b.ID, FilePath: path, Duration: dur})
		out[i-1] = b
	}
	return out
}

func TestScanChapterGroups_StructuredResultAndParams(t *testing.T) {
	s := ddRealStore(t)
	chSeedGroup(t, s, "/lib/A/Tale", "Tale", 3, 300)
	chSeedGroup(t, s, "/lib/B/Saga", "Saga", 2, 900)

	res := chRun(t, &scanChapterGroupsJob{}, s, `{"min_files":3,"max_per_file_duration":600}`, false)
	if res.GroupsFound != 1 || len(res.Groups) != 1 || res.TotalBooksAffected != 3 {
		t.Fatalf("want 1 group of 3 (Saga has 2 < min_files), got %+v", res)
	}
	g := res.Groups[0]
	if g.CommonTitle != "Tale" || g.FileCount != 3 || g.TotalDuration != 900 || len(g.SourceBookIDs) != 2 ||
		g.Confidence == "" || len(g.Reasons) == 0 || len(g.IndexLabels) != 3 {
		t.Fatalf("group summary wrong: %+v", g)
	}

	// max_per_file_duration is advisory: 900 s members still group.
	res = chRun(t, &scanChapterGroupsJob{}, s, `{"min_files":2,"max_per_file_duration":600}`, false)
	if res.GroupsFound != 2 {
		t.Fatalf("min 2: want 2 groups, got %d", res.GroupsFound)
	}
	// path_prefix honoured.
	res = chRun(t, &scanChapterGroupsJob{}, s, `{"path_prefix":"/lib/B"}`, false)
	if res.GroupsFound != 1 || res.Groups[0].CommonTitle != "Saga" {
		t.Fatalf("prefix /lib/B: want only Saga, got %+v", res.Groups)
	}
}

// chSeedBare creates n single-file records titled only by their position
// ("1", "2", ...) in dir, file "<name> - N.mp3", with the corroboration a
// bare-titled run needs: a known chapter-length duration and an author.
func chSeedBare(t *testing.T, s *database.PebbleStore, dir, name string, n int) []*database.Book {
	t.Helper()
	author := 1
	out := make([]*database.Book, n)
	for i := n; i >= 1; i-- {
		path := fmt.Sprintf("%s/%s - %d.mp3", dir, name, i)
		b := ddMustBook(t, s, &database.Book{Title: fmt.Sprint(i), FilePath: path, Duration: chIntP(1200), AuthorID: &author})
		ddMustFile(t, s, &database.BookFile{BookID: b.ID, FilePath: path, Duration: 1200})
		out[i-1] = b
	}
	return out
}

// The owner's shape: records titled "1".."N" with no known duration. The scan
// reports them with the folder's name as the proposed title, a non-primary
// run is reported blocked with the reason, and a reviewed merge replaces the
// bare "1" title and is undoable.
func TestChapterGroups_BareNumberTitlesScanAndMerge(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedBare(t, s, "/lib/A/Eldritch", "Eldritch", 4)
	copies := chSeedBare(t, s, "/lib/B/Wyrm", "Wyrm", 3)
	no := false
	for _, b := range copies {
		if _, err := s.ModifyBook(b.ID, func(x *database.Book) error { x.IsPrimaryVersion = &no; return nil }); err != nil {
			t.Fatalf("ModifyBook: %v", err)
		}
	}

	scan := chRun(t, &scanChapterGroupsJob{}, s, `{}`, false)
	if scan.GroupsFound != 1 || scan.GroupsBlocked != 1 || len(scan.Groups) != 2 {
		t.Fatalf("want 1 mergeable + 1 blocked group, got %+v", scan)
	}
	if g := scan.Groups[0]; g.CommonTitle != "Eldritch" || g.PrimaryBookID != books[0].ID || g.DurationsKnown != 4 {
		t.Fatalf("bare group wrong: %+v", g)
	}
	if g := scan.Groups[1]; g.Status != "blocked" || len(g.Blockers) == 0 || !strings.Contains(g.Blockers[0], "non-primary") {
		t.Fatalf("non-primary run not reported blocked: %+v", g)
	}

	res := chPreviewThenApply(t, s)
	if res.BooksMerged != 3 || res.GroupsFailed != 0 {
		t.Fatalf("merge summary wrong: %+v", res)
	}
	primary := ddMustGet(t, s, books[0].ID)
	if primary.Title != "Eldritch" {
		t.Fatalf("bare primary title not replaced: %q", primary.Title)
	}
	var journal string
	for _, g := range res.Groups {
		if g.PrimaryBookID == books[0].ID {
			journal = g.JournalID
		}
	}
	if journal == "" {
		t.Fatalf("no journal for the merged group: %+v", res.Groups)
	}
	if _, err := merge.NewService(s).UndoCombine(journal); err != nil {
		t.Fatalf("UndoCombine: %v", err)
	}
	if got := ddMustGet(t, s, books[0].ID); got.Title != "1" {
		t.Fatalf("undo did not restore the primary title: %q", got.Title)
	}
}

// A member an earlier merge already made multi-file is not a single-file
// chapter record: the preview blocks the group rather than folding a whole
// book in as a "chapter".
func TestMergeChapterGroups_MultiFileMemberBlocks(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 2, 300)
	ddMustFile(t, s, &database.BookFile{BookID: books[1].ID, FilePath: "/lib/A/Tale/02b - Tale.mp3", Duration: 300})
	res := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if len(res.Groups) != 1 || res.Groups[0].Status != "blocked" || !strings.Contains(strings.Join(res.Groups[0].Blockers, ";"), "has 2 files") {
		t.Fatalf("multi-file member not blocked: %+v", res.Groups)
	}
}

func TestMergeChapterGroups_DryRunWritesNothing(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 3, 300)

	res := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if !res.DryRun || res.GroupsFound != 1 || res.BooksMerged != 2 || res.Groups[0].Status != "would_merge" {
		t.Fatalf("dry run preview wrong: %+v", res)
	}
	if res.Groups[0].PrimaryBookID != books[0].ID {
		t.Fatalf("primary = %s, want chapter 01 %s", res.Groups[0].PrimaryBookID, books[0].ID)
	}
	for _, b := range books {
		got := ddMustGet(t, s, b.ID)
		if got.IsSoftDeleted() || got.MergedIntoBookID != nil || got.Title != b.Title {
			t.Fatalf("dry run mutated book %s: %+v", b.ID, got)
		}
		files, _ := s.GetBookFiles(b.ID)
		if len(files) != 1 {
			t.Fatalf("dry run moved files of %s: %d left", b.ID, len(files))
		}
	}
	if rows, _ := s.GetOperationResults("op-chapter-test"); len(rows) != 0 {
		t.Fatalf("dry run wrote %d audit rows", len(rows))
	}
}

func TestMergeChapterGroups_RealMergeIsCorrectAuditedAndUndoable(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 3, 300)
	// A second group whose primary carries a title someone set that is not
	// just a position (a real title with no position is not a chapter
	// candidate at all; see scanner TestReview_RealTitlesWithNumberedFilesNeverGroup).
	curated := chSeedGroup(t, s, "/lib/B/Saga", "Saga", 2, 300)
	if _, err := s.ModifyBook(curated[0].ID, func(b *database.Book) error { b.Title = "Chapter 1 - Saga"; return nil }); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}

	res := chPreviewThenApply(t, s)
	if res.DryRun || res.GroupsFound != 2 || res.BooksMerged != 3 || res.BooksSkipped != 0 || res.GroupsFailed != 0 {
		t.Fatalf("merge summary wrong: %+v", res)
	}

	// (a) primary is chapter 01 and now owns every file.
	primary := ddMustGet(t, s, books[0].ID)
	files, _ := s.GetBookFiles(primary.ID)
	if len(files) != 3 {
		t.Fatalf("primary (chapter 01) owns %d files, want 3", len(files))
	}
	// (b) filename-derived title replaced; curated title kept.
	if primary.Title != "Tale" {
		t.Fatalf("filename-derived primary title = %q, want %q", primary.Title, "Tale")
	}
	if got := ddMustGet(t, s, curated[0].ID); got.Title != "Chapter 1 - Saga" {
		t.Fatalf("curated title overwritten: %q", got.Title)
	}
	for _, src := range books[1:] {
		if !ddMustGet(t, s, src.ID).IsSoftDeleted() {
			t.Fatalf("source %s not soft-deleted", src.ID)
		}
	}

	// Audit: one row per group, naming primary, sources, moved files, old titles, journal.
	rows, err := s.GetOperationResults("op-chapter-test")
	if err != nil || len(rows) != 2 {
		t.Fatalf("audit rows = %d (%v), want 2", len(rows), err)
	}
	var audit chapterMergeAudit
	for _, r := range rows {
		if r.BookID == books[0].ID {
			if err := json.Unmarshal([]byte(r.ResultJSON), &audit); err != nil {
				t.Fatalf("audit json: %v", err)
			}
		}
	}
	if audit.JournalID == "" || audit.PrimaryTitleBefore != "01 - Tale" || len(audit.SourceBookIDs) != 2 ||
		len(audit.MovedFileIDs[books[1].ID]) != 1 || audit.SourceTitles[books[2].ID] != "03 - Tale" {
		t.Fatalf("audit record incomplete: %+v", audit)
	}

	// (d) a re-run finds nothing: sources are soft-deleted, primary is alone.
	again := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if again.GroupsFound != 0 || again.BooksMerged != 0 {
		t.Fatalf("re-run regrouped merged books: %+v", again)
	}

	// Undo through the existing combine journal restores the sources.
	if _, err := merge.NewService(s).UndoCombine(audit.JournalID); err != nil {
		t.Fatalf("UndoCombine: %v", err)
	}
	for _, src := range books[1:] {
		got := ddMustGet(t, s, src.ID)
		fs, _ := s.GetBookFiles(src.ID)
		if got.IsSoftDeleted() || len(fs) != 1 {
			t.Fatalf("undo did not restore %s (deleted=%v files=%d)", src.ID, got.IsSoftDeleted(), len(fs))
		}
	}
	if got := ddMustGet(t, s, books[0].ID); got.Title != "01 - Tale" {
		t.Fatalf("undo did not restore primary title: %q", got.Title)
	}
}

func TestMergeChapterGroups_AdvertisesDryRunTrue(t *testing.T) {
	raw, _ := json.Marshal((&mergeChapterGroupsJob{}).DefaultParams())
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if p["dry_run"] != true || p["min_files"] == nil || p["max_per_file_duration"] == nil {
		t.Fatalf("DefaultParams = %s", raw)
	}
}

// A real merge without a reviewed group list is refused before any write: it
// must never detect on its own what to merge.
func TestMergeChapterGroups_RealMergeWithoutGroupsIsRefused(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 3, 300)
	if _, err := chRunRaw(t, &mergeChapterGroupsJob{}, s, `{"dry_run":false}`, false); err == nil {
		t.Fatal("a real merge with no groups ran")
	}
	for _, b := range books[1:] {
		if ddMustGet(t, s, b.ID).IsSoftDeleted() {
			t.Fatalf("book %s merged without a reviewed group", b.ID)
		}
	}
	if err := (&mergeChapterGroupsJob{}).ValidateParams(json.RawMessage(`{}`), false); err == nil {
		t.Fatal("ValidateParams accepted a real merge with no groups")
	}
	if err := (&mergeChapterGroupsJob{}).ValidateParams(json.RawMessage(`{}`), true); err != nil {
		t.Fatalf("ValidateParams refused a dry run: %v", err)
	}
}

// The real merge applies exactly the reviewed set: a group whose member changed
// since the preview is skipped as drifted, and a chapter imported after the
// preview is not folded in.
func TestMergeChapterGroups_MergesExactlyTheReviewedSet(t *testing.T) {
	// Nothing outside the reviewed set is merged, and a reviewed set that no
	// longer matches its folder is not merged at all.
	s := ddRealStore(t)
	tale := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 2, 300)
	saga := chSeedGroup(t, s, "/lib/B/Saga", "Saga", 2, 300)
	preview, err := chRunRaw(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	params := chReviewedParams(t, preview)

	// After review: a new chapter lands in Tale, and a Saga member changes.
	late := ddMustBook(t, s, &database.Book{Title: "03 - Tale", FilePath: "/lib/A/Tale/03 - Tale.mp3", Duration: chIntP(300)})
	ddMustFile(t, s, &database.BookFile{BookID: late.ID, FilePath: "/lib/A/Tale/03 - Tale.mp3", Duration: 300})
	if _, err := s.ModifyBook(saga[1].ID, func(b *database.Book) error { b.Title = "02 - Saga (edited)"; return nil }); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}

	// Tale's folder now forms a 3-chapter group, so the reviewed 2-chapter
	// selection is no longer a detected group: merging it would orphan
	// chapter 03. It is refused (selection_mismatch) until previewed again.
	res := chRun(t, &mergeChapterGroupsJob{}, s, params, false)
	if res.BooksMerged != 0 || res.GroupsDrifted != 1 || res.GroupsSelectionMismatch != 1 {
		t.Fatalf("want Tale selection_mismatch and Saga drifted, nothing merged, got %+v", res)
	}
	if ddMustGet(t, s, tale[1].ID).IsSoftDeleted() {
		t.Fatal("a reviewed group whose folder changed was merged without a new preview")
	}
	if ddMustGet(t, s, late.ID).IsSoftDeleted() {
		t.Fatal("a chapter imported after the preview was merged unreviewed")
	}
	if ddMustGet(t, s, saga[1].ID).IsSoftDeleted() {
		t.Fatal("a group that changed since the preview was merged")
	}
}

// Data the merge cannot carry blocks the group instead of being lost at purge.
func TestMergeChapterGroups_UncarriableDataBlocksTheGroup(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 2, 300)
	rating := 4.5
	if _, err := s.ModifyBook(books[1].ID, func(b *database.Book) error { b.UserRatingOverall = &rating; return nil }); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	res := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if len(res.Groups) != 1 || res.Groups[0].Status != "blocked" || len(res.Groups[0].Blockers) == 0 || res.BooksMerged != 0 {
		t.Fatalf("rated source not blocked in preview: %+v", res)
	}
}

// The owner's manual-only libraries are never offered for a bulk merge.
func TestMergeChapterGroups_ExcludesManualOnlyLibraries(t *testing.T) {
	s := ddRealStore(t)
	chSeedGroup(t, s, "/lib/Doctor Who/Story", "Story", 2, 300)
	res := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if res.GroupsFound != 0 || res.BooksExcluded != 2 {
		t.Fatalf("Doctor Who chapters offered for merge: %+v", res)
	}
}

// Source-only metadata is filled onto an empty primary, not a blocker; two
// sources disagreeing on it is a conflict that blocks the group.
func TestMergeChapterGroups_MetadataFillsOrConflictBlocks(t *testing.T) {
	s := ddRealStore(t)
	tale := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 2, 300)
	asin := "B0TALEASIN"
	if _, err := s.ModifyBook(tale[1].ID, func(b *database.Book) error { b.ASIN = &asin; return nil }); err != nil {
		t.Fatalf("ModifyBook: %v", err)
	}
	saga := chSeedGroup(t, s, "/lib/B/Saga", "Saga", 3, 300)
	for i, v := range []string{"Reader One", "Reader Two"} {
		v := v
		if _, err := s.ModifyBook(saga[i+1].ID, func(b *database.Book) error { b.Narrator = &v; return nil }); err != nil {
			t.Fatalf("ModifyBook: %v", err)
		}
	}
	pre := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if pre.GroupsBlocked != 1 || pre.BooksMerged != 1 {
		t.Fatalf("preview: want Saga blocked on a narrator conflict and Tale mergeable, got %+v", pre)
	}
	res := chPreviewThenApply(t, s)
	if res.BooksMerged != 1 || len(res.Groups) != 1 || res.Groups[0].Status != "merged" {
		t.Fatalf("want only Tale merged, got %+v", res)
	}
	p := ddMustGet(t, s, tale[0].ID)
	if p.ASIN == nil || *p.ASIN != asin {
		t.Fatalf("source-only ASIN not filled onto the empty primary: %v", p.ASIN)
	}
	if ddMustGet(t, s, saga[1].ID).IsSoftDeleted() {
		t.Fatal("a group with a metadata conflict was merged")
	}
}

// The fingerprint covers each member's files, not only their count: a file
// swapped or moved on disk after the preview is drift.
func TestMergeChapterGroups_FileChangeAfterPreviewIsDrift(t *testing.T) {
	s := ddRealStore(t)
	tale := chSeedGroup(t, s, "/lib/A/Tale", "Tale", 2, 300)
	preview, err := chRunRaw(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	files, err := s.GetBookFiles(tale[1].ID)
	if err != nil || len(files) != 1 {
		t.Fatalf("GetBookFiles: %v %d", err, len(files))
	}
	f := files[0]
	f.FilePath = "/lib/A/Tale/02 - Tale (replaced).mp3"
	if err := s.UpdateBookFile(f.ID, &f); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
	res := chRun(t, &mergeChapterGroupsJob{}, s, chReviewedParams(t, preview), false)
	if res.GroupsDrifted != 1 || res.BooksMerged != 0 {
		t.Fatalf("a file change after the preview was merged: %+v", res)
	}
}

// chApply sends one hand-built selection for a real merge.
func chApply(t *testing.T, s maintenance.JobStore, sel chapterGroupSelection) chapterGroupsResult {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"dry_run": false, "groups": []chapterGroupSelection{sel}})
	return chRun(t, &mergeChapterGroupsJob{}, s, string(raw), false)
}

// chSelectionFor builds the selection a preview would return for ids, with
// the fingerprint of the members as they are now.
func chSelectionFor(t *testing.T, s maintenance.JobStore, ids []string) chapterGroupSelection {
	t.Helper()
	st, err := readChapterGroup(s, ids)
	if err != nil {
		t.Fatalf("readChapterGroup: %v", err)
	}
	return chapterGroupSelection{PrimaryBookID: ids[0], BookIDs: ids, Fingerprint: chapterFingerprint(st.members)}
}

// The server, not only the card, refuses a LOW-confidence group unless the
// selection carries allow_low_confidence, and refuses a group the detector
// blocks whatever the request says.
func TestMergeChapterGroups_ApplyRefusesLowAndBlockedSelections(t *testing.T) {
	s := ddRealStore(t)
	// Low: a gap (4 missing) plus one unknown duration.
	var low []*database.Book
	for _, i := range []int{6, 5, 3, 2, 1} {
		path := fmt.Sprintf("/lib/A/Tale/%02d - Tale.mp3", i)
		var dur *int
		fileDur := 0
		if i != 3 {
			dur, fileDur = chIntP(300), 300
		}
		b := ddMustBook(t, s, &database.Book{Title: fmt.Sprintf("%02d - Tale", i), FilePath: path, Duration: dur})
		ddMustFile(t, s, &database.BookFile{BookID: b.ID, FilePath: path, Duration: fileDur})
		low = append([]*database.Book{b}, low...)
	}
	preview := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
	if len(preview.Groups) != 1 || preview.Groups[0].Confidence != "low" || preview.Groups[0].Status != "would_merge" {
		t.Fatalf("want one low would_merge group, got %+v", preview.Groups)
	}
	ids := preview.Groups[0].BookIDs
	sel := chSelectionFor(t, s, ids)

	res := chApply(t, s, sel)
	if res.BooksMerged != 0 || res.Groups[0].Status != "blocked" || !strings.Contains(strings.Join(res.Groups[0].Blockers, ";"), "allow_low_confidence") {
		t.Fatalf("low group merged without acknowledgement: %+v", res.Groups)
	}
	if got := ddMustGet(t, s, low[1].ID); got.IsSoftDeleted() {
		t.Fatalf("refused low merge still soft-deleted a source")
	}
	sel.AllowLowConfidence = true
	if res = chApply(t, s, sel); res.BooksMerged != 4 || res.Groups[0].Status != "merged" {
		t.Fatalf("acknowledged low group did not merge: %+v", res.Groups)
	}

	// Blocked: a non-primary run, sent with a valid fingerprint anyway.
	copies := chSeedBare(t, s, "/lib/B/Wyrm", "Wyrm", 3)
	no := false
	cids := make([]string, len(copies))
	for i, b := range copies {
		cids[i] = b.ID
		if _, err := s.ModifyBook(b.ID, func(x *database.Book) error { x.IsPrimaryVersion = &no; return nil }); err != nil {
			t.Fatalf("ModifyBook: %v", err)
		}
	}
	bsel := chSelectionFor(t, s, cids)
	bsel.AllowLowConfidence = true
	res = chApply(t, s, bsel)
	if res.BooksMerged != 0 || res.Groups[0].Status != "blocked" || !strings.Contains(strings.Join(res.Groups[0].Blockers, ";"), "non-primary") {
		t.Fatalf("blocked group merged: %+v", res.Groups)
	}
}

// A SUBSET of a group the detector blocks must never merge: every whole-set
// blocker (mixed containers, duplicate copies, different authors) vanishes
// when the other members are left out of the selection. The merge verifies
// the selection against detection over the members' whole folder and
// refuses anything that is not exactly one detected group.
func TestMergeChapterGroups_SubsetOfBlockedGroupIsRefused(t *testing.T) {
	seed := func(t *testing.T, s *database.PebbleStore, dir string, names []string, exts []string, authors []int) []string {
		var ids []string
		for i, n := range names {
			path := fmt.Sprintf("%s/%s%s", dir, n, exts[i])
			a := authors[i]
			b := ddMustBook(t, s, &database.Book{Title: n, FilePath: path, Duration: chIntP(1500), AuthorID: &a})
			ddMustFile(t, s, &database.BookFile{BookID: b.ID, FilePath: path, Duration: 1500})
			ids = append(ids, b.ID)
		}
		return ids
	}
	cases := map[string]func(t *testing.T, s *database.PebbleStore) []string{
		"mixed containers": func(t *testing.T, s *database.PebbleStore) []string {
			ids := seed(t, s, "/lib/C/Tale", []string{"01 - Tale", "02 - Tale", "03 - Tale", "04 - Tale", "05 - Tale"},
				[]string{".mp3", ".mp3", ".mp3", ".m4a", ".m4a"}, []int{1, 1, 1, 1, 1})
			return ids[:3]
		},
		"two copies (duplicate positions)": func(t *testing.T, s *database.PebbleStore) []string {
			ids := seed(t, s, "/lib/C/Dup", []string{"Chapter 1", "Chapter 2", "Chapter 3"}, []string{".mp3", ".mp3", ".mp3"}, []int{1, 1, 1})
			seed(t, s, "/lib/C/Dup", []string{"01 - Chapter 1", "02 - Chapter 2", "03 - Chapter 3"}, []string{".mp3", ".mp3", ".mp3"}, []int{1, 1, 1})
			return ids
		},
		"two authors": func(t *testing.T, s *database.PebbleStore) []string {
			ids := seed(t, s, "/lib/C/Stories", []string{"Chapter 1", "Chapter 2", "Chapter 3", "Chapter 4"},
				[]string{".mp3", ".mp3", ".mp3", ".mp3"}, []int{1, 1, 2, 2})
			return ids[:2]
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			s := ddRealStore(t)
			sub := mk(t, s)
			sel := chSelectionFor(t, s, sub)
			sel.AllowLowConfidence = true
			res := chApply(t, s, sel)
			if res.BooksMerged != 0 || res.Groups[0].Status == "merged" || res.Groups[0].Status == "partial" {
				t.Fatalf("subset of a blocked folder merged: %+v", res.Groups)
			}
			if st := res.Groups[0].Status; st != "selection_mismatch" && st != "blocked" {
				t.Fatalf("want selection_mismatch/blocked, got %q (%+v)", st, res.Groups[0])
			}
			for _, id := range sub[1:] {
				if ddMustGet(t, s, id).IsSoftDeleted() {
					t.Fatalf("refused subset still soft-deleted %s", id)
				}
			}
		})
	}
}

// A sibling that lands in the folder after the run's folder index was read
// (here: between the pre-lock verification and the merge lock) is seen by
// the re-verification under the lock, which re-lists the folder.
func TestMergeChapterGroups_LateSiblingIsSeenUnderTheLock(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/L/Tale", "Tale", 3, 300)
	ids := []string{books[0].ID, books[1].ID, books[2].ID}
	sel := chSelectionFor(t, s, ids)
	chapterPrecheckTestHook = func() {
		late := ddMustBook(t, s, &database.Book{Title: "01 - Tale", FilePath: "/lib/L/Tale/01 - Tale.m4a", Duration: chIntP(300)})
		ddMustFile(t, s, &database.BookFile{BookID: late.ID, FilePath: "/lib/L/Tale/01 - Tale.m4a", Duration: 300})
	}
	t.Cleanup(func() { chapterPrecheckTestHook = nil })
	res := chApply(t, s, sel)
	if res.BooksMerged != 0 || res.Groups[0].Status == "merged" {
		t.Fatalf("a copy that arrived before the lock was not seen: %+v", res.Groups)
	}
	if ddMustGet(t, s, books[1].ID).IsSoftDeleted() {
		t.Fatal("refused merge soft-deleted a member")
	}
}

// The merge never runs alongside a library scan: it shares library.scan's
// ConcurrencyKey (the registry serializes the two), and a real apply refuses
// to start while a library.scan is running.
func TestMergeChapterGroups_NeverAppliesDuringALibraryScan(t *testing.T) {
	if got := (&mergeChapterGroupsJob{}).Policy().ConcurrencyKey; got != "library.scan" {
		t.Fatalf("merge-chapter-groups ConcurrencyKey = %q, want library.scan", got)
	}
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/S/Tale", "Tale", 2, 300)
	sel := chSelectionFor(t, s, []string{books[0].ID, books[1].ID})
	if err := s.InsertOperationV2(database.OperationV2Row{ID: "op-scan", DefID: "library.scan", Plugin: "library", Status: "queued"}); err != nil {
		t.Fatalf("InsertOperationV2: %v", err)
	}
	now := time.Now()
	if err := s.UpdateOperationV2Status("op-scan", "running", &now, nil, nil); err != nil {
		t.Fatalf("start the scan row: %v", err)
	}
	raw, _ := json.Marshal(map[string]any{"dry_run": false, "groups": []chapterGroupSelection{sel}})
	if _, err := chRunRaw(t, &mergeChapterGroupsJob{}, s, string(raw), false); err == nil || !strings.Contains(err.Error(), "library.scan") {
		t.Fatalf("real merge ran during a library.scan (err=%v)", err)
	}
	if ddMustGet(t, s, books[1].ID).IsSoftDeleted() {
		t.Fatal("merged during a library.scan")
	}
}

// One disc folder of a multi-disc book is refused at apply: verification
// brings the sibling disc folders into its context, so the multi-disc
// blocker the preview reported holds for a hand-built selection too.
func TestMergeChapterGroups_OneDiscOfAMultiDiscBookIsRefused(t *testing.T) {
	for name, folders := range map[string][2]string{
		"Disc N":         {"Disc 1", "Disc 2"},
		"Title - CDn":    {"Dunes - CD1", "Dunes - CD2"},
		"Title (Disc n)": {"Dunes (Disc 1)", "Dunes (Disc 2)"},
	} {
		t.Run(name, func(t *testing.T) {
			s := ddRealStore(t)
			disc1 := chSeedGroup(t, s, "/lib/H/Dunes/"+folders[0], "Dunes", 4, 1500)
			chSeedGroup(t, s, "/lib/H/Dunes/"+folders[1], "Dunes", 4, 1500)
			ids := make([]string, len(disc1))
			for i, b := range disc1 {
				ids[i] = b.ID
			}
			sel := chSelectionFor(t, s, ids)
			sel.AllowLowConfidence = true
			res := chApply(t, s, sel)
			if res.BooksMerged != 0 || res.Groups[0].Status != "blocked" || !strings.Contains(strings.Join(res.Groups[0].Blockers, ";"), "multi-disc") {
				t.Fatalf("one disc of a multi-disc book merged: %+v", res.Groups)
			}
		})
	}
}

// chCountingStore counts whole-library loads.
type chCountingStore struct {
	*database.PebbleStore
	fullLoads int
}

func (c *chCountingStore) GetAllBooksCore(limit, offset int) ([]database.BookCore, error) {
	c.fullLoads++
	return c.PebbleStore.GetAllBooksCore(limit, offset)
}

// A real merge never loads the whole library: verification (before and
// under the process-wide merge lock) re-lists only the members' folder
// through the book_atpath index. Loading every book once per group under
// that lock blocked every dedup and UI merge for the whole run.
func TestMergeChapterGroups_ApplyDoesNotLoadTheLibrary(t *testing.T) {
	s := ddRealStore(t)
	var sels []chapterGroupSelection
	for _, d := range []string{"/lib/P/A", "/lib/P/B", "/lib/P/C"} {
		books := chSeedGroup(t, s, d, "Tale", 3, 300)
		sels = append(sels, chSelectionFor(t, s, []string{books[0].ID, books[1].ID, books[2].ID}))
	}
	cs := &chCountingStore{PebbleStore: s}
	raw, _ := json.Marshal(map[string]any{"dry_run": false, "groups": sels})
	res := chRun(t, &mergeChapterGroupsJob{}, cs, string(raw), false)
	if res.BooksMerged != 6 {
		t.Fatalf("want 3 groups merged (6 sources), got %+v", res)
	}
	if cs.fullLoads != 0 {
		t.Fatalf("a real merge of 3 groups loaded the whole library %d times", cs.fullLoads)
	}
}

// A ZOMBIE library.scan row -- "running" in the store, but with no progress
// for far longer than the registry watchdog lets a live run go quiet -- does
// not block merges forever. The registry itself skips such rows (no live
// handle); a live scan is still refused (NeverAppliesDuringALibraryScan).
func TestMergeChapterGroups_ZombieScanRowDoesNotBlock(t *testing.T) {
	s := ddRealStore(t)
	books := chSeedGroup(t, s, "/lib/Z/Tale", "Tale", 2, 300)
	sel := chSelectionFor(t, s, []string{books[0].ID, books[1].ID})
	if err := s.InsertOperationV2(database.OperationV2Row{ID: "op-zombie", DefID: "library.scan", Plugin: "library", Status: "queued"}); err != nil {
		t.Fatalf("InsertOperationV2: %v", err)
	}
	long := time.Now().Add(-6 * time.Hour)
	if err := s.UpdateOperationV2Status("op-zombie", "running", &long, nil, nil); err != nil {
		t.Fatalf("start the zombie row: %v", err)
	}
	res := chApply(t, s, sel)
	if res.BooksMerged != 1 {
		t.Fatalf("a zombie scan row blocked the merge: %+v", res.Groups)
	}
}
