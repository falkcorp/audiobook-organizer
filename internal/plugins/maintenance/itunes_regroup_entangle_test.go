// file: internal/plugins/maintenance/itunes_regroup_entangle_test.go
// version: 1.7.1
// guid: 9743f8e7-3f4d-43c2-976c-eb2ff7c3e4cc
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
)

// regroupFakeReader serves a fixed book + file set to buildRegroupSnapshot, so
// a test can describe version groups as real database.Book rows (flags and
// all) and drive the snapshot builder and the planner together.
type regroupFakeReader struct {
	books  []database.Book
	files  []database.BookFileCore
	series []database.Series
	// tags, links and authors back the owner-manual check's tag and
	// author-credit reads (nil: none).
	tags    map[string][]string
	links   map[string][]database.BookAuthor
	authors map[int]*database.Author
	// linkErr fails GetBookAuthors for a book (the owner-manual check's
	// one per-book read).
	linkErr map[string]error
}

func (r *regroupFakeReader) GetBookTagsByBookIDs(ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range ids {
		if t := r.tags[id]; len(t) > 0 {
			out[id] = t
		}
	}
	return out, nil
}

func (r *regroupFakeReader) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if err := r.linkErr[bookID]; err != nil {
		return nil, err
	}
	return r.links[bookID], nil
}

func (r *regroupFakeReader) GetAllAuthors() ([]database.Author, error) {
	out := make([]database.Author, 0, len(r.authors))
	for _, a := range r.authors {
		out = append(out, *a)
	}
	return out, nil
}

func (r *regroupFakeReader) GetAuthorByID(id int) (*database.Author, error) {
	return r.authors[id], nil
}

func (r *regroupFakeReader) GetAllSeries() ([]database.Series, error) {
	return r.series, nil
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

var _ itunesRegroupSnapshotReader = (*regroupFakeReader)(nil)

// rgRoot is the library root every regroup test plans against. Paths under
// it are library-folder copies; rgITunes paths are the frozen iTunes tree.
const (
	rgRoot   = "/library"
	rgITunes = "/media/books/itunes/Media"
)

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

// rgFile is one book_file row in the iTunes tree; pid "" is a file that
// belongs to no heal group.
func rgFile(id, bookID, pid string) database.BookFileCore {
	return rgFileAt(id, bookID, pid, rgITunes+"/"+id+".m4b")
}

// rgFileAt is rgFile at an explicit path (rgRoot+"/..." for a library copy).
func rgFileAt(id, bookID, pid, path string) database.BookFileCore {
	return database.BookFileCore{ID: id, BookID: bookID, ITunesPersistentID: pid, FilePath: path}
}

// rgOrganized marks a book row library_state=organized.
func rgOrganized(b database.Book) database.Book {
	st := "organized"
	b.LibraryState = &st
	return b
}

// The entanglement table. Naming: L* is a library-folder copy (files under
// rgRoot; it MAY hold iTunes PIDs -- itunes.clone-into-library moves the PID
// onto the library rows -- and need not be its group's primary), I* is an
// iTunes-imported copy linked into a version group, F* is an ungrouped iTunes
// fragment record. A fixture file is in the iTunes tree unless placed with
// rgFileAt. Every case plans ONE heal group "G" over every PID in its files.
func TestITunesRegroupEntanglementRule(t *testing.T) {
	tr, fa := rgFlag(true), rgFlag(false)
	cases := []struct {
		name        string
		books       []database.Book
		files       []database.BookFileCore
		pids        []string
		wantSkipped bool
		wantReason  string // itunesservice.Entangle* when skipped
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
			wantReason: itunesservice.EntangleGroupedSource,
		},
		{
			name: "grouped member would be emptied and deleted",
			books: []database.Book{
				rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr),
				rgBook("I2", "vg2", fa), rgBook("L2", "vg2", tr),
			},
			files: []database.BookFileCore{rgFile("f1", "I1", "p1"), rgFile("f2", "I2", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleWouldEmpty,
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
			wantReason: itunesservice.EntanglePrimaryTarget,
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
			wantReason: itunesservice.EntanglePrimaryTarget,
		},
		{
			name: "{unset,unset,unset}: no incumbent, target ambiguous",
			books: []database.Book{
				rgBook("A1", "vg1", nil), rgBook("A2", "vg1", nil), rgBook("A3", "vg1", nil),
				rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "A1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleAmbiguous,
		},
		{
			name: "one-member group, explicit primary target",
			books: []database.Book{
				rgBook("S1", "vg1", tr), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{rgFile("f1", "S1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntanglePrimaryTarget,
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
		{
			// Probe A: the flag says I0 is primary, but L1 is the library copy.
			// It holds a PID and outranks F1, so the flag-only rule targeted it
			// and poured an iTunes-folder row into the library copy.
			name: "probe A: non-primary library copy holding a PID is never a target",
			books: []database.Book{
				rgBook("I0", "vg1", tr), rgBook("L1", "vg1", fa), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{
				rgFileAt("f1", "L1", "p1", rgRoot+"/Author/Book/p1.m4b"), rgFile("f2", "F1", "p2"),
			},
			pids: []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleLibraryTarget,
		},
		{
			// Probe B2: F1 is organized with its file under the root -- an
			// ABS-visible book. Moving p2 onto the non-primary I1 hides it.
			name: "probe B2: organized library source is never moved off",
			books: []database.Book{
				rgBook("L0", "vg1", tr), rgBook("I1", "vg1", fa), rgOrganized(rgBook("F1", "", nil)),
			},
			files: []database.BookFileCore{
				rgFile("f1", "I1", "p1"), rgFileAt("f2", "F1", "p2", rgRoot+"/Author/Book/p2.m4b"),
			},
			pids: []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleLibrarySource,
		},
		{
			name: "organized source with no file under root is never moved off",
			books: []database.Book{
				rgBook("L0", "vg1", tr), rgBook("I1", "vg1", fa), rgOrganized(rgBook("F1", "", nil)),
			},
			files: []database.BookFileCore{rgFile("f1", "I1", "p1"), rgFile("f2", "F1", "p2")},
			pids:  []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleLibrarySource,
		},
		{
			name: "ungrouped library copy never receives iTunes rows",
			books: func() []database.Book {
				lib := rgBook("F1", "", nil)
				asin := "B000LIB"
				lib.ASIN = &asin // outranks F2, so it is the target
				return []database.Book{lib, rgBook("F2", "", nil)}
			}(),
			files: []database.BookFileCore{
				rgFileAt("f1", "F1", "p1", rgRoot+"/Author/Book/p1.m4b"), rgFile("f2", "F2", "p2"),
			},
			pids: []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleLibraryTarget,
		},
		{
			// NIT 2: a row with no PID cannot be shown to be this album's.
			name: "grouped target holding a row with no PID is mixed",
			books: []database.Book{
				rgBook("I1", "vg1", fa), rgBook("L1", "vg1", tr), rgBook("F1", "", nil),
			},
			files: []database.BookFileCore{
				rgFile("f1", "I1", "p1"), rgFile("x1", "I1", ""), rgFile("f2", "F1", "p2"),
			},
			pids: []string{"p1", "p2"}, wantSkipped: true,
			wantReason: itunesservice.EntangleMixedTarget,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plugin{}
			snap, err := p.buildRegroupSnapshot(context.Background(),
				&regroupFakeReader{books: tc.books, files: tc.files}, rgRoot, &fakeReporter{})
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
			if a.EntangleReason != tc.wantReason {
				t.Fatalf("EntangleReason = %q, want %q", a.EntangleReason, tc.wantReason)
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
	snap, err := p.buildRegroupSnapshot(context.Background(), r, rgRoot, &fakeReporter{})
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
	if !b.Entangled || b.EntangleReason != itunesservice.EntangleGroupedSource || b.FreshBook || len(b.Moves) != 0 {
		t.Fatalf("group B = %+v, want skipped (split out of a grouped book)", b)
	}
}

// The snapshot's primary semantics, per version-group shape: the incumbent is
// the explicitly-true member, else the ONE unset member; several unset members
// (or none electable) leave the group with no incumbent. A soft-deleted
// explicit-true member does not count, and is not in snap.Books at all (it
// can never be a target or source). Ungrouped books read unset as primary.
// Rows with no CreatedAt must not panic the snapshot (they used to).
func TestBuildRegroupSnapshot_IncumbentSemantics(t *testing.T) {
	tr, fa := rgFlag(true), rgFlag(false)
	trashed := rgBook("T1", "vg4", tr)
	trashed.MarkedForDeletion = rgFlag(true)
	noCreated := rgBook("U2", "", fa)
	noCreated.CreatedAt = nil
	books := []database.Book{
		rgBook("M1", "vg1", tr), rgBook("M2", "vg1", nil), // {marked,unset}
		rgBook("N1", "vg2", nil), rgBook("N2", "vg2", fa), // {unset,not-primary}
		rgBook("A1", "vg3", nil), rgBook("A2", "vg3", nil), rgBook("A3", "vg3", nil), // {unset,unset,unset}
		trashed, rgBook("T2", "vg4", nil), // trashed true member + one live unset
		rgBook("U1", "", nil), noCreated, // ungrouped
	}
	p := &Plugin{}
	snap, err := p.buildRegroupSnapshot(context.Background(), &regroupFakeReader{books: books}, rgRoot, &fakeReporter{})
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	want := map[string]struct{ primary, noIncumbent, legacy bool }{
		"M1": {true, false, true}, "M2": {false, false, true},
		"N1": {true, false, true}, "N2": {false, false, true},
		"A1": {false, true, true}, "A2": {false, true, true}, "A3": {false, true, true},
		"T2": {true, false, true},
		"U1": {true, false, false}, "U2": {false, false, false},
	}
	for id, w := range want {
		got := snap.Books[id]
		if got.IsPrimary != w.primary || got.GroupHasNoIncumbent != w.noIncumbent || got.LegacyEntangled != w.legacy {
			t.Errorf("%s: primary=%v noIncumbent=%v legacy=%v, want %v/%v/%v",
				id, got.IsPrimary, got.GroupHasNoIncumbent, got.LegacyEntangled, w.primary, w.noIncumbent, w.legacy)
		}
	}
	if _, ok := snap.Books["T1"]; ok {
		t.Errorf("soft-deleted T1 is in snap.Books; a trashed book must never be planned")
	}
	if got := snap.Books["U2"].CreatedAtUnix; got != math.MaxInt64 {
		t.Errorf("U2 CreatedAtUnix = %d, want MaxInt64 (unknown ranks newest)", got)
	}
}

const regroupDeltaXML = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
		<key>1</key>
		<dict>
			<key>Track ID</key><integer>1</integer>
			<key>Persistent ID</key><string>AABB0011CCDD2233</string>
			<key>Name</key><string>Delta Book Part 1</string>
			<key>Album</key><string>Delta Album</string>
			<key>Artist</key><string>Delta Author</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>file://localhost/missing/delta1.m4b</string>
		</dict>
		<key>2</key>
		<dict>
			<key>Track ID</key><integer>2</integer>
			<key>Persistent ID</key><string>EEFF4455AABB6677</string>
			<key>Name</key><string>Delta Book Part 2</string>
			<key>Album</key><string>Delta Album</string>
			<key>Artist</key><string>Delta Author</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>file://localhost/missing/delta2.m4b</string>
		</dict>
	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>
`

// The dry run (the default) reports the rule-change delta and writes nothing:
// an iTunes edition linked to a library copy, plus one ungrouped fragment of
// the same album, was skipped by the legacy rule and is planned now.
func TestITunesRegroupDryRun_ReportsRuleDelta(t *testing.T) {
	s := regroupStore(t)
	vg := "vg-delta"
	tr, fa := rgFlag(true), rgFlag(false)
	lib, err := s.CreateBook(&database.Book{Title: "Delta (library)", VersionGroupID: &vg, IsPrimaryVersion: tr})
	if err != nil {
		t.Fatalf("CreateBook library: %v", err)
	}
	ed, err := s.CreateBook(&database.Book{Title: "Delta (itunes)", VersionGroupID: &vg, IsPrimaryVersion: fa})
	if err != nil {
		t.Fatalf("CreateBook edition: %v", err)
	}
	frag := seedBook(t, s, "Delta fragment")
	seedFilePID(t, s, ed.ID, "AABB0011CCDD2233")
	seedFilePID(t, s, frag, "EEFF4455AABB6677")
	_ = lib

	xmlPath := filepath.Join(t.TempDir(), "lib.xml")
	if err := os.WriteFile(xmlPath, []byte(regroupDeltaXML), 0o600); err != nil {
		t.Fatalf("write xml: %v", err)
	}
	raw, _ := json.Marshal(map[string]string{"xmlPath": xmlPath})
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = rgRoot
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })
	p := New(fakeDeps{store: s})
	rep := &fakeReporter{}
	if err := p.runITunesRegroup(context.Background(), raw, rep); err != nil {
		t.Fatalf("runITunesRegroup: %v", err)
	}
	var delta string
	for _, l := range rep.logs {
		if strings.HasPrefix(l, "RULE CHANGE") {
			delta = l
		}
	}
	if !strings.Contains(delta, "unblocked=1 ") || !strings.Contains(delta, "newly-blocked=0 ") || !strings.Contains(delta, "legacy-rule-skipped=1") {
		t.Fatalf("RULE CHANGE line = %q, want unblocked=1 newly-blocked=0 legacy-rule-skipped=1 (logs: %v)", delta, rep.logs)
	}
	if files, _ := s.GetBookFiles(frag); len(files) != 1 {
		t.Fatalf("dry run moved files: fragment has %d files, want 1", len(files))
	}
}

// End-to-end apply on the newly allowed shape: an iTunes edition (non-primary,
// linked to a library copy) receives its album's ungrouped fragment. The
// fragment is deleted; the edition keeps its version link and its explicit
// non-primary flag; the library copy is untouched.
func TestITunesRegroupApply_GroupedEditionReceivesFragment(t *testing.T) {
	s := regroupStore(t)
	vg := "vg-apply"
	tr, fa := rgFlag(true), rgFlag(false)
	lib, err := s.CreateBook(&database.Book{Title: "Library copy", VersionGroupID: &vg, IsPrimaryVersion: tr})
	if err != nil {
		t.Fatalf("CreateBook library: %v", err)
	}
	libFile := &database.BookFile{BookID: lib.ID, FilePath: "/library/book.m4b"}
	if err := s.CreateBookFile(libFile); err != nil {
		t.Fatalf("CreateBookFile library: %v", err)
	}
	ed, err := s.CreateBook(&database.Book{Title: "iTunes edition", VersionGroupID: &vg, IsPrimaryVersion: fa})
	if err != nil {
		t.Fatalf("CreateBook edition: %v", err)
	}
	frag := seedBook(t, s, "fragment")
	seedFilePID(t, s, ed.ID, "p1")
	seedFilePID(t, s, frag, "p2")

	p := &Plugin{}
	rep := &fakeReporter{}
	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "The Book", PIDs: []string{"p1", "p2"}}}, snap)
	if plan.Consolidated != 1 || plan.Groups[0].Target != ed.ID {
		t.Fatalf("plan = %+v, want consolidate onto the edition %s", plan.Groups, ed.ID)
	}
	if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v (logs %v)", err, rep.logs)
	}

	if b, _ := s.GetBookByID(frag); b != nil && !b.IsSoftDeleted() {
		t.Fatalf("fragment %s still live after apply", frag)
	}
	edFiles, _ := s.GetBookFiles(ed.ID)
	edExts, _ := s.GetExternalIDsForBook(ed.ID)
	if len(edFiles) != 2 || len(edExts) != 2 {
		t.Fatalf("edition has %d files / %d ext-ids, want 2/2", len(edFiles), len(edExts))
	}
	edAfter, err := s.GetBookByID(ed.ID)
	if err != nil || edAfter == nil {
		t.Fatalf("GetBookByID edition: %v", err)
	}
	if edAfter.VersionGroupID == nil || *edAfter.VersionGroupID != vg ||
		edAfter.IsPrimaryVersion == nil || *edAfter.IsPrimaryVersion {
		t.Fatalf("edition group/flag changed: vg=%v primary=%v", edAfter.VersionGroupID, edAfter.IsPrimaryVersion)
	}
	libAfter, err := s.GetBookByID(lib.ID)
	if err != nil || libAfter == nil {
		t.Fatalf("GetBookByID library: %v", err)
	}
	if libAfter.Title != "Library copy" || libAfter.VersionGroupID == nil || *libAfter.VersionGroupID != vg ||
		libAfter.IsPrimaryVersion == nil || !*libAfter.IsPrimaryVersion {
		t.Fatalf("library copy changed: %+v", libAfter)
	}
	libFiles, _ := s.GetBookFiles(lib.ID)
	if len(libFiles) != 1 || libFiles[0].FilePath != "/library/book.m4b" {
		t.Fatalf("library copy files changed: %+v", libFiles)
	}
}

// rgPlan builds the snapshot from r and plans groups over it.
func rgPlan(t *testing.T, r *regroupFakeReader, groups []itunesservice.HealGroup) (itunesservice.Snapshot, itunesservice.RegroupPlan) {
	t.Helper()
	p := &Plugin{}
	snap, err := p.buildRegroupSnapshot(context.Background(), r, rgRoot, &fakeReporter{})
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	return snap, itunesservice.PlanRegroup(groups, snap)
}

// rgAssertNeverTouches fails if any group targets id, moves a file off it, or
// the plan deletes it.
func rgAssertNeverTouches(t *testing.T, plan itunesservice.RegroupPlan, id string) {
	t.Helper()
	for _, a := range plan.Groups {
		if a.Target == id {
			t.Fatalf("group %q targets %s: %+v", a.Title, id, a)
		}
		for _, m := range a.Moves {
			if m.From == id {
				t.Fatalf("group %q moves %s off %s", a.Title, m.PID, id)
			}
		}
	}
	for _, d := range plan.DeleteBooks {
		if d == id {
			t.Fatalf("plan deletes %s", id)
		}
	}
}

// Probe B: a trashed book's row is absent from the book scan (the real store
// drops it) while its file rows still come back. Live Y holds p1 and p2,
// trashed X holds p3; G1{p1} claims Y, so G2{p2,p3} used to pick X -- a book
// missing from snap.Books -- and entanglement allowed a target it could not
// find. The second variant keeps X's row but soft-deleted.
func TestITunesRegroupEntanglementRule_TrashedBookNeverTarget(t *testing.T) {
	trashed := rgBook("X", "", nil)
	trashed.MarkedForDeletion = rgFlag(true)
	for name, books := range map[string][]database.Book{
		"row absent from book scan": {rgBook("Y", "", nil)},
		"row present, soft-deleted": {rgBook("Y", "", nil), trashed},
	} {
		t.Run(name, func(t *testing.T) {
			r := &regroupFakeReader{
				books: books,
				files: []database.BookFileCore{rgFile("f1", "Y", "p1"), rgFile("f2", "Y", "p2"), rgFile("f3", "X", "p3")},
			}
			snap, plan := rgPlan(t, r, []itunesservice.HealGroup{
				{Title: "G1", PIDs: []string{"p1"}},
				{Title: "G2", PIDs: []string{"p2", "p3"}},
			})
			if _, ok := snap.PIDLoc["p3"]; ok {
				t.Fatalf("PIDLoc holds p3 on trashed X: %+v", snap.PIDLoc["p3"])
			}
			rgAssertNeverTouches(t, plan, "X")
			if g2 := plan.Groups[1]; len(g2.Unresolved) != 1 || g2.Unresolved[0] != "p3" {
				t.Fatalf("G2 unresolved = %v, want [p3]", g2.Unresolved)
			}
		})
	}
}

// Probe C: a trashed book as a SOURCE. Live Y holds p1, trashed X holds p2;
// G{p1,p2} must not plan a move off X.
func TestITunesRegroupEntanglementRule_TrashedBookNeverSource(t *testing.T) {
	r := &regroupFakeReader{
		books: []database.Book{rgBook("Y", "", nil)},
		files: []database.BookFileCore{rgFile("f1", "Y", "p1"), rgFile("f2", "X", "p2")},
	}
	_, plan := rgPlan(t, r, []itunesservice.HealGroup{{Title: "G", PIDs: []string{"p1", "p2"}}})
	rgAssertNeverTouches(t, plan, "X")
	if a := plan.Groups[0]; a.Target != "Y" || len(a.Moves) != 0 || plan.AlreadyCorrect != 1 {
		t.Fatalf("G = %+v, want already-correct on Y with p2 unresolved", a)
	}
}

// Doctor Who / Big Finish / Torchwood are manual-only: a group is skipped,
// counted, and not even retitled, whether the library is named by the heal
// group's title, a holder's title, file path or series name.
func TestITunesRegroupManualOnlySkipped(t *testing.T) {
	series := rgBook("S1", "", nil)
	sid := 7
	series.SeriesID = &sid
	titled := rgBook("T1", "", nil)
	titled.Title = "Torchwood: Aliens Among Us"
	cases := []struct {
		name   string
		title  string
		books  []database.Book
		files  []database.BookFileCore
		series []database.Series
	}{
		{"group title", "Doctor Who: Spearhead from Space",
			[]database.Book{rgBook("F1", "", nil), rgBook("F2", "", nil)},
			[]database.BookFileCore{rgFile("f1", "F1", "p1"), rgFile("f2", "F2", "p2")}, nil},
		{"holder file path", "Some Album",
			[]database.Book{rgBook("F1", "", nil), rgBook("F2", "", nil)},
			[]database.BookFileCore{rgFileAt("f1", "F1", "p1", rgITunes+"/Big Finish/Album/01.m4b"), rgFile("f2", "F2", "p2")}, nil},
		{"holder series", "Some Album",
			[]database.Book{series, rgBook("F2", "", nil)},
			[]database.BookFileCore{rgFile("f1", "S1", "p1"), rgFile("f2", "F2", "p2")},
			[]database.Series{{ID: 7, Name: "Doctor Who: The Monthly Adventures"}}},
		{"holder title", "Some Album",
			[]database.Book{titled, rgBook("F2", "", nil)},
			[]database.BookFileCore{rgFile("f1", "T1", "p1"), rgFile("f2", "F2", "p2")}, nil},
		{"already correct (no moves, no retitle)", "Doctor Who: Shada",
			[]database.Book{rgBook("F1", "", nil)},
			[]database.BookFileCore{rgFile("f1", "F1", "p1"), rgFile("f2", "F1", "p2")}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &regroupFakeReader{books: tc.books, files: tc.files, series: tc.series}
			rgAssertManualOnlySkip(t, r, tc.title)
		})
	}
}

// The owner-manual signal is on what the book ROW does not carry -- a file's
// transcribed title, an author credit, a franchise: tag -- and title, path,
// series, narrator and publisher are all clean. The row-only check (the
// former applygate.BookRowManualOnly, now the unexported bookRowManualOnly
// inside applygate.BookManualOnly) let each of these through; the
// whole-book check holds them (owner decision 2026-10-05).
func TestITunesRegroupManualOnlySkipped_SignalOffTheRow(t *testing.T) {
	dw := "Doctor Who: The Chimes of Midnight"
	transcribed := rgFile("f1", "F1", "p1")
	transcribed.TranscribedTitle = &dw
	books := func() []database.Book { return []database.Book{rgBook("F1", "", nil), rgBook("F2", "", nil)} }
	cases := []struct {
		name string
		r    *regroupFakeReader
	}{
		{"file transcribed title", &regroupFakeReader{books: books(),
			files: []database.BookFileCore{transcribed, rgFile("f2", "F2", "p2")}}},
		{"author credit", &regroupFakeReader{books: books(),
			files:   []database.BookFileCore{rgFile("f1", "F1", "p1"), rgFile("f2", "F2", "p2")},
			links:   map[string][]database.BookAuthor{"F1": {{BookID: "F1", AuthorID: 9}}},
			authors: map[int]*database.Author{9: {ID: 9, Name: "Big Finish Productions"}}}},
		{"franchise tag", &regroupFakeReader{books: books(),
			files: []database.BookFileCore{rgFile("f1", "F1", "p1"), rgFile("f2", "F2", "p2")},
			tags:  map[string][]string{"F2": franchise.Tags(franchise.BigFinish, "")}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rgAssertManualOnlySkip(t, tc.r, "Some Album")
		})
	}
}

// rgAssertManualOnlySkip plans one two-PID heal group over r and asserts it
// is skipped as owner-manual-only with no target, moves or deletes.
func rgAssertManualOnlySkip(t *testing.T, r *regroupFakeReader, title string) {
	t.Helper()
	_, plan := rgPlan(t, r, []itunesservice.HealGroup{{Title: title, PIDs: []string{"p1", "p2"}}})
	a := plan.Groups[0]
	if !a.ManualOnly || a.Target != "" || len(a.Moves) != 0 || a.FreshBook {
		t.Fatalf("action = %+v, want manual-only skip with no target or moves", a)
	}
	if plan.ManualOnlySkipped != 1 || plan.Consolidated != 0 || plan.AlreadyCorrect != 0 || len(plan.DeleteBooks) != 0 {
		t.Fatalf("manual-only=%d consolidated=%d already-correct=%d deletes=%v, want 1/0/0/none",
			plan.ManualOnlySkipped, plan.Consolidated, plan.AlreadyCorrect, plan.DeleteBooks)
	}
}

// The apply re-reads each group's books just before writing it and re-runs
// the same rule: a group whose books changed since the plan is skipped with
// a logged reason, and nothing of it is written.
func TestITunesRegroupApply_RecheckSkipsChangedGroup(t *testing.T) {
	cases := []struct {
		name   string
		change func(t *testing.T, s *database.PebbleStore, frag, other string)
		want   string
	}{
		{"source became organized", func(t *testing.T, s *database.PebbleStore, frag, _ string) {
			if _, err := s.ModifyBook(frag, func(b *database.Book) error {
				st := "organized"
				b.LibraryState = &st
				return nil
			}); err != nil {
				t.Fatalf("ModifyBook: %v", err)
			}
		}, itunesservice.EntangleLibrarySource},
		{"source file moved elsewhere", func(t *testing.T, s *database.PebbleStore, frag, other string) {
			files, err := s.GetBookFiles(frag)
			if err != nil || len(files) != 1 {
				t.Fatalf("GetBookFiles: %v (%d)", err, len(files))
			}
			if err := s.MoveBookFilesToBook([]string{files[0].ID}, frag, other); err != nil {
				t.Fatalf("MoveBookFilesToBook: %v", err)
			}
		}, itunesservice.EntangleChanged},
		{"source series became Big Finish", func(t *testing.T, s *database.PebbleStore, frag, _ string) {
			sr, err := s.CreateSeries("Big Finish Originals", nil)
			if err != nil || sr == nil {
				t.Fatalf("CreateSeries: %v", err)
			}
			if _, err := s.ModifyBook(frag, func(b *database.Book) error {
				b.SeriesID = &sr.ID
				return nil
			}); err != nil {
				t.Fatalf("ModifyBook: %v", err)
			}
		}, itunesservice.ReasonOwnerManualOnly},
		// Off-the-row signals the row-only check could not see.
		{"source tagged Big Finish", func(t *testing.T, s *database.PebbleStore, frag, _ string) {
			if err := s.AddBookTag(frag, franchise.Tags(franchise.BigFinish, "")[0]); err != nil {
				t.Fatalf("AddBookTag: %v", err)
			}
		}, itunesservice.ReasonOwnerManualOnly},
		{"source credited to Big Finish", func(t *testing.T, s *database.PebbleStore, frag, _ string) {
			a, err := s.CreateAuthor("Big Finish Productions")
			if err != nil || a == nil {
				t.Fatalf("CreateAuthor: %v", err)
			}
			if err := s.SetBookAuthors(frag, []database.BookAuthor{{BookID: frag, AuthorID: a.ID}}); err != nil {
				t.Fatalf("SetBookAuthors: %v", err)
			}
		}, itunesservice.ReasonOwnerManualOnly},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := regroupStore(t)
			vg := "vg-recheck"
			tr, fa := rgFlag(true), rgFlag(false)
			if _, err := s.CreateBook(&database.Book{Title: "Library copy", VersionGroupID: &vg, IsPrimaryVersion: tr}); err != nil {
				t.Fatalf("CreateBook library: %v", err)
			}
			ed, err := s.CreateBook(&database.Book{Title: "iTunes edition", VersionGroupID: &vg, IsPrimaryVersion: fa})
			if err != nil {
				t.Fatalf("CreateBook edition: %v", err)
			}
			frag := seedBook(t, s, "fragment")
			other := seedBook(t, s, "unrelated")
			seedFilePID(t, s, ed.ID, "p1")
			seedFilePID(t, s, frag, "p2")

			p := &Plugin{}
			rep := &fakeReporter{}
			snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
			if err != nil {
				t.Fatalf("buildRegroupSnapshot: %v", err)
			}
			plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "The Book", PIDs: []string{"p1", "p2"}}}, snap)
			if plan.Consolidated != 1 || plan.Groups[0].Target != ed.ID {
				t.Fatalf("plan = %+v, want consolidate onto the edition", plan.Groups)
			}

			tc.change(t, s, frag, other)

			if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
				t.Fatalf("applyRegroupPlan: %v (logs %v)", err, rep.logs)
			}
			var found bool
			for _, l := range rep.logs {
				if strings.Contains(l, "recheck refuses it as "+tc.want) {
					found = true
				}
				if strings.HasPrefix(l, "APPLIED") && !strings.Contains(l, "moved=0 ") {
					t.Fatalf("apply moved files after a refused recheck: %s", l)
				}
			}
			if !found {
				t.Fatalf("no recheck refusal %q logged: %v", tc.want, rep.logs)
			}
			if edFiles, _ := s.GetBookFiles(ed.ID); len(edFiles) != 1 {
				t.Fatalf("edition has %d files, want 1 (group must be skipped)", len(edFiles))
			}
			if edAfter, _ := s.GetBookByID(ed.ID); edAfter == nil || edAfter.Title != "iTunes edition" {
				t.Fatalf("edition retitled despite skip: %+v", edAfter)
			}
		})
	}
}

// Without a library root nothing can tell a library copy apart, so the op
// refuses to run at all -- the dry run included.
func TestITunesRegroup_RefusesWithoutRootDir(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = ""
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })
	raw, _ := json.Marshal(map[string]string{"xmlPath": "/nonexistent/lib.xml"})
	p := New(fakeDeps{store: regroupStore(t)})
	err := p.runITunesRegroup(context.Background(), raw, &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "root_dir") {
		t.Fatalf("err = %v, want a root_dir refusal", err)
	}
}

// rgSeedFileAt adds a book_file row at path carrying pid, plus its itunes
// ext-id mapping.
func rgSeedFileAt(t *testing.T, s *database.PebbleStore, bookID, pid, path string) {
	t.Helper()
	if err := s.CreateBookFile(&database.BookFile{BookID: bookID, ITunesPersistentID: pid, FilePath: path}); err != nil {
		t.Fatalf("CreateBookFile(%s,%s): %v", bookID, pid, err)
	}
	if err := s.CreateExternalIDMapping(&database.ExternalIDMapping{Source: "itunes", ExternalID: pid, BookID: bookID}); err != nil {
		t.Fatalf("CreateExternalIDMapping(%s): %v", pid, err)
	}
}

// A group whose files already all sit on one book renames that book to the
// iTunes album title -- unless the book is a library-folder copy (a file
// under the root, or organized). Owner rule: the library copy is canonical
// and its metadata comes from the metadata pipeline, never from iTunes album
// tags. The iTunes-only control is still retitled. "becomes library copy
// after plan" checks the apply-time recheck keeps the title too.
func TestITunesRegroupApply_LibraryCopyKeepsTitle(t *testing.T) {
	cases := []struct {
		name      string
		dir       string // file directory for both tracks
		organized bool
		afterPlan func(t *testing.T, s *database.PebbleStore, id string)
		wantKept  bool
		planKeeps bool
	}{
		{name: "library copy under root", dir: rgRoot + "/Author/Book", wantKept: true, planKeeps: true},
		{name: "organized with iTunes-tree files", dir: rgITunes + "/Album", organized: true, wantKept: true, planKeeps: true},
		{name: "control: iTunes-only book is retitled", dir: rgITunes + "/Album", wantKept: false},
		{name: "becomes library copy after plan", dir: rgITunes + "/Album", wantKept: true,
			afterPlan: func(t *testing.T, s *database.PebbleStore, id string) {
				if _, err := s.ModifyBook(id, func(b *database.Book) error {
					st := "organized"
					b.LibraryState = &st
					return nil
				}); err != nil {
					t.Fatalf("ModifyBook: %v", err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := regroupStore(t)
			bk := &database.Book{Title: "Canonical Title"}
			if tc.organized {
				st := "organized"
				bk.LibraryState = &st
			}
			b, err := s.CreateBook(bk)
			if err != nil || b == nil {
				t.Fatalf("CreateBook: %v", err)
			}
			rgSeedFileAt(t, s, b.ID, "p1", tc.dir+"/01.m4b")
			rgSeedFileAt(t, s, b.ID, "p2", tc.dir+"/02.m4b")

			p := &Plugin{}
			rep := &fakeReporter{}
			snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
			if err != nil {
				t.Fatalf("buildRegroupSnapshot: %v", err)
			}
			plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "iTunes Album Tag", PIDs: []string{"p1", "p2"}}}, snap)
			a := plan.Groups[0]
			if a.Target != b.ID || len(a.Moves) != 0 || plan.AlreadyCorrect != 1 {
				t.Fatalf("plan = %+v, want already-correct on %s", a, b.ID)
			}
			if a.KeepTitle != tc.planKeeps || (plan.LibraryTitleKept == 1) != tc.planKeeps {
				t.Fatalf("KeepTitle=%v LibraryTitleKept=%d, want keep=%v", a.KeepTitle, plan.LibraryTitleKept, tc.planKeeps)
			}
			if tc.afterPlan != nil {
				tc.afterPlan(t, s, b.ID)
			}
			if _, err := p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep); err != nil {
				t.Fatalf("applyRegroupPlan: %v (logs %v)", err, rep.logs)
			}
			after, err := s.GetBookByID(b.ID)
			if err != nil || after == nil {
				t.Fatalf("GetBookByID: %v", err)
			}
			want := "iTunes Album Tag"
			if tc.wantKept {
				want = "Canonical Title"
			}
			if after.Title != want {
				t.Fatalf("title = %q, want %q", after.Title, want)
			}
			var applied string
			for _, l := range rep.logs {
				if strings.HasPrefix(l, "APPLIED") {
					applied = l
				}
			}
			kept := "library-title-kept=0 "
			if tc.wantKept {
				kept = "library-title-kept=1 "
			}
			if !strings.Contains(applied, kept) {
				t.Fatalf("APPLIED line %q, want %s", applied, kept)
			}
		})
	}
}

// Probe A again under other spellings of the same root: IsWithin compares raw
// strings, so "/library/./" and "/library/" used to miss the library copy and
// plan a move into it.
func TestITunesRegroupEntanglementRule_ProbeARootSpellings(t *testing.T) {
	tr, fa := rgFlag(true), rgFlag(false)
	for _, root := range []string{"/library", "/library/", "/library/./", "/library//"} {
		t.Run(root, func(t *testing.T) {
			r := &regroupFakeReader{
				books: []database.Book{rgBook("I0", "vg1", tr), rgBook("L1", "vg1", fa), rgBook("F1", "", nil)},
				files: []database.BookFileCore{
					rgFileAt("f1", "L1", "p1", "/library/Author//Book/./p1.m4b"), rgFile("f2", "F1", "p2"),
				},
			}
			p := &Plugin{}
			snap, err := p.buildRegroupSnapshot(context.Background(), r, root, &fakeReporter{})
			if err != nil {
				t.Fatalf("buildRegroupSnapshot: %v", err)
			}
			if !snap.Books["L1"].HasLibraryFile {
				t.Fatalf("L1 not seen as a library copy under root %q", root)
			}
			plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "G", PIDs: []string{"p1", "p2"}}}, snap)
			if a := plan.Groups[0]; !a.Entangled || a.EntangleReason != itunesservice.EntangleLibraryTarget || len(a.Moves) != 0 {
				t.Fatalf("action = %+v, want refused as %q", a, itunesservice.EntangleLibraryTarget)
			}
		})
	}
}

// An organized target is a library copy when any of its rows sits outside the
// iTunes tree, even if the root comparison missed it (here: a root that does
// not match the copy's real path, as with a symlinked spelling). The control
// (S4) is an organized in-place iTunes edition whose rows are ALL in the
// iTunes tree: it still consolidates its album's fragment.
func TestITunesRegroupEntanglementRule_OrganizedTarget(t *testing.T) {
	cases := []struct {
		name       string
		targetPath string
		wantReason string
	}{
		{"organized copy outside iTunes tree, root missed", "/mnt/real-library/Author/Book/p1.m4b", itunesservice.EntangleLibraryTarget},
		{"S4: organized in-place iTunes edition consolidates", rgITunes + "/Album/p1.m4b", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tgt := rgOrganized(rgBook("T1", "", nil))
			asin := "B000ORG"
			tgt.ASIN = &asin // outranks F1, so T1 is the target
			r := &regroupFakeReader{
				books: []database.Book{tgt, rgBook("F1", "", nil)},
				files: []database.BookFileCore{rgFileAt("f1", "T1", "p1", tc.targetPath), rgFile("f2", "F1", "p2")},
			}
			_, plan := rgPlan(t, r, []itunesservice.HealGroup{{Title: "G", PIDs: []string{"p1", "p2"}}})
			a := plan.Groups[0]
			if a.EntangleReason != tc.wantReason {
				t.Fatalf("action = %+v, want reason %q", a, tc.wantReason)
			}
			if tc.wantReason == "" && (a.Target != "T1" || len(a.Moves) != 1 || !a.KeepTitle) {
				t.Fatalf("S4 action = %+v, want 1 move onto T1 with its title kept", a)
			}
		})
	}
}

// rgLockProbeStore runs a hook when the apply-time recheck reads the
// target's version group, before the bulk move and before the title write,
// so a test can act while applyRegroupGroup is mid-group.
type rgLockProbeStore struct {
	*database.PebbleStore
	onGroupRead, onMove, onModify func()
}

func (s *rgLockProbeStore) GetBooksByVersionGroup(gid string) ([]database.Book, error) {
	if s.onGroupRead != nil {
		s.onGroupRead()
	}
	return s.PebbleStore.GetBooksByVersionGroup(gid)
}

func (s *rgLockProbeStore) MoveBookFilesToBookBulk(moves []database.BookFileMove, target string) error {
	if s.onMove != nil {
		s.onMove()
	}
	return s.PebbleStore.MoveBookFilesToBookBulk(moves, target)
}

func (s *rgLockProbeStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.onModify != nil {
		s.onModify()
	}
	return s.PebbleStore.ModifyBook(id, fn)
}

// The apply holds the target's version-group hand-off lock from before the
// recheck reads the group through the group's last write: a concurrent
// hand-off that would crown the edition (the target the recheck found a
// non-primary member) cannot land between the recheck and the moves onto it.
// The hand-off starts while the recheck is reading the group, so a lock taken
// only after the recheck lets it through; it must block until the group is
// written and run after.
func TestITunesRegroupApply_HoldsGroupLockAcrossRecheckAndMoves(t *testing.T) {
	s := regroupStore(t)
	vg := "vg-lock"
	tr, fa := rgFlag(true), rgFlag(false)
	lib, err := s.CreateBook(&database.Book{Title: "Library copy", VersionGroupID: &vg, IsPrimaryVersion: tr})
	if err != nil {
		t.Fatalf("CreateBook library: %v", err)
	}
	ed, err := s.CreateBook(&database.Book{Title: "iTunes edition", VersionGroupID: &vg, IsPrimaryVersion: fa})
	if err != nil {
		t.Fatalf("CreateBook edition: %v", err)
	}
	frag := seedBook(t, s, "fragment")
	seedFilePID(t, s, ed.ID, "p1")
	seedFilePID(t, s, frag, "p2")

	p := &Plugin{}
	rep := &fakeReporter{}
	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "The Book", PIDs: []string{"p1", "p2"}}}, snap)
	if plan.Consolidated != 1 || plan.Groups[0].Target != ed.ID {
		t.Fatalf("plan = %+v, want consolidate onto the edition %s", plan.Groups, ed.ID)
	}

	// A lock that is not held lets Crown through in well under a
	// millisecond; the wait only bounds how long a held lock is watched.
	const wait = 500 * time.Millisecond
	crowned := make(chan error, 1)
	var started atomic.Bool
	notCrowned := func(where string) {
		select {
		case err := <-crowned:
			crowned <- err
			t.Errorf("hand-off crowned the target %s (err=%v): group lock not held there", where, err)
		case <-time.After(wait):
		}
	}
	probe := &rgLockProbeStore{PebbleStore: s}
	probe.onGroupRead = func() {
		if !started.CompareAndSwap(false, true) {
			return
		}
		go func() {
			_, err := versionprimary.Crown(s, vg, ed.ID)
			crowned <- err
		}()
		notCrowned("during the recheck")
	}
	probe.onMove = func() { notCrowned("before the moves") }
	probe.onModify = func() { notCrowned("before the title write") }
	if _, err := p.applyRegroupPlan(context.Background(), probe, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v (logs %v)", err, rep.logs)
	}
	if !started.Load() {
		t.Fatalf("apply's recheck never read the target's version group (logs %v)", rep.logs)
	}

	select {
	case err := <-crowned:
		if err != nil {
			t.Fatalf("Crown after apply: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("hand-off still blocked after the apply returned: group lock leaked")
	}
	if edFiles, _ := s.GetBookFiles(ed.ID); len(edFiles) != 2 {
		t.Fatalf("edition has %d files, want 2 (moves written under the lock)", len(edFiles))
	}
	if edAfter, _ := s.GetBookByID(ed.ID); edAfter == nil || edAfter.Title != "The Book" {
		t.Fatalf("edition not retitled under the lock: %+v", edAfter)
	}
	if libAfter, _ := s.GetBookByID(lib.ID); libAfter == nil || libAfter.IsPrimaryVersion == nil || *libAfter.IsPrimaryVersion {
		t.Fatalf("library copy still primary after the queued hand-off: %+v", libAfter)
	}
}

// rgJoinGroupStore moves book id into version group vg right after the first
// read of it returns, as a concurrent writer would between the apply's
// pre-lock read of the target and its recheck.
type rgJoinGroupStore struct {
	*database.PebbleStore
	id, vg string
	done   bool
}

func (s *rgJoinGroupStore) GetBookByID(id string) (*database.Book, error) {
	b, err := s.PebbleStore.GetBookByID(id)
	if id == s.id && !s.done {
		s.done = true
		if _, merr := s.PebbleStore.ModifyBook(id, func(mb *database.Book) error {
			mb.VersionGroupID = &s.vg
			return nil
		}); merr != nil {
			return nil, merr
		}
	}
	return b, err
}

// A target that joins a version group between the apply's pre-lock read and
// the recheck is refused as changed: the recheck would otherwise read the
// group's incumbent without holding that group's lock.
func TestITunesRegroupApply_TargetJoinedGroupRefused(t *testing.T) {
	s := regroupStore(t)
	b1 := seedBook(t, s, "Frag A")
	b2 := seedBook(t, s, "Frag B")
	seedFilePID(t, s, b1, "p1")
	seedFilePID(t, s, b2, "p2")

	p := &Plugin{}
	rep := &fakeReporter{}
	snap, err := p.buildRegroupSnapshot(context.Background(), s, rgRoot, rep)
	if err != nil {
		t.Fatalf("buildRegroupSnapshot: %v", err)
	}
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{{Title: "Merged Book", PIDs: []string{"p1", "p2"}}}, snap)
	if plan.Consolidated != 1 || plan.Groups[0].Target == "" || plan.Groups[0].FreshBook {
		t.Fatalf("plan = %+v, want consolidate onto an existing book", plan.Groups)
	}
	target := plan.Groups[0].Target

	wrapped := &rgJoinGroupStore{PebbleStore: s, id: target, vg: "vg-joined"}
	if _, err := p.applyRegroupPlan(context.Background(), wrapped, plan, rgRoot, rep); err != nil {
		t.Fatalf("applyRegroupPlan: %v (logs %v)", err, rep.logs)
	}
	var found bool
	for _, l := range rep.logs {
		if strings.Contains(l, "recheck refuses it as "+itunesservice.EntangleChanged) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no %q refusal logged: %v", itunesservice.EntangleChanged, rep.logs)
	}
	if files, _ := s.GetBookFiles(target); len(files) != 1 {
		t.Fatalf("target has %d files, want 1 (group must be skipped)", len(files))
	}
}
