// file: internal/maintenance/jobs/manual_only_full_guard_test.go
// version: 1.0.0
// guid: f3165568-40ba-4911-bf92-4619f0de4387
// last-edited: 2026-10-05

package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
)

// Owner decision 2026-10-05: the chapter jobs decide owner-manual-only from
// the whole book (applygate.BookManualOnly), not from the row or path alone.
// Every book below has a clean path, title, series, narrator and publisher;
// the Doctor Who / Big Finish signal is ONLY on an author credit, a
// franchise: tag or a book_file's transcribed title.

func mgTagBigFinish(t *testing.T, s *database.PebbleStore, bookID string) {
	t.Helper()
	if err := s.AddBookTag(bookID, franchise.Tags(franchise.BigFinish, "")[0]); err != nil {
		t.Fatalf("AddBookTag: %v", err)
	}
}

func mgCreditBigFinish(t *testing.T, s *database.PebbleStore, bookID string) {
	t.Helper()
	a, err := s.GetAuthorByName("Big Finish Productions")
	if err != nil || a == nil {
		a, err = s.CreateAuthor("Big Finish Productions")
		if err != nil || a == nil {
			t.Fatalf("CreateAuthor: %v", err)
		}
	}
	if err := s.SetBookAuthors(bookID, []database.BookAuthor{{BookID: bookID, AuthorID: a.ID}}); err != nil {
		t.Fatalf("SetBookAuthors: %v", err)
	}
}

func mgTranscribeDoctorWho(t *testing.T, s *database.PebbleStore, bookID string) {
	t.Helper()
	files, err := s.GetBookFiles(bookID)
	if err != nil || len(files) == 0 {
		t.Fatalf("GetBookFiles %s: %v (%d)", bookID, err, len(files))
	}
	dw := "Doctor Who: The Chimes of Midnight"
	files[0].TranscribedTitle = &dw
	if err := s.UpdateBookFile(files[0].ID, &files[0]); err != nil {
		t.Fatalf("UpdateBookFile: %v", err)
	}
}

// repoint-version-primary: a pair whose twin or member is held only by a
// tag, a credit or a file's transcribed title is refused as owner-manual,
// on either side of the pair.
func TestRepointVersionPrimary_OwnerManualOffTheRow(t *testing.T) {
	cases := []struct {
		name  string
		mark  func(t *testing.T, s *database.PebbleStore, member, twin string)
		label string
	}{
		{"twin tagged Big Finish", func(t *testing.T, s *database.PebbleStore, _, twin string) { mgTagBigFinish(t, s, twin) }, "tag"},
		{"member credited to Big Finish", func(t *testing.T, s *database.PebbleStore, member, _ string) { mgCreditBigFinish(t, s, member) }, "author"},
		{"twin file transcribed as Doctor Who", func(t *testing.T, s *database.PebbleStore, _, twin string) { mgTranscribeDoctorWho(t, s, twin) }, "file transcribed title"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := ddRealStore(t)
			// rvpSeedPair credits every book to author id 1; make that a
			// real, neutral row so a later author row cannot take the id.
			if a, err := s.CreateAuthor("Saga Author"); err != nil || a == nil || a.ID != 1 {
				t.Fatalf("CreateAuthor: %+v %v (want id 1)", a, err)
			}
			im, tw := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
			tc.mark(t, s, im[0].ID, tw[0].ID)
			det, snap, err := detectChapterGroupsForRunWithBooks(context.Background(), s, chapterGroupParams{MinFiles: 2, MaxPerFileDuration: 600})
			if err != nil {
				t.Fatalf("detect: %v", err)
			}
			idx := newRepointIndex(snap, det)
			none := func(*database.BookCore) string { return "" }
			d := (&repointVersionPrimaryJob{}).classify(s, idx, none, im[0].ID, nil, false)
			if d.Bucket != bucketOwnerManualOnly {
				t.Fatalf("held pair not refused as owner-manual: %+v", d)
			}
			if !strings.Contains(d.Reason, tc.label) {
				t.Fatalf("reason %q does not name the %s", d.Reason, tc.label)
			}
			// Control: the unmarked pair next to it still qualifies.
			if d := (&repointVersionPrimaryJob{}).classify(s, idx, none, im[1].ID, nil, false); d.Bucket != bucketWouldRepoint {
				t.Fatalf("unmarked control pair did not qualify: %+v", d)
			}
		})
	}
}

// rvpTagFailStore fails every tag read, so the owner-manual check cannot be
// done.
type rvpTagFailStore struct{ *database.PebbleStore }

func (rvpTagFailStore) GetBookTagsDetailed(string) ([]database.BookTag, error) {
	return nil, errors.New("simulated tag read failure")
}

// A failed owner-manual read leaves the pair alone in its own bucket: not
// would_repoint, and not counted as a Doctor Who pair.
func TestRepointVersionPrimary_OwnerManualReadFailureFailsClosed(t *testing.T) {
	s := ddRealStore(t)
	im, _ := rvpSeedRun(t, s, "/lib/imported/Saga", "Saga", 3, "vg")
	det, snap, err := detectChapterGroupsForRunWithBooks(context.Background(), s, chapterGroupParams{MinFiles: 2, MaxPerFileDuration: 600})
	if err != nil {
		t.Fatalf("detect: %v", err)
	}
	idx := newRepointIndex(snap, det)
	d := (&repointVersionPrimaryJob{}).classify(rvpTagFailStore{s}, idx, func(*database.BookCore) string { return "" }, im[0].ID, nil, false)
	if d.Bucket != bucketOwnerManualCheckFailed {
		t.Fatalf("read failure not failed closed in its own bucket: %+v", d)
	}
}

// merge-chapter-groups: a chapter run with one member held only by a tag, a
// credit or a file's transcribed title is blocked in the preview, and an
// apply of a selection made before the member was marked is blocked too.
func TestMergeChapterGroups_OwnerManualOffTheRow(t *testing.T) {
	cases := []struct {
		name string
		mark func(t *testing.T, s *database.PebbleStore, id string)
	}{
		{"tag", mgTagBigFinish},
		{"author credit", mgCreditBigFinish},
		{"file transcribed title", mgTranscribeDoctorWho},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Preview.
			s := ddRealStore(t)
			books := chSeedGroup(t, s, "/lib/Some Author/Story", "Story", 3, 300)
			tc.mark(t, s, books[1].ID)
			res := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true)
			if len(res.Groups) != 1 || res.Groups[0].Status != "blocked" || res.BooksMerged != 0 ||
				len(res.Groups[0].Blockers) == 0 || !strings.Contains(res.Groups[0].Blockers[0], "owner-manual-only") {
				t.Fatalf("held member offered for merge: %+v", res)
			}

			// Apply of a selection fingerprinted before the mark: the
			// fingerprint does not cover tags or credits, so only the
			// owner-manual check stands between it and a merge.
			s2 := ddRealStore(t)
			books2 := chSeedGroup(t, s2, "/lib/Some Author/Story", "Story", 3, 300)
			ids := []string{books2[0].ID, books2[1].ID, books2[2].ID}
			sel := chSelectionFor(t, s2, ids)
			tc.mark(t, s2, books2[2].ID)
			if tc.name == "file transcribed title" {
				// The file write changes the fingerprint; re-take it so the
				// apply reaches the owner-manual check rather than drift.
				sel = chSelectionFor(t, s2, ids)
			}
			out := chApply(t, s2, sel)
			if len(out.Groups) != 1 || out.Groups[0].Status != "blocked" || out.BooksMerged != 0 {
				t.Fatalf("held member merged: %+v", out)
			}
			for _, id := range ids[1:] {
				if b, _ := s2.GetBookByID(id); b == nil || b.IsSoftDeleted() || (b.MergedIntoBookID != nil && *b.MergedIntoBookID != "") {
					t.Fatalf("source %s was merged away: %+v", id, b)
				}
			}
		})
	}
	// Control: the same run with nothing marked is offered.
	s := ddRealStore(t)
	chSeedGroup(t, s, "/lib/Some Author/Story", "Story", 3, 300)
	if res := chRun(t, &mergeChapterGroupsJob{}, s, `{"dry_run":true}`, true); len(res.Groups) != 1 || res.Groups[0].Status != "would_merge" {
		t.Fatalf("unmarked control not offered: %+v", res)
	}
}
