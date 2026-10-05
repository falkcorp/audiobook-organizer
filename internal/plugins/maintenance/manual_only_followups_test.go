// file: internal/plugins/maintenance/manual_only_followups_test.go
// version: 1.1.0
// guid: 414c97a5-21fa-425f-81df-a00e9bf95fd6
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// Follow-ups to the whole-book owner-manual check (PR #3754 review): a read
// failure fails only what it touches, the regroup snapshot holds no
// BookFile per row, and author-path-link re-checks before it writes.

// --- itunes.regroup: snapshot read failure ---

// One book's owner-manual read fails in the snapshot: that book is marked
// ManualCheckFailed and only the group holding it is skipped; the snapshot
// does not fail and the other group is still planned.
func TestBuildRegroupSnapshot_CheckFailureSkipsOnlyItsGroup(t *testing.T) {
	r := &regroupFakeReader{
		books: []database.Book{rgBook("F1", "", nil), rgBook("F2", "", nil), rgBook("G1", "", nil), rgBook("G2", "", nil)},
		files: []database.BookFileCore{rgFile("f1", "F1", "p1"), rgFile("f2", "F2", "p2"),
			rgFile("g1", "G1", "q1"), rgFile("g2", "G2", "q2")},
		linkErr: map[string]error{"F1": errors.New("simulated book_authors read failure")},
	}
	snap, plan := rgPlan(t, r, []itunesservice.HealGroup{
		{Title: "Album F", PIDs: []string{"p1", "p2"}},
		{Title: "Album G", PIDs: []string{"q1", "q2"}},
	})
	require.True(t, snap.Books["F1"].ManualCheckFailed, "F1's failed check must be recorded")
	require.False(t, snap.Books["F1"].ManualOnly, "a failed check is not an owner-manual book")
	require.False(t, snap.Books["F2"].ManualCheckFailed)

	f, g := plan.Groups[0], plan.Groups[1]
	require.True(t, f.ManualCheckFailed, "group F: %+v", f)
	require.False(t, f.ManualOnly, "group F must not count as owner-manual: %+v", f)
	require.Empty(t, f.Target)
	require.Empty(t, f.Moves)
	require.False(t, f.FreshBook)
	require.Equal(t, 1, plan.ManualCheckFailedSkipped)
	require.Equal(t, 0, plan.ManualOnlySkipped)

	require.False(t, g.ManualCheckFailed, "group G: %+v", g)
	require.NotEmpty(t, g.Target, "group G must still be planned: %+v", g)
	require.Len(t, g.Moves, 1)
	require.Equal(t, 1, plan.Consolidated)
	require.NotContains(t, plan.DeleteBooks, "F1")
	require.NotContains(t, plan.DeleteBooks, "F2")

	// The recheck never re-admits it.
	require.Equal(t, "", plan.Recheck(0, snap), "a skipped group is not rechecked")
}

// The apply skips a ManualCheckFailed group and writes the rest.
func TestITunesRegroupApply_SkipsCheckFailedGroup(t *testing.T) {
	s := regroupStore(t)
	f1, f2 := seedBook(t, s, "Frag F1"), seedBook(t, s, "Frag F2")
	g1, g2 := seedBook(t, s, "Frag G1"), seedBook(t, s, "Frag G2")
	seedFilePID(t, s, f1, "p1")
	seedFilePID(t, s, f2, "p2")
	seedFilePID(t, s, g1, "q1")
	seedFilePID(t, s, g2, "q2")

	p := &Plugin{}
	rep := &fakeReporter{}
	snap, err := p.buildRegroupSnapshot(context.Background(), rgLinkFailStore{s, f1}, rgRoot, rep)
	require.NoError(t, err, "one book's failed check must not fail the snapshot")
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{
		{Title: "Album F", PIDs: []string{"p1", "p2"}},
		{Title: "Album G", PIDs: []string{"q1", "q2"}},
	}, snap)
	require.True(t, plan.Groups[0].ManualCheckFailed)
	require.NoError(t, p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep))

	for _, id := range []string{f1, f2} {
		files, err := s.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1, "book %s of the skipped group must keep its file", id)
	}
	moved := 0
	for _, id := range []string{g1, g2} {
		files, err := s.GetBookFiles(id)
		require.NoError(t, err)
		moved += len(files)
	}
	require.Equal(t, 2, moved, "group G's two files must end on one book")
	tg := plan.Groups[1].Target
	files, err := s.GetBookFiles(tg)
	require.NoError(t, err)
	require.Len(t, files, 2, "group G consolidated onto %s", tg)
}

// rgLinkFailStore fails GetBookAuthors for one book.
type rgLinkFailStore struct {
	*database.PebbleStore
	failID string
}

func (s rgLinkFailStore) GetBookAuthors(bookID string) ([]database.BookAuthor, error) {
	if bookID == s.failID {
		return nil, errors.New("simulated book_authors read failure")
	}
	return s.PebbleStore.GetBookAuthors(bookID)
}

// --- itunes.regroup: recheck read failure ---

// rgTagFailStore fails GetBookTagsDetailed for one book once armed.
type rgTagFailStore struct {
	*database.PebbleStore
	failID string
	armed  bool
}

func (s *rgTagFailStore) GetBookTagsDetailed(bookID string) ([]database.BookTag, error) {
	if s.armed && bookID == s.failID {
		return nil, errors.New("simulated tag read failure")
	}
	return s.PebbleStore.GetBookTagsDetailed(bookID)
}

// A failed owner-manual read in the apply-time recheck skips that group
// (nothing written, not even the title) and the apply goes on to the next
// group; the run ends with an error naming the failure.
func TestITunesRegroupApply_RecheckReadFailureSkipsGroup(t *testing.T) {
	base := regroupStore(t)
	s := &rgTagFailStore{PebbleStore: base}
	f1, f2 := seedBook(t, base, "Frag F1"), seedBook(t, base, "Frag F2")
	g1, g2 := seedBook(t, base, "Frag G1"), seedBook(t, base, "Frag G2")
	seedFilePID(t, base, f1, "p1")
	seedFilePID(t, base, f2, "p2")
	seedFilePID(t, base, g1, "q1")
	seedFilePID(t, base, g2, "q2")

	p := &Plugin{}
	rep := &fakeReporter{}
	snap, err := p.buildRegroupSnapshot(context.Background(), base, rgRoot, rep)
	require.NoError(t, err)
	plan := itunesservice.PlanRegroup([]itunesservice.HealGroup{
		{Title: "Album F", PIDs: []string{"p1", "p2"}},
		{Title: "Album G", PIDs: []string{"q1", "q2"}},
	}, snap)
	require.Equal(t, 2, plan.Consolidated)

	s.failID, s.armed = plan.Groups[0].Target, true
	err = p.applyRegroupPlan(context.Background(), s, plan, rgRoot, rep)
	require.Error(t, err, "a recheck read failure is reported at the end of the run")

	var skipped bool
	for _, l := range rep.logs {
		if strings.Contains(l, `skip group "Album F": apply-time recheck could not read its books`) {
			skipped = true
		}
	}
	require.True(t, skipped, "no recheck-failure skip logged: %v", rep.logs)
	for _, id := range []string{f1, f2} {
		files, err := base.GetBookFiles(id)
		require.NoError(t, err)
		require.Len(t, files, 1, "book %s of the failed group must keep its file", id)
	}
	ft, err := base.GetBookByID(plan.Groups[0].Target)
	require.NoError(t, err)
	require.NotEqual(t, "Album F", ft.Title, "the failed group's target must not be retitled")
	files, err := base.GetBookFiles(plan.Groups[1].Target)
	require.NoError(t, err)
	require.Len(t, files, 2, "group G must still be applied")
}

// --- itunes.regroup: end-of-run status ---

// rgStatusXML is two two-track albums: "Album F" (PIDs F1/F2) and "Album G"
// (PIDs G1/G2).
func rgStatusXML() string {
	track := func(id int, pid, album string) string {
		return fmt.Sprintf(`		<key>%[1]d</key>
		<dict>
			<key>Track ID</key><integer>%[1]d</integer>
			<key>Persistent ID</key><string>%[2]s</string>
			<key>Name</key><string>%[3]s Part %[1]d</string>
			<key>Album</key><string>%[3]s</string>
			<key>Artist</key><string>Status Author</string>
			<key>Kind</key><string>Audiobook</string>
			<key>Location</key><string>file://localhost/missing/%[2]s.m4b</string>
		</dict>
`, id, pid, album)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple Computer//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Major Version</key><integer>1</integer>
	<key>Minor Version</key><integer>1</integer>
	<key>Tracks</key>
	<dict>
` + track(1, rgPIDF1, "Album F") + track(2, rgPIDF2, "Album F") +
		track(3, rgPIDG1, "Album G") + track(4, rgPIDG2, "Album G") + `	</dict>
	<key>Playlists</key><array/>
</dict>
</plist>
`
}

const (
	rgPIDF1 = "F1F1F1F1F1F1F1F1"
	rgPIDF2 = "F2F2F2F2F2F2F2F2"
	rgPIDG1 = "A1A1A1A1A1A1A1A1"
	rgPIDG2 = "A2A2A2A2A2A2A2A2"
)

// The run's end status keys on the groups skipped for a failed owner-manual
// check, not on the snapshot's check-failed books, and the dry run and the
// apply agree. A check-failed book in no heal group (the snapshot checks
// every live book) withholds nothing: no error, in either mode. One in a
// group skips that group: an error, in either mode. The other group is
// planned (and applied) either way.
func TestITunesRegroup_CheckFailedStatus(t *testing.T) {
	prevRoot := config.AppConfig.RootDir
	config.AppConfig.RootDir = rgRoot
	t.Cleanup(func() { config.AppConfig.RootDir = prevRoot })
	xmlPath := filepath.Join(t.TempDir(), "lib.xml")
	require.NoError(t, os.WriteFile(xmlPath, []byte(rgStatusXML()), 0o600))

	for _, inGroup := range []bool{false, true} {
		for _, dryRun := range []bool{true, false} {
			name := fmt.Sprintf("in-group=%v/dry-run=%v", inGroup, dryRun)
			t.Run(name, func(t *testing.T) {
				s := regroupStore(t)
				f1, f2 := seedBook(t, s, "Frag F1"), seedBook(t, s, "Frag F2")
				g1, g2 := seedBook(t, s, "Frag G1"), seedBook(t, s, "Frag G2")
				loner := seedBook(t, s, "Loner")
				seedFilePID(t, s, f1, rgPIDF1)
				seedFilePID(t, s, f2, rgPIDF2)
				seedFilePID(t, s, g1, rgPIDG1)
				seedFilePID(t, s, g2, rgPIDG2)
				seedFilePID(t, s, loner, "0000000000000001") // in no album of the XML
				failID := loner
				if inGroup {
					failID = f1
				}

				raw, err := json.Marshal(map[string]any{"xmlPath": xmlPath, "dry_run": dryRun})
				require.NoError(t, err)
				p := New(fakeDeps{store: rgLinkFailStore{s, failID}})
				rep := &fakeReporter{}
				runErr := p.runITunesRegroup(context.Background(), raw, rep)

				wantSkipped := 0
				if inGroup {
					wantSkipped = 1
				}
				warn := fmt.Sprintf("owner-manual check could not be done for 1 book(s); %d group(s) holding them were skipped", wantSkipped)
				var warned bool
				for _, l := range rep.logs {
					if strings.Contains(l, warn) {
						warned = true
					}
				}
				require.True(t, warned, "the check failure must be logged with its book count (%q): %v", warn, rep.logs)

				if inGroup {
					require.Error(t, runErr, "a group withheld for a failed check ends the run with an error")
					require.Contains(t, runErr.Error(), "1 group(s)")
				} else {
					require.NoError(t, runErr, "a check-failed book in no group withholds nothing")
				}

				// Group G is planned and, on an apply, consolidated, whatever
				// happened to F.
				onG := 0
				for _, id := range []string{g1, g2} {
					files, err := s.GetBookFiles(id)
					require.NoError(t, err)
					if len(files) > 0 {
						onG++
					}
				}
				if dryRun {
					require.Equal(t, 2, onG, "a dry run moves nothing")
				} else {
					require.Equal(t, 1, onG, "group G must be consolidated onto one book")
				}
				if inGroup || dryRun {
					for _, id := range []string{f1, f2} {
						files, err := s.GetBookFiles(id)
						require.NoError(t, err)
						require.Len(t, files, 1, "book %s keeps its file", id)
					}
				}
			})
		}
	}
}

// --- itunes.regroup: snapshot memory ---

// The snapshot's owner-manual files reader holds an int32 index per row into
// the bulk read, never a database.BookFile per row (792 B each: larger than
// the BookFileCore it was copied from).
func TestRegroupSnapshotFiles_HoldsNoBookFilePerRow(t *testing.T) {
	typ := reflect.TypeOf(regroupSnapshotFiles{})
	bookFile := reflect.TypeOf(database.BookFile{})
	var holds func(reflect.Type, int) bool
	holds = func(t reflect.Type, depth int) bool {
		if t == bookFile {
			return true
		}
		if depth > 4 {
			return false
		}
		switch t.Kind() {
		case reflect.Slice, reflect.Array, reflect.Pointer:
			return holds(t.Elem(), depth+1)
		case reflect.Map:
			return holds(t.Key(), depth+1) || holds(t.Elem(), depth+1)
		case reflect.Struct:
			for i := 0; i < t.NumField(); i++ {
				if holds(t.Field(i).Type, depth+1) {
					return true
				}
			}
		}
		return false
	}
	require.False(t, holds(typ, 0), "regroupSnapshotFiles holds a database.BookFile")
	f, ok := typ.FieldByName("byBook")
	require.True(t, ok)
	require.Equal(t, reflect.TypeOf(int32(0)), f.Type.Elem().Elem(), "the per-row index must be int32")

	// And it serves the same rows the check reads, live books only.
	dw := "Doctor Who: Shada"
	rows := []database.BookFileCore{
		{ID: "a1", BookID: "A", FilePath: "/x/a1.m4b"},
		{ID: "d1", BookID: "D", FilePath: "/x/d1.m4b"},
		{ID: "a2", BookID: "A", FilePath: "/x/a2.m4b", TranscribedTitle: &dw},
	}
	r, err := newRegroupSnapshotFiles(rows, 2, func(id string) bool { return id == "A" })
	require.NoError(t, err)
	got, err := r.GetBookFiles("A")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "a1", got[0].ID)
	require.Equal(t, "/x/a2.m4b", got[1].FilePath)
	require.Equal(t, &dw, got[1].TranscribedTitle)
	none, err := r.GetBookFiles("D")
	require.NoError(t, err)
	require.Empty(t, none, "a book the snapshot left out has no rows")
}

// rgSyntheticFiles builds rows book_file rows over rows/perBook books.
func rgSyntheticFiles(rows, perBook int) []database.BookFileCore {
	out := make([]database.BookFileCore, rows)
	for i := range out {
		out[i] = database.BookFileCore{ID: fmt.Sprintf("f%07d", i), BookID: fmt.Sprintf("b%07d", i/perBook),
			FilePath: fmt.Sprintf("/media/books/itunes/Media/Author/Album %d/%02d.m4b", i/perBook, i%perBook)}
	}
	return out
}

// BenchmarkRegroupSnapshotFiles compares what the snapshot's owner-manual
// files reader allocates over 300k rows (B/op, cumulative over the build, not
// resident memory): the former map of one BookFile per row against the int32
// index, at five files per book and at one (every book a single file, the
// most map entries per row). Run with -benchmem.
func BenchmarkRegroupSnapshotFiles(b *testing.B) {
	const rows = 300_000
	all := func(string) bool { return true }
	for _, perBook := range []int{5, 1} {
		files := rgSyntheticFiles(rows, perBook)
		nBooks := rows / perBook
		b.Run(fmt.Sprintf("bookfile-per-row/per-book=%d", perBook), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				m := make(map[string][]database.BookFile, nBooks)
				for i := range files {
					f := &files[i]
					m[f.BookID] = append(m[f.BookID], database.BookFile{ID: f.ID, BookID: f.BookID, FilePath: f.FilePath,
						TranscribedTitle: f.TranscribedTitle, TranscribedAuthor: f.TranscribedAuthor})
				}
				_ = m
			}
		})
		b.Run(fmt.Sprintf("int32-index/per-book=%d", perBook), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				r, err := newRegroupSnapshotFiles(files, nBooks, all)
				if err != nil {
					b.Fatal(err)
				}
				_ = r
			}
		})
	}
}

// --- itunes.regroup: author rows from one bulk read ---

// rgAuthorCountStore counts GetAuthorByID calls.
type rgAuthorCountStore struct {
	database.BookAuthorReader
	mu    sync.Mutex
	calls int
}

func (s *rgAuthorCountStore) GetAuthorByID(id int) (*database.Author, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	return s.BookAuthorReader.GetAuthorByID(id)
}

// A credited author in the bulk map is served without a point read; one the
// bulk read did not return (a tombstoned id) falls back to the store.
func TestRegroupAuthorReader_MapThenStore(t *testing.T) {
	base := &regroupFakeReader{authors: map[int]*database.Author{
		9: {ID: 9, Name: "Big Finish Productions"}, 4: {ID: 4, Name: "Merged Away"}}}
	st := &rgAuthorCountStore{BookAuthorReader: base}
	r := regroupAuthorReader{store: st, byID: map[int]*database.Author{9: base.authors[9]}}
	a, err := r.GetAuthorByID(9)
	require.NoError(t, err)
	require.Equal(t, "Big Finish Productions", a.Name)
	require.Equal(t, 0, st.calls, "a mapped author needs no point read")
	a, err = r.GetAuthorByID(4)
	require.NoError(t, err)
	require.Equal(t, "Merged Away", a.Name)
	require.Equal(t, 1, st.calls, "an unmapped id falls back to the store")
}

// --- itunes.clone-into-library: read failure ---

// icTagFailDeps serves a tag reader whose reads all fail.
type icTagFailDeps struct{ icTestDeps }

type icTagFailReader struct{ BookTagReader }

func (icTagFailReader) GetBookTagsDetailed(string) ([]database.BookTag, error) {
	return nil, errors.New("simulated tag read failure")
}

func (d icTagFailDeps) BookTagReader() BookTagReader {
	return icTagFailReader{d.icTestDeps.BookTagReader()}
}

// A group whose source's owner-manual check cannot be done is skipped as
// owner_manual_check_failed, not cloned and not counted as owner-manual.
func TestITunesClone_OwnerManualReadFailure(t *testing.T) {
	f := newICFixture(t)
	f.book(t, "K", "vg-k", "Book K", []string{f.itunes("Book K.m4b")})
	p := &Plugin{deps: icTagFailDeps{f.deps}, reflinkFile: f.reflink}
	rep, err := p.itunesCloneIntoLibrary(context.Background(), icParams{GroupIDs: []string{"vg-k"}}, f.root, &opIDReporter{id: "op-ic-test"})
	require.NoError(t, err)
	g := icGroup(t, rep, "vg-k")
	require.Equal(t, icDecisionSkip, g.Decision, "%+v", g)
	require.Equal(t, applygate.ReasonOwnerManualCheckFailed, g.Reason, "%+v", g)
	require.Contains(t, g.Error, "owner-manual check")
}

// --- author-strip-merge relink: read failure ---

// A title-as-author book whose owner-manual check cannot be done is failed:
// not relinked, not counted as owner-manual.
func TestAuthorStripMerge_RelinkOwnerManualReadFailure(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.tagErr = map[string]error{"bk-t": errors.New("simulated tag read failure")}
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`)
	if len(calls.setAuthors["bk-t"]) != 0 {
		t.Errorf("a book whose owner-manual check failed was rewritten: %v", calls.setAuthors["bk-t"])
	}
	wantSummary(t, summary, "relink-skipped-owner-manual=0 ", "relink-failed=1 ")
}

// --- author-path-link: apply-time re-check ---

// The apply re-runs the owner-manual check on fresh reads: a franchise: tag
// that appears after the classify pass holds the book, and a read that fails
// only at apply time fails it. Nothing is written for either.
func TestAuthorPathLink_ApplyRechecksOwnerManual(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, "Charles Dickens")
	f.filler(1, 4)
	path := func(id string) string {
		return "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/" + id + "/x.m4b"
	}
	for _, id := range []string{"clean", "late-tag", "late-fail"} {
		f.book(id, path(id), nil)
	}
	s := f.store(t)
	var mu sync.Mutex
	reads := map[string]int{}
	s.GetBookTagsDetailedFunc = func(bookID string) ([]database.BookTag, error) {
		mu.Lock()
		reads[bookID]++
		n := reads[bookID]
		mu.Unlock()
		if n == 1 {
			return nil, nil // the classify pass sees nothing
		}
		switch bookID {
		case "late-tag":
			return []database.BookTag{{BookID: bookID, Tag: franchise.Tags(franchise.BigFinish, "")[0]}}, nil
		case "late-fail":
			return nil, errors.New("simulated tag read failure")
		}
		return nil, nil
	}
	var wrote []string
	s.SetBookAuthorsFunc = func(bookID string, _ []database.BookAuthor) error {
		mu.Lock()
		wrote = append(wrote, bookID)
		mu.Unlock()
		return nil
	}

	res := runPathLink(t, New(&fakeDeps{store: s}), `{"dry_run":true}`)
	require.Equal(t, authorPathLinkWouldLink, outcomeOf(t, res, "clean").Outcome, "control: a clean book links")
	_, listed := findChange(res, "late-tag")
	require.False(t, listed, "late-tag is owner_manual_only (counter-only), but it was listed")
	require.Equal(t, 1, res.Outcomes[authorPathLinkOwnerManual], "outcomes=%v", res.Outcomes)
	fail := outcomeOf(t, res, "late-fail")
	require.Equal(t, authorPathLinkFailed, fail.Outcome)
	require.Contains(t, fail.Error, "owner-manual check")
	require.Empty(t, wrote, "a dry run writes nothing")
}
