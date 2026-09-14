// file: internal/maintenance/jobs/dedup_jobs_round5_test.go
// version: 1.1.0
// guid: 8e4a1c73-2f6b-4d90-a5c8-3b7e9f1d2a64
// last-edited: 2026-09-13

package jobs

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Follow-ups to #3395's final review, on a real PebbleStore. They use only
// helpers and signatures that exist on 05e79341e, so they compile against it
// and show it failing.

// ddMidMergeWriter runs writer once, at the move -- after the merge planned
// its primary hand-off from its first read and before it retires the dup --
// to stand in for a concurrent writer landing in that window.
type ddMidMergeWriter struct {
	*database.PebbleStore
	writer func()
}

func (s ddMidMergeWriter) MoveBookFilesToBook(ids []string, from, to string) error {
	if s.writer != nil {
		s.writer()
	}
	return s.PebbleStore.MoveBookFilesToBook(ids, from, to)
}

// ddAssertMovedToKeeper checks the dup's file row and (when pid is set) its
// iTunes PID now belong to the keeper.
func ddAssertMovedToKeeper(t *testing.T, s *database.PebbleStore, keeperID, dupID, path, pid string) {
	t.Helper()
	if files, err := s.GetBookFiles(dupID); err != nil || len(files) != 0 {
		t.Fatalf("dup still owns %d file(s), %v", len(files), err)
	}
	f, err := s.GetBookFileByPath(path)
	if err != nil || f == nil || f.BookID != keeperID {
		t.Fatalf("file %s = %+v, %v; want it on the keeper %s", path, f, err, keeperID)
	}
	if pid == "" {
		return
	}
	if owner, err := s.GetBookByExternalID("itunes", pid); err != nil || owner != keeperID {
		t.Fatalf("PID %s owner = %q, %v; want the keeper %s", pid, owner, err, keeperID)
	}
}

// (a) The dup was the planned primary, but between the first plan and the
// soft-delete another writer demoted it and promoted a third member. Retiring
// on the stale plan promoted the keeper as well: two primaries. Refusing it
// instead left the dup live and empty after its files had moved. The hand-off
// is planned again from a fresh read right before the retire: the dup is no
// longer primary, so it retires with no promotion and Z stays the only
// primary.
func TestDDMergeDuplicateBook_PrimaryChangedAfterPlanIsReplanned(t *testing.T) {
	s := ddRealStore(t)
	const vg = "vg-stale-plan"
	keeper, dup, file := ddPrimaryPair(t, s, vg)
	no := false
	z := ddMustBook(t, s, &database.Book{Title: "Z", FilePath: "/lib/Z/z.m4b"})
	ddSetGroup(t, s, z.ID, vg, &no)
	yes := true
	store := ddMidMergeWriter{PebbleStore: s, writer: func() {
		ddSetGroup(t, s, dup.ID, vg, &no)
		ddSetGroup(t, s, z.ID, vg, &yes)
	}}

	if err := ddMergeDuplicateBook(store, keeper, dup, false, nil); err != nil {
		t.Fatalf("err = %v, want the merge to finish on a fresh plan", err)
	}
	if g := ddMustGet(t, s, dup.ID); !g.IsSoftDeleted() {
		t.Fatal("dup left live after its files moved")
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); !reflect.DeepEqual(got, []string{"Z"}) {
		t.Fatalf("live primaries = %v, want only the concurrent writer's Z", got)
	}
	ddAssertMovedToKeeper(t, s, keeper.ID, dup.ID, file.FilePath, "")
}

// (a) The planned successor was retired between the first plan and the
// soft-delete. Promoting it made a deleted row the primary. Refusing instead
// left the dup live and empty. The fresh plan skips the retired member; the
// dup is the group's last live member, so it retires with no hand-off, and
// its file and PID end on the keeper.
func TestDDMergeDuplicateBook_SuccessorRetiredAfterPlanIsReplanned(t *testing.T) {
	s := ddRealStore(t)
	const vg = "vg-successor-gone"
	yes, no := true, false
	// M is created first: the earliest-created live member is the successor
	// when the keeper is in another group.
	m := ddMustBook(t, s, &database.Book{Title: "M", FilePath: "/lib/M/m.m4b"})
	dup := ddMustBook(t, s, &database.Book{Title: "Dup", FilePath: "/lib/A/d.m4b"})
	keeper := ddMustBook(t, s, &database.Book{Title: "Keeper", FilePath: "/lib/A/k.m4b"})
	ddSetGroup(t, s, m.ID, vg, &no)
	ddSetGroup(t, s, dup.ID, vg, &yes)
	ddSetGroup(t, s, keeper.ID, "vg-keeper", &yes)
	ddMustFile(t, s, &database.BookFile{BookID: dup.ID, FilePath: "/lib/A/d.m4b"})
	const pid = "DUPPID01"
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: pid, BookID: dup.ID, FilePath: "/lib/A/d.m4b"}); err != nil {
		t.Fatal(err)
	}
	store := ddMidMergeWriter{PebbleStore: s, writer: func() {
		if _, err := s.ModifyBook(m.ID, func(b *database.Book) error {
			b.MarkedForDeletion = &yes
			return nil
		}); err != nil {
			t.Errorf("retire M: %v", err)
		}
	}}

	if err := ddMergeDuplicateBook(store, ddMustGet(t, s, keeper.ID), ddMustGet(t, s, dup.ID), false, nil); err != nil {
		t.Fatalf("err = %v, want the merge to finish on a fresh plan", err)
	}
	if g := ddMustGet(t, s, dup.ID); !g.IsSoftDeleted() {
		t.Fatal("dup left live after its files moved")
	}
	if g := ddMustGet(t, s, m.ID); g.IsPrimaryVersion != nil && *g.IsPrimaryVersion {
		t.Fatal("a retired book was promoted to primary")
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); len(got) != 0 {
		t.Fatalf("live primaries = %v, want none (every member is retired)", got)
	}
	ddAssertMovedToKeeper(t, s, keeper.ID, dup.ID, "/lib/A/d.m4b", pid)
}

// ddFlipOnRetire flips the dup's primary flag immediately before every write
// to the dup: a writer that keeps changing what the hand-off was planned on,
// so every retire attempt is refused.
type ddFlipOnRetire struct {
	*database.PebbleStore
	dupID string
}

func (s ddFlipOnRetire) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if id == s.dupID {
		if _, err := s.PebbleStore.ModifyBook(id, func(b *database.Book) error {
			v := b.IsPrimaryVersion != nil && !*b.IsPrimaryVersion
			b.IsPrimaryVersion = &v
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// (a) When the retire is still refused after a fresh plan, the dup is left
// live and empty -- its files already moved. That must count as a failure
// that names the dup, not as a quiet refusal, and the group must not be left
// with an extra primary.
func TestDDMergeDuplicateBook_RetireStillRefusedIsAFailureNamingTheDup(t *testing.T) {
	s := ddRealStore(t)
	const vg = "vg-keeps-changing"
	keeper, dup, file := ddPrimaryPair(t, s, vg)

	err := ddMergeDuplicateBook(ddFlipOnRetire{PebbleStore: s, dupID: dup.ID}, keeper, dup, false, nil)
	if err == nil {
		t.Fatal("err = nil, want a failure: the dup could not be retired")
	}
	if errors.Is(err, errDDRefused) {
		t.Fatalf("err = %v is a refusal; after the files moved it must count as a failure", err)
	}
	var tally ddTally
	tally.record(err)
	if tally.failed != 1 {
		t.Fatalf("tally = %+v, want it counted as failed", tally)
	}
	if !strings.Contains(err.Error(), dup.ID) || !strings.Contains(err.Error(), "left live and empty") {
		t.Fatalf("err = %v, want it to name the emptied dup %s", err, dup.ID)
	}
	if g := ddMustGet(t, s, dup.ID); g.IsSoftDeleted() {
		t.Fatal("dup retired although every attempt was refused")
	}
	if k := ddMustGet(t, s, keeper.ID); k.IsPrimaryVersion != nil && *k.IsPrimaryVersion {
		t.Fatal("the keeper kept a promotion whose retire was refused")
	}
	ddAssertMovedToKeeper(t, s, keeper.ID, dup.ID, file.FilePath, "")
}

// (b) A pair where one book is already retired: apply re-reads the rows and
// refuses; dry-run must refuse the same way, whether this run retired the
// book (the overlay) or the caller's copy shows it retired.
func TestDDMergeDuplicateBook_DryRunRefusesRetiredLikeApply(t *testing.T) {
	for _, which := range []string{"keeper", "dup"} {
		t.Run(which, func(t *testing.T) {
			s := ddRealStore(t)
			keeper, dup, _ := ddPrimaryPair(t, s, "vg-"+which)
			gone := dup
			if which == "keeper" {
				gone = keeper
			}

			sim := newDDSim()
			sim.retire(gone.ID, "")
			k, d := *keeper, *dup
			if err := ddMergeDuplicateBookSim(s, sim, &k, &d, true, nil); !errors.Is(err, errDDRefused) {
				t.Fatalf("dry-run err = %v, want errDDRefused", err)
			}

			yes := true
			if _, err := s.ModifyBook(gone.ID, func(b *database.Book) error {
				b.MarkedForDeletion = &yes
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			k, d = *keeper, *dup
			if err := ddMergeDuplicateBookSim(s, newDDSim(), &k, &d, false, nil); !errors.Is(err, errDDRefused) {
				t.Fatalf("apply err = %v, want errDDRefused", err)
			}
		})
	}
}
