// file: internal/database/review_store_test.go
// version: 1.0.0
// guid: 9d3b7f21-4a58-4c69-b8e2-1f0a6c5d4e37
// last-edited: 2026-07-13

package database

import (
	"testing"
)

func newReviewTestStore(t *testing.T) *PebbleStore {
	t.Helper()
	s, err := NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mkReviewItem(kind, dedupKey, folder, summary, payload string) ReviewItem {
	return ReviewItem{
		Kind:      kind,
		DedupKey:  dedupKey,
		FolderRef: folder,
		Summary:   summary,
		Payload:   payload,
	}
}

func TestUpsertReviewItem_NewAssignsIDAndPending(t *testing.T) {
	s := newReviewTestStore(t)
	got, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", "dk1", "/books/a", "sum", `{"x":1}`))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got.ID == "" {
		t.Fatal("expected an ID to be assigned")
	}
	if got.Status != ReviewStatusPending {
		t.Fatalf("expected status pending, got %q", got.Status)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatal("expected CreatedAt/UpdatedAt to be set")
	}

	// Round-trip.
	fetched, err := s.GetReviewItem(got.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched == nil || fetched.DedupKey != "dk1" || fetched.Summary != "sum" {
		t.Fatalf("round-trip mismatch: %+v", fetched)
	}
}

func TestUpsertReviewItem_IdempotentOnDedupKey(t *testing.T) {
	s := newReviewTestStore(t)
	first, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", "dk1", "/books/a", "sum1", `{"v":1}`))
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Re-upsert same DedupKey with new Summary/Payload — must update in place, not duplicate.
	second, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", "dk1", "/books/a", "sum2", `{"v":2}`))
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected same ID on re-upsert, got %q vs %q", second.ID, first.ID)
	}
	if second.Summary != "sum2" || second.Payload != `{"v":2}` {
		t.Fatalf("expected Summary/Payload updated on pending re-upsert, got %+v", second)
	}

	// Exactly one row must exist.
	count, err := s.CountReviewItems("")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row after idempotent re-upsert, got %d", count)
	}
}

func TestUpsertReviewItem_PreservesRejectedStatus(t *testing.T) {
	s := newReviewTestStore(t)
	first, err := s.UpsertReviewItem(mkReviewItem("regroup.anthology", "dk2", "/books/b", "sum1", `{"v":1}`))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Human rejects it.
	if _, err := s.SetReviewItemStatus(first.ID, ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// A re-scan re-upserts the same DedupKey with a fresh summary/payload.
	reupsert, err := s.UpsertReviewItem(mkReviewItem("regroup.anthology", "dk2", "/books/b", "NEW-sum", `{"v":9}`))
	if err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if reupsert.Status != ReviewStatusRejected {
		t.Fatalf("re-scan must NOT un-reject: got status %q", reupsert.Status)
	}
	// Full no-op for a decided item: summary/payload preserved, not overwritten.
	if reupsert.Summary != "sum1" || reupsert.Payload != `{"v":1}` {
		t.Fatalf("decided item must be untouched by re-upsert, got %+v", reupsert)
	}

	// Still rejected on disk, still exactly one row.
	fetched, err := s.GetReviewItem(first.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fetched.Status != ReviewStatusRejected {
		t.Fatalf("expected persisted rejected status, got %q", fetched.Status)
	}
	if c, _ := s.CountReviewItems(""); c != 1 {
		t.Fatalf("expected 1 row, got %d", c)
	}
}

func TestCountReviewItems_ByStatusIndex(t *testing.T) {
	s := newReviewTestStore(t)
	// 3 pending, then reject one.
	var ids []string
	for i, dk := range []string{"a", "b", "c"} {
		it, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", dk, "/books/"+dk, "s", ""))
		if err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
		ids = append(ids, it.ID)
	}
	if _, err := s.SetReviewItemStatus(ids[0], ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	if got, _ := s.CountReviewItems(ReviewStatusPending); got != 2 {
		t.Fatalf("expected 2 pending, got %d", got)
	}
	if got, _ := s.CountReviewItems(ReviewStatusRejected); got != 1 {
		t.Fatalf("expected 1 rejected, got %d", got)
	}
	if got, _ := s.CountReviewItems(""); got != 3 {
		t.Fatalf("expected 3 total, got %d", got)
	}
}

func TestListReviewItems_FilterByStatusAndKind(t *testing.T) {
	s := newReviewTestStore(t)
	seed := []struct{ kind, dk string }{
		{"regroup.multidisc", "m1"},
		{"regroup.multidisc", "m2"},
		{"regroup.anthology", "a1"},
	}
	for _, sd := range seed {
		if _, err := s.UpsertReviewItem(mkReviewItem(sd.kind, sd.dk, "/f/"+sd.dk, "s", "")); err != nil {
			t.Fatalf("seed %s: %v", sd.dk, err)
		}
	}

	// Status pending + kind filter.
	items, total, err := s.ListReviewItems(ReviewFilter{Status: ReviewStatusPending, Kind: "regroup.multidisc"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("expected 2 multidisc pending, got total=%d len=%d", total, len(items))
	}
	for _, it := range items {
		if it.Kind != "regroup.multidisc" {
			t.Fatalf("unexpected kind in filtered result: %q", it.Kind)
		}
	}

	// No status filter → all 3.
	_, total, err = s.ListReviewItems(ReviewFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected 3 total, got %d", total)
	}
}

func TestListReviewItems_Pagination(t *testing.T) {
	s := newReviewTestStore(t)
	for i := 0; i < 5; i++ {
		dk := string(rune('a' + i))
		if _, err := s.UpsertReviewItem(mkReviewItem("regroup.multidisc", dk, "/f/"+dk, "s", "")); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	page, total, err := s.ListReviewItems(ReviewFilter{Status: ReviewStatusPending, Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 5 {
		t.Fatalf("expected total 5, got %d", total)
	}
	if len(page) != 2 {
		t.Fatalf("expected page size 2, got %d", len(page))
	}
}

func TestSetReviewItemStatus_MovesIndexRow(t *testing.T) {
	s := newReviewTestStore(t)
	it, err := s.UpsertReviewItem(mkReviewItem("regroup.version-group", "vg1", "/f/vg1", "s", ""))
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	// Move pending → approved.
	updated, err := s.SetReviewItemStatus(it.ID, ReviewStatusApproved)
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
	if updated == nil || updated.Status != ReviewStatusApproved {
		t.Fatalf("expected approved, got %+v", updated)
	}

	// The pending index row must be gone; the approved one present.
	if got, _ := s.CountReviewItems(ReviewStatusPending); got != 0 {
		t.Fatalf("expected 0 pending after move, got %d", got)
	}
	if got, _ := s.CountReviewItems(ReviewStatusApproved); got != 1 {
		t.Fatalf("expected 1 approved after move, got %d", got)
	}

	// The old-status index scan must not resurface it (stale-row guard).
	pendingList, _, err := s.ListReviewItems(ReviewFilter{Status: ReviewStatusPending})
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	if len(pendingList) != 0 {
		t.Fatalf("expected no pending items, got %d", len(pendingList))
	}
}

func TestSetReviewItemStatus_NotFound(t *testing.T) {
	s := newReviewTestStore(t)
	got, err := s.SetReviewItemStatus("nonexistent", ReviewStatusApproved)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for missing item, got %+v", got)
	}
}

func TestReviewStatsByKind(t *testing.T) {
	s := newReviewTestStore(t)
	items := []struct{ kind, dk string }{
		{"regroup.multidisc", "m1"},
		{"regroup.multidisc", "m2"},
		{"regroup.anthology", "a1"},
	}
	var anthologyID string
	for _, sd := range items {
		it, err := s.UpsertReviewItem(mkReviewItem(sd.kind, sd.dk, "/f/"+sd.dk, "s", ""))
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
		if sd.kind == "regroup.anthology" {
			anthologyID = it.ID
		}
	}
	if _, err := s.SetReviewItemStatus(anthologyID, ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	stats, err := s.ReviewStatsByKind()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	pendingByKind := map[string]int{}
	for _, st := range stats {
		if st.Status == ReviewStatusPending {
			pendingByKind[st.Kind] = st.Count
		}
	}
	if pendingByKind["regroup.multidisc"] != 2 {
		t.Fatalf("expected 2 pending multidisc, got %d", pendingByKind["regroup.multidisc"])
	}
	if _, ok := pendingByKind["regroup.anthology"]; ok {
		t.Fatalf("expected no pending anthology (it was rejected), got %d", pendingByKind["regroup.anthology"])
	}
}

func TestUpsertReviewItem_RequiresDedupKeyAndKind(t *testing.T) {
	s := newReviewTestStore(t)
	if _, err := s.UpsertReviewItem(ReviewItem{Kind: "regroup.multidisc"}); err == nil {
		t.Fatal("expected error for empty DedupKey")
	}
	if _, err := s.UpsertReviewItem(ReviewItem{DedupKey: "dk"}); err == nil {
		t.Fatal("expected error for empty Kind")
	}
}
