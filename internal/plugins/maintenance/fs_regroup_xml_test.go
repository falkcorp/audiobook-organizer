// file: internal/plugins/maintenance/fs_regroup_xml_test.go
// version: 1.0.0
// guid: 2a7c5e91-8d34-4b6f-a012-9f3e7c1d56ab
// last-edited: 2026-06-21

package maintenance

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// resultLog returns the final "APPLIED — …" summary the apply path logs.
func resultLog(t *testing.T, logs []string) string {
	t.Helper()
	for i := len(logs) - 1; i >= 0; i-- {
		if strings.HasPrefix(logs[i], "APPLIED —") {
			return logs[i]
		}
	}
	t.Fatalf("no APPLIED summary in logs: %v", logs)
	return ""
}

// Happy path: shattered members that own NO BookFile rows (the virtual-segment model,
// the 595 healed on prod). Survivor gains a BookFile per member; shells are deleted.
func TestApplyFSRegroup_HappyPath_ZeroFileShells(t *testing.T) {
	s := regroupStore(t)
	survivor := seedBook(t, s, "") // title empty — prefix becomes the title
	shellA := seedBook(t, s, "")
	shellB := seedBook(t, s, "")

	base := "/lib/Adrian Tchaikovsky/Cage of Souls - Cage of Souls"
	target := itunesservice.FSRegroupTarget{
		BookFolder: base,
		Title:      "Cage of Souls",
		SurvivorID: survivor,
		Members: []itunesservice.FSBook{
			{ID: survivor, FilePath: base + "/Cage of Souls - 1/01.mp3", DurationSec: 1200},
			{ID: shellA, FilePath: base + "/Cage of Souls - 2/01.mp3", DurationSec: 1300},
			{ID: shellB, FilePath: base + "/Cage of Souls - 3/01.mp3", DurationSec: 1400},
		},
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	if err := p.applyFSRegroup(context.Background(), s, []itunesservice.FSRegroupTarget{target}, 0, rep); err != nil {
		t.Fatalf("applyFSRegroup: %v", err)
	}

	files, _ := s.GetBookFiles(survivor)
	if len(files) != 3 {
		t.Fatalf("survivor has %d files, want 3", len(files))
	}
	if b, _ := s.GetBookByID(shellA); b != nil {
		t.Errorf("shellA not deleted")
	}
	if b, _ := s.GetBookByID(shellB); b != nil {
		t.Errorf("shellB not deleted")
	}
	if sb, _ := s.GetBookByID(survivor); sb == nil || sb.Title != "Cage of Souls" || sb.FilePath != base {
		t.Errorf("survivor title/path = %v, want 'Cage of Souls' @ %q", sb, base)
	}
	if r := resultLog(t, rep.logs); !strings.Contains(r, "delete-skipped=0") || !strings.Contains(r, "healed=1") {
		t.Errorf("result = %q, want healed=1 delete-skipped=0", r)
	}
}

// REGRESSION (the #1548 delete-skipped bug): a non-survivor member that ALREADY owns a
// materialized BookFile row at its path (FileCount==1). The old code called UpsertBookFile,
// which path-matches and preserves the shell's BookID — leaving the file on the shell, so
// the delete-guard skipped it AND the survivor silently lost that chapter's audio. The fix
// reattaches the existing row to the survivor, so the shell empties and is deleted.
func TestApplyFSRegroup_ReattachesOneFileShell(t *testing.T) {
	s := regroupStore(t)
	survivor := seedBook(t, s, "")
	shell := seedBook(t, s, "")

	base := "/lib/Brandon Sanderson/Elantris - Elantris"
	shellPath := base + "/Elantris - 2/01.mp3"
	// Pre-seed a BookFile row OWNED BY THE SHELL at the shell's chapter path.
	if err := s.CreateBookFile(&database.BookFile{BookID: shell, FilePath: shellPath, Duration: 999}); err != nil {
		t.Fatalf("seed shell file: %v", err)
	}

	target := itunesservice.FSRegroupTarget{
		BookFolder: base,
		Title:      "Elantris",
		SurvivorID: survivor,
		Members: []itunesservice.FSBook{
			{ID: survivor, FilePath: base + "/Elantris - 1/01.mp3", DurationSec: 1200},
			{ID: shell, FilePath: shellPath, DurationSec: 1300},
		},
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	if err := p.applyFSRegroup(context.Background(), s, []itunesservice.FSRegroupTarget{target}, 0, rep); err != nil {
		t.Fatalf("applyFSRegroup: %v", err)
	}

	// The pre-existing file must have MOVED to the survivor (not stayed on the shell).
	sfiles, _ := s.GetBookFiles(survivor)
	if len(sfiles) != 2 {
		t.Fatalf("survivor has %d files, want 2 (own + reattached)", len(sfiles))
	}
	if sh, _ := s.GetBookFiles(shell); len(sh) != 0 {
		t.Errorf("shell still owns %d files, want 0 (reattach failed → would orphan audio)", len(sh))
	}
	if b, _ := s.GetBookByID(shell); b != nil {
		t.Errorf("shell not deleted (delete-skipped bug regressed)")
	}
	// The reattached row must preserve its identity (the move keeps the existing row).
	moved, _ := s.GetBookFileByPath(shellPath)
	if moved == nil || moved.BookID != survivor {
		t.Errorf("reattached row = %v, want BookID=%s", moved, survivor)
	}
	if r := resultLog(t, rep.logs); !strings.Contains(r, "delete-skipped=0") {
		t.Errorf("result = %q, want delete-skipped=0", r)
	}
}

// Integration: a real shattered folder layout (Cage of Souls / N) routed through the
// PURE planner guard (GroupShatteredBooks, prefix⊆parent) and then the apply path,
// collapsing N chapter shells into ONE survivor book.
func TestFSRegroup_ShatteredFolder_HealsToOneBook(t *testing.T) {
	s := regroupStore(t)
	base := "/lib/Adrian Tchaikovsky/Cage of Souls - Cage of Souls"
	var fsbooks []itunesservice.FSBook
	var ids []string
	for i := 1; i <= 4; i++ {
		id := seedBook(t, s, "")
		ids = append(ids, id)
		fsbooks = append(fsbooks, itunesservice.FSBook{
			ID:        id,
			FilePath:  base + "/Cage of Souls - " + itoa(i) + "/01.mp3",
			IsPrimary: true,
		})
	}

	targets, st := itunesservice.GroupShatteredBooks(fsbooks)
	if len(targets) != 1 {
		t.Fatalf("planner produced %d targets, want 1 (stats: %+v)", len(targets), st)
	}
	if targets[0].Title != "Cage of Souls" {
		t.Errorf("title = %q, want 'Cage of Souls'", targets[0].Title)
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	if err := p.applyFSRegroup(context.Background(), s, targets, 0, rep); err != nil {
		t.Fatalf("applyFSRegroup: %v", err)
	}

	survivors := 0
	for _, id := range ids {
		if b, _ := s.GetBookByID(id); b != nil {
			survivors++
		}
	}
	if survivors != 1 {
		t.Fatalf("%d books remain, want 1 (shattered → 1)", survivors)
	}
	files, _ := s.GetBookFiles(targets[0].SurvivorID)
	if len(files) != 4 {
		t.Errorf("survivor has %d files, want 4", len(files))
	}
}

// itoa avoids importing strconv for a single tiny use.
func itoa(n int) string { return string(rune('0' + n)) }

// External-id mappings on a shell must be reassigned to the survivor before the shell
// is deleted (so iTunes PID linkage survives the heal).
func TestApplyFSRegroup_ReassignsExternalIDs(t *testing.T) {
	s := regroupStore(t)
	survivor := seedBook(t, s, "")
	shell := seedBook(t, s, "")
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "PID-X", BookID: shell}); err != nil {
		t.Fatalf("seed ext-id: %v", err)
	}

	base := "/lib/Author/Book - Book"
	target := itunesservice.FSRegroupTarget{
		BookFolder: base,
		Title:      "Book",
		SurvivorID: survivor,
		Members: []itunesservice.FSBook{
			{ID: survivor, FilePath: base + "/Book - 1/01.mp3", DurationSec: 100},
			{ID: shell, FilePath: base + "/Book - 2/01.mp3", DurationSec: 200},
		},
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	if err := p.applyFSRegroup(context.Background(), s, []itunesservice.FSRegroupTarget{target}, 0, rep); err != nil {
		t.Fatalf("applyFSRegroup: %v", err)
	}
	if id, _ := s.GetBookByExternalID("itunes", "PID-X"); id != survivor {
		t.Errorf("PID-X -> %q, want survivor %q", id, survivor)
	}
	if b, _ := s.GetBookByID(shell); b != nil {
		t.Errorf("shell not deleted after ext-id reassign")
	}
}

// When ext-id reassignment FAILS, the shell must be skip-counted and NOT deleted
// (so its mappings aren't orphaned). Forced via a mock that errors on reassign.
func TestApplyFSRegroup_ExtIDErrorSkipsDelete(t *testing.T) {
	survivor := &database.Book{ID: "surv"}
	deleted := map[string]bool{}
	mock := &database.MockStore{
		GetBookByIDFunc:       func(id string) (*database.Book, error) { return survivor, nil },
		GetBookFileByPathFunc: func(string) (*database.BookFile, error) { return nil, nil },
		CreateBookFileFunc:    func(*database.BookFile) error { return nil },
		UpdateBookFunc:        func(_ string, b *database.Book) (*database.Book, error) { return b, nil },
		GetExternalIDsForBookFunc: func(string) ([]database.ExternalIDMapping, error) {
			return []database.ExternalIDMapping{{ExternalID: "p", BookID: "shell"}}, nil
		},
		ReassignExternalIDsFunc: func(_, _ string) error { return context.DeadlineExceeded },
		GetBookFilesFunc:        func(string) ([]database.BookFile, error) { return nil, nil },
		DeleteBookFunc:          func(id string) error { deleted[id] = true; return nil },
	}

	base := "/lib/A/B - B"
	target := itunesservice.FSRegroupTarget{
		BookFolder: base, Title: "B", SurvivorID: "surv",
		Members: []itunesservice.FSBook{
			{ID: "surv", FilePath: base + "/B - 1/01.mp3"},
			{ID: "shell", FilePath: base + "/B - 2/01.mp3"},
		},
	}

	p := &Plugin{}
	rep := &fakeReporter{}
	// A reassign failure is skip-counted (delete-skipped), not an errCount → apply returns nil.
	if err := p.applyFSRegroup(context.Background(), mock, []itunesservice.FSRegroupTarget{target}, 0, rep); err != nil {
		t.Fatalf("applyFSRegroup: %v", err)
	}
	if deleted["shell"] {
		t.Errorf("shell deleted despite ext-id reassign failure (orphans mappings)")
	}
	if r := resultLog(t, rep.logs); !strings.Contains(r, "delete-skipped=1") {
		t.Errorf("result = %q, want delete-skipped=1", r)
	}
}
