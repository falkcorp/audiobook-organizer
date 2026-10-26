// file: internal/plugins/maintenance/itunes_regroup_retired_test.go
// version: 1.0.0
// guid: 2b6e90c4-7d15-4a83-9c2f-61e8f3b5a047
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// The regroup snapshot keeps a PID that only a retired row holds out of PIDLoc.
// The retired row is keyed under its holder, so an entry for it would make the
// holder look like the current home of that track and the planner would retitle
// the holder from the track's album and move files out of it. The live PID of
// the same holder stays.
func TestBuildRegroupSnapshot_RetiredRowPIDIsNotLocated(t *testing.T) {
	s := regroupStore(t)
	holder := seedBook(t, s, "Holder Book")
	shell := seedBook(t, s, "Trashed Shell")
	seedFilePID(t, s, holder, "p-live")
	seedFilePID(t, s, shell, "p-retired")

	now := time.Now()
	v, err := s.CreateBookVersion(&database.BookVersion{
		BookID: holder, Status: database.BookVersionStatusRetired, Format: "m4b", Source: "retired_book",
		RetiredFromBookID: shell, RetireOpID: "op-1", RetiredAt: &now,
	})
	if err != nil {
		t.Fatalf("CreateBookVersion: %v", err)
	}
	rows, err := s.GetBookFiles(shell)
	if err != nil || len(rows) != 1 {
		t.Fatalf("GetBookFiles(shell) = %d rows, err %v", len(rows), err)
	}
	if err := s.RetireBookFiles([]database.BookFileMove{{FileIDs: []string{rows[0].ID}, SourceBookID: shell}}, holder, v.ID); err != nil {
		t.Fatalf("RetireBookFiles: %v", err)
	}

	p := &Plugin{}
	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, &fakeReporter{})
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	if _, ok := snap.PIDLoc["p-retired"]; ok {
		t.Errorf("PIDLoc locates the retired PID: %+v", snap.PIDLoc["p-retired"])
	}
	loc, ok := snap.PIDLoc["p-live"]
	if !ok {
		t.Fatalf("PIDLoc lost the live PID: %+v", snap.PIDLoc)
	}
	if loc.BookID != holder {
		t.Errorf("live PID located on %q, want the holder %q", loc.BookID, holder)
	}
}
