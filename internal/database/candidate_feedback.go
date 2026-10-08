// file: internal/database/candidate_feedback.go
// version: 1.0.0
// guid: 6ad4242e-656a-49ea-8fa5-2ff51272f307
// last-edited: 2026-10-07

package database

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Metadata-candidate feedback: labelled examples for the candidate scorer.
//
// The review page's Candidates view lists every candidate a metadata search
// found for a book. A thumbs-down on a row says "this is not the best match"
// without rejecting the book, skipping it, or hiding the candidate; an Apply
// says "this one is". Both are kept here as training data for the scorer, next
// to (not instead of) the one existing ground truth: the applied candidate's
// MetadataSourceHash, which metafetch.calibrate-scoring replays. Every record
// carries the same hash (Candidate.SourceHash) when the candidate has a
// canonical id, so these labels join directly against that harness.
//
// What the apply path does NOT record, and this does: the query that produced
// the list (typed title/author, browse vs per-book), the candidate's score and
// its derivation at the time the user judged it, who judged it, and the
// negatives.
//
// Key layout: candfb:<bookID>:<queryHash>:<candidateHash> → CandidateFeedback
// JSON. One record per book + query + candidate, with the label in the value,
// so a thumbs-down followed by an Apply of the same candidate turns the record
// positive instead of leaving two contradictory rows.
const candidateFeedbackPfx = "candfb:"

// CandidateFeedbackLabel is the judgement on one candidate.
type CandidateFeedbackLabel string

const (
	// CandidateFeedbackNegative: "not the best match" (the thumbs-down).
	CandidateFeedbackNegative CandidateFeedbackLabel = "negative"
	// CandidateFeedbackPositive: the candidate the user applied.
	CandidateFeedbackPositive CandidateFeedbackLabel = "positive"
)

// Query modes.
const (
	// CandidateFeedbackModeBook is the per-book search (the book's identity).
	CandidateFeedbackModeBook = "book"
	// CandidateFeedbackModeBrowse is a "Search again" browse search (what was typed).
	CandidateFeedbackModeBrowse = "browse"
)

// maxCandidateFeedbackBreakdown bounds the stored score derivation. A real
// breakdown is a few hundred bytes; the cap only stops a hostile body.
const maxCandidateFeedbackBreakdown = 64 << 10

// CandidateFeedbackQuery is the search that produced the candidate list.
type CandidateFeedbackQuery struct {
	Title  string `json:"title"`
	Author string `json:"author"`
	// Mode is CandidateFeedbackModeBook or CandidateFeedbackModeBrowse.
	Mode string `json:"mode"`
}

// CandidateFeedbackCandidate is the judged candidate's identity and the
// fields a scorer compares.
type CandidateFeedbackCandidate struct {
	Source         string `json:"source"`
	ASIN           string `json:"asin,omitempty"`
	ISBN           string `json:"isbn,omitempty"`
	ISBN10         string `json:"isbn10,omitempty"`
	ISBN13         string `json:"isbn13,omitempty"`
	Title          string `json:"title"`
	Author         string `json:"author,omitempty"`
	Narrator       string `json:"narrator,omitempty"`
	Series         string `json:"series,omitempty"`
	SeriesPosition string `json:"series_position,omitempty"`
	Publisher      string `json:"publisher,omitempty"`
	Year           int    `json:"year,omitempty"`
	DurationSec    int    `json:"duration_sec,omitempty"`
	FromCatalog    bool   `json:"from_catalog,omitempty"`
	// SourceHash is metafetch.CandidateSourceHash — the value an apply stamps
	// as the book's MetadataSourceHash. Empty when the candidate has no
	// canonical id.
	SourceHash string `json:"source_hash,omitempty"`
}

// CandidateFeedback is one labelled example.
type CandidateFeedback struct {
	// ID is the record key without the family prefix:
	// <bookID>:<queryHash>:<candidateHash>.
	ID        string                     `json:"id"`
	BookID    string                     `json:"book_id"`
	Label     CandidateFeedbackLabel     `json:"label"`
	Query     CandidateFeedbackQuery     `json:"query"`
	Candidate CandidateFeedbackCandidate `json:"candidate"`
	// Score is the candidate's score as shown when it was judged.
	Score float64 `json:"score"`
	// ScoreBreakdown is the candidate's score_breakdown as shown; absent when
	// the search path recorded none.
	ScoreBreakdown json.RawMessage `json:"score_breakdown,omitempty"`
	// Rank is the candidate's 1-based position in the list by score (0 = not sent).
	Rank int `json:"rank,omitempty"`
	// ResultCount is how many candidates the list held (0 = not sent).
	ResultCount int `json:"result_count,omitempty"`
	// UserID / Username name who judged it; empty for a request without a user.
	UserID    string    `json:"user_id,omitempty"`
	Username  string    `json:"username,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ErrCandidateFeedbackInvalid wraps every validation failure, so a handler can
// answer 400 for it and 500 for anything else.
var ErrCandidateFeedbackInvalid = errors.New("invalid candidate feedback")

func invalidFeedback(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCandidateFeedbackInvalid, fmt.Sprintf(format, args...))
}

// CandidateFeedbackStore keeps the labels in the main Pebble store.
type CandidateFeedbackStore struct {
	kv RawKVStore
}

// NewCandidateFeedbackStore wraps kv.
func NewCandidateFeedbackStore(kv RawKVStore) *CandidateFeedbackStore {
	return &CandidateFeedbackStore{kv: kv}
}

func cfbHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return fmt.Sprintf("%x", sum[:8])
}

func cfbNorm(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// CandidateFeedbackID is the deterministic record id for book + query +
// candidate. The candidate half uses SourceHash when there is one (the same
// identity an apply records) and otherwise the candidate's descriptive fields.
func CandidateFeedbackID(bookID string, q CandidateFeedbackQuery, c CandidateFeedbackCandidate) string {
	qh := cfbHash(cfbNorm(q.Title), cfbNorm(q.Author), q.Mode)
	var ch string
	if c.SourceHash != "" {
		ch = cfbHash("h", c.SourceHash)
	} else {
		ch = cfbHash("f", cfbNorm(c.Source), cfbNorm(c.ASIN), cfbNorm(c.ISBN13), cfbNorm(c.ISBN10), cfbNorm(c.ISBN),
			cfbNorm(c.Title), cfbNorm(c.Author), cfbNorm(c.Narrator))
	}
	return bookID + ":" + qh + ":" + ch
}

func cfbValidBookID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, ":/\x00")
}

// Put validates fb, fills its ID and timestamps, and upserts it. A record that
// already exists keeps its CreatedAt; its label and everything else are
// replaced. The stored record is returned.
func (s *CandidateFeedbackStore) Put(fb CandidateFeedback, now time.Time) (*CandidateFeedback, error) {
	if !cfbValidBookID(fb.BookID) {
		return nil, invalidFeedback("book_id is required and may not contain ':' or '/'")
	}
	if fb.Label != CandidateFeedbackNegative && fb.Label != CandidateFeedbackPositive {
		return nil, invalidFeedback("label must be %q or %q", CandidateFeedbackNegative, CandidateFeedbackPositive)
	}
	if fb.Query.Mode != CandidateFeedbackModeBook && fb.Query.Mode != CandidateFeedbackModeBrowse {
		return nil, invalidFeedback("query.mode must be %q or %q", CandidateFeedbackModeBook, CandidateFeedbackModeBrowse)
	}
	if strings.TrimSpace(fb.Candidate.Source) == "" || strings.TrimSpace(fb.Candidate.Title) == "" {
		return nil, invalidFeedback("candidate.source and candidate.title are required")
	}
	if len(fb.ScoreBreakdown) > maxCandidateFeedbackBreakdown {
		return nil, invalidFeedback("score_breakdown is larger than %d bytes", maxCandidateFeedbackBreakdown)
	}
	fb.ID = CandidateFeedbackID(fb.BookID, fb.Query, fb.Candidate)
	fb.CreatedAt = now.UTC()
	fb.UpdatedAt = fb.CreatedAt
	if prev, err := s.Get(fb.ID); err != nil {
		return nil, err
	} else if prev != nil {
		fb.CreatedAt = prev.CreatedAt
	}
	raw, err := json.Marshal(&fb)
	if err != nil {
		return nil, fmt.Errorf("encode candidate feedback: %w", err)
	}
	if err := s.kv.SetRaw(candidateFeedbackPfx+fb.ID, raw); err != nil {
		return nil, fmt.Errorf("write candidate feedback %s: %w", fb.ID, err)
	}
	return &fb, nil
}

// Get reads one record; nil, nil when absent.
func (s *CandidateFeedbackStore) Get(id string) (*CandidateFeedback, error) {
	raw, err := s.kv.GetRaw(candidateFeedbackPfx + id)
	if err != nil {
		return nil, fmt.Errorf("read candidate feedback %s: %w", id, err)
	}
	if raw == nil {
		return nil, nil
	}
	var fb CandidateFeedback
	if err := json.Unmarshal(raw, &fb); err != nil {
		return nil, fmt.Errorf("decode candidate feedback %s: %w", id, err)
	}
	return &fb, nil
}

// Delete removes id when its label is `label` (any label when label is ""),
// so undoing a thumbs-down cannot erase the positive an Apply of the same
// candidate wrote over it. Reports whether a record was removed.
func (s *CandidateFeedbackStore) Delete(id string, label CandidateFeedbackLabel) (bool, error) {
	prev, err := s.Get(id)
	if err != nil || prev == nil {
		return false, err
	}
	if label != "" && prev.Label != label {
		return false, nil
	}
	if err := s.kv.DeleteRaw(candidateFeedbackPfx + id); err != nil {
		return false, fmt.Errorf("delete candidate feedback %s: %w", id, err)
	}
	return true, nil
}

// candidateFeedbackPage bounds memory per Each page.
const candidateFeedbackPage = 500

// Each calls fn for every record, in key order (grouped by book), one page in
// memory at a time. bookID != "" restricts it to that book. A record that
// fails to decode is an error, not a skip: an export that silently drops rows
// is a wrong training set.
func (s *CandidateFeedbackStore) Each(bookID string, fn func(*CandidateFeedback) error) error {
	prefix := candidateFeedbackPfx
	if bookID != "" {
		if !cfbValidBookID(bookID) {
			return invalidFeedback("book_id may not contain ':' or '/'")
		}
		prefix += bookID + ":"
	}
	after := ""
	for {
		pairs, next, err := s.kv.ScanPrefixPage(prefix, after, candidateFeedbackPage)
		if err != nil {
			return fmt.Errorf("scan candidate feedback: %w", err)
		}
		for _, p := range pairs {
			var fb CandidateFeedback
			if err := json.Unmarshal(p.Value, &fb); err != nil {
				return fmt.Errorf("decode candidate feedback %s: %w", p.Key, err)
			}
			if err := fn(&fb); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		after = next
	}
}
