// file: internal/plugins/maintenance/manual_only_full_guard_test.go
// version: 1.0.0
// guid: b949bbe2-b0d3-47ca-8f94-753f51e62844
// last-edited: 2026-10-05

package maintenance

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
)

// Owner decision 2026-10-05: every op that decides which books it may touch
// runs the whole-book owner-manual check (applygate.BookManualOnly), not the
// row-only one. Each test below gives a book a clean path, title, series,
// narrator and publisher, and puts the Doctor Who / Big Finish signal ONLY
// on something the row does not carry: a book_file path, an author credit,
// or a franchise: tag.

// noManualReads is a ManualOnlyReaders source for a book with nothing beyond
// its row: no files, credits, tags or series.
type noManualReads struct{}

func (noManualReads) GetBookFiles(string) ([]database.BookFile, error)       { return nil, nil }
func (noManualReads) GetSeriesByID(int) (*database.Series, error)            { return nil, nil }
func (noManualReads) GetBookAuthors(string) ([]database.BookAuthor, error)   { return nil, nil }
func (noManualReads) GetAuthorByID(int) (*database.Author, error)            { return nil, nil }
func (noManualReads) GetBookTagsDetailed(string) ([]database.BookTag, error) { return nil, nil }

func noManualReaders() applygate.ManualOnlyReaders {
	r := noManualReads{}
	return applygate.ManualOnlyReaders{Files: r, Series: r, Authors: r, Tags: r}
}

// author-path-link: a book with no author under a folder naming an existing
// author would link; with the signal on its files, credits or tags it is
// owner_manual_only, and a failed read is failed (never linked, never
// counted as owner-manual).
func TestAuthorPathLink_OwnerManualOffTheRow(t *testing.T) {
	f := &pathLinkFixture{}
	f.author(1, "Charles Dickens")
	f.author(9, "Big Finish Productions")
	f.filler(1, 4)
	path := func(id string) string {
		return "/mnt/bigdata/books/audiobook-organizer/Charles Dickens/" + id + "/x.m4b"
	}
	for _, id := range []string{"clean", "file", "credit", "tag", "readfail"} {
		f.book(id, path(id), nil)
	}
	s := f.store(t)
	s.GetBookFilesFunc = func(bookID string) ([]database.BookFile, error) {
		switch bookID {
		case "file":
			return []database.BookFile{{BookID: bookID, FilePath: "/mnt/bigdata/books/Big Finish/Dalek Empire/01.mp3"}}, nil
		case "readfail":
			return nil, errors.New("simulated file read failure")
		}
		return []database.BookFile{{BookID: bookID, FilePath: path(bookID)}}, nil
	}
	s.GetBookAuthorsFunc = func(bookID string) ([]database.BookAuthor, error) {
		if bookID == "credit" {
			return []database.BookAuthor{{BookID: bookID, AuthorID: 9}}, nil
		}
		return nil, nil
	}
	s.GetBookTagsDetailedFunc = func(bookID string) ([]database.BookTag, error) {
		if bookID == "tag" {
			return []database.BookTag{{BookID: bookID, Tag: franchise.Tags(franchise.BigFinish, "")[0]}}, nil
		}
		return nil, nil
	}

	res := runPathLink(t, New(&fakeDeps{store: s}), `{"dry_run":true}`)
	require.Equal(t, authorPathLinkWouldLink, outcomeOf(t, res, "clean").Outcome, "control: a clean book links")
	for _, id := range []string{"file", "credit", "tag"} {
		_, listed := findChange(res, id)
		require.False(t, listed, "book %s: owner_manual_only is counter-only, but it was listed", id)
	}
	require.Equal(t, 3, res.Outcomes[authorPathLinkOwnerManual], "file, credit and tag books held (outcomes=%v)", res.Outcomes)
	fail := outcomeOf(t, res, "readfail")
	require.Equal(t, authorPathLinkFailed, fail.Outcome)
	require.Contains(t, fail.Error, "owner-manual check")
}

// The classifier itself, for a caller with no store at hand: noManualReaders
// leaves the row as the only evidence, so a clean row still classifies.
func TestAuthorPathLinkClassify_RowStillHolds(t *testing.T) {
	idx := &authorPathLinkIndex{
		byNormalized: map[string]database.Author{}, bookCount: map[int]int{}, ownBookCount: map[int]int{},
		byLength: map[int][]string{}, titleFragment: map[string]bool{},
	}
	ch := authorPathLinkClassify(&database.BookCore{ID: "b", FilePath: "/lib/Jane Doe/Doctor Who and the Cybermen/x.m4b"}, idx, noManualReaders())
	require.Equal(t, authorPathLinkOwnerManual, ch.Outcome)
}

// itunes.clone-into-library: an organized iTunes-only book with a clean title
// and path is skipped as owner_manual_only when a tag, an author credit or a
// file's transcribed title names the library.
func TestITunesClone_OwnerManualOffTheRow(t *testing.T) {
	f := newICFixture(t)
	f.book(t, "T", "vg-t", "Book T", []string{f.itunes("Book T.m4b")})
	require.NoError(t, f.s.AddBookTag("T", franchise.Tags(franchise.BigFinish, "")[0]))

	f.book(t, "C", "vg-c", "Book C", []string{f.itunes("Book C.m4b")})
	bf, err := f.s.CreateAuthor("Big Finish Productions")
	require.NoError(t, err)
	require.NoError(t, f.s.SetBookAuthors("C", []database.BookAuthor{{BookID: "C", AuthorID: bf.ID}}))

	f.book(t, "F", "vg-f", "Book F", []string{f.itunes("Book F.m4b")})
	files, err := f.s.GetBookFiles("F")
	require.NoError(t, err)
	require.Len(t, files, 1)
	dw := "Doctor Who: The Chimes of Midnight"
	files[0].TranscribedTitle = &dw
	require.NoError(t, f.s.UpdateBookFile(files[0].ID, &files[0]))

	// Control: the same shape with nothing naming the library is cloned.
	f.book(t, "K", "vg-k", "Book K", []string{f.itunes("Book K.m4b")})

	rep := f.run(t, icParams{GroupIDs: []string{"vg-t", "vg-c", "vg-f", "vg-k"}})
	for _, gid := range []string{"vg-t", "vg-c", "vg-f"} {
		g := icGroup(t, rep, gid)
		require.Equal(t, icDecisionSkip, g.Decision, "%s: %+v", gid, g)
		require.Equal(t, applygate.ReasonOwnerManualOnly, g.Reason, "%s: %+v", gid, g)
	}
	require.NotEqual(t, applygate.ReasonOwnerManualOnly, icGroup(t, rep, "vg-k").Reason, "control group must not be held")
}

// author-strip-merge's title-as-author relink: with agreeing evidence both
// books would be relinked; one held only by a franchise: tag and one held
// only by a file's transcribed title are skipped as owner-manual instead.
func TestAuthorStripMerge_RelinkOwnerManualOffTheRow(t *testing.T) {
	f := newRelinkFixture()
	f.fetched["bk-t"] = "T.J. Ward"
	f.fetched["bk-s"] = "T.J. Ward"
	f.tags = map[string][]database.BookTag{"bk-t": {{BookID: "bk-t", Tag: franchise.Tags(franchise.BigFinish, "")[0]}}}
	dw := "Doctor Who: The Chimes of Midnight"
	f.files["bk-s"] = []database.BookFile{{ID: "f1", BookID: "bk-s", FilePath: "/lib/01 Arcane Chef 2/Arcane Chef 2/01.m4b", TranscribedTitle: &dw}}
	calls, summary := f.run(t, `{"apply":true,"relink_title_as_author":true,"delete_title_as_author":true}`)
	if len(calls.setAuthors["bk-t"]) != 0 || len(calls.setAuthors["bk-s"]) != 0 {
		t.Errorf("owner-manual books were rewritten: %v", calls.setAuthors)
	}
	wantSummary(t, summary, "relink-skipped-owner-manual=2 ", "relinked=0 ")
}
