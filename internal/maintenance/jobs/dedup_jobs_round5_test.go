// file: internal/maintenance/jobs/dedup_jobs_round5_test.go
// version: 1.0.0
// guid: 8e4a1c73-2f6b-4d90-a5c8-3b7e9f1d2a64
// last-edited: 2026-09-13

package jobs

import (
	"errors"
	"reflect"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Follow-ups to #3395's final review, on a real PebbleStore. They use only
// helpers and signatures that exist on 05e79341e, so they compile against it
// and show it failing.

// ddMidMergeWriter runs writer once, at the move -- after the merge planned
// its primary hand-off and before it retires the dup -- to stand in for a
// concurrent writer landing in that window.
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

// (a) The dup was the planned primary, but between the plan and the
// soft-delete another writer demoted it and promoted a third member. Retiring
// on the stale plan promoted the keeper as well: two primaries. The retire
// must be refused, and the keeper's promotion undone.
func TestDDMergeDuplicateBook_PrimaryChangedAfterPlanIsRefused(t *testing.T) {
	s := ddRealStore(t)
	const vg = "vg-stale-plan"
	keeper, dup, _ := ddPrimaryPair(t, s, vg)
	no := false
	z := ddMustBook(t, s, &database.Book{Title: "Z", FilePath: "/lib/Z/z.m4b"})
	ddSetGroup(t, s, z.ID, vg, &no)
	yes := true
	store := ddMidMergeWriter{PebbleStore: s, writer: func() {
		ddSetGroup(t, s, dup.ID, vg, &no)
		ddSetGroup(t, s, z.ID, vg, &yes)
	}}

	err := ddMergeDuplicateBook(store, keeper, dup, false, nil)
	if !errors.Is(err, errDDRefused) {
		t.Fatalf("err = %v, want errDDRefused", err)
	}
	if g := ddMustGet(t, s, dup.ID); g.IsSoftDeleted() {
		t.Fatal("dup retired on a stale hand-off plan")
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); !reflect.DeepEqual(got, []string{"Z"}) {
		t.Fatalf("live primaries = %v, want only the concurrent writer's Z", got)
	}
}

// (a) The planned successor was retired between the plan and the
// soft-delete. Promoting it made a deleted row the primary and the dup's
// demote left no live primary. The retire must be refused with the dup still
// the group's primary.
func TestDDMergeDuplicateBook_SuccessorRetiredAfterPlanIsRefused(t *testing.T) {
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
	store := ddMidMergeWriter{PebbleStore: s, writer: func() {
		if _, err := s.ModifyBook(m.ID, func(b *database.Book) error {
			b.MarkedForDeletion = &yes
			return nil
		}); err != nil {
			t.Errorf("retire M: %v", err)
		}
	}}

	err := ddMergeDuplicateBook(store, ddMustGet(t, s, keeper.ID), ddMustGet(t, s, dup.ID), false, nil)
	if !errors.Is(err, errDDRefused) {
		t.Fatalf("err = %v, want errDDRefused", err)
	}
	if g := ddMustGet(t, s, dup.ID); g.IsSoftDeleted() {
		t.Fatal("dup retired although its planned successor was gone")
	}
	if g := ddMustGet(t, s, m.ID); g.IsPrimaryVersion != nil && *g.IsPrimaryVersion {
		t.Fatal("a retired book was promoted to primary")
	}
	if got := ddLiveExplicitPrimaries(t, s, vg); !reflect.DeepEqual(got, []string{"Dup"}) {
		t.Fatalf("live primaries = %v, want the dup still primary", got)
	}
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
