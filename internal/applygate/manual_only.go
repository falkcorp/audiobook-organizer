// file: internal/applygate/manual_only.go
// version: 1.5.0
// guid: a2f62ab5-314e-427a-8ca7-de28de936b75
// last-edited: 2026-09-29

package applygate

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
)

// manualOnlyRe matches the libraries the owner curates by hand: Doctor Who,
// Big Finish and Torchwood. Owner rule (standing): these are never touched by
// a bulk apply or bulk merge -- the owner applies them manually, by explicit
// book id. Separators between words vary across rips ("Doctor.Who",
// "Doctor_Who", "DoctorWho"), so any run of separators, or none, is accepted.
// Match it through matchesManualOnly, never directly: see FoldUnderscores.
//
// The Doctor ranges count too, not only the literal "Doctor Who": Big Finish
// sells "The Thirteenth Doctor Adventures", "The War Doctor", "The Fugitive
// Doctor" and "The 13th Doctor" series whose names never say "Doctor Who",
// and on 2026-09-29 a junk-author trial would have minted "The Thirteenth
// Doctor Adventures" as an author for them. An ordinal ("First" ..
// "Fifteenth", or "1st" .. "15th"), "War" or "Fugitive" directly before
// "Doctor" is one. "War Doctor" also names a surgeon's memoir; the check
// fails toward holding a row, as every owner-manual guard does. A bare
// "Doctor" is not ("Doctor Sleep", "Doctors Orders"), and neither is "The
// Doctor's Wife": it is a Doctor Who episode title, but also a novel, and a
// title with no range, franchise or studio word names neither.
var manualOnlyRe = regexp.MustCompile(`(?i)\b(doctor[\s._-]*who|big[\s._-]*finish|torchwood|` +
	`(?:first|second|third|fourth|fifth|sixth|seventh|eighth|ninth|tenth|eleventh|twelfth|thirteenth|fourteenth|fifteenth|` +
	`[1-9](?:st|nd|rd|th)|1[0-5]th|war|fugitive)[\s._-]*doctor)\b`)

// FoldUnderscores turns every "_" into a space so a \b pattern sees a word
// boundary there. "_" is a regexp word character, so \b never fires next to
// it, and the organizer writes a colon as "_ " in folder names: "Doctor Who_
// Mindwarp" escaped every manual-only guard until this fold. Every
// owner-manual pattern (here and in repairs' title guard) matches the folded
// text.
func FoldUnderscores(s string) string {
	return strings.ReplaceAll(s, "_", " ")
}

// matchesManualOnly is manualOnlyRe on the folded text.
func matchesManualOnly(s string) bool {
	return manualOnlyRe.MatchString(FoldUnderscores(s))
}

// IsOwnerManualOnly reports whether a book with this path or series name
// belongs to a manual-only library and must be left out of every bulk apply or
// bulk merge.
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
	// StoreDetail is the caller's store-backed finding ("" = none): what its
	// read of the book's series name and every book_file path matched.
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
func BulkManualOnlyGuard(files ManualOnlyFilesReader, series ManualOnlySeriesReader, book *database.Book, searchQuery string) ManualOnlyGuard {
	g := ManualOnlyGuard{Bulk: true}
	if IsOwnerManualOnly(searchQuery, "") {
		g.StoreDetail = manualOnlyWhy + "search query " + strconv.Quote(searchQuery)
		return g
	}
	if book == nil {
		g.ReadErr = "no book to check for the owner-manual rule"
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
	}
	return g
}

// ManualOnlyDetail reports why a bulk apply of candidate c onto book must be
// refused, as a reason and detail, or two "" when nothing marks it. The
// reason is ReasonOwnerManualCheckFailed when the caller's store read failed
// (g.ReadErr: the check could not be done) and ReasonOwnerManualOnly when
// something names the library. It checks
// everything the gate holds without a store read: the book's path and title,
// the query it was found by (ts.Query: a blank-titled Big Finish book whose
// intro says "Doctor Who: The Chimes of Midnight"), and the candidate's own
// title and series (Audible answering with a Doctor Who record). The caller's
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
	checks := []struct{ what, value string }{
		{"path", book.FilePath},
		{"title", book.Title},
		{"search query", ts.Query},
	}
	if c != nil {
		checks = append(checks,
			struct{ what, value string }{"candidate title", c.Title},
			struct{ what, value string }{"candidate series", c.Series},
		)
	}
	for _, ch := range checks {
		if matchesManualOnly(ch.value) {
			return ReasonOwnerManualOnly, manualOnlyWhy +
				ch.what + " " + strconv.Quote(ch.value)
		}
	}
	return "", ""
}
