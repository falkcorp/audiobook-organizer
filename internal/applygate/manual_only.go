// file: internal/applygate/manual_only.go
// version: 1.13.0
// guid: a2f62ab5-314e-427a-8ca7-de28de936b75
// last-edited: 2026-10-05

package applygate

import (
	"errors"
	"strconv"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// The owner-manual libraries are Doctor Who, Big Finish and Torchwood. Owner
// rule (standing): these are never touched by a bulk apply or bulk merge --
// the owner applies them manually, by explicit book id. What names them is
// decided in ONE place, internal/franchise (the original pattern plus the
// 2026-10-04 census range terms); this file only decides which fields of a
// book the bulk-apply guard reads.

// FoldUnderscores turns every "_" into a space so a \b pattern sees a word
// boundary there (franchise.Fold). Kept for the callers that fold before
// their own checks.
func FoldUnderscores(s string) string { return franchise.Fold(s) }

// matchesManualOnly reports whether one value names an owner-manual library.
func matchesManualOnly(s string) bool { return franchise.Matches(s) }

// IsOwnerManualOnly reports whether a book with this path or series name
// belongs to a manual-only library and must be left out of every bulk apply or
// bulk merge.
//
// It reads ONE value per argument. A caller deciding whether a whole book is
// owner-manual uses BookManualOnly, which also reads the credits, the
// transcribed fields, the series row, every book_file path and the book's
// franchise tags (owner decision 2026-10-05).
func IsOwnerManualOnly(path, seriesName string) bool {
	return matchesManualOnly(path) || matchesManualOnly(seriesName)
}

// ReasonOwnerManualOnly refuses a bulk apply of a book the owner applies by
// hand (IsOwnerManualOnly). It is a hard reason: no owner-review pin from a
// bulk button lifts it (OwnerReviewOverridable).
const ReasonOwnerManualOnly = "owner_manual_only"

// ReasonOwnerManualCheckFailed refuses a bulk apply whose owner-manual-only
// check could not be completed (a store read of the book's series or files
// failed). It is its own reason, not owner_manual_only, so a report does not
// count a read fault as a Doctor Who book; like owner_manual_only it is hard
// and no bulk owner-review pin lifts it, since reading the fault as "not
// manual-only" would loosen the rule.
const ReasonOwnerManualCheckFailed = "owner_manual_check_failed"

// ManualOnlyGuard is the bulk-apply input to the owner-manual-only check in
// EvaluateTranscribed. The zero value is a single-book caller (the apply
// dialog, one review row the owner approved, metadata.upgrade which checks
// its own way): no check.
type ManualOnlyGuard struct {
	// Bulk is set by a caller applying without a human choosing this one book
	// (a bulk apply, including the review page's bulk buttons).
	Bulk bool
	// StoreDetail is the caller's store-backed finding ("" = none): what
	// BulkManualOnlyGuard's reads matched (the transcribed fields, narrator,
	// publisher, franchise tags, author credits, series name or a book_file
	// path or its transcribed fields).
	StoreDetail string
	// ReadErr is set instead of StoreDetail when the caller's store read
	// failed: the check could not be done, and the gate refuses with
	// ReasonOwnerManualCheckFailed.
	ReadErr string
}

// ManualOnlyFilesReader reads a book's book_file rows for BulkManualOnlyGuard.
type ManualOnlyFilesReader interface {
	GetBookFiles(bookID string) ([]database.BookFile, error)
}

// ManualOnlySeriesReader reads a book's series row for BulkManualOnlyGuard.
type ManualOnlySeriesReader interface {
	GetSeriesByID(id int) (*database.Series, error)
}

// ManualOnlyTagReader reads a book's tag rows for BulkManualOnlyGuard: a
// franchise: tag (the tag-franchise fixer's, or a person's) holds the book
// whatever its title or path says now.
type ManualOnlyTagReader interface {
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
}

// ManualOnlyReaders are the store reads BulkManualOnlyGuard makes. Files is
// required. Series, Authors and Tags may be nil for a caller whose store
// cannot read them; that part of the check is then skipped and the caller
// must re-check where it can (the server's apply re-runs the guard with
// every reader before it writes). Every production caller passes all four.
type ManualOnlyReaders struct {
	Files   ManualOnlyFilesReader
	Series  ManualOnlySeriesReader
	Authors database.BookAuthorReader
	Tags    ManualOnlyTagReader
}

// manualOnlyWhy prefixes every owner_manual_only detail.
const manualOnlyWhy = "Doctor Who / Big Finish / Torchwood are applied by hand, one book at a time; "

// BulkManualOnlyGuard builds the ManualOnlyGuard for a bulk caller: the
// store-backed half of the check, which EvaluateTranscribed cannot do itself
// -- the query the candidate was found by, the book's series name and every
// book_file path. A read failure goes in ReadErr, which the gate refuses as
// owner_manual_check_failed; it is never read as "not manual-only".
//
// series may be nil for a caller whose store cannot read series rows; the
// series is then not checked, and that caller must re-check where it can (the
// auto-match-transcribed op's pre-check does, and the server's apply re-runs
// the guard with both readers before it writes).
func BulkManualOnlyGuard(r ManualOnlyReaders, book *database.Book, searchQuery string) ManualOnlyGuard {
	files, series := r.Files, r.Series
	g := ManualOnlyGuard{Bulk: true}
	if IsOwnerManualOnly(searchQuery, "") {
		g.StoreDetail = manualOnlyWhy + "search query " + strconv.Quote(searchQuery)
		return g
	}
	if book == nil {
		g.ReadErr = "no book to check for the owner-manual rule"
		return g
	}
	// The stand-ins the resolver searches a blank or unsearchable title by,
	// checked here whatever the resolver decided. searchQuery is empty when the
	// resolver SKIPS the row (a part row, or a row whose import root it could
	// not read and so listed as siblings), and a blank-titled Big Finish file
	// whose intro transcription ("Doctor Who: The Chimes of Midnight") is the
	// only signal would otherwise reach the gate with nothing to match. The
	// folder name needs no stand-in check: it is part of every path checked
	// below and in ManualOnlyDetail.
	if t := book.TranscribedTitle; t != nil && IsOwnerManualOnly(*t, "") {
		g.StoreDetail = manualOnlyWhy + "transcribed title " + strconv.Quote(*t)
		return g
	}
	// An intro naming the producer ("Big Finish Productions presents...")
	// often lands in the transcribed author, not the title.
	if a := book.TranscribedAuthor; a != nil && IsOwnerManualOnly(*a, "") {
		g.StoreDetail = manualOnlyWhy + "transcribed author " + strconv.Quote(*a)
		return g
	}
	if series != nil && book.SeriesID != nil {
		sr, err := series.GetSeriesByID(*book.SeriesID)
		switch {
		case err != nil:
			g.ReadErr = "could not read the series for the owner-manual check: " + err.Error()
			return g
		case sr != nil && IsOwnerManualOnly("", sr.Name):
			g.StoreDetail = manualOnlyWhy + "series " + strconv.Quote(sr.Name)
			return g
		}
	}
	// The narrator and publisher fields (no store read): the album and the
	// studio of an iTunes-imported Big Finish book live there.
	// A credit named only by "Missy" does not count here (owner decision
	// 2026-10-05; franchise.MatchesCreditStrong).
	for _, ch := range []struct{ what, v string }{{"narrator", strDeref(book.Narrator)}, {"publisher", strDeref(book.Publisher)}} {
		if franchise.MatchesCreditStrong(ch.v) {
			g.StoreDetail = manualOnlyWhy + ch.what + " " + strconv.Quote(ch.v)
			return g
		}
	}
	// The book's tags: a franchise: tag holds it, whatever its title or
	// path says now.
	if r.Tags != nil {
		tags, err := r.Tags.GetBookTagsDetailed(book.ID)
		if err != nil {
			g.ReadErr = "could not read the tags for the owner-manual check: " + err.Error()
			return g
		}
		for _, t := range tags {
			if tag, ok := franchise.HeldByTags([]string{t.Tag}); ok {
				g.StoreDetail = manualOnlyWhy + "tag " + strconv.Quote(tag)
				return g
			}
		}
	}
	// Its author credits: iTunes-imported Big Finish books carry "Big
	// Finish Productions" as the author while path and title are junk.
	if r.Authors != nil {
		names, err := database.LiveBookAuthorNames(r.Authors, book)
		if err != nil {
			g.ReadErr = "could not read the authors for the owner-manual check: " + err.Error()
			return g
		}
		for _, n := range names {
			if matchesManualOnly(n) {
				g.StoreDetail = manualOnlyWhy + "author " + strconv.Quote(n)
				return g
			}
		}
	}
	bookFiles, err := files.GetBookFiles(book.ID)
	if err != nil {
		g.ReadErr = "could not read the files for the owner-manual check: " + err.Error()
		return g
	}
	for _, f := range bookFiles {
		if IsOwnerManualOnly(f.FilePath, "") {
			g.StoreDetail = manualOnlyWhy + "file " + strconv.Quote(f.FilePath)
			return g
		}
		if t := f.TranscribedTitle; t != nil && IsOwnerManualOnly(*t, "") {
			g.StoreDetail = manualOnlyWhy + "file transcribed title " + strconv.Quote(*t)
			return g
		}
		if a := f.TranscribedAuthor; a != nil && IsOwnerManualOnly(*a, "") {
			g.StoreDetail = manualOnlyWhy + "file transcribed author " + strconv.Quote(*a)
			return g
		}
	}
	return g
}

// manualOnlyCheck is one value ManualOnlyDetail matches. credit marks a
// book credit the guard began reading on 2026-10-04 (narrator, publisher),
// where "Missy" alone does not count (franchise.MatchesCreditStrong).
type manualOnlyCheck struct {
	what, value string
	credit      bool
}

// ManualOnlyDetail reports why a bulk apply of candidate c onto book must be
// refused, as a reason and detail, or two "" when nothing marks it. The
// reason is ReasonOwnerManualCheckFailed when the caller's store read failed
// (g.ReadErr: the check could not be done) and ReasonOwnerManualOnly when
// something names the library. It checks
// everything the gate holds without a store read: the book's path and title,
// the query it was found by (ts.Query: a blank-titled Big Finish book whose
// intro says "Doctor Who: The Chimes of Midnight"), and the candidate's own
// title, series, publisher and author (Audible answering with a Doctor Who
// record, which may say so only in its publisher). The caller's
// store-backed finding (series row, book_file paths) comes in g.StoreDetail.
func ManualOnlyDetail(book *database.Book, c *metafetch.MetadataCandidate, ts TranscribedSearch, g ManualOnlyGuard) (reason, detail string) {
	if !g.Bulk {
		return "", ""
	}
	if g.ReadErr != "" {
		return ReasonOwnerManualCheckFailed, g.ReadErr
	}
	if g.StoreDetail != "" {
		return ReasonOwnerManualOnly, g.StoreDetail
	}
	checks := []manualOnlyCheck{
		{what: "path", value: book.FilePath},
		{what: "title", value: book.Title},
		{what: "search query", value: ts.Query},
		// iTunes-imported Big Finish books keep the album in the narrator
		// ("Stargate SG-1 - Series 2", "The War Master - Series 12") and
		// the studio in the narrator or publisher.
		{what: "narrator", value: strDeref(book.Narrator), credit: true},
		{what: "publisher", value: strDeref(book.Publisher), credit: true},
	}
	if c != nil {
		checks = append(checks,
			manualOnlyCheck{what: "candidate title", value: c.Title},
			manualOnlyCheck{what: "candidate series", value: c.Series},
			// A Big Finish record often carries its franchise ONLY in the
			// publisher ("The Chimes of Midnight", publisher "Big Finish
			// Productions", no series): a blank-titled rip outside any
			// franchise folder had nothing else to match.
			manualOnlyCheck{what: "candidate publisher", value: c.Publisher},
			manualOnlyCheck{what: "candidate author", value: c.Author},
		)
	}
	for _, ch := range checks {
		match := matchesManualOnly
		if ch.credit {
			// Owner decision 2026-10-05: "Missy" alone in a narrator or
			// publisher credit does not hold a book.
			match = franchise.MatchesCreditStrong
		}
		if match(ch.value) {
			return ReasonOwnerManualOnly, manualOnlyWhy +
				ch.what + " " + strconv.Quote(ch.value)
		}
	}
	return "", ""
}

func strDeref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// bookRowManualOnly is the owner-manual check on everything a book ROW
// carries, with no store read: its path, title, series name, narrator and
// publisher (where "Missy" alone does not count, as in BulkManualOnlyGuard)
// and its transcribed title and author. It returns what matched ("" = none).
//
// It is unexported on purpose (owner decision 2026-10-05). It used to be the
// exported BookRowManualOnly, and four whole-library ops decided from it
// alone, so a Big Finish book whose only signal was a book_file path, an
// author credit or a franchise: tag slipped through them. Its one caller is
// BookManualOnly, which adds those reads; a caller outside this package that
// wants "is this book owner-manual" uses BookManualOnly.
func bookRowManualOnly(core *database.BookCore, seriesName string) string {
	if core == nil {
		return ""
	}
	for _, ch := range []manualOnlyCheck{
		{what: "path", value: core.FilePath},
		{what: "series", value: seriesName},
		{what: "title", value: core.Title},
		{what: "narrator", value: strDeref(core.Narrator), credit: true},
		{what: "publisher", value: strDeref(core.Publisher), credit: true},
		{what: "transcribed title", value: strDeref(core.TranscribedTitle)},
		{what: "transcribed author", value: strDeref(core.TranscribedAuthor)},
	} {
		match := matchesManualOnly
		if ch.credit {
			match = franchise.MatchesCreditStrong
		}
		if match(ch.value) {
			return ch.what + " " + strconv.Quote(ch.value)
		}
	}
	return ""
}

// BookManualOnly is the whole-book owner-manual check for a caller that
// decides OUTSIDE the metadata apply gate which books it may touch (a
// maintenance op choosing what to merge, move, link or demote). held is true
// when the book is Doctor Who / Big Finish / Torchwood by anything it
// carries: the row itself (path, title, narrator, publisher, transcribed
// fields) or anything BulkManualOnlyGuard reads (series row, franchise tags,
// author credits, every book_file path and its transcribed fields). detail
// says what matched.
//
// err is set when one of those reads failed: the check could not be done.
// The caller must fail closed -- leave the book alone -- and must not count
// it as an owner-manual book (the ReasonOwnerManualCheckFailed split).
//
// The row part runs first and needs no read, so a book held by its row
// costs nothing more than the row-only check did.
func BookManualOnly(r ManualOnlyReaders, book *database.Book) (held bool, detail string, err error) {
	if book == nil {
		return false, "", errors.New("no book to check for the owner-manual rule")
	}
	core := book.Core()
	if d := bookRowManualOnly(&core, ""); d != "" {
		return true, manualOnlyWhy + d, nil
	}
	if r.Files == nil {
		return false, "", errors.New("no book_file reader for the owner-manual check")
	}
	g := BulkManualOnlyGuard(r, book, "")
	if g.ReadErr != "" {
		return false, "", errors.New(g.ReadErr)
	}
	if g.StoreDetail != "" {
		return true, g.StoreDetail, nil
	}
	return false, "", nil
}
