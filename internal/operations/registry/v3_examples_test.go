// file: internal/operations/registry/v3_examples_test.go
// version: 1.0.1
// guid: 483e4659-2825-4023-a407-0e33318d6b94
// last-edited: 2026-10-20

package registry

// The three worked examples of
// docs/proposals/2026-10-holistic/05-operations-v3/examples.md, written
// against pkg/ops and run on the registry over a real Pebble store. Each
// keeps the example's shape with three changes the PR's contract forces:
// deps are test fakes (never database.Store), Example 2's ModifyBookFile
// takes the book id as well (the store's ModifyBookFile needs it), and
// Example 3 drops w.LockFields (not in the v3 Writer yet; todo.d). Example
// 2's worked resume (chunks {0, 2} done, resume leases exactly {1, 3} and
// starts at 512) is TestChunkLeasing_ResumeLeasesExactlyUnfinished.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// ── Example 1: trivial op, library.size-refresh ───────────────────────────

type sizeResult struct {
	LibraryBytes int64 `json:"library_bytes"`
	ImportBytes  int64 `json:"import_bytes"`
}

// sizeDeps is the package's Deps: one narrow interface per capability.
type sizeDeps struct {
	Paths interface {
		GetAllImportPaths(ctx context.Context) ([]string, error)
	}
}

type fakePaths []string

func (f fakePaths) GetAllImportPaths(context.Context) ([]string, error) { return f, nil }

func sizeRefresh(deps sizeDeps, enabled func() bool) ops.Definition {
	return ops.Task("library.size-refresh", ops.TaskSpec[ops.NoParams, sizeResult]{
		Title:      "Library size refresh",
		Help:       "Walks the library and import trees to refresh the on-disk size cache.",
		Effects:    ops.ReadOnly(ops.ResFiles),
		Permission: auth.PermLibraryView,
		Schedule:   ops.InMaintenanceWindow(30).When(enabled),
		Run: func(rc *ops.RC, _ ops.NoParams) (sizeResult, error) {
			folders, err := deps.Paths.GetAllImportPaths(rc.Context())
			if err != nil {
				return sizeResult{}, err
			}
			return sizeResult{LibraryBytes: 1000, ImportBytes: int64(len(folders)) * 10}, nil
		},
	})
}

func TestExample1SizeRefresh(t *testing.T) {
	store := newPebbleV3(t)
	r := startV3Registry(t, store, Options{})
	def := sizeRefresh(sizeDeps{Paths: fakePaths{"/imports/a", "/imports/b"}}, func() bool { return true })
	if err := Register(r, "library", def); err != nil {
		t.Fatal(err)
	}
	id, err := r.EnqueueOp(context.Background(), "library.size-refresh", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, id)
	var got sizeResult
	resultOf(t, row, &got)
	if row.Status != "completed" || got != (sizeResult{LibraryBytes: 1000, ImportBytes: 20}) {
		t.Fatalf("%s %+v", row.Status, got)
	}
}

// ── Example 2: batch op, acoustid.lsh-backfill ────────────────────────────

type lshResult struct {
	Indexed, Skipped, Failed, Previewed int64
}

type lshDeps struct {
	Files interface {
		ListBookFiles(ctx context.Context) ([]database.BookFile, error)
	}
}

type fakeFiles []database.BookFile

func (f fakeFiles) ListBookFiles(context.Context) ([]database.BookFile, error) { return f, nil }

func lshBackfill(deps lshDeps, indexed func(fileID string) bool) ops.Definition {
	return ops.Batch("acoustid.lsh-backfill", ops.BatchSpec[ops.NoParams, database.BookFile, lshResult]{
		Title:      "Backfill LSH fingerprint index",
		Help:       "Re-saves book files that have a whole-file fingerprint but no LSH index entry. Idempotent.",
		Effects:    ops.Writes(ops.ResBookFiles, ops.ResIndexes),
		Exclusive:  "acoustid.fingerprint",
		Permission: auth.PermSettingsManage,
		Source: func(rc *ops.RC, _ ops.NoParams) (ops.Source[database.BookFile], error) {
			files, err := deps.Files.ListBookFiles(rc.Context())
			return ops.Source[database.BookFile]{
				Items: files, Key: func(f database.BookFile) string { return f.ID },
				Order: ops.Snapshot, Total: ops.Known(int64(len(files))),
			}, err
		},
		// Concurrency omitted: CPU() is the default. ChunkSize omitted: 256.
		Label: func(f database.BookFile) string { return f.ID },
		Item: func(rc *ops.ItemRC, _ ops.NoParams, f database.BookFile) error {
			switch {
			case f.AcoustIDFingerprintDurationSec == 0:
				rc.Skip("no_fingerprint")
				return nil
			case indexed(f.ID):
				rc.Skip("already_indexed")
				return nil
			}
			_, err := rc.Writer().ModifyBookFile(rc.Context(), f.BookID, f.ID, func(*database.BookFile) error { return nil })
			if err == nil {
				rc.Count("indexed", 1)
			}
			return err
		},
		Finish: func(_ *ops.RC, _ ops.NoParams, s ops.Summary) (lshResult, error) {
			return lshResult{Indexed: s.Counter("indexed"), Skipped: s.Skipped, Failed: s.Failed,
				Previewed: s.Counter("previewed")}, nil
		},
	})
}

func TestExample2LSHBackfill(t *testing.T) {
	store := newPebbleV3(t)
	book, err := store.CreateBook(&database.Book{Title: "Example", FilePath: "/library/example"})
	if err != nil {
		t.Fatal(err)
	}
	var files []database.BookFile
	for i := range 6 {
		f := database.BookFile{ID: fmt.Sprintf("bf-%d", i), BookID: book.ID, FilePath: fmt.Sprintf("/library/example/%d.m4b", i)}
		if i%3 != 0 {
			f.AcoustIDFingerprintDurationSec = 120
		}
		if err := store.CreateBookFile(&f); err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	r := startV3Registry(t, store, Options{})
	def := lshBackfill(lshDeps{Files: fakeFiles(files)}, func(id string) bool { return id == "bf-1" })
	if err := Register(r, "acoustid", def); err != nil {
		t.Fatal(err)
	}
	if od, _ := r.Def("acoustid.lsh-backfill"); od.ConcurrencyKey != "acoustid.fingerprint" || len(od.Writes) != 2 {
		t.Fatalf("registered = %+v", od)
	}

	// A manual run with no mode is a preview: it reports how many files it
	// would re-save and writes nothing.
	ctx := context.Background()
	id, err := r.EnqueueOp(ctx, "acoustid.lsh-backfill", nil)
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, id)
	var preview lshResult
	resultOf(t, row, &preview)
	if row.Status != "completed" || preview.Previewed != 3 || preview.Indexed != 0 || preview.Skipped != 3 || preview.Failed != 0 {
		t.Fatalf("preview: %s %+v", row.Status, preview)
	}

	id, err = r.EnqueueOp(ctx, "acoustid.lsh-backfill", map[string]bool{"dry_run": false})
	if err != nil {
		t.Fatal(err)
	}
	row = awaitTerminal(t, store, id)
	var live lshResult
	resultOf(t, row, &live)
	if row.Status != "completed" || live.Indexed != 3 || live.Skipped != 3 || live.Previewed != 0 {
		t.Fatalf("live: %s %+v", row.Status, live)
	}
	if left, err := store.ListIntentsV3(id); err != nil || len(left) != 0 {
		t.Fatalf("intents left after a clean run: %v %v", left, err)
	}
	// A no-op re-save changes no tracked field, so it records no history row.
	if changes, _ := store.GetOperationChanges(id); len(changes) != 0 {
		t.Fatalf("a no-op re-save recorded %d history rows", len(changes))
	}
}

// ── Example 3: trial -> approve -> apply fixer ────────────────────────────

type ordinalDeps struct {
	Books interface {
		GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
		GetBookByID(id string) (*database.Book, error)
	}
}

func letterLOrdinals(deps ordinalDeps) ops.Definition {
	return ops.Fixer("maintenance.normalize-letter-l-ordinals", ops.FixerSpec[ops.NoParams, database.BookCore]{
		Title:      `Letter-l ordinals ("l Volume" -> "1 Volume")`,
		Help:       "Rewrites a leading letter-l ordinal to digits.",
		Effects:    ops.Writes(ops.ResBooks, ops.ResFieldStates),
		Permission: auth.PermSettingsManage,
		Candidates: func(rc *ops.RC, _ ops.NoParams) (ops.Source[database.BookCore], error) {
			all, err := deps.Books.GetAllBooksCore(0, 0)
			var out []database.BookCore
			for _, b := range all {
				if strings.HasPrefix(b.Title, "l ") {
					out = append(out, b)
				}
			}
			return ops.Source[database.BookCore]{Items: out, Key: func(b database.BookCore) string { return b.ID }, Order: ops.Snapshot}, err
		},
		Load: func(rc *ops.RC, _ ops.NoParams, id string) (database.BookCore, bool, error) {
			b, err := deps.Books.GetBookByID(id)
			if err != nil || b == nil {
				return database.BookCore{}, false, err
			}
			return database.BookCore{ID: b.ID, Title: b.Title}, true, nil
		},
		Evaluate: func(rc *ops.ItemRC, _ ops.NoParams, b database.BookCore) (ops.Row, error) {
			row := ops.Row{ID: b.ID, Subjects: ops.Books(b.ID), Title: b.Title, Risk: ops.RiskLow,
				Current: map[string]string{"title": b.Title}, Inputs: map[string]string{"title": b.Title}}
			rest, ok := strings.CutPrefix(b.Title, "l ")
			if !ok {
				row.Hold = "the title no longer uses a letter-l ordinal"
				return row, nil
			}
			row.Proposed = map[string]string{"title": "1 " + rest}
			row.Reason = "the ordinal is the letter l, not the digit 1 / roman I"
			return row, nil
		},
		Apply: func(rc *ops.ItemRC, w ops.Writer, _ ops.NoParams, row ops.Row) error {
			_, err := w.ModifyBook(rc.Context(), row.ID, func(b *database.Book) error {
				if b.Title != row.Current["title"] {
					return ops.ErrChangedSincePlan // compare-and-set under the write lock
				}
				b.Title = row.Proposed["title"]
				return nil
			})
			return err
		},
	})
}

func TestExample3LetterLOrdinalFixer(t *testing.T) {
	store := newPebbleV3(t)
	var ids []string
	for _, title := range []string{"l Volume", "l Book", "Plain"} {
		b, err := store.CreateBook(&database.Book{Title: title, FilePath: "/library/" + title})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	r := startV3Registry(t, store, Options{})
	def := letterLOrdinals(ordinalDeps{Books: store})
	if err := Register(r, "maintenance", def); err != nil {
		t.Fatal(err)
	}
	if od, _ := r.Def(def.ID()); od.ResumePolicy != ResumeAsk {
		t.Fatalf("a fixer resumes by asking, got %v", od.ResumePolicy)
	}
	ctx := context.Background()
	// 1. Plan (Preview).
	planID, err := r.EnqueueOp(ctx, def.ID(), nil)
	if err != nil {
		t.Fatal(err)
	}
	row := awaitTerminal(t, store, planID)
	var plan ops.FixerPlan
	resultOf(t, row, &plan)
	if row.Status != "completed" || len(plan.Rows) != 2 {
		t.Fatalf("plan: %s %+v", row.Status, plan)
	}
	// Someone edits the second book between plan and apply.
	if _, err := store.ModifyBook(ids[1], func(b *database.Book) error { b.Title = "l Book (edited)"; return nil }); err != nil {
		t.Fatal(err)
	}
	// 2. A person approves both rows; the Live run applies what still
	// matches its plan.
	params, _ := json.Marshal(map[string]any{"dry_run": false, "plan_ref": planID, "rows": plan.Rows})
	applyID, err := r.EnqueueOp(ctx, def.ID(), json.RawMessage(params))
	if err != nil {
		t.Fatal(err)
	}
	row = awaitTerminal(t, store, applyID)
	var applied ops.FixerResult
	resultOf(t, row, &applied)
	if row.Status != "completed" || len(applied.Applied) != 1 || applied.Applied[0] != ids[0] {
		t.Fatalf("apply: %s %+v", row.Status, applied)
	}
	b0, _ := store.GetBookByID(ids[0])
	b1, _ := store.GetBookByID(ids[1])
	if b0.Title != "1 Volume" || b1.Title != "l Book (edited)" {
		t.Fatalf("titles after apply: %q %q", b0.Title, b1.Title)
	}
	hist, err := store.GetMetadataChangeHistory(ids[0], "title", 10)
	if err != nil || len(hist) != 1 || hist[0].ChangeType != historyChangeTypeV3 {
		t.Fatalf("history after the apply: %+v %v", hist, err)
	}
	rec, _ := store.GetRunV3(applyID)
	if rec == nil || rec.Progress.Counters["skipped:"+ops.SkipChangedSincePlan] != 1 {
		t.Fatalf("the changed row must be counted changed_since_plan: %+v", rec)
	}
}
