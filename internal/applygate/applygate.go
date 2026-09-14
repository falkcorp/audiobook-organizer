// file: internal/applygate/applygate.go
// version: 1.3.0
// guid: 2f8d4a61-0c3b-4e7a-9d52-b6e1f3a08c47
// last-edited: 2026-09-13

// Package applygate is the certainty gate every BULK metadata apply consults
// before it writes a candidate onto a book: the cached batch apply
// (metadata.batch-apply-cached), the op-results batch apply
// (/metadata/batch-apply-candidates) and the metadata auto-upgrade.
//
// Why it exists. Bulk apply took the top-scored candidate with no score floor
// and no identity check, and the owner watched it put "Big Cats 3" onto the
// file for "Big Cats 1" more than once. An apply overwrites series, position,
// ISBN, ASIN, cover and description, and with auto_rename_on_apply and
// auto_write_tags_on_apply on it also retags and moves the files, so one wrong
// match does damage in three places. A book that fails this gate is NOT
// applied; it is reported with a reason so it goes to manual review.
//
// The gate has four legs, all required:
//
//  1. score: >= MinScore, or >= MinScoreAudioConfirmed when the book's
//     transcribed (audio-derived) title/author independently confirm the
//     candidate. These are the auto-upgrade thresholds, moved here from
//     internal/metabatch so both paths share one number. As in auto-upgrade, a
//     book WITH a transcribed title whose candidate does not match it is
//     refused outright, whatever the score.
//  2. identity: the caller's staleness check (metafetch.ValidateCachedIdentity
//     for cache-backed applies) passed. The caller runs it and hands in the
//     error, because only the caller knows where the candidate came from.
//  3. sequence: CheckSequence — the book's volume number and the candidate's
//     must agree.
//  4. evidence: CheckEvidence (evidence.go) — no hard contradiction from the
//     runtime, author-role, author-vs-path, title, narrator, ASIN,
//     cast-in-author (cast.go), series-number-lost (booknum.go) or
//     partial-book (partial.go) checks, and at least MinAgreements
//     independent signals positively agree. The last three only ever block.
//
// It must never be imported by internal/metafetch: the gate wraps the apply
// from outside, it is not part of ApplyMetadataCandidate (the single-book
// manual apply in the UI is the user choosing, and is deliberately ungated).
package applygate

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/seqnum"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// MinScore is the minimum candidate score for an unattended apply.
const MinScore = 0.90

// MinScoreAudioConfirmed relaxes MinScore when the candidate independently
// matches the book's audio-derived title/author.
const MinScoreAudioConfirmed = 0.85

// Reason vocabulary. Stable strings: they are counted in dry-run summaries and
// op logs.
const (
	ReasonIdentityStale              = "identity_stale"
	ReasonTranscriptionMismatch      = "transcription_mismatch"
	ReasonScoreBelowFloor            = "score_below_floor"
	ReasonSequenceMismatch           = "sequence_mismatch"
	ReasonSequenceMissingOnCandidate = "sequence_missing_on_candidate"
	ReasonBookSequenceConflict       = "book_sequence_conflict"
	ReasonCandidateSequenceConflict  = "candidate_sequence_conflict"
)

// SourceNumber is one number found in one place.
type SourceNumber struct {
	Source string `json:"source"` // series_position | title | subtitle | file_name | folder_name
	Number string `json:"number"`
	Form   string `json:"form"`
}

// SequenceVerdict is the result of CheckSequence.
type SequenceVerdict struct {
	Pass             bool           `json:"pass"`
	Reason           string         `json:"reason,omitempty"`
	Detail           string         `json:"detail,omitempty"`
	BookNumbers      []SourceNumber `json:"book_numbers,omitempty"`
	CandidateNumbers []SourceNumber `json:"candidate_numbers,omitempty"`
}

// Verdict is the whole gate's decision for one book/candidate pair.
type Verdict struct {
	Allowed        bool            `json:"allowed"`
	Reason         string          `json:"reason,omitempty"`
	Detail         string          `json:"detail,omitempty"`
	Score          float64         `json:"score"`
	ScoreFloor     float64         `json:"score_floor"`
	AudioConfirmed bool            `json:"audio_confirmed"`
	Sequence       SequenceVerdict `json:"sequence"`
	Evidence       EvidenceVerdict `json:"evidence"`
}

// TranscriptionConfirms reports whether the candidate's title/author
// independently match the book's transcribed title/author. The rule is
// util.TitleAgrees and util.AuthorAgrees (internal/util/transcript_match.go),
// shared with auto-fetch and with the apply's audio_confirmed marker. Before
// 2026-09-13 this demanded exact title equality and the noisy transcribed
// author as a substring of the candidate's, which refused every real book on
// the review lane (Whisper appends credits and misspells names).
func TranscriptionConfirms(book *database.Book, c *metafetch.MetadataCandidate) bool {
	if book == nil || c == nil || book.TranscribedTitle == nil || *book.TranscribedTitle == "" {
		return false
	}
	if !util.TitleAgrees(c.Title, *book.TranscribedTitle) {
		return false
	}
	return util.AuthorAgrees(c.Author, derefStr(book.TranscribedAuthor), derefStr(book.IntroTranscription))
}

func derefStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ScoreGate applies legs 1 of the gate: the transcription hard gate and the
// score floor. It is exported separately because auto-upgrade uses it to rank
// several candidates before choosing one. ok=false carries the reason.
func ScoreGate(book *database.Book, c *metafetch.MetadataCandidate) (ok bool, floor float64, audioConfirmed bool, reason string) {
	audioConfirmed = TranscriptionConfirms(book, c)
	floor = MinScore
	if audioConfirmed {
		floor = MinScoreAudioConfirmed
	}
	hasTranscribedTitle := book != nil && book.TranscribedTitle != nil && *book.TranscribedTitle != ""
	if hasTranscribedTitle && !audioConfirmed {
		// A score-only pass would let a same-author, wrong-title record win.
		return false, floor, false, ReasonTranscriptionMismatch
	}
	if c.Score < floor {
		return false, floor, audioConfirmed, ReasonScoreBelowFloor
	}
	return true, floor, audioConfirmed, ""
}

// Evaluate runs all three legs. identityErr is the caller's staleness check
// result (nil = passed). Every leg is computed even after one fails, so a
// dry-run report shows the sequence evidence for a book blocked on score.
func Evaluate(book *database.Book, c *metafetch.MetadataCandidate, identityErr error) Verdict {
	return EvaluateInBatch(book, c, identityErr, nil)
}

// EvaluateInBatch is Evaluate for a bulk apply that knows its whole batch:
// claims (built from every book of the batch before any is applied) lets the
// evidence leg see a sibling folder holding another part of the same book.
// nil is the single-book case and skips only that sibling test.
func EvaluateInBatch(book *database.Book, c *metafetch.MetadataCandidate, identityErr error, claims *ClaimIndex) Verdict {
	v := Verdict{Score: c.Score}
	scoreOK, floor, audio, scoreReason := ScoreGate(book, c)
	v.ScoreFloor, v.AudioConfirmed = floor, audio
	v.Sequence = CheckSequence(book, c)
	v.Evidence = CheckEvidenceInBatch(book, c, audio, claims)

	switch {
	case identityErr != nil:
		v.Reason, v.Detail = ReasonIdentityStale, identityErr.Error()
	case !scoreOK:
		v.Reason = scoreReason
		if scoreReason == ReasonScoreBelowFloor {
			v.Detail = "score " + ftoa(c.Score) + " < floor " + ftoa(floor)
		} else {
			v.Detail = "book has a transcribed title the candidate does not match"
		}
	case !v.Sequence.Pass:
		v.Reason, v.Detail = v.Sequence.Reason, v.Sequence.Detail
	case !v.Evidence.Pass:
		v.Reason, v.Detail = v.Evidence.Reason, v.Evidence.Detail
	default:
		v.Allowed = true
	}
	return v
}

// CheckSequence compares the book's volume number with the candidate's.
//
// Book numbers come from the series position, the title, and the primary
// file's name and folder name. Candidate numbers come from its series
// position, title and subtitle (Audible often carries "Book 3" only in the
// subtitle). Decision rule:
//
//   - the book's sources disagree with each other → BLOCK (book_sequence_conflict):
//     one of them is wrong, very possibly from an earlier bad apply, and the
//     gate cannot tell which;
//   - the candidate's sources disagree with each other → BLOCK;
//   - book has a number, candidate has none → BLOCK;
//   - both have one and they differ → BLOCK;
//   - neither has one, or only the candidate has one → pass.
func CheckSequence(book *database.Book, c *metafetch.MetadataCandidate) SequenceVerdict {
	v := SequenceVerdict{BookNumbers: BookNumbers(book), CandidateNumbers: CandidateNumbers(c)}
	bookNum, bookOK, bookConflict := agree(v.BookNumbers)
	candNum, candOK, candConflict := agree(v.CandidateNumbers)

	switch {
	case bookConflict:
		v.Reason, v.Detail = ReasonBookSequenceConflict, "book sources disagree: "+describe(v.BookNumbers)
	case !bookOK:
		// No number on the book: nothing to protect.
		v.Pass = true
	case candConflict:
		v.Reason, v.Detail = ReasonCandidateSequenceConflict, "candidate sources disagree: "+describe(v.CandidateNumbers)
	case !candOK:
		v.Reason, v.Detail = ReasonSequenceMissingOnCandidate, "book is #"+bookNum.Text+" ("+describe(v.BookNumbers)+"), candidate has no number"
	case !seqnum.Equal(bookNum, candNum):
		v.Reason, v.Detail = ReasonSequenceMismatch, "book is #"+bookNum.Text+", candidate is #"+candNum.Text
	default:
		v.Pass = true
	}
	return v
}

// BookNumbers lists every number found on the book, one per source.
func BookNumbers(book *database.Book) []SourceNumber {
	if book == nil {
		return nil
	}
	var out []SourceNumber
	add := func(src string, n seqnum.Number, ok bool) {
		if ok {
			out = append(out, SourceNumber{Source: src, Number: n.Text, Form: n.Form})
		}
	}
	switch {
	case book.SeriesPositionRaw != nil && strings.TrimSpace(*book.SeriesPositionRaw) != "":
		n, ok := seqnum.ParsePosition(*book.SeriesPositionRaw)
		add("series_position", n, ok)
	case book.SeriesSequence != nil && *book.SeriesSequence > 0:
		n, ok := seqnum.ParsePosition(itoa(*book.SeriesSequence))
		add("series_position", n, ok)
	}
	n, ok := seqnum.Parse(book.Title)
	add("title", n, ok)

	if p := strings.TrimSpace(book.FilePath); p != "" {
		base := filepath.Base(p)
		if ext := filepath.Ext(base); ext != "" && len(ext) <= 5 {
			// A file: its name (sans extension) and its folder.
			n, ok := seqnum.Parse(strings.TrimSuffix(base, ext))
			add("file_name", n, ok)
			n, ok = seqnum.Parse(filepath.Base(filepath.Dir(p)))
			add("folder_name", n, ok)
		} else {
			// A directory (multi-file book): its own name.
			n, ok := seqnum.Parse(base)
			add("folder_name", n, ok)
		}
	}
	return out
}

// CandidateNumbers lists every number found on the candidate, one per source.
func CandidateNumbers(c *metafetch.MetadataCandidate) []SourceNumber {
	if c == nil {
		return nil
	}
	var out []SourceNumber
	if n, ok := seqnum.ParsePosition(c.SeriesPosition); ok {
		out = append(out, SourceNumber{Source: "series_position", Number: n.Text, Form: n.Form})
	}
	if n, ok := seqnum.Parse(c.Title); ok {
		out = append(out, SourceNumber{Source: "title", Number: n.Text, Form: n.Form})
	}
	if n, ok := seqnum.Parse(c.Subtitle); ok {
		out = append(out, SourceNumber{Source: "subtitle", Number: n.Text, Form: n.Form})
	}
	return out
}

// agree reduces a source list to one number. ok=false when the list is empty;
// conflict=true when two sources name different numbers.
func agree(src []SourceNumber) (n seqnum.Number, ok, conflict bool) {
	for _, s := range src {
		cur, _ := seqnum.ParsePosition(s.Number)
		if !ok {
			n, ok = cur, true
			continue
		}
		if !seqnum.Equal(n, cur) {
			return n, true, true
		}
	}
	return n, ok, false
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
func itoa(i int) string     { return strconv.Itoa(i) }

func describe(src []SourceNumber) string {
	parts := make([]string, 0, len(src))
	for _, s := range src {
		parts = append(parts, s.Source+"="+s.Number)
	}
	return strings.Join(parts, ", ")
}
