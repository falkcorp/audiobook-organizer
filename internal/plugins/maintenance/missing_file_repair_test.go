// file: internal/plugins/maintenance/missing_file_repair_test.go
// version: 1.0.0
// guid: c47f0a91-5e6b-4d28-93af-2b8054e17c6d
// last-edited: 2026-08-17

package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// repairFixture reuses the audit fixture's shape — real files on disk, real
// absent paths beside them — and adds a row that cannot be stat'd at all.
//
// The NUL byte is rejected by Go before it reaches the syscall, so it produces
// an error that is neither nil nor IsNotExist on every platform. That is the
// "I could not tell" case, and it is the one that must never license a delete.
func repairFixture(t *testing.T) []database.BookFileCore {
	t.Helper()
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
		return p
	}
	return []database.BookFileCore{
		// intact — nothing to do.
		{ID: "f1", BookID: "intact", FilePath: write("intact-1.m4b")},
		// partial — the live shape. f2 is a phantom row; f3 survives.
		{ID: "f2", BookID: "partial", FilePath: filepath.Join(dir, "gone.m4b")},
		{ID: "f3", BookID: "partial", FilePath: write("partial-real.m4b")},
		// allgone — every row dead. MUST NOT be touched.
		{ID: "f4", BookID: "allgone", FilePath: filepath.Join(dir, "vanished-1.m4b")},
		{ID: "f5", BookID: "allgone", FilePath: filepath.Join(dir, "vanished-2.m4b")},
		// unsure — one dead row, one un-stat-able row. MUST NOT be touched.
		{ID: "f6", BookID: "unsure", FilePath: filepath.Join(dir, "missing.m4b")},
		{ID: "f7", BookID: "unsure", FilePath: filepath.Join(dir, "cannot\x00stat.m4b")},
	}
}

func planRepair(t *testing.T, rows []database.BookFileCore, params missingFileRepairParams) repairPlan {
	t.Helper()
	store := &database.MockStore{
		GetAllBookFilesCoreFunc: func() ([]database.BookFileCore, error) { return rows, nil },
	}
	plan, err := planMissingFileRepair(context.Background(), store, params, &fakeReporter{})
	if err != nil {
		t.Fatalf("planMissingFileRepair: %v", err)
	}
	return plan
}

// TestMissingFileRepair_DeletesOnlyFromBooksThatKeepAFile is the whole contract.
func TestMissingFileRepair_DeletesOnlyFromBooksThatKeepAFile(t *testing.T) {
	plan := planRepair(t, repairFixture(t), missingFileRepairParams{})

	got := append([]string(nil), plan.RowsFlagged...)
	sort.Strings(got)
	if len(got) != 1 || got[0] != "f2" {
		t.Fatalf("only f2 (dead row in a book that keeps f3) may be deleted; got %v", got)
	}

	for _, c := range []struct {
		name      string
		got, want int
	}{
		{"BooksExamined", plan.BooksExamined, 4},
		{"BooksRepairable", plan.BooksRepairable, 1},
		{"BooksFullyBroken", plan.BooksFullyBroken, 1},
		{"BooksSkippedUnreadable", plan.BooksSkippedUnreadable, 1},
		{"BooksIntact", plan.BooksIntact, 1},
	} {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestMissingFileRepair_NeverEmptiesABook is the property that made this a
// decision rather than a cleanup: deleting every row of a book whose files are
// all gone turns a wrong index into a book with nothing at all.
func TestMissingFileRepair_NeverEmptiesABook(t *testing.T) {
	plan := planRepair(t, repairFixture(t), missingFileRepairParams{})
	for _, id := range plan.RowsFlagged {
		if id == "f4" || id == "f5" {
			t.Fatalf("row %s belongs to a book with NO surviving file and must never be deleted; plan=%v",
				id, plan.RowsFlagged)
		}
	}
	if plan.BooksFullyBroken != 1 {
		t.Fatalf("the fully-broken book must be counted for review, got %d", plan.BooksFullyBroken)
	}
	if len(plan.FullyBroken) != 1 || plan.FullyBroken[0] != "allgone" {
		t.Fatalf("the fully-broken book must be NAMED so it can be looked at by hand, got %v", plan.FullyBroken)
	}
}

// TestMissingFileRepair_UnreadableBlocksTheWholeBook covers the unmounted-share
// scenario: a path we could not stat must not be read as "the file is gone", and
// must protect its siblings too.
func TestMissingFileRepair_UnreadableBlocksTheWholeBook(t *testing.T) {
	plan := planRepair(t, repairFixture(t), missingFileRepairParams{})
	for _, id := range plan.RowsFlagged {
		if id == "f6" {
			t.Fatal("f6 is a confirmed-missing row, but its book also has an un-stat-able path; " +
				"the book must be skipped entirely rather than partially pruned")
		}
	}
	if plan.BooksSkippedUnreadable != 1 {
		t.Fatalf("expected 1 book skipped for unreadability, got %d", plan.BooksSkippedUnreadable)
	}
}

// TestMissingFileRepair_MaxDeletesCaps verifies the blast-radius limit truncates
// and says so, rather than silently doing less than it reports.
func TestMissingFileRepair_MaxDeletesCaps(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live.m4b")
	if err := os.WriteFile(live, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	rows := []database.BookFileCore{{ID: "keep", BookID: "b", FilePath: live}}
	for i := range 10 {
		rows = append(rows, database.BookFileCore{
			ID:       filepath.Join("dead", string(rune('a'+i))),
			BookID:   "b",
			FilePath: filepath.Join(dir, "gone-"+string(rune('a'+i))+".m4b"),
		})
	}

	plan := planRepair(t, rows, missingFileRepairParams{MaxFlagged: 4})
	if len(plan.RowsFlagged) != 4 {
		t.Fatalf("MaxDeletes=4 should cap the plan at 4 rows, got %d", len(plan.RowsFlagged))
	}
	if plan.CappedAt != 4 {
		t.Fatalf("a truncated plan must report CappedAt so the run is known to be partial, got %d", plan.CappedAt)
	}
}

// TestMissingFileRepair_ApplyIsOptIn pins that the destructive direction is what
// you have to ask for, not what you get by forgetting a flag.
func TestMissingFileRepair_ApplyIsOptIn(t *testing.T) {
	var p missingFileRepairParams
	if p.Apply {
		t.Fatal("the zero value of missingFileRepairParams must be a DRY RUN")
	}
}

// TestMissingFileRepair_ApplyIsRejected locks in the 2026-08-19 decision that this
// operation never deletes.
//
// It replaces two tests that asserted the delete path executed the plan exactly.
// Those tests were correct about the code and wrong about the requirement: the
// full-population audit found that rows the plan classified as safe to prune are
// the only pointer to files present on disk under a different name.
//
// Asserting an ERROR rather than a silent no-op is the point. A parameter that
// quietly stops doing what its name says is the silent-failure shape this codebase
// works to remove, and any caller still passing apply is acting on a stale belief.
func TestMissingFileRepair_ApplyIsRejected(t *testing.T) {
	deleteCalled := false
	store := &database.MockStore{
		DeleteBookFilesByIDsFunc: func(_ []string) error { deleteCalled = true; return nil },
	}
	p := &Plugin{deps: fakeDeps{store: store}}

	err := p.runMissingFileRepair(context.Background(), json.RawMessage(`{"apply": true}`), &fakeReporter{})
	if err == nil {
		t.Fatal("{\"apply\": true} must be rejected, not silently ignored")
	}
	if !strings.Contains(err.Error(), "never deletes") {
		t.Fatalf("the error must say why, got: %v", err)
	}
	if deleteCalled {
		t.Fatal("no delete may be issued under any params")
	}
}

// TestMissingFileRepairDef_DeclaresNoWriteCapability guards the removal at the
// capability layer as well as the code layer. An op that cannot request
// CapLibraryWrite cannot be walked back into deleting by a later edit without
// someone explicitly re-adding the capability and being asked why.
func TestMissingFileRepairDef_DeclaresNoWriteCapability(t *testing.T) {
	def := (&Plugin{}).missingFileRepairDef()
	for _, c := range def.Capabilities {
		if c == sdk.CapLibraryWrite {
			t.Fatal("missing-file-repair must not declare CapLibraryWrite; it reports, it does not write")
		}
	}
}
