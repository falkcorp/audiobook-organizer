// file: internal/maintenance/jobs/dedup_jobs_review_test.go
// version: 1.1.0
// guid: 3c9e7a15-6b2d-4f80-9a41-d5e8f2b6c073
// last-edited: 2026-09-13

package jobs

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Real-Pebble tests for the second review round of the dedup-jobs data-loss
// fixes: shared junk paths, verified repoints, primary hand-off timing, rerun
// convergence and dry-run parity.

func ddSetGroup(t *testing.T, s *database.PebbleStore, id, vg string, prim *bool) {
	t.Helper()
	if _, err := s.ModifyBook(id, func(b *database.Book) error {
		b.VersionGroupID = &vg
		b.IsPrimaryVersion = prim
		return nil
	}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
}

// ddLiveExplicitPrimaries returns the titles of vg's live explicit primaries.
func ddLiveExplicitPrimaries(t *testing.T, s *database.PebbleStore, vg string) []string {
	t.Helper()
	members, err := s.GetBooksByVersionGroup(vg)
	if err != nil {
		t.Fatalf("GetBooksByVersionGroup: %v", err)
	}
	var out []string
	for _, m := range members {
		if !m.IsSoftDeleted() && m.IsPrimaryVersion != nil && *m.IsPrimaryVersion {
			out = append(out, m.Title)
		}
	}
	sort.Strings(out)
	return out
}

// A "read by narrator" row naming a live book's file is retired with its
// FilePath cleared, so PurgeSoftDeletedBooks(deleteFiles) cannot os.Remove
// the live book's audio through it. Every retired junk row loses its path.
func TestDedupBooks_JunkSharingLivePathIsRetiredWithPathCleared(t *testing.T) {
	s := ddRealStore(t)
	dir := t.TempDir()
	shared := filepath.Join(dir, "Author", "Real Book", "book.m4b")
	ddWriteAudio(t, shared)
	live := ddMustBook(t, s, &database.Book{Title: "Real Book", FilePath: shared})
	ddMustFile(t, s, &database.BookFile{BookID: live.ID, FilePath: shared})
	junk := ddMustBook(t, s, &database.Book{Title: "Read by Narrator", FilePath: shared})
	own := filepath.Join(dir, "Other", "junk.m4b")
	junkOwn := ddMustBook(t, s, &database.Book{Title: "read by narrator", FilePath: own})

	if err := (&dedupBooksJob{}).Run(context.Background(), s, ddJobReporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := ddMustGet(t, s, junk.ID)
	if !got.IsSoftDeleted() {
		t.Fatal("junk row sharing a live path was not retired")
	}
	if got.FilePath != "" {
		t.Fatalf("junk FilePath = %q: a delete-files purge would remove the live book's audio", got.FilePath)
	}
	if g := ddMustGet(t, s, junkOwn.ID); !g.IsSoftDeleted() || g.FilePath != "" {
		t.Fatalf("junk with its own path %s: deleted=%v path=%q, want retired with the path cleared", own, g.IsSoftDeleted(), g.FilePath)
	}
	if g := ddMustGet(t, s, live.ID); g.IsSoftDeleted() || g.FilePath != shared {
		t.Fatalf("live book changed: %+v", g)
	}
}

// A name match whose old file is still on disk is not the same file: the fix
// is refused and nothing moves.
func TestVGPlanAuthorDirFix_OldFileStillOnDiskIsNotRepointed(t *testing.T) {
	s := ddRealStore(t)
	authorDir := filepath.Join(t.TempDir(), "Some Author")
	sub := filepath.Join(authorDir, "Alpha Chronicle")
	ddWriteAudio(t, filepath.Join(sub, "01.mp3"))
	flat := filepath.Join(authorDir, "01.mp3")
	ddWriteAudio(t, flat)
	book := ddMustBook(t, s, &database.Book{Title: "Alpha Chronicle", FilePath: authorDir})
	old := ddMustFile(t, s, &database.BookFile{BookID: book.ID, FilePath: flat, FileSize: int64(len(ddAudioBytes))})

	if _, err := vgPlanAuthorDirFix(s, book.ID, sub); !errors.Is(err, errVGRefused) {
		t.Fatalf("err = %v, want errVGRefused: the old file still exists", err)
	}
	files, _ := s.GetBookFiles(book.ID)
	if len(files) != 1 || files[0].ID != old.ID || files[0].FilePath != flat {
		t.Fatalf("rows changed on a refused fix: %+v", files)
	}
}

// Old path gone but the sizes differ: not provably the same file, refused.
func TestVGPlanAuthorDirFix_SizeMismatchIsNotRepointed(t *testing.T) {
	s := ddRealStore(t)
	authorDir := filepath.Join(t.TempDir(), "Some Author")
	sub := filepath.Join(authorDir, "Alpha Chronicle")
	ddWriteAudio(t, filepath.Join(sub, "01.mp3"))
	book := ddMustBook(t, s, &database.Book{Title: "Alpha Chronicle", FilePath: authorDir})
	ddMustFile(t, s, &database.BookFile{BookID: book.ID, FilePath: filepath.Join(authorDir, "01.mp3"), FileSize: 999_999})

	if _, err := vgPlanAuthorDirFix(s, book.ID, sub); !errors.Is(err, errVGRefused) {
		t.Fatalf("err = %v, want errVGRefused on a size mismatch", err)
	}
}

// vgUnlinkOutliers against a real store: both groups end with exactly one
// explicit primary.
func TestVGUnlinkOutliers_RealStoreKeepsOnePrimaryEachSide(t *testing.T) {
	s := ddRealStore(t)
	yes, no := true, false
	const vg = "vg-outlier"
	a := ddMustBook(t, s, &database.Book{Title: "Alpha", FilePath: "/lib/A/a.m4b"})
	b := ddMustBook(t, s, &database.Book{Title: "Alpha", FilePath: "/lib/A/b.m4b"})
	o := ddMustBook(t, s, &database.Book{Title: "Omega", FilePath: "/lib/O/o.m4b"})
	ddSetGroup(t, s, a.ID, vg, &no)
	ddSetGroup(t, s, b.ID, vg, &no)
	ddSetGroup(t, s, o.ID, vg, &yes)
	var group []database.BookCore
	for _, id := range []string{a.ID, b.ID, o.ID} {
		group = append(group, ddMustGet(t, s, id).Core())
	}
	if err := vgUnlinkOutliers(s, group, group[2:]); err != nil {
		t.Fatalf("vgUnlinkOutliers: %v", err)
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); len(got) != 1 {
		t.Fatalf("old group primaries = %v, want exactly one", got)
	}
	og := ddMustGet(t, s, o.ID)
	if og.VersionGroupID == nil || *og.VersionGroupID == vg {
		t.Fatal("outlier was not moved to a new group")
	}
	if got := ddLiveExplicitPrimaries(t, s, *og.VersionGroupID); len(got) != 1 {
		t.Fatalf("new group primaries = %v, want the outlier", got)
	}
}

type ddMoveFails struct{ *database.PebbleStore }

func (ddMoveFails) MoveBookFilesToBook([]string, string, string) error {
	return errors.New("pebble: batch commit failed")
}

type ddReassignFails struct {
	*database.PebbleStore
	fail *bool
}

func (s ddReassignFails) ReassignExternalIDs(oldID, newID string) error {
	if *s.fail {
		return errors.New("pebble: disk full")
	}
	return s.PebbleStore.ReassignExternalIDs(oldID, newID)
}

// ddPrimaryPair seeds keeper (non-primary) and dup (explicit primary) in one
// group, with dup owning one file.
func ddPrimaryPair(t *testing.T, s *database.PebbleStore, vg string) (keeper, dup *database.Book, file *database.BookFile) {
	t.Helper()
	yes, no := true, false
	keeper = ddMustBook(t, s, &database.Book{Title: "Keeper", FilePath: "/lib/A/k.m4b"})
	dup = ddMustBook(t, s, &database.Book{Title: "Dup", FilePath: "/lib/A/d.m4b"})
	ddSetGroup(t, s, keeper.ID, vg, &no)
	ddSetGroup(t, s, dup.ID, vg, &yes)
	file = ddMustFile(t, s, &database.BookFile{BookID: dup.ID, FilePath: "/lib/A/d.m4b"})
	return ddMustGet(t, s, keeper.ID), ddMustGet(t, s, dup.ID), file
}

// A failed move must not leave two primaries: the keeper is promoted only
// after the move, right before the dup is retired.
func TestDDMergeDuplicateBook_MoveFailureKeepsOnePrimary(t *testing.T) {
	s := ddRealStore(t)
	const vg = "vg-move-fail"
	keeper, dup, _ := ddPrimaryPair(t, s, vg)
	if err := ddMergeDuplicateBook(ddMoveFails{s}, keeper, dup, false, nil); err == nil {
		t.Fatal("merge succeeded although the move failed")
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); len(got) != 1 || got[0] != "Dup" {
		t.Fatalf("live primaries = %v, want only the untouched dup", got)
	}
	if g := ddMustGet(t, s, dup.ID); g.IsSoftDeleted() {
		t.Fatal("dup retired although its files never moved")
	}
	if files, _ := s.GetBookFiles(dup.ID); len(files) != 1 {
		t.Fatalf("dup owns %d rows, want its 1 row still there", len(files))
	}
}

// A failure after the move (external-ID reassign) leaves the dup live and the
// group with one primary; a rerun converges with nothing lost.
func TestDDMergeDuplicateBook_PostMoveFailureRerunConverges(t *testing.T) {
	s := ddRealStore(t)
	const vg = "vg-post-move"
	keeper, dup, file := ddPrimaryPair(t, s, vg)
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "PID-DUP", BookID: dup.ID}); err != nil {
		t.Fatalf("CreateExternalIDMapping: %v", err)
	}
	fail := true
	store := ddReassignFails{PebbleStore: s, fail: &fail}

	if err := ddMergeDuplicateBook(store, keeper, dup, false, nil); err == nil {
		t.Fatal("merge succeeded although the external-ID reassign failed")
	}
	if g := ddMustGet(t, s, dup.ID); g.IsSoftDeleted() {
		t.Fatal("dup retired after a failed step")
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); len(got) != 1 || got[0] != "Dup" {
		t.Fatalf("after the failure live primaries = %v, want only the dup", got)
	}

	fail = false
	keeper, dup = ddMustGet(t, s, keeper.ID), ddMustGet(t, s, dup.ID)
	if err := ddMergeDuplicateBook(store, keeper, dup, false, nil); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if g := ddMustGet(t, s, dup.ID); !g.IsSoftDeleted() || g.FilePath != "" {
		t.Fatalf("rerun: dup deleted=%v path=%q, want retired with path cleared", g.IsSoftDeleted(), g.FilePath)
	}
	kf, _ := s.GetBookFiles(keeper.ID)
	if len(kf) != 1 || kf[0].ID != file.ID {
		t.Fatalf("keeper rows = %+v, want the dup's row", kf)
	}
	ids, err := s.GetExternalIDsForBook(keeper.ID)
	if err != nil {
		t.Fatalf("GetExternalIDsForBook: %v", err)
	}
	found := false
	for _, m := range ids {
		found = found || m.ExternalID == "PID-DUP"
	}
	if !found {
		t.Fatalf("keeper external IDs = %+v, want PID-DUP moved over", ids)
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); len(got) != 1 || got[0] != "Keeper" {
		t.Fatalf("after the rerun live primaries = %v, want only the keeper", got)
	}
}

// Round 3: the live book's FilePath is a directory and the junk row owns a
// row inside it -- at the same path as a live row (written last, so the
// book_file_path index names the junk) or at a path with no live row. An
// exact-string check said "not shared" for both. Both are refused.
func TestDedupBooks_JunkOwningFileUnderLiveDirIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name       string
		liveHasRow bool
	}{
		{"same path as a live row", true},
		{"no live row at that path", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ddRealStore(t)
			dir := filepath.Join(t.TempDir(), "Author", "Real Book")
			f := filepath.Join(dir, "01.mp3")
			ddWriteAudio(t, f)
			live := ddMustBook(t, s, &database.Book{Title: "Real Book", FilePath: dir})
			if tc.liveHasRow {
				ddMustFile(t, s, &database.BookFile{BookID: live.ID, FilePath: f})
			}
			junk := ddMustBook(t, s, &database.Book{Title: "Read by Narrator", FilePath: filepath.Join(t.TempDir(), "elsewhere.m4b")})
			ddMustFile(t, s, &database.BookFile{BookID: junk.ID, FilePath: f})

			if err := (&dedupBooksJob{}).Run(context.Background(), s, ddJobReporter{}, false); err != nil {
				t.Fatalf("Run: %v (a refusal must not fail the run)", err)
			}
			if g := ddMustGet(t, s, junk.ID); g.IsSoftDeleted() {
				t.Fatal("junk row owning a file under a live book's directory was retired")
			}
			if files, _ := s.GetBookFiles(junk.ID); len(files) != 1 {
				t.Fatalf("junk rows = %d, want its row untouched", len(files))
			}
		})
	}
}

// Round 3: the junk row's FilePath is a file inside a live book's directory
// and it owns no rows. It is retired with the FilePath cleared, so a
// delete-files purge has nothing to remove through it.
func TestDedupBooks_JunkPathUnderLiveDirIsRetiredWithPathCleared(t *testing.T) {
	s := ddRealStore(t)
	dir := filepath.Join(t.TempDir(), "Author", "Real Book")
	f := filepath.Join(dir, "01.mp3")
	ddWriteAudio(t, f)
	live := ddMustBook(t, s, &database.Book{Title: "Real Book", FilePath: dir})
	ddMustFile(t, s, &database.BookFile{BookID: live.ID, FilePath: f})
	junk := ddMustBook(t, s, &database.Book{Title: "read by narrator", FilePath: f})

	if err := (&dedupBooksJob{}).Run(context.Background(), s, ddJobReporter{}, false); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if g := ddMustGet(t, s, junk.ID); !g.IsSoftDeleted() || g.FilePath != "" {
		t.Fatalf("junk: deleted=%v path=%q, want retired with the path cleared", g.IsSoftDeleted(), g.FilePath)
	}
	if lf, _ := s.GetBookFiles(live.ID); len(lf) != 1 || lf[0].FilePath != f {
		t.Fatalf("live rows changed: %+v", lf)
	}
}

// Round 3: a subdir holding only files that match none of the book's rows
// used to pass the gate on "create" alone, moving the book's path and
// stranding every old row. Refused now.
func TestVGPlanAuthorDirFix_OnlyUnmatchedFilesRefuses(t *testing.T) {
	s := ddRealStore(t)
	authorDir := filepath.Join(t.TempDir(), "Some Author")
	sub := filepath.Join(authorDir, "Alpha Chronicle")
	ddWriteAudio(t, filepath.Join(sub, "99.mp3"))
	book := ddMustBook(t, s, &database.Book{Title: "Alpha Chronicle", FilePath: authorDir})
	old := ddMustFile(t, s, &database.BookFile{BookID: book.ID, FilePath: filepath.Join(authorDir, "01.mp3"), FileSize: int64(len(ddAudioBytes))})

	if _, err := vgPlanAuthorDirFix(s, book.ID, sub); !errors.Is(err, errVGRefused) {
		t.Fatalf("err = %v, want errVGRefused: no existing row lands in the subdir", err)
	}
	files, _ := s.GetBookFiles(book.ID)
	if len(files) != 1 || files[0].ID != old.ID || files[0].FilePath != old.FilePath {
		t.Fatalf("rows changed on a refused fix: %+v", files)
	}
	if got := ddMustGet(t, s, book.ID); got.FilePath != authorDir {
		t.Fatalf("book path moved to %q on a refused fix", got.FilePath)
	}
}

// Hand-offs within one run must match between dry-run and apply. Both modes
// are compared the same way: the successor each retirement promotes, in
// order, from the run's overlay, mapped to titles. Apply's final rows are
// checked against the expected primary too.
func TestDedupBooks_DryRunHandoffMatchesApply(t *testing.T) {
	type pair struct{ keeper, dup string }
	cases := []struct {
		name string
		// keepers maps each keeper's title to its version group ("" = none).
		keepers map[string]string
		pairs   []pair
		want    []string // successor title per retirement, "" for none
		primary string   // G's live explicit primary after apply
	}{
		{
			// Keepers sit in their own groups: two hand-offs inside G, and
			// the second must skip the member the first retired.
			name:    "keepers in other groups",
			keepers: map[string]string{"Z1": "other-Z1", "Z2": "other-Z2"},
			pairs:   []pair{{"Z1", "M1"}, {"Z2", "M2"}},
			want:    []string{"M2", "M3"},
			primary: "M3",
		},
		{
			// K has no group, so the keeper fill moves it into G and it takes
			// the primary. The second retirement then needs no hand-off --
			// which dry-run only sees if it models the join.
			name:    "groupless keeper joins through the fill",
			keepers: map[string]string{"K": "", "Z": "other-Z"},
			pairs:   []pair{{"K", "M1"}, {"Z", "M2"}},
			want:    []string{"K", ""},
			primary: "K",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := func(dry bool) (*database.PebbleStore, []string) {
				s := ddRealStore(t)
				yes := true
				byTitle := map[string]*database.Book{}
				for _, title := range []string{"M1", "M2", "M3"} {
					b := ddMustBook(t, s, &database.Book{Title: title, FilePath: "/lib/G/" + title + ".m4b"})
					ddSetGroup(t, s, b.ID, "G", nil)
					byTitle[title] = b
				}
				for _, title := range []string{"K", "Z", "Z1", "Z2"} {
					vg, ok := tc.keepers[title]
					if !ok {
						continue
					}
					b := ddMustBook(t, s, &database.Book{Title: title, FilePath: "/lib/K/" + title + ".m4b"})
					if vg != "" {
						ddSetGroup(t, s, b.ID, vg, &yes)
					} else if _, err := s.ModifyBook(b.ID, func(bk *database.Book) error {
						bk.VersionGroupID = nil
						bk.IsPrimaryVersion = nil
						return nil
					}); err != nil {
						t.Fatalf("clear group: %v", err)
					}
					byTitle[title] = b
				}
				titleOf := map[string]string{}
				// Loaded once up front, as Run does.
				for k, b := range byTitle {
					byTitle[k] = ddMustGet(t, s, b.ID)
					titleOf[b.ID] = k
				}
				sim := newDDSim()
				for _, p := range tc.pairs {
					if err := ddMergeDuplicateBookSim(s, sim, byTitle[p.keeper], byTitle[p.dup], dry, nil); err != nil {
						t.Fatalf("dry=%v merge %s<-%s: %v", dry, p.keeper, p.dup, err)
					}
				}
				got := make([]string, len(sim.handoffs))
				for i, id := range sim.handoffs {
					got[i] = titleOf[id]
				}
				return s, got
			}

			_, dry := run(true)
			applyStore, apply := run(false)
			if !reflect.DeepEqual(dry, apply) {
				t.Fatalf("hand-offs differ: dry-run %q, apply %q", dry, apply)
			}
			if !reflect.DeepEqual(apply, tc.want) {
				t.Fatalf("hand-offs = %q, want %q", apply, tc.want)
			}
			if got := ddLiveExplicitPrimaries(t, applyStore, "G"); len(got) != 1 || got[0] != tc.primary {
				t.Fatalf("apply left G's primaries %v, want [%s]", got, tc.primary)
			}
		})
	}
}
