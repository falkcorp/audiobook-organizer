// file: internal/applygate/transcribed_identity.go
// version: 1.0.0
// guid: fedfaa92-fca3-4c73-b38b-25f4b0426918
// last-edited: 2026-09-28

package applygate

import (
	"strconv"
	"strings"

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
	// Source is where Query came from (a metabatch.SearchQuerySource* value),
	// carried into the recorded evidence.
	Source string
	// ExplainsStaleIdentity: the caller proved the candidate's cache row was
	// fetched for (Query, the book's current author) -- it differs from the
	// book's own identity in the query ONLY. Without that proof an
	// identity_stale error is never lifted.
	ExplainsStaleIdentity bool
}

// TranscribedTitleMatches reports whether candidate c's title matches the
// transcribed title it was found by. It is the title half of the rule the
// unattended score leg already uses (TranscriptionConfirms): normalized
// equality, as origin/main required (util.MainTranscriptionConfirms), AND the
// shared matcher (util.TitleAgrees with the candidate's series position), so
// it is never looser than main. A provider's fuzzy answer to the query --
// "Planet Hulk: Gladiator" for "Planet Hulk" -- does not match.
func TranscribedTitleMatches(query string, c *metafetch.MetadataCandidate) bool {
	q := strings.TrimSpace(query)
	if c == nil || q == "" || strings.TrimSpace(c.Title) == "" {
		return false
	}
	if util.NormalizeTitle(c.Title) != util.NormalizeTitle(q) {
		return false
	}
	return util.TitleAgrees(c.Title, c.SeriesPosition, q)
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
func applyTranscribedTitle(ev *EvidenceVerdict, query string, audioConfirmed bool) bool {
	for i := range ev.Checks {
		ch := &ev.Checks[i]
		if ch.Name != "title" || ch.Outcome == OutcomeAgree {
			continue
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
