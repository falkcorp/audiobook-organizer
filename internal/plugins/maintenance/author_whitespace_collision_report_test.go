// file: internal/plugins/maintenance/author_whitespace_collision_report_test.go
// version: 1.0.0
// guid: 153134ba-aa4f-4a82-ae54-9e54490af6c5
// last-edited: 2026-09-12

package maintenance

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// collisionMutations records every write the op could attempt. The op is
// report-only, so the assertion that matters is that this stays zero-valued.
type collisionMutations struct {
	created, renamed, deleted, setBookAuthors, updatedBooks, opChanges, aliases int
}

func newCollisionPlugin(authors []database.Author, books, refs map[int]int, booksErr error, muts *collisionMutations) *Plugin {
	store := &database.MockStore{
		GetAllAuthorsFunc:             func() ([]database.Author, error) { return authors, nil },
		GetAllAuthorBookCountsFunc:    func() (map[int]int, error) { return books, booksErr },
		GetAllAuthorBookRefCountsFunc: func() (map[int]int, error) { return refs, nil },
		GetAuthorByNameFunc: func(name string) (*database.Author, error) {
			for i := range authors {
				if util.NormalizeAuthor(authors[i].Name) == util.NormalizeAuthor(name) {
					return &authors[i], nil
				}
			}
			return nil, nil
		},
		CreateAuthorFunc: func(name string) (*database.Author, error) {
			muts.created++
			return &database.Author{Name: name}, nil
		},
		UpdateAuthorNameFunc: func(int, string) error { muts.renamed++; return nil },
		DeleteAuthorFunc:     func(int) error { muts.deleted++; return nil },
		SetBookAuthorsFunc:   func(string, []database.BookAuthor) error { muts.setBookAuthors++; return nil },
		UpdateBookFunc: func(id string, b *database.Book) (*database.Book, error) {
			muts.updatedBooks++
			return b, nil
		},
		CreateOperationChangeFunc: func(*database.OperationChange) error { muts.opChanges++; return nil },
		CreateAuthorAliasFunc: func(int, string, string) (*database.AuthorAlias, error) {
			muts.aliases++
			return &database.AuthorAlias{}, nil
		},
	}
	return &Plugin{deps: &fakeDeps{store: store}}
}

func collisionFixture() ([]database.Author, map[int]int, map[int]int) {
	authors := []database.Author{
		{ID: 40775, Name: "Raymond L. Weil"},
		{ID: 45616, Name: "Raymond  L.  Weil"},
		{ID: 42117, Name: "raymond l.\tweil"},
		{ID: 10, Name: "Unknown Author"},
		{ID: 11, Name: "UNKNOWN AUTHOR"}, // legacy (case-only) collision
		{ID: 20, Name: "Isaac Asimov"},   // no collision
		{ID: 30, Name: "   "},            // blank, skipped
	}
	books := map[int]int{40775: 3, 45616: 1, 42117: 0, 10: 0, 11: 2128, 20: 40}
	refs := map[int]int{40775: 3, 45616: 1, 42117: 2, 10: 0, 11: 2128, 20: 40}
	return authors, books, refs
}

func TestAuthorWhitespaceCollision_GroupsAndKinds(t *testing.T) {
	authors, books, refs := collisionFixture()
	r := findWhitespaceCollisions(authors, books, refs, 100)
	if r.TotalAuthors != 7 || r.Blank != 1 || r.Groups != 2 || r.WhitespaceGroups != 1 || r.LegacyGroups != 1 || r.AuthorsInGroups != 5 {
		t.Fatalf("counts wrong: %+v", r)
	}
	ws := r.WhitespaceSample[0]
	if ws.Key != "raymond l. weil" || ws.Kind != collisionKindWhitespace || ws.TotalRefs != 6 {
		t.Fatalf("whitespace group wrong: %+v", ws)
	}
	var ids []int
	for _, m := range ws.Members {
		ids = append(ids, m.ID)
	}
	if !reflect.DeepEqual(ids, []int{40775, 42117, 45616}) {
		t.Fatalf("members not ordered by ID: %v", ids)
	}
	if ws.Members[1].Books != 0 || ws.Members[1].Refs != 2 {
		t.Fatalf("book/ref counts not carried: %+v", ws.Members[1])
	}
	lg := r.LegacySample[0]
	if lg.Key != "unknown author" || lg.Kind != collisionKindLegacy || len(lg.Members) != 2 {
		t.Fatalf("legacy group wrong: %+v", lg)
	}
}

func TestAuthorWhitespaceCollision_SampleLimitPerKind(t *testing.T) {
	var authors []database.Author
	for i := 0; i < 5; i++ {
		base := string(rune('a'+i)) + " name"
		authors = append(authors,
			database.Author{ID: i*10 + 1, Name: base},
			database.Author{ID: i*10 + 2, Name: strings.Replace(base, " ", "  ", 1)})
	}
	r := findWhitespaceCollisions(authors, nil, nil, 2)
	if r.WhitespaceGroups != 5 || len(r.WhitespaceSample) != 2 {
		t.Fatalf("limit not applied per kind: groups=%d sample=%d", r.WhitespaceGroups, len(r.WhitespaceSample))
	}
}

func TestAuthorWhitespaceCollision_RunWritesNothingAndLogsQuotedNames(t *testing.T) {
	authors, books, refs := collisionFixture()
	snapshot := append([]database.Author(nil), authors...)
	var muts collisionMutations
	p := newCollisionPlugin(authors, books, refs, nil, &muts)
	rep := &fakeReporter{}
	if err := p.runAuthorWhitespaceCollisionReport(context.Background(), nil, rep); err != nil {
		t.Fatalf("run: %v", err)
	}
	if muts != (collisionMutations{}) {
		t.Fatalf("report-only op attempted writes: %+v", muts)
	}
	if !reflect.DeepEqual(authors, snapshot) {
		t.Fatalf("op modified the author slice it was handed")
	}
	var groupLines []string
	for _, l := range rep.logs {
		if strings.HasPrefix(l, "collision group ") {
			groupLines = append(groupLines, l)
		}
	}
	if len(groupLines) != 2 {
		t.Fatalf("want 2 group lines, got %d: %v", len(groupLines), rep.logs)
	}
	if !strings.Contains(groupLines[0], `name="Raymond  L.  Weil"`) || !strings.Contains(groupLines[0], "resolves_to=40775") {
		t.Fatalf("whitespace group line must quote names and show the index owner: %s", groupLines[0])
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "REPORT ONLY (nothing changed)") {
		t.Fatalf("summary missing: %v", rep.logs)
	}
}

func TestAuthorWhitespaceCollision_FailsWhenCountsFail(t *testing.T) {
	authors, _, refs := collisionFixture()
	var muts collisionMutations
	p := newCollisionPlugin(authors, nil, refs, errors.New("memdb incomplete"), &muts)
	if err := p.runAuthorWhitespaceCollisionReport(context.Background(), nil, &fakeReporter{}); err == nil {
		t.Fatal("op must fail rather than report zero book counts")
	}
}

func TestAuthorWhitespaceCollision_DefIsManualReadOnly(t *testing.T) {
	def := (&Plugin{}).authorWhitespaceCollisionReportDef()
	if def.Schedule != nil {
		t.Fatal("report op must have no schedule")
	}
	if !reflect.DeepEqual(def.Capabilities, []sdk.Capability{sdk.CapLibraryRead}) {
		t.Fatalf("report op must request CapLibraryRead only, got %v", def.Capabilities)
	}
	if def.Liveness != sdk.LivenessManual {
		t.Fatalf("report op must be manual, got %v", def.Liveness)
	}
}

// TestAuthorWhitespaceCollision_Registered: the op is registered exactly once,
// so it can be triggered by hand; the def test above pins that it has no schedule.
func TestAuthorWhitespaceCollision_Registered(t *testing.T) {
	reg := &phantomCaptureRegistry{}
	if err := New(fakeDeps{}).Register(reg); err != nil {
		t.Fatalf("register: %v", err)
	}
	found := 0
	for _, id := range reg.ids {
		if id == "maintenance.author-whitespace-collision-report" {
			found++
		}
	}
	if found != 1 {
		t.Fatalf("op registered %d times, want 1", found)
	}
}
