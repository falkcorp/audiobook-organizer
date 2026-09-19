// file: internal/maintenance/jobs/chapter_groups_test.go
// version: 1.2.0
// guid: 24b634b3-fd8d-4f7f-8809-0843e63141c8
// last-edited: 2026-09-19

package jobs

import (
	"context"
	"encoding/json"
	"testing"

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
		t.Fatalf("want 1 group of 3 (Saga is 900 s x2 < min_files), got %+v", res)
	}
	g := res.Groups[0]
	if g.CommonTitle != "Tale" || g.FileCount != 3 || g.TotalDuration != 900 || len(g.SourceBookIDs) != 2 {
		t.Fatalf("group summary wrong: %+v", g)
	}

	// max_per_file_duration honoured.
	res = chRun(t, &scanChapterGroupsJob{}, s, `{"min_files":3,"max_per_file_duration":1000}`, false)
	if res.GroupsFound != 2 {
		t.Fatalf("max 1000: want 2 groups, got %d", res.GroupsFound)
	}
	// path_prefix honoured.
	res = chRun(t, &scanChapterGroupsJob{}, s, `{"max_per_file_duration":1000,"path_prefix":"/lib/B"}`, false)
	if res.GroupsFound != 1 || res.Groups[0].CommonTitle != "Saga" {
		t.Fatalf("prefix /lib/B: want only Saga, got %+v", res.Groups)
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
	// A second group whose primary carries a curated title.
	curated := chSeedGroup(t, s, "/lib/B/Saga", "Saga", 2, 300)
	if _, err := s.ModifyBook(curated[0].ID, func(b *database.Book) error { b.Title = "The Saga: Collector's Edition"; return nil }); err != nil {
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
	if got := ddMustGet(t, s, curated[0].ID); got.Title != "The Saga: Collector's Edition" {
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

	res := chRun(t, &mergeChapterGroupsJob{}, s, params, false)
	if res.BooksMerged != 1 || res.GroupsDrifted != 1 {
		t.Fatalf("want Tale merged (1 source) and Saga drifted, got %+v", res)
	}
	if !ddMustGet(t, s, tale[1].ID).IsSoftDeleted() {
		t.Fatal("reviewed Tale chapter 02 was not merged")
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
