// file: internal/plugins/maintenance/credits_backfill_test.go
// version: 1.1.0
// guid: 6950317d-b65c-41e5-86a8-9cc277a95054
// last-edited: 2026-10-27

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
)

// Fixtures: Batman characters as people, Batman storylines as titles, paths
// under /lib/Gotham. Nothing here comes from a real library.

const cbTestOpID = "op-credits-backfill-test"

// cbReporter is fakeReporter plus an op id (so the apply can journal), a
// result sink and an optional cancel after n completed items.
type cbReporter struct {
	fakeReporter
	id          string
	result      any
	mu          sync.Mutex
	checkpoints []any
	progress    atomic.Int64
	cancelAfter int64
	cancel      context.CancelFunc
}

func (r *cbReporter) OpID() string          { return r.id }
func (r *cbReporter) SetResult(v any) error { r.result = v; return nil }
func (r *cbReporter) Checkpoint(v any) error {
	r.mu.Lock()
	r.checkpoints = append(r.checkpoints, v)
	r.mu.Unlock()
	return nil
}
func (r *cbReporter) UpdateProgress(cur, total int, _ string) error {
	if r.cancel != nil && r.progress.Add(1) == r.cancelAfter {
		r.cancel()
	}
	return nil
}

type cbFixture struct {
	t *testing.T
	s *database.PebbleStore
	// narrators are the narrator records the test made on purpose. CreateBook
	// runs the store's narrator sync, which resolves-or-creates every name in
	// the column (splits included); book() deletes whatever else it minted so
	// a fixture starts with only the people the test named.
	narrators map[int]bool
}

func newCBFixture(t *testing.T) *cbFixture {
	t.Helper()
	return &cbFixture{t: t, s: newRepairPebble(t), narrators: map[int]bool{}}
}

func (f *cbFixture) author(name string) int {
	f.t.Helper()
	a, err := f.s.CreateAuthor(name)
	if err != nil || a == nil {
		f.t.Fatalf("CreateAuthor(%q): %v", name, err)
	}
	return a.ID
}

func (f *cbFixture) narrator(name string) int {
	f.t.Helper()
	n, err := f.s.CreateNarrator(name)
	if err != nil || n == nil {
		f.t.Fatalf("CreateNarrator(%q): %v", name, err)
	}
	f.narrators[n.ID] = true
	return n.ID
}

// book creates a book, then puts its credit lists in exactly the given raw
// shape (positions untouched, bypassing the store's normalisation and the
// narrator sync), as older builds left them in production. nil lists leave
// the key empty.
func (f *cbFixture) book(id, title string, mutate func(b *database.Book), join []database.BookAuthor, junction []database.BookNarrator) {
	f.t.Helper()
	b := &database.Book{ID: id, Title: title, FilePath: "/lib/Gotham/" + title + "/" + title + ".m4b"}
	if mutate != nil {
		mutate(b)
	}
	if _, err := f.s.CreateBook(b); err != nil {
		f.t.Fatalf("CreateBook(%s): %v", id, err)
	}
	f.raw("book_authors:"+id, join)
	f.raw("book_narrators:"+id, junction)
	ns, err := f.s.ListNarrators()
	if err != nil {
		f.t.Fatal(err)
	}
	for _, n := range ns {
		if !f.narrators[n.ID] {
			if err := f.s.DeleteNarrator(n.ID); err != nil {
				f.t.Fatalf("DeleteNarrator(%d): %v", n.ID, err)
			}
		}
	}
}

func (f *cbFixture) raw(key string, v any) {
	f.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		f.t.Fatal(err)
	}
	if string(data) == "null" {
		data = []byte("[]")
	}
	if err := f.s.SetRaw(key, data); err != nil {
		f.t.Fatalf("SetRaw(%s): %v", key, err)
	}
}

func (f *cbFixture) state(id string) undo.CreditsBackfillState {
	f.t.Helper()
	b, err := f.s.GetBookByID(id)
	if err != nil || b == nil {
		f.t.Fatalf("GetBookByID(%s): %v", id, err)
	}
	ja, err := f.s.GetBookAuthors(id)
	if err != nil {
		f.t.Fatal(err)
	}
	jn, err := f.s.GetBookNarrators(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return undo.CreditsBackfillStateOf(b, ja, jn)
}

func (f *cbFixture) run(params creditsBackfillParams, workers int) (*creditsBackfillResult, error) {
	f.t.Helper()
	return f.runWith(context.Background(), &cbReporter{id: cbTestOpID}, params, workers)
}

func (f *cbFixture) runWith(ctx context.Context, rep *cbReporter, params creditsBackfillParams, workers int) (*creditsBackfillResult, error) {
	env := creditsBackfillEnv{store: f.s, opID: rep.id, workers: workers}
	if params.Apply {
		env.mod = f.s
	}
	return runCreditsBackfillScan(ctx, env, params, rep)
}

func ba(bookID string, rows ...[2]int) []database.BookAuthor {
	out := make([]database.BookAuthor, len(rows))
	for i, r := range rows {
		out[i] = database.BookAuthor{BookID: bookID, AuthorID: r[0], Role: "author", Position: r[1]}
	}
	return out
}

func bn(bookID string, rows ...[2]int) []database.BookNarrator {
	out := make([]database.BookNarrator, len(rows))
	for i, r := range rows {
		role := "narrator"
		if i > 0 {
			role = "co-narrator"
		}
		out[i] = database.BookNarrator{BookID: bookID, NarratorID: r[0], Role: role, Position: r[1]}
	}
	return out
}

func strp(v string) *string { return &v }

func cbClassIDs(res *creditsBackfillResult, key string) []string {
	for _, c := range res.Classes {
		if c.Key == key {
			return c.BookIDs
		}
	}
	return nil
}

func cbClassOf(res *creditsBackfillResult, key string) *cbClass {
	for i := range res.Classes {
		if res.Classes[i].Key == key {
			return &res.Classes[i]
		}
	}
	return nil
}

func cbAuthorIDs(rows []database.BookAuthor) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.AuthorID)
	}
	return out
}

func cbNarratorIDs(rows []database.BookNarrator) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.NarratorID)
	}
	return out
}

func assertPositions01(t *testing.T, id string, n int, positions []int) {
	t.Helper()
	if len(positions) != n {
		t.Fatalf("%s: %d rows, want %d", id, len(positions), n)
	}
	for i, p := range positions {
		if p != i {
			t.Fatalf("%s: positions %v, want 0..%d", id, positions, n-1)
		}
	}
}

func TestCreditsBackfill_FillsAuthorFromAuthorID(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("cbf-01", "Year One", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := f.state("cbf-01")
	if got := cbAuthorIDs(st.Authors); !slices.Equal(got, []int{bruce}) {
		t.Fatalf("join = %v, want [%d]", got, bruce)
	}
	if st.Authors[0].Position != 0 || st.Authors[0].Role != "author" {
		t.Fatalf("row = %+v, want author@0", st.Authors[0])
	}
	if got := cbClassIDs(res, cbAuthorFillFromAuthorID); !slices.Equal(got, []string{"cbf-01"}) {
		t.Fatalf("%s = %v", cbAuthorFillFromAuthorID, got)
	}
	if res.BooksToWrite != 1 || !res.Complete {
		t.Fatalf("result to_write=%d complete=%t err=%q", res.BooksToWrite, res.Complete, res.Error)
	}
}

func TestCreditsBackfill_FillsAuthorFromSnapshotByIDOnly(t *testing.T) {
	f := newCBFixture(t)
	selina := f.author("Selina Kyle")
	f.book("cbf-02", "Dark Victory", func(b *database.Book) { b.Author = &database.Author{ID: selina, Name: "Selina Kyle"} }, nil, nil)
	// A snapshot whose id resolves to nothing is never looked up by name.
	f.author("Harvey Dent")
	f.book("cbf-03", "The Long Halloween", func(b *database.Book) { b.Author = &database.Author{ID: 9999, Name: "Harvey Dent"} }, nil, nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := f.state("cbf-02")
	if !slices.Equal(cbAuthorIDs(st.Authors), []int{selina}) || st.AuthorID == nil || *st.AuthorID != selina {
		t.Fatalf("cbf-02 state = %+v, want join [Selina] and AuthorID Selina", st)
	}
	if got := cbClassIDs(res, cbAuthorFillFromSnapshot); !slices.Equal(got, []string{"cbf-02"}) {
		t.Fatalf("%s = %v", cbAuthorFillFromSnapshot, got)
	}
	if got := cbClassIDs(res, cbAuthorSnapshotUnresolved); !slices.Equal(got, []string{"cbf-03"}) {
		t.Fatalf("%s = %v", cbAuthorSnapshotUnresolved, got)
	}
	if st3 := f.state("cbf-03"); len(st3.Authors) != 0 || st3.AuthorID != nil {
		t.Fatalf("cbf-03 was written: %+v", st3)
	}
}

func TestCreditsBackfill_NarratorFromColumnThroughCleanNarratorCredit(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	oswald := f.narrator("Oswald Cobblepot")
	edward := f.narrator("Edward Nygma")
	// Two existing people.
	f.book("cbn-01", "Hush", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Oswald Cobblepot, Edward Nygma")
	}, ba("cbn-01", [2]int{bruce, 0}), nil)
	// One new person: the credit is one name, so the record may be created.
	f.book("cbn-02", "Knightfall", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Pamela Isley")
	}, ba("cbn-02", [2]int{bruce, 0}), nil)
	// A translator credit is dropped by the cleaner.
	f.book("cbn-03", "Gothic", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Edward Nygma, Lucius Fox - translator")
	}, ba("cbn-03", [2]int{bruce, 0}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := f.state("cbn-01")
	if got := cbNarratorIDs(st.Narrators); !slices.Equal(got, []int{oswald, edward}) {
		t.Fatalf("cbn-01 junction = %v, want [%d %d]", got, oswald, edward)
	}
	if st.Narrators[0].Role != "narrator" || st.Narrators[1].Role != "co-narrator" {
		t.Fatalf("cbn-01 roles = %+v", st.Narrators)
	}
	st2 := f.state("cbn-02")
	if len(st2.Narrators) != 1 {
		t.Fatalf("cbn-02 junction = %+v", st2.Narrators)
	}
	n, _ := f.s.GetNarratorByID(st2.Narrators[0].NarratorID)
	if n == nil || n.Name != "Pamela Isley" {
		t.Fatalf("cbn-02 narrator = %+v, want a new Pamela Isley record", n)
	}
	if got := cbNarratorIDs(f.state("cbn-03").Narrators); !slices.Equal(got, []int{edward}) {
		t.Fatalf("cbn-03 junction = %v, want [Edward Nygma] (translator dropped)", got)
	}
	if got := cbClassIDs(res, cbNarratorFillFromColumn); !slices.Equal(got, []string{"cbn-01", "cbn-02", "cbn-03"}) {
		t.Fatalf("%s = %v", cbNarratorFillFromColumn, got)
	}
	if got := cbClassIDs(res, cbNarratorCreatesRecord); !slices.Equal(got, []string{"cbn-02"}) {
		t.Fatalf("%s = %v", cbNarratorCreatesRecord, got)
	}
	if res.NarratorsCreated != 1 || !slices.Equal(res.NarratorNames, []string{"Pamela Isley"}) {
		t.Fatalf("created=%d names=%v", res.NarratorsCreated, res.NarratorNames)
	}
}

func TestCreditsBackfill_SelfReadNarrators(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	edward := f.narrator("Edward Nygma")
	// A self-reading co-narrator: the cleaner drops the author, as the sync does.
	f.book("cbs-01", "Zero Year", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Bruce Wayne, Edward Nygma")
	}, ba("cbs-01", [2]int{bruce, 0}), nil)
	// The column names only the author: a self-read or a mis-tag.
	f.book("cbs-02", "Haunted Knight", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Bruce Wayne")
	}, ba("cbs-02", [2]int{bruce, 0}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := cbNarratorIDs(f.state("cbs-01").Narrators); !slices.Equal(got, []int{edward}) {
		t.Fatalf("cbs-01 junction = %v, want [Edward Nygma]", got)
	}
	if got := cbClassIDs(res, cbNarratorCleanDroppedAuthor); !slices.Equal(got, []string{"cbs-01"}) {
		t.Fatalf("%s = %v", cbNarratorCleanDroppedAuthor, got)
	}
	if st := f.state("cbs-02"); len(st.Narrators) != 0 {
		t.Fatalf("cbs-02 written without the opt-in: %+v", st.Narrators)
	}
	if c := cbClassOf(res, cbNarratorSelfRead); c == nil || c.Kind != cbKindReview || !slices.Equal(c.BookIDs, []string{"cbs-02"}) {
		t.Fatalf("%s = %+v", cbNarratorSelfRead, c)
	}

	res, err = f.run(creditsBackfillParams{Apply: true, Allow: []string{cbAllowSelfRead}}, 2)
	if err != nil {
		t.Fatalf("run with allow: %v", err)
	}
	st := f.state("cbs-02")
	if len(st.Narrators) != 1 {
		t.Fatalf("cbs-02 junction = %+v", st.Narrators)
	}
	n, _ := f.s.GetNarratorByID(st.Narrators[0].NarratorID)
	if n == nil || n.Name != "Bruce Wayne" {
		t.Fatalf("cbs-02 narrator = %+v, want Bruce Wayne", n)
	}
	if c := cbClassOf(res, cbNarratorSelfRead); c == nil || c.Kind != cbKindWrite || !slices.Contains(c.BookIDs, "cbs-02") {
		t.Fatalf("%s with allow = %+v", cbNarratorSelfRead, c)
	}
}

func TestCreditsBackfill_RenumbersPositionsWithAuthorIDTieBreaker(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	oswald := f.narrator("Oswald Cobblepot")
	edward := f.narrator("Edward Nygma")
	// The "A @0, B @0" shape: every row at 0, AuthorID NOT first in stored order.
	f.book("cbp-01", "The Killing Joke", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Oswald Cobblepot, Edward Nygma")
	}, ba("cbp-01", [2]int{selina, 0}, [2]int{bruce, 0}), bn("cbp-01", [2]int{oswald, 4}, [2]int{edward, 9}))

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	st := f.state("cbp-01")
	if got := cbAuthorIDs(st.Authors); !slices.Equal(got, []int{bruce, selina}) {
		t.Fatalf("join = %v, want AuthorID first [%d %d]", got, bruce, selina)
	}
	assertPositions01(t, "authors", 2, []int{st.Authors[0].Position, st.Authors[1].Position})
	if got := cbNarratorIDs(st.Narrators); !slices.Equal(got, []int{oswald, edward}) {
		t.Fatalf("junction = %v", got)
	}
	assertPositions01(t, "narrators", 2, []int{st.Narrators[0].Position, st.Narrators[1].Position})
	for _, k := range []string{cbAuthorPositionsRenumbered, cbNarratorPositionsRenumbered, cbFilingAuthorChanges} {
		if got := cbClassIDs(res, k); !slices.Equal(got, []string{"cbp-01"}) {
			t.Fatalf("%s = %v", k, got)
		}
	}
}

func TestCreditsBackfill_PrependsAuthorIDMissingFromJoin(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	dick := f.author("Dick Grayson")
	f.book("cba-01", "A Death in the Family", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cba-01", [2]int{dick, 0}), nil)
	// The join's first author-role row becomes AuthorID when it is unset.
	f.book("cba-02", "Court of Owls", nil, ba("cba-02", [2]int{dick, 0}, [2]int{bruce, 1}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := cbAuthorIDs(f.state("cba-01").Authors); !slices.Equal(got, []int{bruce, dick}) {
		t.Fatalf("cba-01 join = %v, want [%d %d]", got, bruce, dick)
	}
	if st := f.state("cba-02"); st.AuthorID == nil || *st.AuthorID != dick || !slices.Equal(cbAuthorIDs(st.Authors), []int{dick, bruce}) {
		t.Fatalf("cba-02 = %+v, want AuthorID %d and the join unchanged", st, dick)
	}
	if got := cbClassIDs(res, cbAuthorIDPrepended); !slices.Equal(got, []string{"cba-01"}) {
		t.Fatalf("%s = %v", cbAuthorIDPrepended, got)
	}
	if got := cbClassIDs(res, cbFilingAuthorChanges); !slices.Equal(got, []string{"cba-01"}) {
		t.Fatalf("%s = %v (cba-02 keeps its filing author)", cbFilingAuthorChanges, got)
	}
	if got := cbClassIDs(res, cbAuthorIDFromJoin); !slices.Equal(got, []string{"cba-02"}) {
		t.Fatalf("%s = %v", cbAuthorIDFromJoin, got)
	}
}

func TestCreditsBackfill_NeverCreatesAPersonFromASplit(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("cbx-01", "No Man's Land", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Jonathan Crane, Victor Fries")
	}, ba("cbx-01", [2]int{bruce, 0}), nil)
	authorsBefore, _ := f.s.GetAllAuthors()

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if ns, _ := f.s.ListNarrators(); len(ns) != 0 {
		t.Fatalf("narrators created from a split without the opt-in: %+v", ns)
	}
	if st := f.state("cbx-01"); len(st.Narrators) != 0 {
		t.Fatalf("cbx-01 written: %+v", st.Narrators)
	}
	if c := cbClassOf(res, cbNarratorNewFromSplit); c == nil || c.Kind != cbKindReview || !slices.Equal(c.BookIDs, []string{"cbx-01"}) {
		t.Fatalf("%s = %+v", cbNarratorNewFromSplit, c)
	}
	if authorsAfter, _ := f.s.GetAllAuthors(); len(authorsAfter) != len(authorsBefore) {
		t.Fatalf("authors created: %d -> %d", len(authorsBefore), len(authorsAfter))
	}

	if _, err := f.run(creditsBackfillParams{Apply: true, Allow: []string{cbAllowNewFromSplit}}, 2); err != nil {
		t.Fatalf("run with allow: %v", err)
	}
	if st := f.state("cbx-01"); len(st.Narrators) != 2 {
		t.Fatalf("cbx-01 with allow = %+v, want two narrators", st.Narrators)
	}
	if authorsAfter, _ := f.s.GetAllAuthors(); len(authorsAfter) != len(authorsBefore) {
		t.Fatalf("an author was created: %d -> %d", len(authorsBefore), len(authorsAfter))
	}
}

func TestCreditsBackfill_ReviewClassesAreListedNotWritten(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	combined := f.author("Bruce Wayne, Selina Kyle")
	oswald := f.narrator("Oswald Cobblepot")
	edward := f.narrator("Edward Nygma")
	f.book("cbr-dangling", "Arkham Asylum", func(b *database.Book) { b.AuthorID = intp(9999) }, nil, nil)
	f.book("cbr-combined", "The Dark Knight Returns", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cbr-combined", [2]int{combined, 0}, [2]int{bruce, 1}), nil)
	f.book("cbr-notprimary", "Night of the Owls", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cbr-notprimary", [2]int{selina, 0}, [2]int{bruce, 1}), nil)
	f.book("cbr-differs", "Death of the Family", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Oswald Cobblepot")
	}, ba("cbr-differs", [2]int{bruce, 0}), bn("cbr-differs", [2]int{edward, 0}))
	f.book("cbr-junk", "Batman: Earth One", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("https://gotham.example.invalid/narrator")
	}, ba("cbr-junk", [2]int{bruce, 0}), nil)
	f.book("cbr-nocolumn", "Batman: Black and White", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cbr-nocolumn", [2]int{bruce, 0}), bn("cbr-nocolumn", [2]int{oswald, 0}))
	f.book("cbr-dangling-narrator", "Batman: The Cult", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Oswald Cobblepot")
	}, ba("cbr-dangling-narrator", [2]int{bruce, 0}), bn("cbr-dangling-narrator", [2]int{oswald, 0}, [2]int{8888, 1}))

	ids := []string{"cbr-dangling", "cbr-combined", "cbr-notprimary", "cbr-differs", "cbr-junk", "cbr-nocolumn", "cbr-dangling-narrator"}
	before := map[string]undo.CreditsBackfillState{}
	for _, id := range ids {
		before[id] = f.state(id)
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 3)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, id := range ids {
		if !undo.SameCreditsBackfillState(f.state(id), before[id]) {
			t.Fatalf("%s was written:\nbefore %+v\nafter  %+v", id, before[id], f.state(id))
		}
	}
	want := map[string][]string{
		ccAuthorIDDangling:         {"cbr-dangling"},
		ccAuthorJoinCombined:       {"cbr-combined"},
		ccAuthorIDNotPrimary:       {"cbr-notprimary"},
		ccNarratorColumnDiffers:    {"cbr-differs"},
		ccNarratorColumnNotPeople:  {"cbr-junk"},
		ccNarratorJunctionNoColumn: {"cbr-nocolumn"},
		ccNarratorIDDangling:       {"cbr-dangling-narrator"},
	}
	for k, v := range want {
		c := cbClassOf(res, k)
		if c == nil || c.Kind != cbKindReview || !slices.Equal(c.BookIDs, v) {
			t.Fatalf("%s = %+v, want review %v", k, c, v)
		}
	}
	if rows, _ := f.s.GetOperationChanges(cbTestOpID); len(rows) != 0 {
		t.Fatalf("%d journal rows for books that were not written", len(rows))
	}
}

func TestCreditsBackfill_PlanReviewsTombstonedIDs(t *testing.T) {
	// A real store cannot cheaply build a tombstone; the census fake can,
	// and the plan reads only the frozen index.
	s := newCensusFakeStore()
	s.authors[1] = database.Author{ID: 1, Name: "Bruce Wayne"}
	s.tombstones[2] = 1
	idx, err := buildCreditCensusIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	book := &database.Book{ID: "cbt-01", Title: "Batman: Hush", AuthorID: intp(2)}
	census, err := classifyCreditBook(idx, book, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planCreditsBackfill(idx, func(string) (database.Narrator, bool, error) { return database.Narrator{}, false, nil },
		nil, book, nil, nil, census, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan.hasWrite() || !slices.Equal(plan.reviews, []string{ccAuthorIDTombstoned}) {
		t.Fatalf("plan = writes %v reviews %v, want review %s only", plan.writes, plan.reviews, ccAuthorIDTombstoned)
	}
}

func TestCreditsBackfill_ITunesHeldUnlessAllowed(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("cbi-01", "Hush", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.FilePath = "/lib/Gotham/books/itunes/Hush/Hush.m4b"
	}, nil, nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if st := f.state("cbi-01"); len(st.Authors) != 0 {
		t.Fatalf("iTunes book written without the opt-in: %+v", st.Authors)
	}
	if got := cbClassIDs(res, cbHeldITunes); !slices.Equal(got, []string{"cbi-01"}) {
		t.Fatalf("%s = %v", cbHeldITunes, got)
	}

	res, err = f.run(creditsBackfillParams{Apply: true, Allow: []string{cbAllowITunes}}, 2)
	if err != nil {
		t.Fatalf("run with allow: %v", err)
	}
	if st := f.state("cbi-01"); !slices.Equal(cbAuthorIDs(st.Authors), []int{bruce}) {
		t.Fatalf("iTunes book with allow = %+v", st.Authors)
	}
	if got := cbClassIDs(res, cbITunesDatabaseOnly); !slices.Equal(got, []string{"cbi-01"}) {
		t.Fatalf("%s = %v", cbITunesDatabaseOnly, got)
	}
}

func TestCreditsBackfill_OwnerManualAndUserLockHeld(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.book("cbh-01", "Batman: Gotham Knights", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Publisher = strp("Big Finish Productions")
	}, nil, nil)
	// User-locked author whose filing author would change: author side held.
	f.book("cbh-02", "Batman: Year Two", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cbh-02", [2]int{selina, 0}), nil)
	if err := f.s.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: "cbh-02", Field: database.FieldKeyAuthorName, OverrideLocked: true}); err != nil {
		t.Fatal(err)
	}
	b1, b2 := f.state("cbh-01"), f.state("cbh-02")

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !undo.SameCreditsBackfillState(f.state("cbh-01"), b1) || !undo.SameCreditsBackfillState(f.state("cbh-02"), b2) {
		t.Fatal("a held book was written")
	}
	if got := cbClassIDs(res, cbHeldOwnerManual); !slices.Equal(got, []string{"cbh-01"}) {
		t.Fatalf("%s = %v", cbHeldOwnerManual, got)
	}
	if got := cbClassIDs(res, cbHeldUserLocked); !slices.Equal(got, []string{"cbh-02"}) {
		t.Fatalf("%s = %v", cbHeldUserLocked, got)
	}
}

func TestCreditsBackfill_DryRunWritesNothing(t *testing.T) {
	f := newCBFixture(t)
	ids := seedCBCensusFixture(f)
	before := map[string]undo.CreditsBackfillState{}
	for _, id := range ids {
		before[id] = f.state(id)
	}
	narrBefore, _ := f.s.ListNarrators()

	res, err := f.run(creditsBackfillParams{}, 3)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	for _, id := range ids {
		if !undo.SameCreditsBackfillState(f.state(id), before[id]) {
			t.Fatalf("dry run wrote %s", id)
		}
	}
	if rows, _ := f.s.GetOperationChanges(cbTestOpID); len(rows) != 0 {
		t.Fatalf("dry run journaled %d rows", len(rows))
	}
	if narrAfter, _ := f.s.ListNarrators(); len(narrAfter) != len(narrBefore) {
		t.Fatalf("dry run created narrators: %d -> %d", len(narrBefore), len(narrAfter))
	}
	if res.Mode != "dry_run" || res.BooksToWrite == 0 {
		t.Fatalf("mode=%s to_write=%d", res.Mode, res.BooksToWrite)
	}
	c := cbClassOf(res, cbNarratorFillFromColumn)
	if c == nil || len(c.Samples) == 0 {
		t.Fatalf("no before/after samples for %s", cbNarratorFillFromColumn)
	}
	s0 := c.Samples[0]
	if len(s0.Before.Narrators) != 0 || len(s0.After.Narrators) == 0 {
		t.Fatalf("sample = %+v, want an empty before and a filled after", s0)
	}
	if res.NarratorsToCreate == 0 || res.NarratorsCreated != 0 {
		t.Fatalf("dry run narrators_to_create=%d created=%d", res.NarratorsToCreate, res.NarratorsCreated)
	}
}

// seedCBCensusFixture builds one book per census class the backfill fixes and
// returns their ids.
func seedCBCensusFixture(f *cbFixture) []string {
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	alfred := f.author("Alfred Pennyworth")
	oswald := f.narrator("Oswald Cobblepot")
	edward := f.narrator("Edward Nygma")
	f.book("cbc-join-empty", "Year One", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	f.book("cbc-no-author-id", "Court of Owls", nil, ba("cbc-no-author-id", [2]int{selina, 0}), nil)
	f.book("cbc-not-in-join", "Hush", func(b *database.Book) { b.AuthorID = intp(bruce) }, ba("cbc-not-in-join", [2]int{alfred, 0}), nil)
	f.book("cbc-all-zero", "The Killing Joke", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cbc-all-zero", [2]int{selina, 0}, [2]int{bruce, 0}), nil)
	f.book("cbc-gaps", "Knightfall", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("cbc-gaps", [2]int{bruce, 2}, [2]int{selina, 7}), nil)
	f.book("cbc-snapshot", "Dark Victory", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Author = &database.Author{ID: bruce, Name: "B. Wayne"}
	}, ba("cbc-snapshot", [2]int{bruce, 0}), nil)
	f.book("cbc-narrator-column", "The Long Halloween", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Oswald Cobblepot, Edward Nygma")
	}, ba("cbc-narrator-column", [2]int{bruce, 0}), nil)
	f.book("cbc-narrators-json", "Batman: Gothic", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.NarratorsJSON = strp(`["Edward Nygma","Pamela Isley"]`)
	}, ba("cbc-narrators-json", [2]int{bruce, 0}), nil)
	f.book("cbc-narrator-positions", "Zero Year", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Oswald Cobblepot, Edward Nygma")
	}, ba("cbc-narrator-positions", [2]int{bruce, 0}), bn("cbc-narrator-positions", [2]int{oswald, 3}, [2]int{edward, 8}))
	f.book("cbc-clean", "No Man's Land", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Edward Nygma")
	}, ba("cbc-clean", [2]int{bruce, 0}), bn("cbc-clean", [2]int{edward, 0}))
	return []string{"cbc-join-empty", "cbc-no-author-id", "cbc-not-in-join", "cbc-all-zero", "cbc-gaps", "cbc-snapshot",
		"cbc-narrator-column", "cbc-narrators-json", "cbc-narrator-positions", "cbc-clean"}
}

// cbFixedCensusClasses are the census disagreement classes the backfill
// takes to zero on its own.
var cbFixedCensusClasses = []string{
	ccAuthorIDJoinEmpty, ccAuthorJoinNoAuthorID, ccAuthorIDNotInJoin, ccAuthorPositionsNotNorm,
	ccAuthorPositionsAllZero, ccAuthorSnapshotStale, ccNarratorColumnNoJunction, ccNarratorsJSONNoJunction,
	ccNarratorPositionsNotNorm,
}

func TestCreditsBackfill_CensusIsCleanAfterApply(t *testing.T) {
	f := newCBFixture(t)
	seedCBCensusFixture(f)

	before, err := runCreditCensusScan(context.Background(), f.s, &fakeReporter{}, 2)
	if err != nil {
		t.Fatalf("census before: %v", err)
	}
	for _, k := range cbFixedCensusClasses {
		if before.Counts[k] == 0 {
			t.Fatalf("fixture does not exercise census class %s", k)
		}
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 3)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !res.Complete {
		t.Fatalf("apply incomplete: %s", res.Error)
	}
	after, err := runCreditCensusScan(context.Background(), f.s, &fakeReporter{}, 2)
	if err != nil {
		t.Fatalf("census after: %v", err)
	}
	for _, def := range creditCensusClasses {
		if def.Kind == creditKindDisagreement && after.Counts[def.Key] != 0 {
			t.Errorf("census %s after the backfill = %d %v, want 0", def.Key, after.Counts[def.Key], after.Classes)
		}
	}
	if after.DisagreementBooks != 0 {
		t.Errorf("disagreement books after = %d", after.DisagreementBooks)
	}
	// A second apply finds nothing left: the pass is idempotent.
	again, err := f.run(creditsBackfillParams{Apply: true}, 3)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	rows, _ := f.s.GetOperationChanges(cbTestOpID)
	if want := res.BooksToWrite; len(rows) != want {
		t.Fatalf("journal rows after two applies = %d, want %d (one per written book)", len(rows), want)
	}
	if again.BooksWrittenEarlierAttempts != res.BooksToWrite {
		t.Fatalf("second apply: earlier=%d want %d", again.BooksWrittenEarlierAttempts, res.BooksToWrite)
	}
}

func TestCreditsBackfill_RevertRestoresThePreImage(t *testing.T) {
	f := newCBFixture(t)
	ids := seedCBCensusFixture(f)
	before := map[string]undo.CreditsBackfillState{}
	for _, id := range ids {
		before[id] = f.state(id)
	}
	if _, err := f.run(creditsBackfillParams{Apply: true}, 3); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// A person edits one book after the backfill: its revert must refuse.
	edited := "cbc-join-empty"
	selina, _ := f.s.GetAuthorByName("Selina Kyle")
	if _, err := f.s.ModifyBookAuthors(edited, func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
		return append(cur, database.BookAuthor{BookID: edited, AuthorID: selina.ID, Role: "author", Position: len(cur)}), nil
	}); err != nil {
		t.Fatal(err)
	}
	editedState := f.state(edited)

	rev, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID)
	if err == nil {
		t.Fatalf("revert reported no error although %s changed since; result %+v", edited, rev)
	}
	for _, id := range ids {
		got := f.state(id)
		switch id {
		case edited:
			if !undo.SameCreditsBackfillState(got, editedState) {
				t.Fatalf("revert overwrote the later edit on %s", id)
			}
		default:
			// The revert restores through Restored: rows in STORED order with
			// positions renumbered 0..n-1 (not Canonical, whose AuthorID
			// tie-breaker would reorder an all-zero pre-image and change the
			// organizer's filing author). For a pre-image whose positions
			// were already 0..n-1 that is exact.
			want := before[id].Restored()
			if !undo.SameCreditsBackfillState(got, want) {
				t.Fatalf("%s not restored:\nwant %+v\ngot  %+v", id, want, got)
			}
		}
	}
	for _, id := range []string{"cbc-snapshot", "cbc-narrator-column", "cbc-narrators-json", "cbc-no-author-id"} {
		if !undo.SameCreditsBackfillState(f.state(id), before[id]) {
			t.Fatalf("%s: canonical pre-image not restored exactly", id)
		}
	}
}

func TestCreditsBackfill_ConcurrentDisjointPartition(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.narrator("Oswald Cobblepot")
	const n = 120
	var ids []string
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("cbq-%03d", i)
		ids = append(ids, id)
		title := fmt.Sprintf("Batman: Legends of the Dark Knight %d", i)
		switch i % 3 {
		case 0:
			f.book(id, title, func(b *database.Book) { b.AuthorID = intp(bruce); b.Narrator = strp("Oswald Cobblepot") }, nil, nil)
		case 1:
			f.book(id, title, func(b *database.Book) { b.AuthorID = intp(bruce) }, ba(id, [2]int{selina, 0}, [2]int{bruce, 0}), nil)
		default:
			// Every third book names the same NEW narrator: one record, many books.
			f.book(id, title, func(b *database.Book) { b.AuthorID = intp(bruce); b.Narrator = strp("Victor Fries") },
				ba(id, [2]int{bruce, 0}), nil)
		}
	}
	dry1, err := f.run(creditsBackfillParams{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	dry8, err := f.run(creditsBackfillParams{}, 8)
	if err != nil {
		t.Fatal(err)
	}
	for i := range dry1.Classes {
		if !slices.Equal(dry1.Classes[i].BookIDs, dry8.Classes[i].BookIDs) {
			t.Fatalf("class %s differs between 1 and 8 workers", dry1.Classes[i].Key)
		}
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 8)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if res.BooksToWrite != n || !res.Complete {
		t.Fatalf("to_write=%d complete=%t err=%q", res.BooksToWrite, res.Complete, res.Error)
	}
	rows, _ := f.s.GetOperationChanges(cbTestOpID)
	per := map[string]int{}
	for _, r := range rows {
		per[r.BookID]++
	}
	for _, id := range ids {
		if per[id] != 1 {
			t.Fatalf("%s journaled %d times, want exactly once", id, per[id])
		}
	}
	victors := 0
	ns, _ := f.s.ListNarrators()
	for _, nr := range ns {
		if nr.Name == "Victor Fries" {
			victors++
		}
	}
	if victors != 1 || res.NarratorsCreated != 1 {
		t.Fatalf("Victor Fries records=%d created=%d, want one", victors, res.NarratorsCreated)
	}
	// Forty books got the one record back; exactly one journal row says it
	// created it.
	claims := 0
	for _, r := range rows {
		_, w, err := undo.DecodeCreditsBackfill(r)
		if err != nil {
			t.Fatal(err)
		}
		claims += len(w.CreatedNarrators)
	}
	if claims != 1 {
		t.Fatalf("%d journal rows claim to have created a narrator, want 1", claims)
	}
}

// cbInterleavingModifier runs edit once, just before the first
// ModifyBookCredits call for bookID, the way a person's edit can land between
// the backfill's read and its write.
type cbInterleavingModifier struct {
	database.BookCreditsModifier
	bookID string
	edit   func()
	once   sync.Once
}

func (m *cbInterleavingModifier) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	if id == m.bookID {
		m.once.Do(m.edit)
	}
	return m.BookCreditsModifier.ModifyBookCredits(id, fn)
}

func TestCreditsBackfill_BookChangedSincePlanIsNotOverwritten(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.book("cbe-01", "Batman: Year One", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	f.book("cbe-02", "Batman: Hush", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)

	mod := &cbInterleavingModifier{BookCreditsModifier: f.s, bookID: "cbe-01", edit: func() {
		if _, err := f.s.ModifyBookAuthors("cbe-01", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "cbe-01", AuthorID: selina, Role: "author"}}, nil
		}); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
	}}
	rep := &cbReporter{id: cbTestOpID}
	env := creditsBackfillEnv{store: f.s, mod: mod, opID: rep.id, workers: 1}
	res, err := runCreditsBackfillScan(context.Background(), env, creditsBackfillParams{Apply: true}, rep)
	if res == nil {
		t.Fatalf("no result: %v", err)
	}
	if got := cbAuthorIDs(f.state("cbe-01").Authors); !slices.Equal(got, []int{selina}) {
		t.Fatalf("cbe-01 join = %v, want the interleaved edit [%d] kept", got, selina)
	}
	if got := cbClassIDs(res, cbChangedSincePlan); !slices.Equal(got, []string{"cbe-01"}) {
		t.Fatalf("%s = %v", cbChangedSincePlan, got)
	}
	if got := cbAuthorIDs(f.state("cbe-02").Authors); !slices.Equal(got, []int{bruce}) {
		t.Fatalf("cbe-02 join = %v, the other book must still be written", got)
	}
	// The row journaled before the refused write must not let a revert
	// overwrite the person's edit.
	if _, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID); err != nil {
		t.Logf("revert: %v", err)
	}
	if got := cbAuthorIDs(f.state("cbe-01").Authors); !slices.Equal(got, []int{selina}) {
		t.Fatalf("revert overwrote the interleaved edit: %v", got)
	}
	if st := f.state("cbe-02"); len(st.Authors) != 0 {
		t.Fatalf("cbe-02 not reverted: %+v", st.Authors)
	}
}

func TestCreditsBackfill_ResumeAfterPartialRun(t *testing.T) {
	f := newCBFixture(t)
	ids := seedCBCensusFixture(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// One worker, canceled after four items: a run killed part-way.
	// Progress calls: one for the index, one per book while planning, then one
	// per write. Cancel four writes into the apply pass.
	rep := &cbReporter{id: cbTestOpID, cancel: cancel, cancelAfter: int64(len(ids)) + 1 + 4}
	first, err := f.runWith(ctx, rep, creditsBackfillParams{Apply: true}, 1)
	if err == nil {
		t.Fatal("the interrupted run reported success")
	}
	if first == nil || first.Complete {
		t.Fatalf("interrupted result = %+v", first)
	}
	firstWritten := first.BooksToWrite
	if firstWritten == 0 || firstWritten >= len(ids)-1 {
		t.Fatalf("first attempt wrote %d books; the test needs a partial run", firstWritten)
	}
	// The registry hands a resumed run its checkpoint merged into the params.
	rep.mu.Lock()
	ck := rep.checkpoints[len(rep.checkpoints)-1].(creditsBackfillParams)
	rep.mu.Unlock()
	if !ck.Apply || ck.Attempt != 1 {
		t.Fatalf("checkpoint = %+v, want apply and attempt 1", ck)
	}
	second, err := f.runWith(context.Background(), &cbReporter{id: cbTestOpID}, ck, 2)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	if second.Attempt != 2 || second.BooksWrittenEarlierAttempts != firstWritten {
		t.Fatalf("resumed attempt=%d earlier=%d, want 2 and %d", second.Attempt, second.BooksWrittenEarlierAttempts, firstWritten)
	}
	rows, _ := f.s.GetOperationChanges(cbTestOpID)
	per := map[string]int{}
	for _, r := range rows {
		per[r.BookID]++
	}
	for id, c := range per {
		if c != 1 {
			t.Fatalf("%s journaled %d times across the two attempts", id, c)
		}
	}
	if len(per) != second.BooksToWrite {
		t.Fatalf("journaled books=%d, result to_write=%d (must cover both attempts)", len(per), second.BooksToWrite)
	}
	census, err := runCreditCensusScan(context.Background(), f.s, &fakeReporter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if census.DisagreementBooks != 0 {
		t.Fatalf("census after resume: %d disagreement books (%v)", census.DisagreementBooks, census.Counts)
	}
}

func TestCreditsBackfill_RefusesUnknownAllowKey(t *testing.T) {
	f := newCBFixture(t)
	if _, err := f.run(creditsBackfillParams{Allow: []string{"authors_from_thin_air"}}, 1); err == nil {
		t.Fatal("an unknown allow key was accepted")
	}
}

func TestCreditsBackfill_ClassKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	var keys []string
	for _, c := range creditsBackfillClasses {
		if seen[c.Key] {
			t.Fatalf("duplicate class %s", c.Key)
		}
		seen[c.Key] = true
		keys = append(keys, c.Key)
	}
	sort.Strings(keys)
	for _, k := range creditsBackfillAllowKeys {
		if k != cbAllowITunes && !seen[k] {
			t.Fatalf("allow key %s has no class", k)
		}
	}
}

// Two workers that planned the same new name before either created it both
// call CreateNarrator and both get the one record back. Only the first claim
// counts as a creation, so one journal row names it.
func TestCreditsBackfill_ClaimCreatedOnlyOnce(t *testing.T) {
	a := newCBAccumulator()
	victor := database.Narrator{ID: 41, Name: "Victor Fries"}
	pamela := database.Narrator{ID: 42, Name: "Pamela Isley"}
	if got := a.claimCreated([]database.Narrator{victor}); len(got) != 1 || got[0].ID != 41 {
		t.Fatalf("first claim = %+v", got)
	}
	if got := a.claimCreated([]database.Narrator{victor, pamela}); len(got) != 1 || got[0].ID != 42 {
		t.Fatalf("second claim = %+v, want only Pamela Isley", got)
	}
	if len(a.createdIDs) != 2 {
		t.Fatalf("created ids = %v", a.createdIDs)
	}
}

// The census does not reach 0 on its own: books the backfill holds or leaves
// for review keep their census class. This pins which ones, so a census
// re-run after the apply can be read against it.
func TestCreditsBackfill_CensusResidualIsHeldAndReviewBooks(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("cbz-split", "Batman: Venom", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Jonathan Crane, Victor Fries")
	}, ba("cbz-split", [2]int{bruce, 0}), nil)
	f.book("cbz-itunes", "Batman: Prey", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.FilePath = "/lib/Gotham/books/itunes/Prey/Prey.m4b"
	}, nil, nil)
	f.book("cbz-manual", "Batman: Gothic Knights", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Publisher = strp("Big Finish Productions")
	}, nil, nil)

	if _, err := f.run(creditsBackfillParams{Apply: true}, 2); err != nil {
		t.Fatalf("apply: %v", err)
	}
	census, err := runCreditCensusScan(context.Background(), f.s, &fakeReporter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := censusClassIDs(census, ccNarratorColumnNoJunction); !slices.Equal(got, []string{"cbz-split"}) {
		t.Fatalf("%s after apply = %v, want the split-name book left for review", ccNarratorColumnNoJunction, got)
	}
	if got := censusClassIDs(census, ccAuthorIDJoinEmpty); !slices.Equal(got, []string{"cbz-itunes", "cbz-manual"}) {
		t.Fatalf("%s after apply = %v, want the iTunes and owner-manual books held", ccAuthorIDJoinEmpty, got)
	}

	if _, err := f.run(creditsBackfillParams{Apply: true, Allow: []string{cbAllowNewFromSplit, cbAllowITunes}}, 2); err != nil {
		t.Fatalf("apply with allow: %v", err)
	}
	census, err = runCreditCensusScan(context.Background(), f.s, &fakeReporter{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := censusClassIDs(census, ccNarratorColumnNoJunction); len(got) != 0 {
		t.Fatalf("%s with the opt-in = %v", ccNarratorColumnNoJunction, got)
	}
	if got := censusClassIDs(census, ccAuthorIDJoinEmpty); !slices.Equal(got, []string{"cbz-manual"}) {
		t.Fatalf("%s with the opt-ins = %v, want only the owner-manual book", ccAuthorIDJoinEmpty, got)
	}
}

func TestCreditsBackfill_LimitHoldsAcrossAResume(t *testing.T) {
	f := newCBFixture(t)
	listed := seedCBCensusFixture(f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The progress counter runs through the planning pass (one call for the
	// index, one per book) before the first write: cancel two writes in.
	rep := &cbReporter{id: cbTestOpID, cancel: cancel, cancelAfter: int64(len(listed)) + 1 + 2}
	first, _ := f.runWith(ctx, rep, creditsBackfillParams{Apply: true, Limit: 3}, 1)
	if first == nil || first.BooksToWrite == 0 || first.BooksToWrite >= 3 {
		t.Fatalf("first attempt wrote %v books; the test needs a partial cohort", first)
	}
	rep.mu.Lock()
	ck := rep.checkpoints[len(rep.checkpoints)-1].(creditsBackfillParams)
	rep.mu.Unlock()
	second, err := f.runWith(context.Background(), &cbReporter{id: cbTestOpID}, ck, 2)
	if err != nil {
		t.Fatalf("resumed run: %v", err)
	}
	rows, _ := f.s.GetOperationChanges(cbTestOpID)
	if len(rows) != 3 || second.BooksToWrite != 3 {
		t.Fatalf("journal rows=%d to_write=%d after the resume, want the cohort of 3", len(rows), second.BooksToWrite)
	}
	if len(cbClassIDs(second, cbLimitReached)) == 0 {
		t.Fatal("the resumed attempt did not report the limit")
	}
}

// cbRefusedWriteRun runs an apply in which cbe-01's write is refused (a person
// edits the book between the plan and the write) and cbe-02's lands.
func cbRefusedWriteRun(t *testing.T, f *cbFixture) (bruce, selina int) {
	t.Helper()
	bruce = f.author("Bruce Wayne")
	selina = f.author("Selina Kyle")
	f.book("cbe-01", "Batman: Year One", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	f.book("cbe-02", "Batman: Hush", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	mod := &cbInterleavingModifier{BookCreditsModifier: f.s, bookID: "cbe-01", edit: func() {
		if _, err := f.s.ModifyBookAuthors("cbe-01", func([]database.BookAuthor) ([]database.BookAuthor, error) {
			return []database.BookAuthor{{BookID: "cbe-01", AuthorID: selina, Role: "author"}}, nil
		}); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
	}}
	rep := &cbReporter{id: cbTestOpID}
	env := creditsBackfillEnv{store: f.s, mod: mod, opID: rep.id, workers: 1}
	if _, err := runCreditsBackfillScan(context.Background(), env, creditsBackfillParams{Apply: true}, rep); err == nil {
		t.Fatal("a refused write reported a clean run")
	}
	return bruce, selina
}

func cbJournalRowsOf(t *testing.T, f *cbFixture, bookID string) []*database.OperationChange {
	t.Helper()
	rows, err := f.s.GetOperationChanges(cbTestOpID)
	if err != nil {
		t.Fatal(err)
	}
	var out []*database.OperationChange
	for _, r := range rows {
		if r.BookID == bookID {
			out = append(out, r)
		}
	}
	return out
}

// N6: a journal row written for a write that was refused is voided, the way
// every journaling writer takes back a row for a write it did not make: the
// revert ignores it and the resume does not read it as a written book.
func TestCreditsBackfill_RefusedWriteVoidsItsJournalRow(t *testing.T) {
	f := newCBFixture(t)
	cbRefusedWriteRun(t, f)
	rows := cbJournalRowsOf(t, f, "cbe-01")
	if len(rows) != 1 || !rows[0].Voided || rows[0].RevertedAt == nil {
		t.Fatalf("cbe-01 journal rows = %+v, want one voided row", rows)
	}
	if rows := cbJournalRowsOf(t, f, "cbe-02"); len(rows) != 1 || rows[0].Voided {
		t.Fatalf("cbe-02 journal rows = %+v, want one live row", rows)
	}

	// The attempt that resumes it: cbe-01 is dirty again (AuthorID not in the
	// join) and its voided row is not a prior write.
	second, err := f.run(creditsBackfillParams{Apply: true, Attempt: 1}, 2)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if second.BooksWrittenEarlierAttempts != 1 {
		t.Fatalf("earlier=%d, want 1 (cbe-02 only; cbe-01's row was voided)", second.BooksWrittenEarlierAttempts)
	}
	if got := cbClassIDs(second, cbJournalUnverified); len(got) != 0 {
		t.Fatalf("%s = %v, want none: a voided row is not a stranded one", cbJournalUnverified, got)
	}
	if got := cbClassIDs(second, cbAuthorIDPrepended); !slices.Equal(got, []string{"cbe-01"}) {
		t.Fatalf("%s = %v, want cbe-01 written now", cbAuthorIDPrepended, got)
	}
	// The revert acts on the live rows only and puts the person's edit back
	// where it was before the second attempt's write.
	if _, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	selina, _ := f.s.GetAuthorByName("Selina Kyle")
	if got := cbAuthorIDs(f.state("cbe-01").Authors); !slices.Equal(got, []int{selina.ID}) {
		t.Fatalf("cbe-01 after revert = %v, want the person's edit [%d]", got, selina.ID)
	}
}

// A row that was journaled and never voided (the run stopped between the
// journal and the write) is journal_unverified when the book does not hold
// its post-image, and the revert restores from the row that matches.
func TestCreditsBackfill_ResumeDoesNotCountAStrandedRow(t *testing.T) {
	f := newCBFixture(t)
	cbRefusedWriteRun(t, f)
	// Un-void cbe-01's row: a crash between the journal and the write leaves
	// it standing.
	row := *cbJournalRowsOf(t, f, "cbe-01")[0]
	row.Voided, row.RevertedAt = false, nil
	if err := f.s.CreateOperationChange(&row); err != nil {
		t.Fatal(err)
	}

	second, err := f.run(creditsBackfillParams{Apply: true, Attempt: 1}, 2)
	if err != nil {
		t.Fatalf("second attempt: %v", err)
	}
	if second.BooksWrittenEarlierAttempts != 1 {
		t.Fatalf("earlier=%d, want 1 (cbe-02 only; cbe-01's row never landed)", second.BooksWrittenEarlierAttempts)
	}
	if got := cbClassIDs(second, cbJournalUnverified); !slices.Equal(got, []string{"cbe-01"}) {
		t.Fatalf("%s = %v", cbJournalUnverified, got)
	}
	if got := cbClassIDs(second, cbAuthorIDPrepended); !slices.Equal(got, []string{"cbe-01"}) {
		t.Fatalf("%s = %v, want cbe-01 written now", cbAuthorIDPrepended, got)
	}
	// Two rows for cbe-01; the revert restores from the one that matches.
	if _, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID); err != nil {
		t.Logf("revert: %v (the stranded row refuses)", err)
	}
	selina, _ := f.s.GetAuthorByName("Selina Kyle")
	if got := cbAuthorIDs(f.state("cbe-01").Authors); !slices.Equal(got, []int{selina.ID}) {
		t.Fatalf("cbe-01 after revert = %v, want the person's edit [%d]", got, selina.ID)
	}
}

func censusClassIDs(res *creditCensusResult, key string) []string {
	for _, c := range res.Classes {
		if c.Key == key {
			return c.BookIDs
		}
	}
	return nil
}
