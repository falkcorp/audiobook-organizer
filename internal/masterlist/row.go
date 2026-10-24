// file: internal/masterlist/row.go
// version: 1.1.0
// guid: e52147e5-a7b2-4b1b-99e5-616e6dac1eeb
// last-edited: 2026-10-23

// Package masterlist is the input side of the master-list import: the
// canonical row an uploaded list is stored as, the validation of its
// identifiers and timestamps, the CSV and JSON readers, and the collapse of
// rows that name the same book. It imports nothing from the module, so both
// the upload endpoint and the Repairs fixer can use it.
//
// Design: .claude/notes/2026-10-23-master-list-import-design.md, section 3.
package masterlist

import (
	"strings"
	"time"
)

// Row statuses (Row.Status). An empty status is allowed: a list may carry
// identifiers only.
const (
	StatusFinished   = "finished"
	StatusInProgress = "in_progress"
	StatusAbandoned  = "abandoned"
	StatusUnstarted  = "unstarted"
)

// Issue codes. Each is a single word, so the Repairs lane can use it as a row
// flag ("flag:<code>" filters the rows).
const (
	// IssueBadIdentifier: an ISBN or ASIN failed its check. The bad value is
	// shown (Issue.Value) and never used to match.
	IssueBadIdentifier = "bad_identifier"
	// IssueIdentifierConflict: the row's own identifiers disagree (an ISBN-10
	// and an ISBN-13 that are not the same edition).
	IssueIdentifierConflict = "identifier_conflict"
	// IssueBadTimestamp: a time is not RFC 3339 with a zone. It is dropped,
	// never guessed as UTC or local time.
	IssueBadTimestamp = "bad_timestamp"
	// IssueFutureTimestamp: a time is in the future (checked at plan time,
	// against the planner's clock).
	IssueFutureTimestamp = "future_timestamp"
	// IssueBadStatus: the status is not one of the four known values.
	IssueBadStatus = "bad_status"
	// IssueBadNumber: a runtime, percent or position is negative or out of
	// range.
	IssueBadNumber = "bad_number"
	// IssueListConflict: rows of one group (rows joined by a shared ASIN, ISBN
	// or source id) carry different identifiers.
	IssueListConflict = "list_conflict"
	// IssueBadField: a JSON field had the wrong type (a number where a list
	// of authors goes, an object where a string goes). The field is left out
	// of the row; the row's other fields are kept.
	IssueBadField = "bad_field"
	// IssueBadRow: a JSON array element that is not an object. The row is
	// kept, empty, so the list's line numbers still count it.
	IssueBadRow = "bad_row"
)

// Issue is one finding about a row. The row is kept; the finding travels with
// it so the plan can show it and never match on the offending value.
type Issue struct {
	Code   string `json:"code"`
	Field  string `json:"field,omitempty"`
	Value  string `json:"value,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Line is the input line the finding came from; set when rows are
	// combined, so a combined row still says which line was wrong.
	Line int `json:"line,omitempty"`
}

// SeriesRef is one series a book belongs to.
type SeriesRef struct {
	Name     string `json:"name"`
	Position string `json:"position,omitempty"`
}

// Source is where a row was obtained: one provenance entry. A combined row
// carries one per input row it absorbed.
type Source struct {
	Source         string `json:"source"`
	SourceID       string `json:"source_id,omitempty"`
	PurchasedAt    string `json:"purchased_at,omitempty"`
	LastListenedAt string `json:"last_listened_at,omitempty"`
}

// Row is one line of a master list in canonical form: identifiers validated
// and normalised, timestamps checked, findings in Issues. It is what the list
// store keeps (one JSON document per input row) and what the Repairs plan
// reads.
type Row struct {
	// Line is the 1-based position in the upload: the CSV line (the header is
	// line 1) or the index in the JSON array plus one.
	Line      int         `json:"line"`
	Title     string      `json:"title,omitempty"`
	Subtitle  string      `json:"subtitle,omitempty"`
	Authors   []string    `json:"authors,omitempty"`
	Narrators []string    `json:"narrators,omitempty"`
	Series    []SeriesRef `json:"series,omitempty"`
	// ISBN10, ISBN13 and ASIN hold only values that passed validation. An
	// ISBN-10 is also stored as an ISBN-13 (its 978 form) when ISBN13 is empty.
	ISBN10      string   `json:"isbn10,omitempty"`
	ISBN13      string   `json:"isbn13,omitempty"`
	ASIN        string   `json:"asin,omitempty"`
	Sources     []Source `json:"sources,omitempty"`
	RuntimeMin  int      `json:"runtime_min,omitempty"`
	Status      string   `json:"status,omitempty"`
	ProgressPct int      `json:"progress_pct,omitempty"`
	PositionSec int      `json:"position_sec,omitempty"`
	FinishedAt  string   `json:"finished_at,omitempty"`
	Note        string   `json:"note,omitempty"`
	Issues      []Issue  `json:"issues,omitempty"`
}

// HasIdentity reports whether the row names a book at all: a title, a valid
// identifier or a source id. A row that does not is invalid: it cannot be
// matched and says nothing.
func (r Row) HasIdentity() bool {
	if strings.TrimSpace(r.Title) != "" || r.ASIN != "" || r.ISBN10 != "" || r.ISBN13 != "" {
		return true
	}
	for _, s := range r.Sources {
		if s.SourceID != "" {
			return true
		}
	}
	return false
}

// HasIssue reports whether the row carries a finding with this code.
func (r Row) HasIssue(code string) bool {
	for _, is := range r.Issues {
		if is.Code == code {
			return true
		}
	}
	return false
}

// ParseTime reads an RFC 3339 time WITH a zone ("Z" or an offset). A time
// without a zone is not read as UTC or as local time.
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// FutureTimestamps lists the row's times that are later than now+slack: a
// clock or export error, and a future stamp would beat every real listen that
// comes after the import. It looks at finish, purchase and last-listen times.
func (r Row) FutureTimestamps(now time.Time, slack time.Duration) []Issue {
	var out []Issue
	limit := now.Add(slack)
	check := func(field, v string) {
		if t, ok := ParseTime(v); ok && t.After(limit) {
			out = append(out, Issue{Code: IssueFutureTimestamp, Field: field, Value: v, Line: r.Line})
		}
	}
	check("finished_at", r.FinishedAt)
	for _, s := range r.Sources {
		check("purchased_at", s.PurchasedAt)
		check("last_listened_at", s.LastListenedAt)
	}
	return out
}

// NaturalKey is the first identifier the row carries, as the key a re-import
// finds the same book by: "asin:<ASIN>", else "isbn13:<ISBN-13>", else
// "src:<source>:<id>" for the first source with an id. It is "" for a row with
// none; the caller then keys the row by a hash of its folded title and
// authors, which needs the matcher's folding.
func NaturalKey(r Row) string {
	switch {
	case r.ASIN != "":
		return "asin:" + r.ASIN
	case r.ISBN13 != "":
		return "isbn13:" + r.ISBN13
	}
	for _, s := range r.Sources {
		if s.SourceID != "" {
			return "src:" + s.Source + ":" + s.SourceID
		}
	}
	return ""
}
