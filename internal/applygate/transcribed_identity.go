// file: internal/applygate/transcribed_identity.go
// version: 1.5.0
// guid: fedfaa92-fca3-4c73-b38b-25f4b0426918
// last-edited: 2026-09-29

package applygate

import (
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// TranscribedSearch tells the gate that a candidate was found by searching
// the book's transcribed (audio intro) title rather than its stored title.
// The batch candidate fetch does that for a book whose stored title is empty,
// a placeholder or a chapter number (metabatch.ResolveCandidateSearchQuery),
// and the gate then has nothing on the book row to confirm the candidate by:
// the title check compares against "" or "Chapter 3" and refuses, and a
// cache row hashed with the transcribed query reads as identity_stale.
//
// The caller fills it only from facts it holds, never from the candidate:
// the cached-apply path from the cache row's hash
// (metafetch.Service.CachedQueryMatchesIdentity), the op-result path from the
// query the fetch recorded (CandidateResult.SearchQuery/SearchQuerySource).
type TranscribedSearch struct {
	// Query is the transcribed title the provider search asked. Empty: the
	// candidate was not found by a transcribed-title search.
	Query string
	// Author is the author heard in the same transcription as Query
	// (metabatch.CandidateSearchQuery.Author: the book's or its first file's
	// TranscribedAuthor), "" when none was heard. A candidate must match it as
	// well as the title (TranscribedSearchConfirms).
	Author string
	// Source is where Query came from (a metabatch.SearchQuerySource* value),
	// carried into the recorded evidence.
	Source string
	// ExplainsStaleIdentity: the caller proved the candidate's cache row was
	// fetched for (Query, the book's current author) -- it differs from the
	// book's own identity in the query ONLY. Without that proof an
	// identity_stale error is never lifted.
	ExplainsStaleIdentity bool
	// FirstFilePath is the path of the book's first present file in play
	// order (metabatch.FirstPresentFilePath), "" when the caller has none.
	// The lift refusal reads the book's work folder from it; without it the
	// book's own FilePath is used, which for a multi-file book is a
	// directory (metadata.WorkFolderTitle handles both).
	FirstFilePath string
}

// TranscribedSearchConfirms reports whether candidate c matches the
// transcription it was found by, title AND author. It is the whole rule the
// unattended score leg uses (TranscriptionConfirms), applied to ts instead of
// the book's own transcribed fields: origin/main's util.MainTranscriptionConfirms
// (normalized title equality; a heard author longer than 3 characters must
// appear in the candidate's author) AND the shared title matcher
// (util.TitleAgrees with the candidate's series position), so it is never
// looser than main. A provider's fuzzy answer to the query -- "Planet Hulk:
// Gladiator" for "Planet Hulk" -- does not match, and neither does the right
// title by another author ("X, by A" heard, candidate "X" by B).
func TranscribedSearchConfirms(ts TranscribedSearch, c *metafetch.MetadataCandidate) bool {
	q := strings.TrimSpace(ts.Query)
	if c == nil || q == "" || strings.TrimSpace(c.Title) == "" {
		return false
	}
	if !util.MainTranscriptionConfirms(c.Title, c.Author, q, strings.TrimSpace(ts.Author)) {
		return false
	}
	return util.TitleAgrees(c.Title, c.SeriesPosition, q)
}

// TranscribedIdentityLifts reports whether ts lifts an identity_stale error
// for candidate c on book: the caller proved the cache row differs from the
// book in its query only (ts.ExplainsStaleIdentity), c matches the
// transcription (TranscribedSearchConfirms), and nothing on the book
// contradicts it (transcribedLiftRefusal). It is exactly the identity lift
// EvaluateTranscribed applies, exported for a caller that checks the cache
// identity without running the whole gate (the auto-match-transcribed apply),
// so the two can never disagree about a row.
func TranscribedIdentityLifts(book *database.Book, authors Authors, c *metafetch.MetadataCandidate, ts TranscribedSearch) bool {
	if !ts.ExplainsStaleIdentity || strings.TrimSpace(ts.Query) == "" || !TranscribedSearchConfirms(ts, c) {
		return false
	}
	return transcribedLiftRefusal(book, authors, c, ts) == ""
}

// applyTranscribedTitle revises the evidence leg's title check for a
// candidate whose title matches the transcribed title it was found by, and
// reports whether it changed anything. A title check that already agreed is
// left alone (the stored title vouched on its own).
//
// Otherwise the block is lifted, but the match is counted as an agreement
// only when the transcription check has not already counted it
// (audioConfirmed: the book-level transcribed title confirmed the candidate).
// It is ONE fact -- the audio says this title -- and counting it twice would
// let a placeholder book with no runtime, author or ASIN evidence pass
// MinAgreements on the transcription alone. A file-level transcription does
// not reach the transcription check, so there the title check carries it.
//
// Two blocks are never lifted (transcribedLiftRefusal): a candidate whose
// title is the series name, and a book whose folder names a different work
// than the transcription. The first is the title check's hard block and an
// intro announcing "Discworld" names no volume; the second is evidence
// against the transcription, not a missing title.
//
// refusal is transcribedLiftRefusal's answer; when set, a block is annotated
// with it and nothing is lifted.
func applyTranscribedTitle(ev *EvidenceVerdict, query, refusal string, audioConfirmed bool) bool {
	for i := range ev.Checks {
		ch := &ev.Checks[i]
		if ch.Name != "title" || ch.Outcome == OutcomeAgree {
			continue
		}
		if refusal != "" {
			if ch.Outcome == OutcomeBlock {
				ch.Detail += "; not lifted by the transcribed title " + strconv.Quote(query) + ": " + refusal
			}
			return false
		}
		detail := "matches the transcribed title " + strconv.Quote(query) + " it was found by"
		if ch.Detail != "" {
			detail += " (stored title and path: " + ch.Detail + ")"
		}
		ch.Reason = ""
		if audioConfirmed {
			ch.Outcome = OutcomeNeutral
			ch.Detail = detail + "; counted once, under transcription"
		} else {
			ch.Outcome = OutcomeAgree
			ch.Detail = detail
		}
		ev.tally()
		return true
	}
	return false
}

// transcribedLiftRefusal returns why a transcribed-title match may not lift
// the title check, or "":
//
//   - the candidate's title is a series name (candidateTitleIsSeriesName):
//     "Discworld" heard and "Discworld" (series Discworld) returned;
//   - the book's work folder names a different work: an intro heard as
//     "Guards! Guards!" in ".../Terry Pratchett/Mort/" is a mis-filed file or
//     a mis-heard intro, and the gate cannot tell which. A folder that says
//     nothing (a placeholder or chapter folder, metadata.IsUnsearchableTitle)
//     or names one of the book's authors is no evidence either way.
//
// The work folder is metadata.WorkFolderTitle of ts.FirstFilePath when the
// caller read it, else of the book's own FilePath. A multi-file book's
// FilePath is the work folder itself (".../Terry Pratchett/Mort"); read as a
// file path it named "Terry Pratchett" or nothing, and the refusal never
// fired for it.
func transcribedLiftRefusal(book *database.Book, authors Authors, c *metafetch.MetadataCandidate, ts TranscribedSearch) string {
	if candidateTitleIsSeriesName(book, c) {
		return "the candidate's title " + strconv.Quote(c.Title) + " is the series name"
	}
	path := strings.TrimSpace(ts.FirstFilePath)
	if path == "" && book != nil {
		path = book.FilePath
	}
	folder, ok := metadata.WorkFolderTitle(path)
	if !ok || metadata.IsUnsearchableTitle(folder) {
		return ""
	}
	query := ts.Query
	for _, a := range authors {
		// The same person test as metabatch's heading check
		// (authorjunk.SamePersonName): "Roiphe, Anne", "J.R.R. Tolkien".
		if authorjunk.SamePersonName(a, folder) {
			return ""
		}
	}
	if titleSim(query, folder) < 0.5 {
		return "the book's folder names " + strconv.Quote(folder)
	}
	return ""
}
