// file: internal/plugins/maintenance/itunes_regroup_entangle_test.go
// version: 1.0.0
// guid: 9743f8e7-3f4d-43c2-976c-eb2ff7c3e4cc
// last-edited: 2026-10-01

package maintenance

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// regroupFakeReader serves a fixed book + file set to buildRegroupSnapshot, so
// a test can describe version groups as real database.Book rows (flags and
// all) and drive the snapshot builder and the planner together.
type regroupFakeReader struct {
	books []database.Book
	files []database.BookFileCore
}

func (r *regroupFakeReader) GetAllBookFilesCore() ([]database.BookFileCore, error) {
	return r.files, nil
}

func (r *regroupFakeReader) GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error) {
	sorted := append([]database.Book(nil), r.books...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	var out []database.Book
	for _, b := range sorted {
		if b.ID > afterID {
			out = append(out, b)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

var _ regroupSnapshotReader = (*regroupFakeReader)(nil)

func rgFlag(v bool) *bool { return &v }

// rgBook builds a book row. vg "" means ungrouped; flag nil means unset.
func rgBook(id, vg string, flag *bool) database.Book {
	now := time.Unix(1700000000, 0)
	b := database.Book{ID: id, Title: id, IsPrimaryVersion: flag, CreatedAt: &now}
	if vg != "" {
		b.VersionGroupID = &vg
	}
	return b
}

// rgFile is one book_file row; pid "" is a file that belongs to no heal group.
func rgFile(id, bookID, pid string) database.BookFileCore {
	return database.BookFileCore{ID: id, BookID: bookID, ITunesPersistentID: pid}
}

// The entanglement table. Naming: L* is a library-folder copy (holds no iTunes
// PIDs), I* is an iTunes-imported copy linked into a version group, F* is an
// ungrouped iTunes fragment record. Every case plans ONE heal group "G" over
// every PID in its files.
func TestITunesRegroupEntanglementRule(t *testing.T) {
	tr, fa := rgFlag(true), rgFlag(false)
	cases := []struct {
		name        string
		books       []database.Book
		files       []database.BookFileCore
		pids        []string
		wantSkipped bool
		wantTarget  string // checked when not skipped
		wantMoves   int    // checked when not skipped
	}{
		{
			name:  "ungrouped fragments consolidate",
			books: []database.Book{rgBook("F1", "", nil), rgBook("F2", "", nil)},
			files: []database.BookFileCore{rgFile("f1", "F1", "p1"), rgFile("f2", "F2", "p2")},
			pids:  []string{"p1", "p2"}, wantTarget: "F1", wantMoves: 1,
		},
		{
			name: "grouped source would lose files (two editions in different groups)",
			books: []database.Book{
				rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr),
				rgBook("I2", "vg2", fa), rgBook("L2", "vg2", tr),
			},
			files: []database.BookFileCore{
				rgFile("f1", "I1", "p1"), rgFile("x1", "I1", ""),
				rgFile("f2", "I2", "p2"), rgFile("x2", "I2", ""),
			},
			pids: []string{"p1", "p2"}, wantSkipped: true,
		},
		{
			name: "grouped member would be emptied and deleted",
			books: []database.Book{
				rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr),
				rgBook("I2", "vg2", fa), rgBook("L2", "vg2", tr),
			},
			files: []database.BookFileCore{rgFile("f1", "I1", "p1"), rgFile("f2", "I2", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
		},
		{
			name: "grouped non-primary target receives from ungrouped fragments",
			books: []database.Book{
				rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr),
				rgBook("F1", "", nil), rgBook("F2", "", nil),
			},
			files: []database.BookFileCore{
				rgFile("f1", "I1", "p1"), rgFile("f2", "F1", "p2"), rgFile("f3", "F2", "p3"),
			},
			pids: []string{"p1", "p2", "p3"}, wantTarget: "I1", wantMoves: 2,
		},
		{
			name: "{marked,unset}: unset iTunes copy receives",
			books: []database.Book{
				rgBook("L1", "vg1", tr), rgBook("I1", "vg1", nil), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "I1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantTarget: "I1", wantMoves: 1,
		},
		{
			name: "{marked,unset}: marked primary would receive",
			books: []database.Book{
				rgBook("L1", "vg1", tr), rgBook("I1", "vg1", nil), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "L1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
		},
		{
			name: "{unset,not-primary}: not-primary iTunes copy receives",
			books: []database.Book{
				rgBook("L1", "vg1", nil), rgBook("I1", "vg1", fa), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "I1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantTarget: "I1", wantMoves: 1,
		},
		{
			name: "{unset,not-primary}: sole-unset incumbent would receive",
			books: []database.Book{
				rgBook("L1", "vg1", nil), rgBook("I1", "vg1", fa), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "L1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
		},
		{
			name: "{unset,unset,unset}: no incumbent, target ambiguous",
			books: []database.Book{
				rgBook("A1", "vg1", nil), rgBook("A2", "vg1", nil), rgBook("A3", "vg1", nil),
				rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "A1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
		},
		{
			name: "one-member group, explicit primary target",
			books: []database.Book{
				rgBook("S1", "vg1", tr), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "S1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
		},
		{
			name: "grouped copy outranks a richer ungrouped fragment as target",
			books: func() []database.Book {
				rich := rgBook("F1", "", nil)
				asin := "B000TEST"
				rich.ASIN = &asin
				return []database.Book{rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr), rich}
			}(),
			files: []database.BookFileCore{rgFile("f1", "I1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantTarget: "I1", wantMoves: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plugin{}
			snap, err := p.buildRegroupSnapshot(context.Background(),
				&regroupFakeReader{books: tc.books, files: tc.files}, &fakeReporter{})
			if err != nil {
				t.Fatalf("buildRegroupSnapshot: %v", err)
			}
			plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "G", PIDs: tc.pids}}, snap)
			if len(plan.Groups) != 1 {
				t.Fatalf("groups = %d, want 1", len(plan.Groups))
			}
			a := plan.Groups[0]
			if a.Entangled != tc.wantSkipped {
				t.Fatalf("Entangled = %v, want %v (action %+v)", a.Entangled, tc.wantSkipped, a)
			}
			if tc.wantSkipped {
				if len(a.Moves) != 0 || a.Target != "" || len(plan.DeleteBooks) != 0 {
					t.Fatalf("skipped group must plan nothing, got %+v deletes=%v", a, plan.DeleteBooks)
				}
				return
			}
			if a.Target != tc.wantTarget || a.FreshBook || len(a.Moves) != tc.wantMoves {
				t.Fatalf("target=%q fresh=%v moves=%d, want %q/false/%d", a.Target, a.FreshBook, len(a.Moves), tc.wantTarget, tc.wantMoves)
			}
			for _, id := range plan.DeleteBooks {
				if snap.Books[id].VersionGroupID != "" {
					t.Fatalf("plan deletes version-group member %s", id)
				}
			}
		})
	}
}

// A fresh-book split that would pull one heal group's files OUT of a grouped
// book is skipped: the grouped book is an edition, and taking files from it
// changes what that edition is.
func TestITunesRegroupEntanglementRule_SplitOutOfGroupedSkipped(t *testing.T) {
	fa, tr := rgFlag(false), rgFlag(true)
	r := &regroupFakeReader{
		books: []database.Book{rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr)},
		files: []database.BookFileCore{
			rgFile("a1", "I1", "pa1"), rgFile("a2", "I1", "pa2"),
			rgFile("b1", "I1", "pb1"),
		},
	}
	p := &Plugin{}
	snap, err := p.buildRegroupSnapshot(context.Background(), r, &fakeReporter{})
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{
		{Title: "A", PIDs: []string{"pa1", "pa2"}},
		{Title: "B", PIDs: []string{"pb1"}},
	}, snap)
	a, b := plan.Groups[0], plan.Groups[1]
	if a.Target != "I1" || a.Entangled {
		t.Fatalf("group A = %+v, want already-correct on I1", a)
	}
	if !b.Entangled || b.FreshBook || len(b.Moves) != 0 {
		t.Fatalf("group B = %+v, want skipped (split out of a grouped book)", b)
	}
}
