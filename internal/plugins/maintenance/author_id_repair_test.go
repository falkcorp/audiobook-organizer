// file: internal/plugins/maintenance/author_id_repair_test.go
// version: 1.0.1
// guid: d58e1b7a-3f94-4c26-a0b1-7e6c9f2d4853
// last-edited: 2026-09-13

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

func newRepairPebble(t *testing.T) *database.PebbleStore {
	t.Helper()
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	s.WaitForWarmup()
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func repairAuthor(t *testing.T, s database.Store, name string) *database.Author {
	t.Helper()
	a, err := s.CreateAuthor(name)
	if err != nil || a == nil {
		t.Fatalf("CreateAuthor(%q): %v", name, err)
	}
	return a
}

func repairBook(t *testing.T, s database.Store, id string, scalar *int, joins ...int) {
	t.Helper()
	if _, err := s.CreateBook(&database.Book{ID: id, Title: "T " + id, FilePath: "/lib/" + id + ".m4b", AuthorID: scalar}); err != nil {
		t.Fatalf("CreateBook(%s): %v", id, err)
	}
	if len(joins) == 0 {
		return
	}
	ba := make([]database.BookAuthor, len(joins))
	for i, a := range joins {
		ba[i] = database.BookAuthor{BookID: id, AuthorID: a, Role: "author", Position: i}
	}
	if err := s.SetBookAuthors(id, ba); err != nil {
		t.Fatalf("SetBookAuthors(%s): %v", id, err)
	}
}

func runRepair(t *testing.T, p *Plugin, params string) *authorIDRepairResult {
	t.Helper()
	res, err := p.authorIDRepair(context.Background(), decodeRepairParams(t, params), &fakeReporter{})
	if err != nil {
		t.Fatalf("authorIDRepair(%s): %v", params, err)
	}
	return res
}

func decodeRepairParams(t *testing.T, params string) authorIDRepairParams {
	t.Helper()
	var ps authorIDRepairParams
	if params != "" {
		if err := json.Unmarshal([]byte(params), &ps); err != nil {
			t.Fatalf("params: %v", err)
		}
	}
	return ps
}

func scalarOf(t *testing.T, s database.Store, id string) int {
	t.Helper()
	b, err := s.GetBookByID(id)
	if err != nil || b == nil || b.AuthorID == nil {
		t.Fatalf("book %s: %+v %v", id, b, err)
	}
	return *b.AuthorID
}

// 🔴 DRY RUN WRITES NOTHING. Every write method fails the test; the fixture
// holds one dangling scalar and one duplicate cluster so both phases plan.
func TestAuthorIDRepair_DryRunWritesNothing(t *testing.T) {
	dangling := 777
	dup, canon := 2, 1
	authors := []database.Author{{ID: 1, Name: "Jane Doe"}, {ID: 2, Name: "JANE DOE"}, {ID: 3, Name: database.UnknownAuthorName}}
	byID := map[int]*database.Author{1: &authors[0], 2: &authors[1], 3: &authors[2]}
	books := map[string]*database.Book{
		"dng": {ID: "dng", AuthorID: &dangling},
		"dup": {ID: "dup", AuthorID: &dup},
	}
	fail := func(what string) { t.Errorf("dry run called %s", what) }
	store := &database.MockStore{
		GetAllAuthorsFunc: func() ([]database.Author, error) { return authors, nil },
		GetAllBooksCoreCompleteFunc: func(limit, offset int) ([]database.BookCore, error) {
			if offset > 0 {
				return nil, nil
			}
			return []database.BookCore{books["dng"].Core(), books["dup"].Core()}, nil
		},
		GetBookByIDFunc:   func(id string) (*database.Book, error) { b := *books[id]; return &b, nil },
		GetAuthorByIDFunc: func(id int) (*database.Author, error) { return byID[id], nil },
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			if name == database.UnknownAuthorName {
				return byID[3], nil
			}
			return byID[canon], nil
		},
		GetBookAuthorsFunc: func(id string) ([]database.BookAuthor, error) {
			if id == "dup" {
				return []database.BookAuthor{{BookID: "dup", AuthorID: dup}}, nil
			}
			return nil, nil
		},
		GetBooksByAuthorIDForRelinkFunc: func(id int) ([]database.BookCore, error) {
			if id == dup {
				return []database.BookCore{books["dup"].Core()}, nil
			}
			return nil, nil
		},
		GetBookIDsCreditingAuthorDurableFunc: func(id int) ([]string, error) {
			if id == dup {
				return []string{"dup"}, nil
			}
			return nil, nil
		},
		UpdateBookFunc:            func(string, *database.Book) (*database.Book, error) { fail("UpdateBook"); return nil, errors.New("no") },
		SetBookAuthorsFunc:        func(string, []database.BookAuthor) error { fail("SetBookAuthors"); return errors.New("no") },
		DeleteAuthorFunc:          func(int) error { fail("DeleteAuthor"); return errors.New("no") },
		CreateAuthorFunc:          func(string) (*database.Author, error) { fail("CreateAuthor"); return nil, errors.New("no") },
		CreateOperationChangeFunc: func(*database.OperationChange) error { fail("CreateOperationChange"); return errors.New("no") },
	}
	p := New(&fakeDeps{store: store})
	for _, params := range []string{``, `{"dry_run":true}`, `{"dryRun":true}`} {
		res := runRepair(t, p, params)
		if !res.DryRun {
			t.Fatalf("params %q ran as apply", params)
		}
		if len(res.DanglingChanges) != 1 || res.DanglingChanges[0].NewAuthorID != 3 || res.DanglingChanges[0].Reason != authorIDRepairReasonUnknown {
			t.Fatalf("params %q dangling plan = %+v", params, res.DanglingChanges)
		}
		if len(res.DuplicateChanges) != 1 || res.DuplicateChanges[0].Outcome != authorIDMergeWouldMerge ||
			res.DuplicateChanges[0].CanonicalID != canon || fmt.Sprint(res.DuplicateChanges[0].ScalarRepoints) != "[dup]" {
			t.Fatalf("params %q duplicate plan = %+v", params, res.DuplicateChanges)
		}
	}
}

// Apply on a real store: dangling scalars repointed (by position, and via the
// Unknown Author fallback), nil scalars untouched, and a duplicate cluster with
// OVERLAPPING join rows merged onto the id the NAME INDEX resolves -- which in
// this fixture is deliberately NOT the lowest id and NOT the row with most books.
func TestAuthorIDRepair_ApplyRepointsAndMergesOntoIndexResolvedID(t *testing.T) {
	s := newRepairPebble(t)
	x := repairAuthor(t, s, "Jane Doe")  // lowest id, most books
	y := repairAuthor(t, s, "jane twin") // renamed below: takes the index
	z := repairAuthor(t, s, "Co Author")
	w := repairAuthor(t, s, "Next Up")
	if err := s.UpdateAuthorName(y.ID, "JANE DOE"); err != nil {
		t.Fatalf("UpdateAuthorName: %v", err)
	}
	if r, _ := s.GetAuthorByName("jane doe"); r == nil || r.ID != y.ID {
		t.Fatalf("fixture: index must resolve to y=%d, got %+v", y.ID, r)
	}

	dangling := 99999
	repairBook(t, s, "overlap", &x.ID, x.ID, y.ID) // both dup and canonical credited
	repairBook(t, s, "xonly1", &x.ID, x.ID)
	repairBook(t, s, "xonly2", &x.ID, x.ID)
	repairBook(t, s, "coauth", &z.ID, z.ID, x.ID)
	repairBook(t, s, "dangl-next", &dangling, dangling, w.ID)
	repairBook(t, s, "dangl-none", &dangling)
	repairBook(t, s, "nilscalar", nil)

	res := runRepair(t, New(&fakeDeps{store: s}), `{"dry_run":false}`)

	if got := scalarOf(t, s, "dangl-next"); got != w.ID {
		t.Fatalf("dangl-next scalar = %d, want next-by-position %d", got, w.ID)
	}
	u, _ := s.GetAuthorByName(database.UnknownAuthorName)
	if u == nil || scalarOf(t, s, "dangl-none") != u.ID {
		t.Fatalf("dangl-none must fall back to Unknown Author %+v", u)
	}
	if b, _ := s.GetBookByID("nilscalar"); b.AuthorID != nil {
		t.Fatalf("nil scalar must be left alone, got %d", *b.AuthorID)
	}
	if res.NilScalarBooks != 1 {
		t.Fatalf("NilScalarBooks = %d, want 1", res.NilScalarBooks)
	}

	if gone, _ := s.GetAuthorByID(x.ID); gone != nil && gone.ID == x.ID {
		t.Fatalf("duplicate x=%d must be deleted; canonical is the index-resolved y=%d", x.ID, y.ID)
	}
	if kept, _ := s.GetAuthorByID(y.ID); kept == nil {
		t.Fatalf("canonical y=%d was deleted", y.ID)
	}
	for _, id := range []string{"overlap", "xonly1", "xonly2"} {
		if got := scalarOf(t, s, id); got != y.ID {
			t.Fatalf("%s scalar = %d, want canonical %d", id, got, y.ID)
		}
	}
	if got := scalarOf(t, s, "coauth"); got != z.ID {
		t.Fatalf("coauth primary must stay z=%d, got %d", z.ID, got)
	}
	ov, _ := s.GetBookAuthors("overlap")
	if len(ov) != 1 || ov[0].AuthorID != y.ID || ov[0].Position != 0 {
		t.Fatalf("overlapping joins must dedupe to one canonical row at position 0, got %+v", ov)
	}
	co, _ := s.GetBookAuthors("coauth")
	if len(co) != 2 || co[0].AuthorID != z.ID || co[1].AuthorID != y.ID || co[1].Position != 1 {
		t.Fatalf("coauth joins must keep order with x replaced in place, got %+v", co)
	}
	if res.DuplicateOutcomes[authorIDMergeMerged] != 1 {
		t.Fatalf("duplicate outcomes = %v", res.DuplicateOutcomes)
	}

	// Idempotent: a second apply finds nothing.
	again := runRepair(t, New(&fakeDeps{store: s}), `{"dry_run":false}`)
	if again.DanglingBooks != 0 || again.DuplicateClusters != 0 {
		t.Fatalf("second run not clean: dangling=%d clusters=%d", again.DanglingBooks, again.DuplicateClusters)
	}
}

// 🔴 DELETE ONLY WHEN EMPTY. The duplicate's scalar repoint fails, so a book
// still names it. Whichever store sees the leftover -- only the durable Pebble
// scan, or only the relink view -- the delete must be refused.
func TestAuthorIDRepair_RefusesDeleteWhileScalarStillReferences(t *testing.T) {
	for _, tc := range []struct {
		name           string
		durableSees    bool
		relinkViewSees bool
	}{
		{"only-durable-scan-sees-it", true, false},
		{"only-relink-view-sees-it", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dup := 2
			authors := []database.Author{{ID: 1, Name: "Jane Doe"}, {ID: 2, Name: "JANE DOE"}}
			book := &database.Book{ID: "b1", AuthorID: &dup}
			moves := 0
			store := &database.MockStore{
				GetAllAuthorsFunc:           func() ([]database.Author, error) { return authors, nil },
				GetAllBooksCoreCompleteFunc: func(int, int) ([]database.BookCore, error) { return nil, nil },
				GetAuthorByNameFunc:         func(string) (*database.Author, error) { return &authors[0], nil },
				GetAuthorByIDFunc:           func(id int) (*database.Author, error) { return &authors[id-1], nil },
				GetBookByIDFunc:             func(string) (*database.Book, error) { b := *book; return &b, nil },
				GetBookAuthorsFunc:          func(string) ([]database.BookAuthor, error) { return nil, nil },
				GetBooksByAuthorIDForRelinkFunc: func(int) ([]database.BookCore, error) {
					moves++
					if moves == 1 || tc.relinkViewSees { // first call lists; later is the re-check
						return []database.BookCore{book.Core()}, nil
					}
					return nil, nil
				},
				GetBookIDsCreditingAuthorDurableFunc: func(int) ([]string, error) {
					if tc.durableSees {
						return []string{"b1"}, nil
					}
					return nil, nil
				},
				UpdateBookFunc: func(string, *database.Book) (*database.Book, error) {
					return nil, errors.New("simulated write failure: scalar stays on the duplicate")
				},
				DeleteAuthorFunc: func(id int) error {
					t.Errorf("DeleteAuthor(%d) called while a scalar still references it", id)
					return nil
				},
				CreateOperationChangeFunc: func(*database.OperationChange) error { return nil },
			}
			res := runRepair(t, New(&fakeDeps{store: store}), `{"dry_run":false}`)
			if len(res.DuplicateChanges) != 1 || res.DuplicateChanges[0].Outcome != authorIDMergeHeld {
				t.Fatalf("want held_still_referenced, got %+v", res.DuplicateChanges)
			}
		})
	}
}

// Phase (a) under Concurrency > 1 with -race: many dangling books processed by
// the RunItems pool, every one repointed exactly once.
func TestAuthorIDRepair_DanglingPhaseConcurrent(t *testing.T) {
	if runtime.NumCPU() < 2 {
		t.Skip("needs >1 CPU for RunItems concurrency")
	}
	s := newRepairPebble(t)
	next := repairAuthor(t, s, "Survivor")
	const n = 120
	for i := 0; i < n; i++ {
		dead := 50000 + i%7 // several books share each dangling id
		if i%2 == 0 {
			repairBook(t, s, fmt.Sprintf("b%03d", i), &dead, dead, next.ID)
		} else {
			repairBook(t, s, fmt.Sprintf("b%03d", i), &dead)
		}
	}
	res := runRepair(t, New(&fakeDeps{store: s}), `{"dry_run":false,"skip_duplicates":true}`)
	if res.DanglingBooks != n || res.DanglingOutcomes["repointed"] != n || len(res.DanglingChanges) != n {
		t.Fatalf("dangling=%d outcomes=%v changes=%d, want %d repointed", res.DanglingBooks, res.DanglingOutcomes, len(res.DanglingChanges), n)
	}
	u, _ := s.GetAuthorByName(database.UnknownAuthorName)
	if u == nil {
		t.Fatalf("Unknown Author not created")
	}
	for i := 0; i < n; i++ {
		want := u.ID
		if i%2 == 0 {
			want = next.ID
		}
		if got := scalarOf(t, s, fmt.Sprintf("b%03d", i)); got != want {
			t.Fatalf("b%03d scalar = %d, want %d", i, got, want)
		}
	}
	// Exactly one Unknown Author row despite concurrent fallbacks.
	all, _ := s.GetAllAuthors()
	unknowns := 0
	for _, a := range all {
		if a.Name == database.UnknownAuthorName {
			unknowns++
		}
	}
	if unknowns != 1 {
		t.Fatalf("%d Unknown Author rows created, want 1", unknowns)
	}
}

func TestRewriteJoinsOnto(t *testing.T) {
	in := []database.BookAuthor{{AuthorID: 5, Position: 0}, {AuthorID: 2, Position: 1}, {AuthorID: 1, Position: 2}, {AuthorID: 9, Position: 3}}
	out, changed := rewriteJoinsOnto(in, 2, 1)
	if !changed || fmt.Sprint(out) != fmt.Sprint([]database.BookAuthor{{AuthorID: 5, Position: 0}, {AuthorID: 1, Position: 1}, {AuthorID: 9, Position: 2}}) {
		t.Fatalf("rewriteJoinsOnto = %+v changed=%v", out, changed)
	}
	if _, changed := rewriteJoinsOnto([]database.BookAuthor{{AuthorID: 5}}, 2, 1); changed {
		t.Fatalf("no-op rewrite reported a change")
	}
}
