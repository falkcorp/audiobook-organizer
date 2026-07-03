// file: internal/plugins/maintenance/itunes_regroup_test.go
// version: 1.1.0
// guid: 6f7a8b9c-0d1e-2f3a-4b5c-6d7e8f9a0b1c
// last-edited: 2026-07-03

package maintenance

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

func regroupStore(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	// Memdb warmup publishes asynchronously; writes made before it publishes
	// are invisible to memdb-backed reads (see PebbleStore.WaitForWarmup).
	s.WaitForWarmup()
	t.Cleanup(func() { s.Close() })
	return s
}

func seedBook(t *testing.T, s *database.PebbleStore, title string) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{Title: title})
	if err != nil || b == nil {
		t.Fatalf("CreateBook(%q): %v", title, err)
	}
	return b.ID
}

func seedFilePID(t *testing.T, s *database.PebbleStore, bookID, pid string) {
	t.Helper()
	f := &database.BookFile{BookID: bookID, ITunesPersistentID: pid, FilePath: "/x/" + pid + ".m4b"}
	if err := s.CreateBookFile(f); err != nil {
		t.Fatalf("CreateBookFile(%s,%s): %v", bookID, pid, err)
	}
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: pid, BookID: bookID}); err != nil {
		t.Fatalf("CreateExternalIDMapping(%s): %v", pid, err)
	}
}

// End-to-end: two fragment books → one merged book; the emptied fragment is
// deleted; both PIDs and files land on the survivor; title is set.
func TestITunesRegroupApply_MergeAndDelete(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")

	p := &Plugin{}
	rep := &fakeReporter{}
	groups := []itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}

	snap, err := p.buildRegroupSnapshot(context.Background(), s, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	if len(snap.PIDLoc) != 2 || len(snap.Books) != 2 {
		t.Fatalf("snapshot pidloc=%d books=%d, want 2/2", len(snap.PIDLoc), len(snap.Books))
	}
	plan := itunesservice.PlanRegroup(groups, snap)
	if plan.Consolidated != 1 || len(plan.DeleteBooks) != 1 {
		t.Fatalf("plan consolidate=%d deletes=%d, want 1/1", plan.Consolidated, len(plan.DeleteBooks))
	}
	if err := p.applyRegroupPlan(context.Background(), s, plan, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}

	survivor := plan.Groups[0].Target
	loser := b1
	if survivor == b1 {
		loser = b2
	}
	files, _ := s.GetBookFiles(survivor)
	if len(files) != 2 {
		t.Fatalf("survivor has %d files, want 2", len(files))
	}
	for _, pid := range []string{"p1", "p2"} {
		if id, _ := s.GetBookByExternalID("itunes", pid); id != survivor {
			t.Errorf("PID %s -> %q, want survivor %q", pid, id, survivor)
		}
	}
	if b, _ := s.GetBookByID(loser); b != nil {
		t.Errorf("loser %s not deleted", loser)
	}
	if sb, _ := s.GetBookByID(survivor); sb == nil || sb.Title != "Merged Book" {
		t.Errorf("survivor title = %v, want 'Merged Book'", sb)
	}
}

// The delete guard must refuse to delete a projected-empty book that still has a
// residual ext-id mapping (zero files ≠ zero mappings — the canary lesson).
func TestITunesRegroupApply_DeleteGuardSkipsResidualExtID(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")
	// Residual mapping on b2 that NO group claims (e.g. a PID no longer in the XML).
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "resid", BookID: b2}); err != nil {
		t.Fatalf("seed residual: %v", err)
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	groups := []itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}
	snap, _ := p.buildRegroupSnapshot(context.Background(), s, rep)
	plan := itunesservice.PlanRegroup(groups, snap)

	survivor := plan.Groups[0].Target
	if survivor != b1 {
		t.Skipf("survivor was %s not b1 (tiebreak); residual-guard case needs b2 to be the loser", survivor)
	}
	if err := p.applyRegroupPlan(context.Background(), s, plan, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v", err)
	}
	// b2 still has the residual mapping → must NOT have been deleted.
	if b, _ := s.GetBookByID(b2); b == nil {
		t.Fatalf("b2 deleted despite residual ext-id (guard failed)")
	}
}
