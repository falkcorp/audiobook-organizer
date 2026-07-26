// file: internal/plugins/maintenance/regroup_shattered_ai_test.go
// version: 1.1.0
// guid: 3a9f6c04-8e21-4b57-9d0a-6f2e7c1b5a48
// last-edited: 2026-07-25

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// TestBuildRegroupPayload_DiscTrackRoundTrip closes the classifier→payload→apply seam:
// a real classified group serialized by buildRegroupPayload and decoded back by
// decodeRegroupPayload must preserve the per-file disc/track arrays, index-aligned with
// Files/MemberBookIDs. This guards the parallel-array copy the apply path relies on.
func TestBuildRegroupPayload_DiscTrackRoundTrip(t *testing.T) {
	// A flat, sequentially-numbered same-disc set (the "When We Were Sisters" shape).
	base := "/mnt/bigdata/books/audiobook-organizer/Ann Napolitano/When We Were Sisters"
	var books []itunesservice.ShatterBook
	for i := 1; i <= 5; i++ {
		books = append(books, itunesservice.ShatterBook{
			BookID:    fmt.Sprintf("b%02d", i),
			FilePath:  fmt.Sprintf("%s/When We Were Sisters_%d.mp3", base, i),
			FileCount: 1,
			IsPrimary: true,
		})
	}
	groups, _ := itunesservice.ClassifyShatteredFolders(books)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.Kind != itunesservice.KindMultidisc || !g.Confident {
		t.Fatalf("want confident multidisc, got kind=%q confident=%v", g.Kind, g.Confident)
	}

	raw, err := buildRegroupPayload(g)
	if err != nil {
		t.Fatalf("buildRegroupPayload: %v", err)
	}
	p, err := decodeRegroupPayload(database.ReviewItem{Payload: raw})
	if err != nil {
		t.Fatalf("decodeRegroupPayload: %v", err)
	}

	if len(p.DiscNumbers) != len(p.Files) || len(p.TrackNumbers) != len(p.Files) {
		t.Fatalf("arrays not parallel: files=%d discs=%d tracks=%d",
			len(p.Files), len(p.DiscNumbers), len(p.TrackNumbers))
	}
	// Every member is a same-disc chapter: disc 0, contiguous track numbers.
	for i := range p.Files {
		if p.DiscNumbers[i] != 0 {
			t.Errorf("file %d (%s): disc=%d, want 0", i, p.Files[i], p.DiscNumbers[i])
		}
	}
	// The classifier ordered members by track, so tracks are 1..N in payload order.
	for i, tr := range p.TrackNumbers {
		if tr != i+1 {
			t.Errorf("track order: index %d has track %d, want %d", i, tr, i+1)
		}
	}
}

// TestBuildRegroupPayload_AnthologyCarriesTracks verifies the owner decision that an
// anthology is ONE book: its payload now carries disc/track (so the combine gets
// chapter order) and its proposed action is a COMBINE, not a split.
func TestBuildRegroupPayload_AnthologyCarriesTracks(t *testing.T) {
	base := "/mnt/bigdata/books/audiobook-organizer/George R R Martin/Dangerous Women Anthology"
	titles := []string{"The Princess and the Queen", "Some Desperate Glory", "Bombshells", "Raisa Stepanova", "Noras Song"}
	var books []itunesservice.ShatterBook
	for i, title := range titles {
		books = append(books, itunesservice.ShatterBook{
			BookID:    fmt.Sprintf("a%02d", i),
			FilePath:  fmt.Sprintf("%s/%s.mp3", base, title),
			FileCount: 1,
			IsPrimary: true,
		})
	}
	groups, _ := itunesservice.ClassifyShatteredFolders(books)
	g := groups[0]
	if g.Kind != itunesservice.KindAnthology {
		t.Fatalf("want anthology, got %q", g.Kind)
	}
	raw, err := buildRegroupPayload(g)
	if err != nil {
		t.Fatalf("buildRegroupPayload: %v", err)
	}
	p, err := decodeRegroupPayload(database.ReviewItem{Payload: raw})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Anthology now carries disc/track (combine kind) — this is the behavior change.
	if len(p.DiscNumbers) != len(p.Files) || len(p.TrackNumbers) != len(p.Files) {
		t.Fatalf("anthology payload must carry disc/track: files=%d discs=%d tracks=%d",
			len(p.Files), len(p.DiscNumbers), len(p.TrackNumbers))
	}
	for i := range p.DiscNumbers {
		if p.DiscNumbers[i] != 0 {
			t.Errorf("anthology file %d disc=%d, want 0 (one book)", i, p.DiscNumbers[i])
		}
	}
	if !strings.Contains(p.ProposedAction, "combine into one") {
		t.Errorf("anthology action = %q, want combine", p.ProposedAction)
	}
}

// TestRegroupSummary_Labels pins the owner-facing labels: a real disc set reads
// "Multi-disc", same-disc chapters read "Chapters" (the original confusion), and an
// anthology reads "→ 1 book" (combine, not split).
func TestRegroupSummary_Labels(t *testing.T) {
	cases := []struct {
		name string
		g    itunesservice.RegroupGroup
		want string
	}{
		{"real disc set", itunesservice.RegroupGroup{Kind: itunesservice.KindMultidisc, Structure: "disc", Members: make([]itunesservice.ShatterBook, 3), FolderRef: "/x"}, "Multi-disc: 3 discs → 1 book"},
		{"same-disc chapters", itunesservice.RegroupGroup{Kind: itunesservice.KindMultidisc, Structure: "flat", Members: make([]itunesservice.ShatterBook, 6), FolderRef: "/x"}, "Chapters: 6 tracks → 1 book"},
		{"anthology", itunesservice.RegroupGroup{Kind: itunesservice.KindAnthology, DistinctWorks: 5, Members: make([]itunesservice.ShatterBook, 9), FolderRef: "/x"}, "Anthology/collection: 9 files (5 stories) → 1 book"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := regroupSummary(c.g)
			if !strings.Contains(got, c.want) {
				t.Errorf("summary = %q, want it to contain %q", got, c.want)
			}
		})
	}
}

// seedShatterBook creates a single-file book at a chapter-shatter path so the dry-run
// op's scan groups it into a review hold.
func seedShatterBook(t *testing.T, s *database.PebbleStore, path string) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{})
	if err != nil || b == nil {
		t.Fatalf("CreateBook: %v", err)
	}
	if err := s.CreateBookFile(&database.BookFile{BookID: b.ID, FilePath: path}); err != nil {
		t.Fatalf("CreateBookFile(%s): %v", path, err)
	}
	return b.ID
}

// countBooksAndFiles returns the TOTAL live book and book-file counts across the
// whole store (not just a seeded subset), so the zero-mutation assertion proves the
// dry-run neither deleted nor CREATED any book/file rows.
func countBooksAndFiles(t *testing.T, s *database.PebbleStore) (books, files int) {
	t.Helper()
	ids, err := s.ListBookIDs()
	if err != nil {
		t.Fatalf("ListBookIDs: %v", err)
	}
	for _, id := range ids {
		books++
		bf, _ := s.GetBookFiles(id)
		files += len(bf)
	}
	return books, files
}

// The dry-run op must write review-queue rows and make ZERO book/file mutations, and
// re-running it must be idempotent (same review-row count, not doubled).
func TestRegroupShatteredAI_DryRun_WritesHolds_ZeroMutations_Idempotent(t *testing.T) {
	s := regroupStore(t)

	// A chapter-shatter book folder: 6 single-file chapter shells under one book folder.
	base := "/lib/Adrian Tchaikovsky/Cage of Souls"
	var ids []string
	for i := 1; i <= 6; i++ {
		ids = append(ids, seedShatterBook(t, s, base+"/Cage of Souls - "+itoa(i)+"/01.mp3"))
	}
	// A lone genuine single-file book that must NOT produce a hold.
	ids = append(ids, seedShatterBook(t, s, "/lib/Andy Weir/The Martian/The Martian.m4b"))

	beforeBooks, beforeFiles := countBooksAndFiles(t, s)

	p := &Plugin{deps: fakeDeps{store: s}}
	rep := &fakeReporter{}

	// Run #1.
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #1: %v", err)
	}

	// ZERO book/file mutations.
	afterBooks, afterFiles := countBooksAndFiles(t, s)
	if afterBooks != beforeBooks || afterFiles != beforeFiles {
		t.Fatalf("dry-run mutated books/files: books %d→%d, files %d→%d",
			beforeBooks, afterBooks, beforeFiles, afterFiles)
	}

	// Exactly one review hold (the chapter-shatter folder; the lone single is skipped).
	items1, total1, err := s.ListReviewItems(database.ReviewFilter{})
	if err != nil {
		t.Fatalf("ListReviewItems: %v", err)
	}
	if total1 != 1 {
		t.Fatalf("run #1 wrote %d review items, want 1: %+v", total1, items1)
	}
	hold := items1[0]
	if hold.Kind != "regroup.multidisc" {
		t.Errorf("hold kind = %q, want regroup.multidisc", hold.Kind)
	}
	if hold.FolderRef != base {
		t.Errorf("hold folderRef = %q, want %q", hold.FolderRef, base)
	}
	if hold.Status != database.ReviewStatusPending {
		t.Errorf("hold status = %q, want pending", hold.Status)
	}
	// Payload carries the 6 member book IDs and the derived survivor title.
	var payload regroupPayload
	if err := json.Unmarshal([]byte(hold.Payload), &payload); err != nil {
		t.Fatalf("payload unmarshal: %v", err)
	}
	if len(payload.MemberBookIDs) != 6 || len(payload.Files) != 6 {
		t.Errorf("payload members=%d files=%d, want 6/6", len(payload.MemberBookIDs), len(payload.Files))
	}
	if payload.SurvivorTitle != "Cage of Souls" {
		t.Errorf("payload survivorTitle = %q, want 'Cage of Souls'", payload.SurvivorTitle)
	}
	if payload.Confidence != "high" {
		t.Errorf("payload confidence = %q, want high", payload.Confidence)
	}

	// Run #2 — UPSERT idempotency: the same folder must NOT create a second row.
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	_, total2, err := s.ListReviewItems(database.ReviewFilter{})
	if err != nil {
		t.Fatalf("ListReviewItems #2: %v", err)
	}
	if total2 != 1 {
		t.Fatalf("run #2 review items = %d, want 1 (upsert must not duplicate)", total2)
	}

	// Books/files still untouched after the second run.
	after2Books, after2Files := countBooksAndFiles(t, s)
	if after2Books != beforeBooks || after2Files != beforeFiles {
		t.Fatalf("run #2 mutated books/files: books %d→%d, files %d→%d",
			beforeBooks, after2Books, beforeFiles, after2Files)
	}
}

// A human-rejected hold must not be resurfaced (status preserved) on a re-scan.
func TestRegroupShatteredAI_DryRun_PreservesDecision(t *testing.T) {
	s := regroupStore(t)
	base := "/lib/Brandon Sanderson/Elantris"
	for i := 1; i <= 5; i++ {
		seedShatterBook(t, s, base+"/Elantris - "+itoa(i)+"/01.mp3")
	}

	p := &Plugin{deps: fakeDeps{store: s}}
	rep := &fakeReporter{}
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #1: %v", err)
	}
	items, _, _ := s.ListReviewItems(database.ReviewFilter{})
	if len(items) != 1 {
		t.Fatalf("want 1 hold, got %d", len(items))
	}
	// Human rejects it.
	if _, err := s.SetReviewItemStatus(items[0].ID, database.ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// Re-scan must leave the rejected item rejected (no resurfacing).
	if err := p.runRegroupShatteredAI(context.Background(), nil, rep); err != nil {
		t.Fatalf("run #2: %v", err)
	}
	after, total, _ := s.ListReviewItems(database.ReviewFilter{})
	if total != 1 {
		t.Fatalf("re-scan changed item count to %d, want 1", total)
	}
	if after[0].Status != database.ReviewStatusRejected {
		t.Errorf("re-scan resurfaced a rejected hold: status=%q", after[0].Status)
	}
}

// seedHold writes one pending review hold directly (bypassing the scan) so reconcile
// tests can set up a queue state precisely.
func seedHold(t *testing.T, s *database.PebbleStore, kind, dedupKey, folder string) database.ReviewItem {
	t.Helper()
	it, err := s.UpsertReviewItem(database.ReviewItem{
		Kind: kind, DedupKey: dedupKey, FolderRef: folder, Summary: "s", Payload: "{}",
	})
	if err != nil {
		t.Fatalf("seedHold: %v", err)
	}
	return it
}

// remainingKeys returns the set of DedupKeys still present across all statuses.
func remainingKeys(t *testing.T, s *database.PebbleStore) map[string]bool {
	t.Helper()
	items, _, err := s.ListReviewItems(database.ReviewFilter{Limit: 1000})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	keys := map[string]bool{}
	for _, it := range items {
		keys[it.DedupKey] = true
	}
	return keys
}

// TestReconcileStaleHolds_PurgesSupersededPendingOnly: a full run deletes PENDING
// regroup holds whose folder is no longer emitted, while preserving emitted holds,
// human-decided (rejected) holds, and another producer's (non-regroup) holds.
func TestReconcileStaleHolds_PurgesSupersededPendingOnly(t *testing.T) {
	s := regroupStore(t)

	emittedKey := regroupDedupKey(itunesservice.KindMultidisc, "/books/keep")
	seedHold(t, s, itunesservice.KindMultidisc, emittedKey, "/books/keep")

	staleKey := regroupDedupKey(itunesservice.KindAnthology, "/books/stale")
	seedHold(t, s, itunesservice.KindAnthology, staleKey, "/books/stale")

	rejKey := regroupDedupKey(itunesservice.KindMultidisc, "/books/rej")
	rej := seedHold(t, s, itunesservice.KindMultidisc, rejKey, "/books/rej")
	if _, err := s.SetReviewItemStatus(rej.ID, database.ReviewStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}

	// A different producer's pending hold — must never be purged by the regroup reconcile.
	seedHold(t, s, "dedup.candidate", "dedup-key-1", "/books/dedup")

	// This run emits only the "keep" folder.
	groups := []itunesservice.RegroupGroup{
		{Kind: itunesservice.KindMultidisc, FolderRef: "/books/keep"},
	}
	purged := reconcileStaleHolds(context.Background(), s, &fakeReporter{}, groups, 0)
	if purged != 1 {
		t.Fatalf("expected exactly 1 stale hold purged, got %d", purged)
	}

	keys := remainingKeys(t, s)
	if !keys[emittedKey] {
		t.Error("emitted hold was wrongly purged")
	}
	if keys[staleKey] {
		t.Error("stale pending regroup hold was NOT purged")
	}
	if !keys[rejKey] {
		t.Error("human-decided (rejected) hold was wrongly purged — only PENDING may be purged")
	}
	if !keys["dedup-key-1"] {
		t.Error("another producer's hold was wrongly purged — reconcile must only touch regroup.*")
	}
}

// TestReconcileStaleHolds_SkippedOnCappedRun: a canary run (limit > 0) emits only a
// subset, so reconcile must be a no-op rather than purging the un-emitted remainder.
func TestReconcileStaleHolds_SkippedOnCappedRun(t *testing.T) {
	s := regroupStore(t)
	staleKey := regroupDedupKey(itunesservice.KindMultidisc, "/books/x")
	seedHold(t, s, itunesservice.KindMultidisc, staleKey, "/books/x")

	purged := reconcileStaleHolds(context.Background(), s, &fakeReporter{}, nil, 5) // limit > 0
	if purged != 0 {
		t.Fatalf("capped run must not purge, got %d", purged)
	}
	if n, _ := s.CountReviewItems(""); n != 1 {
		t.Fatalf("hold must remain after a capped run, got %d", n)
	}
}
