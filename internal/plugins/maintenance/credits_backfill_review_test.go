// file: internal/plugins/maintenance/credits_backfill_review_test.go
// version: 1.0.2
// guid: 5b0f2c1e-8a47-4d3b-9e6a-27c4d1f08b93
// last-edited: 2026-10-28

package maintenance

import (
	"context"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/audiobooks"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/organizer"
	"github.com/falkcorp/audiobook-organizer/internal/undo"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// Tests for the review of PR #4195 (credits backfill). Fixtures: Batman
// characters as people, Batman storylines as titles, paths under /lib/Gotham
// (cbFixture.book). Nothing here comes from a real library.

func (f *cbFixture) lock(bookID, field string) {
	f.t.Helper()
	if err := f.s.UpsertMetadataFieldState(&database.MetadataFieldState{BookID: bookID, Field: field, OverrideLocked: true}); err != nil {
		f.t.Fatal(err)
	}
}

// cbUntouched asserts the book's whole credit state is exactly as it was.
func cbUntouched(t *testing.T, f *cbFixture, id string, before undo.CreditsBackfillState) {
	t.Helper()
	if got := f.state(id); !undo.SameCreditsBackfillState(got, before) {
		t.Errorf("%s was written:\nbefore %+v\nafter  %+v", id, before, got)
	}
}

func cbHasID(res *creditsBackfillResult, class, id string) bool {
	return slices.Contains(cbClassIDs(res, class), id)
}

// B1: a rescan reverts AuthorID from tags and never touches the join or the
// snapshot. When the snapshot and the join agree on another author, AuthorID
// is the stale signal: the book goes to review, nothing is written, and the
// snapshot is not rewritten to follow AuthorID.
func TestCreditsBackfillReview_SnapshotNamingAJoinMemberBlocksTheAuthorIDPrepend(t *testing.T) {
	f := newCBFixture(t)
	jason := f.author("Jason Todd")
	dick := f.author("Dick Grayson")
	f.book("rv-b1", "Under the Red Hood", func(b *database.Book) {
		b.AuthorID = intp(jason)
		b.Author = &database.Author{ID: dick, Name: "Dick Grayson"}
	}, ba("rv-b1", [2]int{dick, 0}), nil)
	before := f.state("rv-b1")

	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	cbUntouched(t, f, "rv-b1", before)
	if got := cbClassIDs(res, cbAuthorIDSnapshotConflict); !slices.Equal(got, []string{"rv-b1"}) {
		t.Fatalf("%s = %v", cbAuthorIDSnapshotConflict, got)
	}
	if res.BooksToWrite != 0 || res.BooksInReview != 1 {
		t.Fatalf("to_write=%d review=%d, want 0 and 1", res.BooksToWrite, res.BooksInReview)
	}
}

// The plan's rule holds where nothing contradicts AuthorID: no snapshot, a
// snapshot that names AuthorID, or one that names an author outside the join.
// (The reviewer's repro R2, AuthorID Jason against a join of Dick with no
// snapshot, has the shape of the plan's own case; it is written, and listed
// under filing_author_changes so the owner sees it.)
func TestCreditsBackfillReview_CleanCasesKeepThePlansPrependRule(t *testing.T) {
	f := newCBFixture(t)
	jason := f.author("Jason Todd")
	dick := f.author("Dick Grayson")
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.book("rv-c1", "Under the Hood", func(b *database.Book) { b.AuthorID = intp(jason) }, ba("rv-c1", [2]int{dick, 0}), nil)
	f.book("rv-c2", "Batman: Year One", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Author = &database.Author{ID: bruce, Name: "Bruce Wayne"}
	}, ba("rv-c2", [2]int{dick, 0}), nil)
	f.book("rv-c3", "Batman: Hush", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Author = &database.Author{ID: selina, Name: "Selina Kyle"} // outside the join
	}, ba("rv-c3", [2]int{dick, 0}), nil)

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]int{"rv-c1": jason, "rv-c2": bruce, "rv-c3": bruce} {
		if st := f.state(id); len(st.Authors) != 2 || st.Authors[0].AuthorID != want {
			t.Errorf("%s join = %v, want %d first", id, cbAuthorIDs(st.Authors), want)
		}
	}
	if got := cbClassIDs(res, cbAuthorIDPrepended); !slices.Equal(got, []string{"rv-c1", "rv-c2", "rv-c3"}) {
		t.Fatalf("%s = %v", cbAuthorIDPrepended, got)
	}
	if !cbHasID(res, cbFilingAuthorChanges, "rv-c1") {
		t.Fatalf("rv-c1 is not listed under %s: %v", cbFilingAuthorChanges, cbClassIDs(res, cbFilingAuthorChanges))
	}
	if len(cbClassIDs(res, cbAuthorIDSnapshotConflict)) != 0 {
		t.Fatalf("%s = %v", cbAuthorIDSnapshotConflict, cbClassIDs(res, cbAuthorIDSnapshotConflict))
	}
}

// B1, second half: the all-zero tie-break puts AuthorID first. When that
// reorders the join and the snapshot names the other member, it is review.
func TestCreditsBackfillReview_AllZeroTieBreakGoesToReviewWhenTheSnapshotNamesTheOtherMember(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.book("rv-z1", "The Long Halloween", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Author = &database.Author{ID: selina, Name: "Selina Kyle"}
	}, ba("rv-z1", [2]int{selina, 0}, [2]int{bruce, 0}), nil)
	// No snapshot: the plan's tie-break (AuthorID first) applies.
	f.book("rv-z2", "Dark Victory", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("rv-z2", [2]int{selina, 0}, [2]int{bruce, 0}), nil)
	// The snapshot names AuthorID itself: also clean.
	f.book("rv-z3", "Catwoman: When in Rome", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Author = &database.Author{ID: bruce, Name: "Bruce Wayne"}
	}, ba("rv-z3", [2]int{selina, 0}, [2]int{bruce, 0}), nil)
	// The tie-break changes nothing (AuthorID already first): the snapshot is
	// irrelevant and the renumber is written.
	f.book("rv-z4", "Batman: Ego", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Author = &database.Author{ID: selina, Name: "Selina Kyle"}
	}, ba("rv-z4", [2]int{bruce, 0}, [2]int{selina, 0}), nil)
	before := f.state("rv-z1")

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	cbUntouched(t, f, "rv-z1", before)
	if got := cbClassIDs(res, cbAuthorIDSnapshotConflict); !slices.Equal(got, []string{"rv-z1"}) {
		t.Fatalf("%s = %v", cbAuthorIDSnapshotConflict, got)
	}
	for _, id := range []string{"rv-z2", "rv-z3", "rv-z4"} {
		if st := f.state(id); st.Authors[0].AuthorID != bruce || st.Authors[1].Position != 1 {
			t.Errorf("%s join = %+v, want renumbered with %d first", id, st.Authors, bruce)
		}
		if !cbHasID(res, cbAuthorPositionsRenumbered, id) {
			t.Errorf("%s not in %s", id, cbAuthorPositionsRenumbered)
		}
	}
}

// B2: the narrator user lock is honoured. A person who clears the narrator in
// the UI locks the field (column "", junction [], override ""), and the
// NarratorsJSON a UI edit does not touch must not refill it.
func TestCreditsBackfillReview_NarratorUserLockHoldsTheNarratorSide(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Alfred Pennyworth")
	f.book("rv-n1", "Knightfall", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("")
		b.NarratorsJSON = strp(`["Alfred Pennyworth"]`)
	}, ba("rv-n1", [2]int{bruce, 0}), nil)
	f.lock("rv-n1", database.FieldKeyNarrator)
	// The same lock on a book whose author side also has work: the author
	// side is written, the narrator side held.
	f.book("rv-n2", "Batman: Death of the Family", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Alfred Pennyworth")
	}, nil, nil)
	f.lock("rv-n2", database.FieldKeyNarrator)
	before := f.state("rv-n1")

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	cbUntouched(t, f, "rv-n1", before)
	if got := cbClassIDs(res, cbHeldNarratorLocked); !slices.Equal(got, []string{"rv-n1", "rv-n2"}) {
		t.Fatalf("%s = %v", cbHeldNarratorLocked, got)
	}
	st2 := f.state("rv-n2")
	if len(st2.Narrators) != 0 {
		t.Fatalf("rv-n2 narrator junction = %v, want held", cbNarratorIDs(st2.Narrators))
	}
	if len(st2.Authors) != 1 || st2.Authors[0].AuthorID != bruce {
		t.Fatalf("rv-n2 author side = %v, want it written", cbAuthorIDs(st2.Authors))
	}
}

// B3: the placeholder author is never credited, made primary, or the source of
// AuthorID.
func TestCreditsBackfillReview_PlaceholderAuthorGoesToReview(t *testing.T) {
	f := newCBFixture(t)
	unknown := f.author("Unknown Author")
	dick := f.author("Dick Grayson")
	// AuthorID is the placeholder a rescan reverted to; the join holds the
	// applied author; the snapshot agrees with AuthorID.
	f.book("rv-p1", "Hush", func(b *database.Book) {
		b.AuthorID = intp(unknown)
		b.Author = &database.Author{ID: unknown, Name: "Unknown Author"}
	}, ba("rv-p1", [2]int{dick, 0}), nil)
	// Same without a snapshot.
	f.book("rv-p2", "Hush Returns", func(b *database.Book) { b.AuthorID = intp(unknown) }, ba("rv-p2", [2]int{dick, 0}), nil)
	// A placeholder-only join and no AuthorID: taking AuthorID from it would
	// make the organizer read the book as resolved.
	f.book("rv-p3", "Batman: Earth One", nil, ba("rv-p3", [2]int{unknown, 0}), nil)
	// Empty join, AuthorID the placeholder.
	f.book("rv-p4", "Batman: Zero Year", func(b *database.Book) { b.AuthorID = intp(unknown) }, nil, nil)
	// Empty join, no AuthorID, snapshot the placeholder by id.
	f.book("rv-p5", "Batman: Endgame", func(b *database.Book) { b.Author = &database.Author{ID: unknown, Name: "Unknown Author"} }, nil, nil)
	ids := []string{"rv-p1", "rv-p2", "rv-p3", "rv-p4", "rv-p5"}
	before := map[string]undo.CreditsBackfillState{}
	for _, id := range ids {
		before[id] = f.state(id)
	}
	resolvedBefore := cbResolved(t, f, "rv-p3")

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		cbUntouched(t, f, id, before[id])
	}
	if got := cbClassIDs(res, cbAuthorPlaceholder); !slices.Equal(got, ids) {
		t.Fatalf("%s = %v, want %v", cbAuthorPlaceholder, got, ids)
	}
	if res.BooksToWrite != 0 {
		t.Fatalf("to_write=%d", res.BooksToWrite)
	}
	if after := cbResolved(t, f, "rv-p3"); after != resolvedBefore {
		t.Fatalf("HasResolvedAuthor flipped %t -> %t", resolvedBefore, after)
	}
}

func cbResolved(t *testing.T, f *cbFixture, id string) bool {
	t.Helper()
	o := organizer.NewOrganizer(nil)
	o.SetStore(f.s)
	b, err := f.s.GetBookByID(id)
	if err != nil || b == nil {
		t.Fatalf("GetBookByID(%s): %v", id, err)
	}
	return o.HasResolvedAuthor(b)
}

// S1: a user lock on the author field holds the WHOLE author side, including
// the writes that leave the filing author alone (AuthorID from the join, the
// snapshot refresh). The narrator side is planned on its own.
func TestCreditsBackfillReview_AuthorUserLockHoldsTheWholeAuthorSide(t *testing.T) {
	f := newCBFixture(t)
	dick := f.author("Dick Grayson")
	f.narrator("Alfred Pennyworth")
	f.book("rv-l1", "Nightwing: Year One", func(b *database.Book) {
		b.Author = &database.Author{ID: 0, Name: "Richard Grayson"}
	}, ba("rv-l1", [2]int{dick, 0}), nil)
	f.lock("rv-l1", database.FieldKeyAuthorName)
	f.book("rv-l2", "Nightwing: Rebirth", func(b *database.Book) {
		b.Narrator = strp("Alfred Pennyworth")
	}, ba("rv-l2", [2]int{dick, 0}), nil)
	f.lock("rv-l2", database.FieldKeyAuthorName)
	before := f.state("rv-l1")

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	cbUntouched(t, f, "rv-l1", before)
	st := f.state("rv-l1")
	if st.AuthorID != nil || st.Author == nil || st.Author.Name != "Richard Grayson" {
		t.Fatalf("locked author's AuthorID or snapshot rewritten: %+v %+v", st.AuthorID, st.Author)
	}
	if got := cbClassIDs(res, cbHeldUserLocked); !slices.Equal(got, []string{"rv-l1", "rv-l2"}) {
		t.Fatalf("%s = %v", cbHeldUserLocked, got)
	}
	st2 := f.state("rv-l2")
	if st2.AuthorID != nil {
		t.Fatalf("rv-l2 AuthorID written despite the author lock: %v", *st2.AuthorID)
	}
	if len(st2.Narrators) != 1 || !cbHasID(res, cbNarratorFillFromColumn, "rv-l2") {
		t.Fatalf("rv-l2 narrator side not written: %v", cbNarratorIDs(st2.Narrators))
	}
}

// B4: reverting the AuthorID tie-break gives the organizer back the filing
// author it had before the backfill (the first STORED row at the lowest
// position), not the AuthorID-first order the forward write used.
func TestCreditsBackfillReview_RevertRestoresTheOrganizersFilingAuthor(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.book("rv-r1", "The Long Halloween", func(b *database.Book) { b.AuthorID = intp(bruce) },
		ba("rv-r1", [2]int{selina, 0}, [2]int{bruce, 0}), nil)
	filing := func() string {
		idx, err := buildCreditCensusIndex(f.s)
		if err != nil {
			t.Fatal(err)
		}
		st := f.state("rv-r1")
		name, err := filingAuthorName(idx, st.Authors, st.Author, st.AuthorID)
		if err != nil {
			t.Fatal(err)
		}
		return name
	}
	pre := filing()
	if pre != "Selina Kyle" {
		t.Fatalf("fixture: filing author before = %q, want Selina Kyle (first stored row)", pre)
	}
	if _, err := f.run(creditsBackfillParams{Apply: true}, 1); err != nil {
		t.Fatal(err)
	}
	if mid := filing(); mid != "Bruce Wayne" {
		t.Fatalf("filing author after the backfill = %q, want the plan's AuthorID-first order", mid)
	}
	if _, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	if post := filing(); post != pre {
		t.Fatalf("revert left the filing author %q, want %q (join %v)", post, pre, cbAuthorIDs(f.state("rv-r1").Authors))
	}
	// Reverting again is refused as "already reverted" (every row is marked
	// reverted), never as a changed-since refusal of a book that is back at its
	// pre-image, and it writes nothing. A row still unmarked on a book at its
	// pre-image is reported restored: see
	// TestCreditsBackfill_RevertOfAWriteThatNeverLandedIsNotRefused.
	second, err := audiobooks.NewRevertService(f.s).RevertOperation(cbTestOpID)
	if err == nil || !strings.Contains(err.Error(), "already been reverted") {
		t.Fatalf("second revert = %+v, %v; want an \"already been reverted\" answer", second, err)
	}
	if post := filing(); post != pre {
		t.Fatalf("second revert changed the filing author to %q", post)
	}
}

// S2: the dry run counts the books whose write makes the store re-identify
// them (AuthorID nil to a value, or to an author of another name), with the
// ids. An AuthorID the write leaves alone does not count.
func TestCreditsBackfillReview_CountsBooksThatWouldBeReidentified(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	dick := f.author("Dick Grayson")
	f.book("rv-s2a", "Batman: Year Three", nil, ba("rv-s2a", [2]int{dick, 0}), nil)                                                         // AuthorID nil -> dick
	f.book("rv-s2b", "Batman: Hush", func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)                                         // AuthorID unchanged
	f.book("rv-s2c", "Batman: Year Two", func(b *database.Book) { b.AuthorID = intp(bruce) }, ba("rv-s2c", [2]int{dick, 0}), nil)           // prepend, AuthorID unchanged
	f.book("rv-s2d", "Batman: Zero Hour", func(b *database.Book) { b.Author = &database.Author{ID: dick, Name: "Dick Grayson"} }, nil, nil) // snapshot fill: nil -> dick

	res, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.BooksToReidentify != 2 {
		t.Fatalf("books_to_reidentify = %d, want 2", res.BooksToReidentify)
	}
	if got := cbClassIDs(res, cbSearchIdentityChanges); !slices.Equal(got, []string{"rv-s2a", "rv-s2d"}) {
		t.Fatalf("%s = %v", cbSearchIdentityChanges, got)
	}

	// An apply reports the same books for the writes that landed.
	g := newCBFixture(t)
	gd := g.author("Dick Grayson")
	g.book("rv-s2e", "Batman: Year Three", nil, ba("rv-s2e", [2]int{gd, 0}), nil)
	res, err = g.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.BooksToReidentify != 1 || !cbHasID(res, cbSearchIdentityChanges, "rv-s2e") {
		t.Fatalf("apply: books_to_reidentify=%d ids=%v", res.BooksToReidentify, cbClassIDs(res, cbSearchIdentityChanges))
	}
}

// S3: the dry run honours the limit, and the cohort is the first N ELIGIBLE
// ids in sorted order in both modes.
func TestCreditsBackfillReview_LimitCohortIsTheFirstEligibleIDsInBothModes(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	// cbl-00 is clean (not eligible) and sorts first: the cohort skips it.
	f.book("cbl-00", "Batman: Year One", func(b *database.Book) { b.AuthorID = intp(bruce) }, ba("cbl-00", [2]int{bruce, 0}), nil)
	for _, id := range []string{"cbl-05", "cbl-03", "cbl-04", "cbl-01", "cbl-02"} {
		f.book(id, "Batman: "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	want := []string{"cbl-01", "cbl-02", "cbl-03"}

	dry, err := f.run(creditsBackfillParams{Limit: 3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(dry, cbAuthorFillFromAuthorID); !slices.Equal(got, want) {
		t.Fatalf("dry run cohort = %v, want %v", got, want)
	}
	if got := cbClassIDs(dry, cbLimitReached); !slices.Equal(got, []string{"cbl-04", "cbl-05"}) {
		t.Fatalf("dry run %s = %v", cbLimitReached, got)
	}
	if dry.BooksEligible != 5 || dry.BooksToWrite != 3 || dry.Limit != 3 {
		t.Fatalf("dry run eligible=%d to_write=%d limit=%d", dry.BooksEligible, dry.BooksToWrite, dry.Limit)
	}
	for _, id := range want {
		if len(f.state(id).Authors) != 0 {
			t.Fatalf("dry run wrote %s", id)
		}
	}

	applied, err := f.run(creditsBackfillParams{Apply: true, Limit: 3}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(applied, cbAuthorFillFromAuthorID); !slices.Equal(got, want) {
		t.Fatalf("apply cohort = %v, want %v", got, want)
	}
	for _, id := range want {
		if len(f.state(id).Authors) != 1 {
			t.Fatalf("apply did not write %s", id)
		}
	}
	for _, id := range []string{"cbl-04", "cbl-05"} {
		if len(f.state(id).Authors) != 0 {
			t.Fatalf("apply wrote %s beyond the limit", id)
		}
	}
}

// S4: the op declares a books write, so the dispatcher's write-set gate keeps
// it apart from other book-writing ops.
func TestCreditsBackfillReview_DeclaresABooksWrite(t *testing.T) {
	def := (&Plugin{}).creditsBackfillDef()
	if !slices.Contains(def.Writes, sdk.ResBooks) {
		t.Fatalf("Writes = %v, want books", def.Writes)
	}
}

// S5: stand-ins are never credited, created or linked.
func TestCreditsBackfillReview_JunkNarratorsGoToReview(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	// An existing "Unknown" record must not be linked either.
	f.narrator("Unknown")
	names := map[string]string{"rv-j1": "Unknown", "rv-j2": "Full Cast", "rv-j3": "Various", "rv-j4": "N/A", "rv-j5": "Narrator", "rv-j6": "Anonymous"}
	var ids []string
	for id, name := range names {
		ids = append(ids, id)
		f.book(id, "Batman: "+id, func(b *database.Book) {
			b.AuthorID = intp(bruce)
			b.Narrator = strp(name)
		}, ba(id, [2]int{bruce, 0}), nil)
	}
	slices.Sort(ids)
	// The JSON path too.
	f.book("rv-j7", "Batman: Gotham Central", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.NarratorsJSON = strp(`["Unknown Narrator"]`)
	}, ba("rv-j7", [2]int{bruce, 0}), nil)
	ids = append(ids, "rv-j7")
	narratorsBefore, _ := f.s.ListNarrators()

	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbClassIDs(res, cbNarratorJunkName); !slices.Equal(got, ids) {
		t.Fatalf("%s = %v, want %v", cbNarratorJunkName, got, ids)
	}
	for _, id := range ids {
		if st := f.state(id); len(st.Narrators) != 0 {
			t.Errorf("%s junction = %v, want empty", id, cbNarratorIDs(st.Narrators))
		}
	}
	if after, _ := f.s.ListNarrators(); len(after) != len(narratorsBefore) {
		t.Fatalf("narrators %d -> %d: a stand-in was created", len(narratorsBefore), len(after))
	}
}

// S5: names that differ only in diacritics are one narrator.
func TestCreditsBackfillReview_NarratorNamesMatchWithoutDiacritics(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	renee := f.narrator("Renée Montoya")
	f.book("rv-d1", "Gotham Central: In the Line of Duty", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("rv-d1", [2]int{bruce, 0}), nil)
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := cbNarratorIDs(f.state("rv-d1").Narrators); !slices.Equal(got, []int{renee}) {
		t.Fatalf("junction = %v, want the existing record [%d]", got, renee)
	}
	if ns, _ := f.s.ListNarrators(); len(ns) != 1 || res.NarratorsCreated != 0 {
		t.Fatalf("narrators=%d created=%d, want the existing record linked", len(ns), res.NarratorsCreated)
	}
}

// S5: two books naming "Renee" and "Renée" before either record exists get one
// record, in a dry run's count and in the apply.
func TestCreditsBackfillReview_NewNarratorsOfOneFoldKeyShareARecord(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.book("rv-d2", "Gotham Central: Half a Life", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("rv-d2", [2]int{bruce, 0}), nil)
	f.book("rv-d3", "Gotham Central: Unresolved", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renée Montoya")
	}, ba("rv-d3", [2]int{bruce, 0}), nil)

	dry, err := f.run(creditsBackfillParams{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if dry.NarratorsToCreate != 1 {
		t.Fatalf("dry run narrators_to_create = %d (%v), want 1", dry.NarratorsToCreate, dry.NarratorNames)
	}
	res, err := f.run(creditsBackfillParams{Apply: true}, 2)
	if err != nil {
		t.Fatal(err)
	}
	ns, _ := f.s.ListNarrators()
	if len(ns) != 1 || res.NarratorsCreated != 1 {
		t.Fatalf("narrators=%d created=%d, want one record", len(ns), res.NarratorsCreated)
	}
	a, b := f.state("rv-d2").Narrators, f.state("rv-d3").Narrators
	if len(a) != 1 || len(b) != 1 || a[0].NarratorID != b[0].NarratorID {
		t.Fatalf("junctions %v / %v, want the one record on both", cbNarratorIDs(a), cbNarratorIDs(b))
	}
}

// S5: a name that matches no record exactly and folds to several is for a
// person to settle.
func TestCreditsBackfillReview_AmbiguousFoldIsReview(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	f.narrator("Renée Montoya")
	f.narrator("Renèe Montoya")
	f.book("rv-d4", "Gotham Central: Corrigan", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Renee Montoya")
	}, ba("rv-d4", [2]int{bruce, 0}), nil)
	before := f.state("rv-d4")
	res, err := f.run(creditsBackfillParams{Apply: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	cbUntouched(t, f, "rv-d4", before)
	if got := cbClassIDs(res, cbNarratorNameAmbiguous); !slices.Equal(got, []string{"rv-d4"}) {
		t.Fatalf("%s = %v", cbNarratorNameAmbiguous, got)
	}
}

// cbBarrierModifier makes ModifyBookCredits wait until two calls are in
// flight at once, so a run that is not concurrent never gets past it.
type cbBarrierModifier struct {
	database.BookCreditsModifier
	inflight atomic.Int32
	maxSeen  atomic.Int32
	release  chan struct{}
	once     sync.Once
	timeout  time.Duration
}

func (m *cbBarrierModifier) ModifyBookCredits(id string, fn func(*database.Book, *database.BookCreditsEdit) error) (*database.Book, database.BookCreditsEdit, error) {
	n := m.inflight.Add(1)
	defer m.inflight.Add(-1)
	for {
		cur := m.maxSeen.Load()
		if n <= cur || m.maxSeen.CompareAndSwap(cur, n) {
			break
		}
	}
	if n >= 2 {
		m.once.Do(func() { close(m.release) })
	}
	select {
	case <-m.release:
	case <-time.After(m.timeout):
	}
	return m.BookCreditsModifier.ModifyBookCredits(id, fn)
}

// N1: the apply pass writes books concurrently. With one worker the barrier
// times out and the test fails; it never hangs.
func TestCreditsBackfillReview_ApplyWritesConcurrently(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	for _, id := range []string{"rv-w1", "rv-w2", "rv-w3", "rv-w4"} {
		f.book(id, "Batman: "+id, func(b *database.Book) { b.AuthorID = intp(bruce) }, nil, nil)
	}
	mod := &cbBarrierModifier{BookCreditsModifier: f.s, release: make(chan struct{}), timeout: 2 * time.Second}
	rep := &cbReporter{id: cbTestOpID}
	env := creditsBackfillEnv{store: f.s, mod: mod, opID: rep.id, workers: 4}
	res, err := runCreditsBackfillScan(context.Background(), env, creditsBackfillParams{Apply: true}, rep)
	if err != nil {
		t.Fatal(err)
	}
	if res.BooksToWrite != 4 {
		t.Fatalf("to_write=%d", res.BooksToWrite)
	}
	if got := mod.maxSeen.Load(); got < 2 {
		t.Fatalf("at most %d write(s) in flight at once, want the pool to write concurrently", got)
	}
}

// N3: narrators_created counts the records a successful write credits, not
// every create call: a book whose write was refused after its narrator was
// created does not count (the record is deleted again at the end of the apply,
// since no book credits it and this run made it).
func TestCreditsBackfillReview_NarratorsCreatedCountsOnlySuccessfulWrites(t *testing.T) {
	f := newCBFixture(t)
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	f.book("rv-m1", "Batman: Year One", func(b *database.Book) {
		b.AuthorID = intp(bruce)
		b.Narrator = strp("Harvey Bullock")
	}, ba("rv-m1", [2]int{bruce, 0}), nil)
	mod := &cbInterleavingModifier{BookCreditsModifier: f.s, bookID: "rv-m1", edit: func() {
		if _, err := f.s.ModifyBookAuthors("rv-m1", func(cur []database.BookAuthor) ([]database.BookAuthor, error) {
			return append(cur, database.BookAuthor{BookID: "rv-m1", AuthorID: selina, Role: "author", Position: len(cur)}), nil
		}); err != nil {
			t.Errorf("interleaved edit: %v", err)
		}
	}}
	rep := &cbReporter{id: cbTestOpID}
	env := creditsBackfillEnv{store: f.s, mod: mod, opID: rep.id, workers: 1}
	res, err := runCreditsBackfillScan(context.Background(), env, creditsBackfillParams{Apply: true}, rep)
	if err == nil {
		t.Fatal("a refused write reported a clean run")
	}
	if ns, _ := f.s.ListNarrators(); len(ns) != 0 {
		t.Fatalf("narrators = %d, want the record the refused attempt created deleted again", len(ns))
	}
	if res.NarratorsCreatedUnused != 1 || res.UnusedNarratorsDeleted != 1 {
		t.Fatalf("unused=%d deleted=%d, want the one record reported unused and deleted", res.NarratorsCreatedUnused, res.UnusedNarratorsDeleted)
	}
	if res.NarratorsCreated != 0 {
		t.Fatalf("narrators_created = %d, want 0 (no write landed)", res.NarratorsCreated)
	}
}

// S6: the backfill's prediction of the filing author is the organizer's own
// rule (organizer.PrimaryJoinAuthor, JoinAuthorName, ScalarAuthorName): a
// placeholder or dangling join primary is skipped, a present snapshot decides
// alone (a blank one yields "" and does not fall through to AuthorID, a
// placeholder-named one is used), and ties at the lowest position keep stored
// order. Each case is also checked against the real organizer's
// HasResolvedAuthor.
func TestCreditsBackfillReview_FilingAuthorNameMirrorsTheOrganizer(t *testing.T) {
	f := newCBFixture(t)
	unknown := f.author("Unknown Author")
	bruce := f.author("Bruce Wayne")
	selina := f.author("Selina Kyle")
	dick := f.author("Dick Grayson")
	cases := []struct {
		id    string
		setup func(b *database.Book)
		join  []database.BookAuthor
		want  string
	}{
		{"fn-1", func(b *database.Book) { b.Author = &database.Author{ID: dick, Name: "Dick Grayson"} }, ba("fn-1", [2]int{unknown, 0}), "Dick Grayson"},
		{"fn-2", func(b *database.Book) { b.AuthorID = intp(bruce) }, ba("fn-2", [2]int{unknown, 0}), "Bruce Wayne"},
		{"fn-3", func(b *database.Book) { b.AuthorID = intp(bruce); b.Author = &database.Author{Name: "  "} }, nil, ""},
		{"fn-4", func(b *database.Book) { b.AuthorID = intp(bruce) }, ba("fn-4", [2]int{selina, 0}, [2]int{bruce, 0}), "Selina Kyle"},
		{"fn-5", func(b *database.Book) { b.Author = &database.Author{Name: "Unknown Author"} }, nil, "Unknown Author"},
		{"fn-6", func(b *database.Book) { b.Author = &database.Author{ID: dick, Name: "Dick Grayson"} }, ba("fn-6", [2]int{999999, 0}), "Dick Grayson"},
		{"fn-7", nil, nil, ""},
	}
	for _, c := range cases {
		f.book(c.id, "Batman: "+c.id, c.setup, c.join, nil)
	}
	idx, err := buildCreditCensusIndex(f.s)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		b, err := f.s.GetBookByID(c.id)
		if err != nil || b == nil {
			t.Fatalf("%s: %v", c.id, err)
		}
		join, err := f.s.GetBookAuthors(c.id)
		if err != nil {
			t.Fatal(err)
		}
		got, err := filingAuthorName(idx, join, b.Author, b.AuthorID)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("%s: filingAuthorName = %q, want %q", c.id, got, c.want)
		}
		if resolved := cbResolved(t, f, c.id); resolved != (got != "") {
			t.Errorf("%s: organizer HasResolvedAuthor = %t but filingAuthorName = %q", c.id, resolved, got)
		}
	}
}
