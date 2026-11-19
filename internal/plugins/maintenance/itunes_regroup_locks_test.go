// file: internal/plugins/maintenance/itunes_regroup_locks_test.go
// version: 1.0.0
// guid: 9d0e4b71-2c35-4f86-a1b9-5e7c3d8a6f20
// last-edited: 2026-11-18

package maintenance

import (
	"context"
	"fmt"
	"strings"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// regroupLockStore wraps a real store so a test can make the lock read fail
// for chosen books while every other call goes straight through.
type regroupLockStore struct {
	*database.PebbleStore
	unreadable map[string]bool
}

func (s regroupLockStore) GetMetadataFieldStates(id string) ([]database.MetadataFieldState, error) {
	if s.unreadable[id] {
		return nil, fmt.Errorf("field states for %s: store closed", id)
	}
	return s.PebbleStore.GetMetadataFieldStates(id)
}

// applyRegroupRetitle seeds an iTunes-only book whose files already form the
// plan's one group, locks/breaks per the arguments, applies the plan and
// returns the book's title afterwards with the APPLIED log line.
func applyRegroupRetitle(t *testing.T, lockTitle, unreadable bool) (title, applied string, err error) {
	t.Helper()
	s := regroupStore(t)
	b, cerr := s.CreateBook(&database.Book{Title: "Canonical Title"})
	if cerr != nil || b == nil {
		t.Fatalf("CreateBook: %v", cerr)
	}
	rgSeedFileAt(t, s, b.ID, "p1", rgITunes+"/Album/01.m4b")
	rgSeedFileAt(t, s, b.ID, "p2", rgITunes+"/Album/02.m4b")
	if lockTitle {
		if uerr := s.UpsertMetadataFieldState(&database.MetadataFieldState{
			BookID: b.ID, Field: database.FieldKeyTitle, OverrideLocked: true,
		}); uerr != nil {
			t.Fatalf("lock title: %v", uerr)
		}
	}
	rep := &fakeReporter{}
	snap, serr := buildRegroupSnapshot(context.Background(), s, rgRoot, t6g5RgReporter(context.Background(), rep))
	if serr != nil {
		t.Fatalf("buildRegroupSnapshot: %v", serr)
	}
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "iTunes Album Tag", PIDs: []string{"p1", "p2"}}}, snap)
	if a := plan.Groups[0]; a.Target != b.ID || a.KeepTitle {
		t.Fatalf("plan = %+v, want an already-correct group on %s that is allowed to retitle", a, b.ID)
	}
	st := regroupLockStore{PebbleStore: s, unreadable: map[string]bool{b.ID: unreadable}}
	_, err = t6g5ApplyRegroupPlan(context.Background(), st, plan, rgRoot, t6g5RgReporter(context.Background(), rep), (&Plugin{}).t6g5RegroupUserStateFor)
	after, gerr := s.GetBookByID(b.ID)
	if gerr != nil || after == nil {
		t.Fatalf("GetBookByID: %v", gerr)
	}
	for _, l := range rep.logs {
		if strings.HasPrefix(l, "APPLIED") {
			applied = l
		}
	}
	return after.Title, applied, err
}

func TestITunesRegroupApply_UnlockedSurvivorIsRetitled(t *testing.T) {
	title, applied, err := applyRegroupRetitle(t, false, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if title != "iTunes Album Tag" {
		t.Fatalf("control: title = %q, want the iTunes album tag", title)
	}
	if !strings.Contains(applied, "titled=1 ") || !strings.Contains(applied, "user-title-locked=0 ") {
		t.Fatalf("APPLIED line %q", applied)
	}
}

// The user locked the survivor's title: the iTunes album tag does not replace it.
func TestITunesRegroupApply_UserLockedTitleIsKept(t *testing.T) {
	title, applied, err := applyRegroupRetitle(t, true, false)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if title != "Canonical Title" {
		t.Fatalf("title = %q: a user-locked title was overwritten", title)
	}
	if !strings.Contains(applied, "titled=0 ") || !strings.Contains(applied, "user-title-locked=1 ") {
		t.Fatalf("APPLIED line %q, want titled=0 user-title-locked=1", applied)
	}
}

// A lock set that cannot be read writes nothing and fails the run.
func TestITunesRegroupApply_UnreadableLocksWriteNoTitle(t *testing.T) {
	title, _, err := applyRegroupRetitle(t, false, true)
	if err == nil {
		t.Fatalf("apply succeeded although the lock read failed")
	}
	if title != "Canonical Title" {
		t.Fatalf("title = %q: the title was written without knowing the locks", title)
	}
}
