// file: internal/plugins/maintenance/fs_regroup_xml_test.go
// version: 2.2.0
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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
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
		if !fsRegroupProtectedPath(p) { // never write into a protected tree, even a fake one
			fsWriteFile(t, p)
		}
		id := fsSeedBook(t, s, "", p)
		if i == 1 {
			fsSeedRow(t, s, id, p)
		}
		ids = append(ids, id)
	}
	return ids
}

// fsFragBases gives each test (and subtest) its own real book folder, so the
// chapter files the op stats before creating a row exist on disk.
var fsFragBases sync.Map // *testing.T -> string

func fsFragBase(t *testing.T) string {
	t.Helper()
	if v, ok := fsFragBases.Load(t); ok {
		return v.(string)
	}
	base := filepath.Join(t.TempDir(), "Adrian Tchaikovsky", "Cage of Souls - Cage of Souls")
	fsFragBases.Store(t, base)
	t.Cleanup(func() { fsFragBases.Delete(t) })
	return base
}

// fsWriteFile creates a stand-in audio file at p.
func fsWriteFile(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fsForce relabels every group, to drive the apply's own re-checks on a group
// the planner already refused.
func fsForce(plan *fsRepairPlan, cat string) {
	for i := range plan.Groups {
		plan.Groups[i].Category = cat
	}
}

func fsPrimary(t *testing.T, s *database.PebbleStore, id string) bool {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("book %s is gone (err=%v)", id, err)
	}
	return b.IsPrimaryVersion == nil || *b.IsPrimaryVersion
}

// Category 1, dry run: the default run reports the group and its plan line
// and writes nothing.
func TestFsRegroupFragments_DryRunReportsPlanAndWritesNothing(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
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
	if !strings.Contains(strings.Join(rep.logs, "\n"), "PLAN fragments folder=\""+fsFragBase(t)+"\"") {
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
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
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
	if sb.Title != "Cage of Souls" || sb.FilePath != fsFragBase(t) {
		t.Errorf("survivor = %q @ %q", sb.Title, sb.FilePath)
	}
	if id, _ := s.GetBookByExternalID("itunes", "PID-X"); id != survivor {
		t.Errorf("PID-X → %q, want survivor", id)
	}
	led := fsLedgerTypes(t, s, "op-frag")
	if led[fsChangeSoftDelete] != shells {
		t.Errorf("book_soft_delete rows = %d, want %d", led[fsChangeSoftDelete], shells)
	}
	// Two members had no row: each gets one on itself, then the shells' rows move.
	if led[fsChangeFileCreate] != 2 || led[fsChangeFileReassign] != 2 || res.RowsCreated != 2 || res.RowsMoved != 2 {
		t.Errorf("create/reassign rows = %d/%d (res %s), want 2/2", led[fsChangeFileCreate], led[fsChangeFileReassign], res)
	}
	if led[fsChangePrimaryDemote] != shells {
		t.Errorf("book_primary_demote rows = %d, want %d", led[fsChangePrimaryDemote], shells)
	}
	if led["metadata_update"] != 1 || led[fsChangePathUpdate] != 1 {
		t.Errorf("metadata_update/book_path_update rows = %d/%d, want 1/1 (title, path)",
			led["metadata_update"], led[fsChangePathUpdate])
	}
	if led[fsChangeExtIDs] != 1 {
		t.Errorf("external_id_reassign rows = %d, want 1 (PID-X)", led[fsChangeExtIDs])
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
			ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
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
	seedFragments(t, s, fsFragBase(t), "Cage of Souls", 2)
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
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
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

// Undo: reverting the apply's operation through the Activity Log revert engine
// clears every shell's deletion mark, moves each reassigned row back to the
// book it came from with its old track number, and restores the survivor's
// title and path. No row is deleted either way.
func TestFsRegroupFragments_RevertRestoresShellsAndRows(t *testing.T) {
	s := regroupStore(t)
	var ids []string
	for i := 1; i <= 3; i++ {
		p := fmt.Sprintf("%s/Cage of Souls - %d/%d.mp3", fsFragBase(t), i, 3)
		id := fsSeedBook(t, s, "", p)
		fsSeedRow(t, s, id, p)
		ids = append(ids, id)
	}
	owner := map[string]string{} // row id -> its book before the apply
	track := map[string]int{}
	for _, id := range ids {
		rows, err := s.GetBookFiles(id)
		if err != nil || len(rows) != 1 {
			t.Fatalf("seed rows of %s = %d (err=%v), want 1", id, len(rows), err)
		}
		owner[rows[0].ID] = id
		track[rows[0].ID] = rows[0].TrackNumber
	}
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	frag := fsGroupsOf(plan, fsCatFragments)
	if len(frag) != 1 {
		t.Fatalf("fragments groups = %d, want 1 (plan %s)", len(frag), plan.summary())
	}
	survivor := frag[0].SurvivorID
	pre, _ := s.GetBookByID(survivor)
	preTitle, prePath := pre.Title, pre.FilePath

	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-undo"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	led := fsLedgerTypes(t, s, "op-undo")
	if led[fsChangeFileReassign] != 2 || led[fsChangeSoftDelete] != 2 || led[fsChangeFileTrack] == 0 {
		t.Fatalf("ledger = %v, want 2 reassign, 2 soft-delete and track rows", led)
	}

	for _, id := range ids {
		if id != survivor && fsPrimary(t, s, id) {
			t.Errorf("retired shell %s is still primary", id)
		}
	}

	res, err := audiobooks.NewRevertService(s).RevertOperation("op-undo")
	if err != nil {
		t.Fatalf("revert: %v (%+v)", err, res)
	}
	for _, id := range ids {
		if !fsPrimary(t, s, id) {
			t.Errorf("book %s still demoted after revert", id)
		}
	}
	if res.Failed != 0 || res.NotRestorable != 0 || res.Restored != res.Total {
		t.Errorf("revert result = %+v, want every row restored", res)
	}
	for rowID, bookID := range owner {
		f, err := s.GetBookFileByID(bookID, rowID)
		if err != nil || f == nil {
			t.Errorf("row %s is not back on %s (err=%v)", rowID, bookID, err)
			continue
		}
		if f.TrackNumber != track[rowID] {
			t.Errorf("row %s track = %d, want %d", rowID, f.TrackNumber, track[rowID])
		}
	}
	for _, id := range ids {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("book %s still soft-deleted after revert", id)
		}
	}
	if sb, _ := s.GetBookByID(survivor); sb.Title != preTitle || sb.FilePath != prePath {
		t.Errorf("survivor = %q @ %q after revert, want %q @ %q", sb.Title, sb.FilePath, preTitle, prePath)
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("book_file rows %d -> %d across apply+revert", before, got)
	}
}

// Finding: two live members at one chapter path, neither with a row. The
// planner refuses the group, and the apply refuses it on its own too, instead
// of creating two rows for one file.
func TestFsRegroupFragments_SharedPathRefused(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	b2, _ := s.GetBookByID(ids[1])
	twin := fsSeedBook(t, s, "", b2.FilePath)
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatFragments)); n != 0 {
		t.Fatalf("members sharing a path were classified fragments: %s", plan.summary())
	}
	fsForce(plan, fsCatFragments)
	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-share"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("rows %d -> %d: the apply created rows for a shared path", before, got)
	}
	for _, id := range append(ids, twin) {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("book %s soft-deleted", id)
		}
	}
}

// Finding: a member whose only row is at a path other than its Book.FilePath
// would get both a moved row and a created one; it is refused instead.
func TestFsRegroupFragments_RowPathDiffersRefused(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	b3, _ := s.GetBookByID(ids[2])
	other := filepath.Join(filepath.Dir(b3.FilePath), "other.mp3")
	fsWriteFile(t, other)
	fsSeedRow(t, s, ids[2], other)
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatFragments)); n != 0 {
		t.Fatalf("a member whose row path differs was classified fragments: %s", plan.summary())
	}
	fsForce(plan, fsCatFragments)
	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-differs"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("rows %d -> %d", before, got)
	}
	for _, id := range ids {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("book %s soft-deleted", id)
		}
	}
}

// Findings: rows are created on the shell whose path they are, so a revert
// leaves each member owning exactly the row at its own path; track numbers
// come from each row's folder number with gaps kept (1, 2, 4).
func TestFsRegroupFragments_RevertAfterCreateLeavesRowsOnTheirBooks(t *testing.T) {
	s := regroupStore(t)
	base := fsFragBase(t)
	var ids []string
	for i, n := range []int{1, 2, 4} {
		p := fmt.Sprintf("%s/Cage of Souls - %d/%d.mp3", base, n, n)
		fsWriteFile(t, p)
		id := fsSeedBook(t, s, "", p)
		if i == 0 {
			fsSeedRow(t, s, id, p)
		}
		ids = append(ids, id)
	}
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	frag := fsGroupsOf(plan, fsCatFragments)
	if len(frag) != 1 {
		t.Fatalf("fragments groups = %d, want 1 (plan %s)", len(frag), plan.summary())
	}
	survivor := frag[0].SurvivorID
	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-create"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	rows, _ := s.GetBookFiles(survivor)
	if len(rows) != 3 {
		t.Fatalf("survivor has %d rows, want 3", len(rows))
	}
	for _, f := range rows {
		if _, n, _, ok := chapterPartsForTest(f.FilePath); !ok || f.TrackNumber != n {
			t.Errorf("row %q track = %d, want its folder number", f.FilePath, f.TrackNumber)
		}
	}
	led := fsLedgerTypes(t, s, "op-create")
	res, err := audiobooks.NewRevertService(s).RevertOperation("op-create")
	if err != nil {
		t.Fatalf("revert: %v (%+v)", err, res)
	}
	if res.Failed != 0 || res.NotRestorable != led[fsChangeFileCreate] {
		t.Errorf("revert = %+v, want no failures and only the %d create rows not restorable", res, led[fsChangeFileCreate])
	}
	for _, id := range ids {
		b, _ := s.GetBookByID(id)
		own, _ := s.GetBookFiles(id)
		if len(own) != 1 || own[0].FilePath != b.FilePath {
			t.Errorf("book %s owns %d rows after revert, want one at its own path", id, len(own))
		}
		if b.IsSoftDeleted() || !fsPrimary(t, s, id) {
			t.Errorf("book %s not restored", id)
		}
	}
	if got := fsRowCount(t, s); got != before+led[fsChangeFileCreate] {
		t.Errorf("rows = %d, want %d + %d created (none deleted)", got, before, led[fsChangeFileCreate])
	}
	if n := len(fsGroupsOf(fsPlan(t, s), fsCatDuplicates)); n != 0 {
		t.Errorf("after revert a re-plan finds %d duplicates groups, want 0", n)
	}
}

// Finding: the revert is compare-and-set. A survivor path and a track number
// edited after the apply are reported by the preflight and left alone.
func TestFsRegroupRevert_RefusesFieldsChangedSince(t *testing.T) {
	s := regroupStore(t)
	base := fsFragBase(t)
	var ids []string
	for i := 1; i <= 3; i++ {
		p := fmt.Sprintf("%s/Cage of Souls - %d/%d.mp3", base, i, 3)
		id := fsSeedBook(t, s, "", p)
		fsSeedRow(t, s, id, p)
		ids = append(ids, id)
	}
	plan := fsPlan(t, s)
	frag := fsGroupsOf(plan, fsCatFragments)
	if len(frag) != 1 {
		t.Fatalf("fragments groups = %d (plan %s)", len(frag), plan.summary())
	}
	survivor := frag[0].SurvivorID
	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-drift"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	edited := base + "/edited"
	sb, _ := s.GetBookByID(survivor)
	sb.FilePath = edited
	if _, err := s.UpdateBook(sb.ID, sb); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.GetBookFiles(survivor)
	rows[0].TrackNumber = 99
	if err := s.UpdateBookFile(rows[0].ID, &rows[0]); err != nil {
		t.Fatal(err)
	}

	report, err := undo.PreflightUndoConflicts(s, "op-drift")
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	changed := 0
	for _, it := range report.CheckFailed {
		if it.Reason == undo.ReasonChangedSince {
			changed++
		}
	}
	if changed != 2 {
		t.Errorf("preflight changed-since = %d (%+v), want 2 (path, track)", changed, report)
	}
	res, err := audiobooks.NewRevertService(s).RevertOperation("op-drift")
	if err == nil || res == nil || res.Failed < 2 {
		t.Fatalf("revert err=%v res=%+v, want the two edited rows refused", err, res)
	}
	if got, _ := s.GetBookByID(survivor); got.FilePath != edited {
		t.Errorf("revert overwrote the edited path with %q", got.FilePath)
	}
	for _, id := range ids {
		if f, _ := s.GetBookFileByID(id, rows[0].ID); f != nil && f.TrackNumber != 99 {
			t.Errorf("revert overwrote the edited track with %d", f.TrackNumber)
		}
	}
}

// Finding: the apply re-reads members under the merge lock. A survivor
// soft-deleted between the plan and the apply skips the group untouched.
func TestFsRegroupApply_SurvivorSoftDeletedMidRunSkipsGroup(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	frag := fsGroupsOf(plan, fsCatFragments)
	if len(frag) != 1 {
		t.Fatalf("fragments groups = %d (plan %s)", len(frag), plan.summary())
	}
	survivor := frag[0].SurvivorID
	if err := merge.SoftDeleteBook(s, survivor); err != nil {
		t.Fatal(err)
	}
	res, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-mid"})
	if err != nil || res.Skipped != 1 || res.Groups != 0 {
		t.Fatalf("apply err=%v res=%s, want the group skipped", err, res)
	}
	for _, id := range ids {
		if id != survivor && fsSoftDeleted(t, s, id) {
			t.Errorf("shell %s retired onto a soft-deleted survivor", id)
		}
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("rows %d -> %d", before, got)
	}
	if led := fsLedgerTypes(t, s, "op-mid"); len(led) != 0 {
		t.Errorf("ledger %v, want none", led)
	}
}

// Finding: shells are never retired in favour of a non-primary owner, in the
// plan or (re-checked) in the apply.
func TestFsRegroupDuplicates_NonPrimaryOwnerRefused(t *testing.T) {
	s := regroupStore(t)
	owner, shells := seedDuplicates(t, s, "/lib/Kevin J. Anderson/Metal Swarm")
	ob, _ := s.GetBookByID(owner)
	notPrimary := false
	ob.IsPrimaryVersion = &notPrimary
	if _, err := s.UpdateBook(owner, ob); err != nil {
		t.Fatal(err)
	}
	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatDuplicates)); n != 0 {
		t.Fatalf("duplicates of a non-primary owner were planned: %s", plan.summary())
	}
	for i := range plan.Groups {
		if plan.Groups[i].Category == fsCatMixed {
			plan.Groups[i].Category, plan.Groups[i].OwnerID = fsCatDuplicates, owner
		}
	}
	res, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-nonprim"})
	if err != nil || res.Skipped != 1 {
		t.Fatalf("apply err=%v res=%s, want the group skipped", err, res)
	}
	for _, id := range shells {
		if fsSoftDeleted(t, s, id) {
			t.Errorf("shell %s retired in favour of a non-primary owner", id)
		}
	}
}

// Finding: the op never adds a row for a file that is not on disk. The dry run
// flags it, and the apply refuses the group on its own too.
func TestFsRegroupFragments_MissingFileRefused(t *testing.T) {
	s := regroupStore(t)
	ids := seedFragments(t, s, fsFragBase(t), "Cage of Souls", 3)
	b3, _ := s.GetBookByID(ids[2])
	if err := os.Remove(b3.FilePath); err != nil {
		t.Fatal(err)
	}
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatFragments)); n != 0 {
		t.Fatalf("a group with a missing file was classified fragments: %s", plan.summary())
	}
	rep := &fakeReporter{}
	reportFSRepairPlan(plan, true, rep)
	if !strings.Contains(strings.Join(rep.logs, "\n"), "not on disk") {
		t.Errorf("dry run does not flag the missing file: %v", rep.logs)
	}
	fsForce(plan, fsCatFragments)
	if _, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-missing"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("rows %d -> %d: a row was created for a missing file", before, got)
	}
}

// Finding: a re-run after an earlier merge (its survivor now sits at the book
// folder) refuses the leftover shells instead of splitting the book.
func TestFsRegroupFragments_EarlierSurvivorRefusesRerun(t *testing.T) {
	s := regroupStore(t)
	base := fsFragBase(t)
	seedFragments(t, s, base, "Cage of Souls", 3)
	fsSeedBook(t, s, "Cage of Souls", base)
	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatFragments)); n != 0 {
		t.Fatalf("leftover shells beside an earlier survivor were classified fragments: %s", plan.summary())
	}
	mixed := fsGroupsOf(plan, fsCatMixed)
	if len(mixed) != 1 || !strings.Contains(mixed[0].Reason, "earlier merge") {
		t.Errorf("mixed = %+v, want one group naming the earlier survivor", mixed)
	}
}

// Finding: protected paths are re-checked on the state the apply re-reads,
// not only on the plan snapshot.
func TestFsRegroupApply_ProtectedAfterPlanSkipsGroup(t *testing.T) {
	s := regroupStore(t)
	base := fsFragBase(t)
	seedFragments(t, s, base, "Cage of Souls", 3)
	before := fsRowCount(t, s)
	plan := fsPlan(t, s)
	if n := len(fsGroupsOf(plan, fsCatFragments)); n != 1 {
		t.Fatalf("fragments groups = %d (plan %s)", n, plan.summary())
	}
	old := config.AppConfig.ProtectedPaths
	config.AppConfig.ProtectedPaths = []string{filepath.Dir(base)}
	t.Cleanup(func() { config.AppConfig.ProtectedPaths = old })
	res, err := applyFSRepairPlan(context.Background(), s, nil, fsQueue{}, plan, fsRegroupParams{}, &opIDReporter{id: "op-prot-late"})
	if err != nil || res.Skipped != 1 {
		t.Fatalf("apply err=%v res=%s, want the group skipped", err, res)
	}
	if got := fsRowCount(t, s); got != before {
		t.Errorf("rows %d -> %d", before, got)
	}
	if led := fsLedgerTypes(t, s, "op-prot-late"); len(led) != 0 {
		t.Errorf("ledger %v, want none", led)
	}
}
