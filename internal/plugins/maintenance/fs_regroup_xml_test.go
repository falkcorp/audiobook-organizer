// file: internal/plugins/maintenance/fs_regroup_xml_test.go
// version: 2.0.0
// guid: 2a7c5e91-8d34-4b6f-a012-9f3e7c1d56ab
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// itoa is shared with regroup_shattered_ai_test.go.
func itoa(n int) string { return strconv.Itoa(n) }

// resultLog returns the last log line with the given prefix.
func resultLog(t *testing.T, logs []string, prefix string) string {
	t.Helper()
	for _, log := range slices.Backward(logs) {
		if strings.HasPrefix(log, prefix) {
			return log
		}
	}
	t.Fatalf("no %q line in logs: %v", prefix, logs)
	return ""
}

// fsQueue is an OpQueueReader returning fixed active operations.
type fsQueue struct {
	rows []database.OperationV2Row
	err  error
}

func (q fsQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) { return q.rows, q.err }

// fsGuardStore counts hard deletes and can fail path lookups. The op must
// never hard-delete a book; the store slice it uses has no book_file delete.
type fsGuardStore struct {
	*database.PebbleStore
	hardDeletes atomic.Int64
	lookupErr   error
}

func (s *fsGuardStore) DeleteBook(id string) error {
	s.hardDeletes.Add(1)
	return s.PebbleStore.DeleteBook(id)
}

func (s *fsGuardStore) GetBookFileByPath(p string) (*database.BookFile, error) {
	if s.lookupErr != nil {
		return nil, s.lookupErr
	}
	return s.PebbleStore.GetBookFileByPath(p)
}

func fsSeedBook(t *testing.T, s *database.PebbleStore, title, path string) string {
	t.Helper()
	b, err := s.CreateBook(&database.Book{Title: title, FilePath: path})
	if err != nil || b == nil {
		t.Fatalf("CreateBook(%q): %v", path, err)
	}
	return b.ID
}

func fsSeedRow(t *testing.T, s *database.PebbleStore, bookID, path string) {
	t.Helper()
	if err := s.CreateBookFile(&database.BookFile{BookID: bookID, FilePath: path}); err != nil {
		t.Fatalf("CreateBookFile(%s, %q): %v", bookID, path, err)
	}
}

func fsRowCount(t *testing.T, s *database.PebbleStore) int {
	t.Helper()
	cores, err := s.GetAllBookFilesCore()
	if err != nil {
		t.Fatalf("GetAllBookFilesCore: %v", err)
	}
	return len(cores)
}

func fsPlan(t *testing.T, s regroupSnapshotReader) *fsRepairPlan {
	t.Helper()
	plan, err := planFSRepair(context.Background(), s, &fakeReporter{})
	if err != nil {
		t.Fatalf("planFSRepair: %v", err)
	}
	return plan
}

func fsGroupsOf(plan *fsRepairPlan, cat string) []fsRepairGroup {
	var out []fsRepairGroup
	for _, g := range plan.Groups {
		if g.Category == cat {
			out = append(out, g)
		}
	}
	return out
}

func fsLedgerTypes(t *testing.T, s *database.PebbleStore, opID string) map[string]int {
	t.Helper()
	changes, err := s.GetOperationChanges(opID)
	if err != nil {
		t.Fatalf("GetOperationChanges: %v", err)
	}
	out := map[string]int{}
	for _, c := range changes {
		out[c.ChangeType]++
	}
	return out
}

func fsSoftDeleted(t *testing.T, s *database.PebbleStore, id string) bool {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("book %s is gone (err=%v) — the op must never hard-delete", id, err)
	}
	return b.IsSoftDeleted()
}

// seedFragments seeds n chapter-fragment books under base; the first one owns
// a book_file row at its path, the rest own none (both shapes are in prod).
func seedFragments(t *testing.T, s *database.PebbleStore, base, prefix string, n int) []string {
	t.Helper()
	var ids []string
	for i := 1; i <= n; i++ {
		p := fmt.Sprintf("%s/%s - %d/%d.mp3", base, prefix, i, n)
		id := fsSeedBook(t, s, "", p)
		if i == 1 {
			fsSeedRow(t, s, id, p)
		}
		ids = append(ids, id)
	}
	return ids
}

const fsFragBase = "/lib/Adrian Tchaikovsky/Cage of Souls - Cage of Souls"

// Category 1, dry run: the default run reports the group and its plan line
// and writes nothing.
func TestFsRegroupFragments_DryRunReportsPlanAndWritesNothing(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase, "Cage of Souls", 3)
	before := fsRowCount(t, s)

	p := &Plugin{deps: &fakeDeps{store: s}}
	rep := &opIDReporter{id: "op-dry"}
	if err := p.runFSRegroupXML(context.Background(), nil, rep); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	sum := resultLog(t, rep.logs, "DRY RUN PLAN:")
	if !strings.Contains(sum, "fragments: groups=1 rows=3") {
		t.Errorf("summary = %q, want fragments groups=1 rows=3", sum)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "PLAN fragments folder=\""+fsFragBase+"\"") {
		t.Errorf("no per-group plan line in %v", rep.logs)
	}
	for _, id := range ids {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("dry run soft-deleted %s", id)
		}
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("dry run changed book_file rows %d → %d", before, got)
	}
	if n := len(fsLedgerTypes(t, s, "op-dry")); n != 0 {
		t.Errorf("dry run wrote %d ledger types", n)
	}
}

// Category 1, apply: rows move onto the survivor, a row is created for each
// chapter path with none, shells are soft-deleted (never hard), and every
// change is journaled under the op id.
func TestFsRegroupFragments_ApplyMergesSoftDeletesAndJournals(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase, "Cage of Souls", 3)
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: "PID-X", BookID: ids[2]}); err != nil {
		t.Fatalf("seed ext-id: %v", err)
	}
	before := fsRowCount(t, s)
	g := &fsGuardStore{PebbleStore: s}
	plan := fsPlan(t, g)
	frag := fsGroupsOf(plan, fsCatFragments)
	if len(frag) != 1 {
		t.Fatalf("fragments groups = %d, want 1 (plan %s)", len(frag), plan.summary())
	}
	survivor := frag[0].SurvivorID

	rep := &opIDReporter{id: "op-frag"}
	res, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, rep)
	if err != nil {
		t.Fatalf("apply: %v (%s)", err, res)
	}
	files, _ := s.GetBookFiles(survivor)
	if len(files) != 3 {
		t.Fatalf("survivor has %d rows, want 3", len(files))
	}
	for _, f := range files {
		if _, n, _, ok := chapterPartsForTest(f.FilePath); !ok || f.TrackNumber != n {
			t.Errorf("row %q track = %d, want its folder number", f.FilePath, f.TrackNumber)
		}
	}
	shells := 0
	for _, id := range ids {
		if id == survivor {
			if fsSoftDeleted(t, s, id) {
				t.Errorf("survivor soft-deleted")
			}
			continue
		}
		shells++
		if !fsSoftDeleted(t, s, id) {
			t.Errorf("shell %s not soft-deleted", id)
		}
	}
	if g.hardDeletes.Load() != 0 {
		t.Errorf("hard DeleteBook called %d times", g.hardDeletes.Load())
	}
	if after := fsRowCount(t, s); after < before {
		t.Errorf("book_file rows decreased %d → %d", before, after)
	}
	sb, _ := s.GetBookByID(survivor)
	if sb.Title != "Cage of Souls" || sb.FilePath != fsFragBase {
		t.Errorf("survivor = %q @ %q", sb.Title, sb.FilePath)
	}
	if id, _ := s.GetBookByExternalID("itunes", "PID-X"); id != survivor {
		t.Errorf("PID-X → %q, want survivor", id)
	}
	led := fsLedgerTypes(t, s, "op-frag")
	if led[fsChangeSoftDelete] != shells {
		t.Errorf("book_soft_delete rows = %d, want %d", led[fsChangeSoftDelete], shells)
	}
	if led[fsChangeFileReassign]+led[fsChangeFileCreate] != 2 || res.RowsMoved+res.RowsCreated != 2 {
		t.Errorf("reassign+create rows = %d+%d (res %s), want 2 total", led[fsChangeFileReassign], led[fsChangeFileCreate], res)
	}
	if led["metadata_update"] != 2 {
		t.Errorf("metadata_update rows = %d, want 2 (title, file_path)", led["metadata_update"])
	}
}

// chapterPartsForTest reads the folder number back out of a chapter path.
func chapterPartsForTest(p string) (string, int, string, bool) {
	dir := filepath.Base(filepath.Dir(p))
	i := strings.LastIndex(dir, " - ")
	if i < 0 {
		return "", 0, "", false
	}
	n, err := strconv.Atoi(dir[i+3:])
	return dir[:i], n, "", err == nil
}

// seedDuplicates seeds a multi-file owner at base (each file in its own chapter
// folder, i.e. also a layout book) plus one single-file shell per file.
func seedDuplicates(t *testing.T, s *database.PebbleStore, base string) (owner string, shells []string) {
	t.Helper()
	owner = fsSeedBook(t, s, "Metal Swarm", base+"/Metal Swarm - 1")
	for i := 1; i <= 3; i++ {
		p := fmt.Sprintf("%s/Metal Swarm - %d/103.mp3", base, i)
		fsSeedRow(t, s, owner, p)
		shells = append(shells, fsSeedBook(t, s, "", p))
	}
	return owner, shells
}

// Category 2: dry run classifies the shells as duplicates of the owner; apply
// soft-deletes them and leaves the owner and every row untouched.
func TestFsRegroupDuplicates_DryRunAndApplyRetireShellsOnly(t *testing.T) {
	s := regroupStore(t)
	owner, shells := seedDuplicates(t, s, "/lib/Kevin J. Anderson/Metal Swarm")
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	dup := fsGroupsOf(plan, fsCatDuplicates)
	if len(dup) != 1 || dup[0].OwnerID != owner || dup[0].Rows != 3 {
		t.Fatalf("duplicates = %+v, want one group of 3 owned by %s (plan %s)", dup, owner, plan.summary())
	}
	rep := &fakeReporter{}
	reportFSRepairPlan(plan, true, rep)
	if !strings.Contains(strings.Join(rep.logs, "\n"), "PLAN duplicates") {
		t.Errorf("no duplicates plan line in %v", rep.logs)
	}

	g := &fsGuardStore{PebbleStore: s}
	if _, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-dup"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, id := range shells {
		if !fsSoftDeleted(t, s, id) {
			t.Errorf("shell %s not soft-deleted", id)
		}
	}
	if fsSoftDeleted(t, s, owner) {
		t.Errorf("owner soft-deleted")
	}
	if rows, _ := s.GetBookFiles(owner); len(rows) != 3 {
		t.Errorf("owner rows = %d, want 3", len(rows))
	}
	if after := fsRowCount(t, s); after != before {
		t.Errorf("book_file rows %d → %d, want unchanged", before, after)
	}
	if g.hardDeletes.Load() != 0 {
		t.Errorf("hard DeleteBook called")
	}
	if led := fsLedgerTypes(t, s, "op-dup"); led[fsChangeSoftDelete] != 3 {
		t.Errorf("ledger = %v, want 3 book_soft_delete", led)
	}
}

// Category 3: a book whose files each sit in their own chapter folder is
// detected and planned; the apply refuses and nothing on disk or in the DB
// moves.
func TestFsRegroupLayout_DetectedAndApplyRefuses(t *testing.T) {
	s := regroupStore(t)
	base := filepath.Join(t.TempDir(), "Stephen King", "The Shining")
	book := fsSeedBook(t, s, "The Shining", base+"/The Shining - 1")
	var paths []string
	for i := 1; i <= 10; i++ {
		dir := fmt.Sprintf("%s/The Shining - %d", base, i)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		p := dir + "/58.MP3"
		if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
			t.Fatal(err)
		}
		fsSeedRow(t, s, book, p)
		paths = append(paths, p)
	}
	plan := fsPlan(t, s)
	lay := fsGroupsOf(plan, fsCatLayout)
	if len(lay) != 1 || lay[0].SurvivorID != book || len(lay[0].Moves) != 10 {
		t.Fatalf("layout = %+v, want one book with 10 moves (plan %s)", lay, plan.summary())
	}
	if got, want := lay[0].Moves[0].To, base+"/The Shining - 01.MP3"; got != want {
		t.Errorf("first target = %q, want %q", got, want)
	}
	if !strings.Contains(plan.summary(), "chapter_folder_layout (detection only): books=1 files=10") {
		t.Errorf("summary = %q", plan.summary())
	}

	rep := &opIDReporter{id: "op-lay"}
	_, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{Categories: []string{fsCatLayout}}, rep)
	if err == nil || !strings.Contains(err.Error(), "not implemented") {
		t.Fatalf("layout apply err = %v, want the not-implemented refusal", err)
	}
	for _, p := range paths {
		if _, serr := os.Stat(p); serr != nil {
			t.Errorf("file %q moved: %v", p, serr)
		}
	}
	rows, _ := s.GetBookFiles(book)
	for _, r := range rows {
		if !slices.Contains(paths, r.FilePath) {
			t.Errorf("row repointed to %q", r.FilePath)
		}
	}
	if led := fsLedgerTypes(t, s, "op-lay"); len(led) != 0 {
		t.Errorf("refused apply wrote ledger rows %v", led)
	}
}

// Books under the iTunes tree or a configured protected path are refused in
// every category, before any read of their files.
func TestFsRegroupProtectedPath_Refused(t *testing.T) {
	old := config.AppConfig.ProtectedPaths
	config.AppConfig.ProtectedPaths = []string{"/lib/protected"}
	t.Cleanup(func() { config.AppConfig.ProtectedPaths = old })

	s := regroupStore(t)
	itunes := seedFragments(t, s, "/lib/books/itunes/Neil Gaiman/American Gods - American Gods", "American Gods", 3)
	prot := seedFragments(t, s, "/lib/protected/Neil Gaiman/Mythos", "Mythos", 2)
	layBook := fsSeedBook(t, s, "Kenobi", "/lib/books/itunes/John Jackson Miller/Kenobi/Kenobi - 1")
	for i := 1; i <= 2; i++ {
		fsSeedRow(t, s, layBook, fmt.Sprintf("/lib/books/itunes/John Jackson Miller/Kenobi/Kenobi - %d/102.mp3", i))
	}
	before := fsRowCount(t, s)

	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatProtected)); n != 3 {
		t.Fatalf("protected groups = %d, want 3 (plan %s)", n, plan.summary())
	}
	if n := len(fsGroupsOf(plan, fsCatFragments)) + len(fsGroupsOf(plan, fsCatLayout)); n != 0 {
		t.Fatalf("protected books leaked into appliable categories: %s", plan.summary())
	}
	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-prot"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, id := range append(itunes, prot...) {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("protected book %s soft-deleted", id)
		}
	}
	if after := fsRowCount(t, s); after != before {
		t.Errorf("rows %d → %d", before, after)
	}
	if led := fsLedgerTypes(t, s, "op-prot"); len(led) != 0 {
		t.Errorf("ledger %v, want none", led)
	}
}

// The apply refuses, before writing, when the stand-down cannot be acquired
// and when a library.scan is queued or running.
func TestFsRegroupApply_RefusesDuringScan(t *testing.T) {
	cases := []struct {
		name  string
		scan  *recordingScanController
		queue fsQueue
	}{
		{"stand-down not acquired", &recordingScanController{acquireErr: errors.New("scan would not park")}, fsQueue{}},
		{"library.scan running", &recordingScanController{renewOK: true},
			fsQueue{rows: []database.OperationV2Row{{ID: "scan-1", DefID: "library.scan", Status: "running"}}}},
		{"queue unreadable", &recordingScanController{renewOK: true}, fsQueue{err: errors.New("boom")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := regroupStore(t)
			ids := seedFragments(t, s, fsFragBase, "Cage of Souls", 3)
			before := fsRowCount(t, s)
			plan := fsPlan(t, s)
			if _, err := applyFSRepairPlan(context.Background(), s, tc.scan, tc.queue, plan, fsRegroupParams{}, &opIDReporter{id: "op-scan"}); err == nil {
				t.Fatalf("apply succeeded, want a refusal")
			}
			for _, id := range ids {
				if fsSoftDeleted(t, s, id) {
					t.Errorf("book %s soft-deleted during a refused apply", id)
				}
			}
			if after := fsRowCount(t, s); after != before {
				t.Errorf("rows %d → %d", before, after)
			}
		})
	}

	// And with a live lease the stand-down is held for the write phase.
	s := regroupStore(t)
	seedFragments(t, s, fsFragBase, "Cage of Souls", 2)
	c := &recordingScanController{renewOK: true}
	if _, err := applyFSRepairPlan(context.Background(), s, c, fsQueue{}, fsPlan(t, s), fsRegroupParams{}, &opIDReporter{id: "op-held"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if acq, rel, ren := c.counts(); acq != 1 || rel != 1 || ren < 1 {
		t.Errorf("stand-down acquires=%d releases=%d renews=%d, want 1/1/>=1", acq, rel, ren)
	}
}

// A path lookup error skips the group instead of being read as "no row"
// (which would create a second row for the same path), and an apply with no
// op id refuses because it could not journal.
func TestFsRegroupApply_LookupErrorSkipsGroupAndOpIDRequired(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase, "Cage of Souls", 3)
	before := fsRowCount(t, s)
	g := &fsGuardStore{PebbleStore: s, lookupErr: errors.New("transient")}
	plan := fsPlan(t, g)

	if _, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, &fakeReporter{}); err == nil ||
		!strings.Contains(err.Error(), "operation id") {
		t.Fatalf("apply without op id: err = %v, want refusal", err)
	}
	res, err := applyFSRepairPlan(context.Background(), g, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-lookup"})
	if err == nil || res.Groups != 0 {
		t.Fatalf("apply with lookup errors: err=%v res=%s, want error and 0 groups", err, res)
	}
	for _, id := range ids {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("book %s soft-deleted after a lookup error", id)
		}
	}
	if after := fsRowCount(t, s); after != before {
		t.Errorf("rows %d → %d", before, after)
	}
}
