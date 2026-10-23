// file: internal/masterlist/parse.go
// version: 1.0.0
// guid: 3a1896ea-2d97-4da6-bb85-27abe9094240
// last-edited: 2026-10-23

package masterlist

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// MaxRows is the most rows one list may hold. A personal master list is
// hundreds to a few thousand lines; the cap keeps a wrong file from filling
// the store.
const MaxRows = 50000

// Upload formats.
const (
	FormatCSV  = "csv"
	FormatJSON = "json"
)

// Parse errors that are about the file as a whole, not one row. A row's own
// problems are Issues on the row.
var (
	ErrNoRows     = errors.New("masterlist: the file has no rows")
	ErrTooManyRow = fmt.Errorf("masterlist: the file has more than %d rows", MaxRows)
	ErrNoColumns  = errors.New("masterlist: the header names no known column (want at least title, asin, isbn10, isbn13, isbn or source_id)")
)

// Parse reads a list in the named format ("csv" or "json").
func Parse(format string, r io.Reader) ([]Row, error) {
	switch format {
	case FormatCSV:
		return ParseCSV(r)
	case FormatJSON:
		return ParseJSON(r)
	}
	return nil, fmt.Errorf("masterlist: unknown format %q (want csv or json)", format)
}

// skipBOM drops a leading UTF-8 byte order mark, which spreadsheet exports
// write and the JSON decoder rejects.
func skipBOM(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	if b, err := br.Peek(3); err == nil && bytes.Equal(b, []byte{0xef, 0xbb, 0xbf}) {
		_, _ = br.Discard(3)
	}
	return br
}

// inRow is one input row as read, before validation: every value a string or
// a lenient number, so a bad value becomes an Issue on the row instead of
// failing the file.
type inRow struct {
	Title     string          `json:"title"`
	Subtitle  string          `json:"subtitle"`
	Authors   names           `json:"authors"`
	Narrators names           `json:"narrators"`
	Series    json.RawMessage `json:"series"`
	SeriesPos flexString      `json:"series_position"`
	ISBN      flexString      `json:"isbn"`
	ISBN10    flexString      `json:"isbn10"`
	ISBN13    flexString      `json:"isbn13"`
	ASIN      flexString      `json:"asin"`
	Sources   []inSource      `json:"sources"`
	// Flat provenance, for a list with one source per row.
	Source         flexString `json:"source"`
	SourceID       flexString `json:"source_id"`
	PurchasedAt    flexString `json:"purchased_at"`
	LastListenedAt flexString `json:"last_listened_at"`
	RuntimeMin     flexNum    `json:"runtime_min"`
	Status         flexString `json:"status"`
	ProgressPct    flexNum    `json:"progress_pct"`
	PositionSec    flexNum    `json:"position_sec"`
	FinishedAt     flexString `json:"finished_at"`
	Note           string     `json:"note"`
}

type inSource struct {
	Source         flexString `json:"source"`
	SourceID       flexString `json:"source_id"`
	PurchasedAt    flexString `json:"purchased_at"`
	LastListenedAt flexString `json:"last_listened_at"`
}

// ParseJSON reads a JSON list: an array of rows, or an object holding the
// array under "rows" or "items". Unknown fields are ignored.
func ParseJSON(r io.Reader) ([]Row, error) {
	dec := json.NewDecoder(skipBOM(r))
	var top json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return nil, fmt.Errorf("masterlist: read JSON: %w", err)
	}
	top = bytes.TrimSpace(top)
	var items []json.RawMessage
	if len(top) > 0 && top[0] == '{' {
		var obj struct {
			Rows  []json.RawMessage `json:"rows"`
			Items []json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(top, &obj); err != nil {
			return nil, fmt.Errorf("masterlist: read JSON object: %w", err)
		}
		items = obj.Rows
		if len(items) == 0 {
			items = obj.Items
		}
	} else if err := json.Unmarshal(top, &items); err != nil {
		return nil, fmt.Errorf("masterlist: the JSON must be an array of rows or an object with a rows array: %w", err)
	}
	if len(items) == 0 {
		return nil, ErrNoRows
	}
	if len(items) > MaxRows {
		return nil, ErrTooManyRow
	}
	out := make([]Row, 0, len(items))
	for i, raw := range items {
		var in inRow
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("masterlist: row %d: %w", i+1, err)
		}
		out = append(out, build(in, i+1))
	}
	return out, nil
}

// csvColumns maps a header (lower-cased, spaces and hyphens as underscores)
// to the field it fills.
var csvColumns = map[string]string{
	"title": "title", "subtitle": "subtitle",
	"author": "authors", "authors": "authors",
	"narrator": "narrators", "narrators": "narrators",
	"series": "series", "series_name": "series",
	"series_position": "series_position", "series_number": "series_position",
	"isbn": "isbn", "isbn10": "isbn10", "isbn_10": "isbn10", "isbn13": "isbn13", "isbn_13": "isbn13",
	"asin":   "asin",
	"source": "source", "source_id": "source_id",
	"purchased_at": "purchased_at", "last_listened_at": "last_listened_at",
	"runtime_min": "runtime_min", "runtime_length_min": "runtime_min",
	"status": "status", "progress_pct": "progress_pct", "position_sec": "position_sec",
	"finished_at": "finished_at", "finished_at_timestamp": "finished_at",
	"note": "note",
}

func csvHeaderKey(h string) string {
	h = strings.TrimPrefix(h, "\ufeff")
	h = strings.ToLower(strings.TrimSpace(h))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(h)
}

// ParseCSV reads a CSV list. The first record is the header. Credits are
// separated by ";" or "|" inside a cell (a comma is part of a name, as in
// "Smith, Jane"). Line numbers are the file's, the header being line 1.
func ParseCSV(r io.Reader) ([]Row, error) {
	cr := csv.NewReader(skipBOM(r))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, ErrNoRows
	}
	if err != nil {
		return nil, fmt.Errorf("masterlist: read CSV header: %w", err)
	}
	cols := make([]string, len(header))
	known := false
	for i, h := range header {
		cols[i] = csvColumns[csvHeaderKey(h)]
		switch cols[i] {
		case "title", "asin", "isbn", "isbn10", "isbn13", "source_id":
			known = true
		}
	}
	if !known {
		return nil, ErrNoColumns
	}
	var out []Row
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			return nil, fmt.Errorf("masterlist: CSV line %d: %w", line, err)
		}
		if len(out) >= MaxRows {
			return nil, ErrTooManyRow
		}
		out = append(out, build(csvRecord(cols, rec), line))
	}
	if len(out) == 0 {
		return nil, ErrNoRows
	}
	return out, nil
}

func csvRecord(cols, rec []string) inRow {
	var in inRow
	for i, v := range rec {
		if i >= len(cols) || cols[i] == "" {
			continue
		}
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		switch cols[i] {
		case "title":
			in.Title = v
		case "subtitle":
			in.Subtitle = v
		case "authors":
			in.Authors = splitCredits(v)
		case "narrators":
			in.Narrators = splitCredits(v)
		case "series":
			in.Series = jsonString(v)
		case "series_position":
			in.SeriesPos = flexString(v)
		case "isbn":
			in.ISBN = flexString(v)
		case "isbn10":
			in.ISBN10 = flexString(v)
		case "isbn13":
			in.ISBN13 = flexString(v)
		case "asin":
			in.ASIN = flexString(v)
		case "source":
			in.Source = flexString(v)
		case "source_id":
			in.SourceID = flexString(v)
		case "purchased_at":
			in.PurchasedAt = flexString(v)
		case "last_listened_at":
			in.LastListenedAt = flexString(v)
		case "runtime_min":
			in.RuntimeMin = parseCSVNum(v)
		case "status":
			in.Status = flexString(v)
		case "progress_pct":
			in.ProgressPct = parseCSVNum(v)
		case "position_sec":
			in.PositionSec = parseCSVNum(v)
		case "finished_at":
			in.FinishedAt = flexString(v)
		case "note":
			in.Note = v
		}
	}
	return in
}

func jsonString(s string) json.RawMessage {
	b, _ := json.Marshal(s) // a string always marshals
	return b
}

// parseCSVNum reads a number from text. Text that is not a number is kept in
// flexNum.bad, so build reports bad_number instead of reading it as 0.
func parseCSVNum(v string) flexNum {
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return flexNum{bad: v}
	}
	return flexNum{val: f, set: true}
}

// ---- lenient scalar types ----

// flexString reads a JSON string, or a number or boolean written bare (an
// ISBN or ASIN a spreadsheet exported as a number).
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = ""
		return nil
	}
	if b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		*s = flexString(v)
		return nil
	}
	if b[0] == '{' || b[0] == '[' {
		return fmt.Errorf("want a string, got %s", string(b))
	}
	*s = flexString(string(b))
	return nil
}

// flexNum is a number written as a JSON number or a numeric string. bad holds
// text that was not a number.
type flexNum struct {
	val float64
	set bool
	bad string
}

func (n *flexNum) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	*n = flexNum{}
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s = strings.TrimSpace(s); s == "" {
			return nil
		}
		*n = parseCSVNum(s)
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		*n = flexNum{bad: string(b)}
		return nil
	}
	*n = flexNum{val: f, set: true}
	return nil
}

// names reads a list of credits: an array of strings, an array of {"name"}
// objects, or one string (split on ";" and "|").
type names []string

func (n *names) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*n = nil
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*n = splitCredits(s)
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("credits: want a string or an array: %w", err)
	}
	out := make(names, 0, len(raw))
	for _, r := range raw {
		var s string
		if json.Unmarshal(r, &s) == nil {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
			continue
		}
		var o struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(r, &o); err != nil {
			return fmt.Errorf("credits: entry %s is neither a string nor {name}", string(r))
		}
		if s = strings.TrimSpace(o.Name); s != "" {
			out = append(out, s)
		}
	}
	*n = out
	return nil
}

func splitCredits(s string) names {
	var out names
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == '|' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---- validation: inRow -> Row ----

// build validates one input row into canonical form. It never fails: a bad
// value is dropped from the field and recorded as an Issue.
func build(in inRow, line int) Row {
	r := Row{Line: line,
		Title:     strings.TrimSpace(in.Title),
		Subtitle:  strings.TrimSpace(in.Subtitle),
		Authors:   []string(in.Authors),
		Narrators: []string(in.Narrators),
		Note:      strings.TrimSpace(in.Note)}
	issue := func(code, field, value, detail string) {
		r.Issues = append(r.Issues, Issue{Code: code, Field: field, Value: value, Detail: detail, Line: line})
	}

	buildIdentifiers(&r, in, issue)

	r.Series = buildSeries(in)

	// Sources: the nested list, then the flat columns as one more entry.
	for _, s := range in.Sources {
		r.addSource(string(s.Source), string(s.SourceID), string(s.PurchasedAt), string(s.LastListenedAt), issue)
	}
	r.addSource(string(in.Source), string(in.SourceID), string(in.PurchasedAt), string(in.LastListenedAt), issue)

	if in.RuntimeMin.bad != "" || (in.RuntimeMin.set && (in.RuntimeMin.val < 0 || in.RuntimeMin.val > 1e6)) {
		issue(IssueBadNumber, "runtime_min", numText(in.RuntimeMin), "a runtime is a number of minutes, 0 or more")
	} else if in.RuntimeMin.set {
		r.RuntimeMin = int(in.RuntimeMin.val + 0.5)
	}
	if in.ProgressPct.bad != "" || (in.ProgressPct.set && (in.ProgressPct.val < 0 || in.ProgressPct.val > 100)) {
		issue(IssueBadNumber, "progress_pct", numText(in.ProgressPct), "a percent is a number from 0 to 100")
	} else if in.ProgressPct.set {
		r.ProgressPct = int(in.ProgressPct.val + 0.5)
	}
	if in.PositionSec.bad != "" || (in.PositionSec.set && (in.PositionSec.val < 0 || in.PositionSec.val > 1e9)) {
		issue(IssueBadNumber, "position_sec", numText(in.PositionSec), "a position is a number of seconds, 0 or more")
	} else if in.PositionSec.set {
		r.PositionSec = int(in.PositionSec.val)
	}

	if st := normalizeStatus(string(in.Status)); st != "" {
		if validStatus(st) {
			r.Status = st
		} else {
			issue(IssueBadStatus, "status", string(in.Status), "want finished, in_progress, abandoned or unstarted")
		}
	}
	if v := strings.TrimSpace(string(in.FinishedAt)); v != "" {
		if _, ok := ParseTime(v); ok {
			r.FinishedAt = v
		} else {
			issue(IssueBadTimestamp, "finished_at", v, "not an RFC 3339 time with a zone; it is not guessed as UTC or local time")
		}
	}
	return r
}

func numText(n flexNum) string {
	if n.bad != "" {
		return n.bad
	}
	return strconv.FormatFloat(n.val, 'f', -1, 64)
}

func normalizeStatus(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "_", "-", "_").Replace(s)
}

func validStatus(s string) bool {
	switch s {
	case StatusFinished, StatusInProgress, StatusAbandoned, StatusUnstarted:
		return true
	}
	return false
}

// buildIdentifiers validates the ISBN and ASIN fields into r.
func buildIdentifiers(r *Row, in inRow, issue func(code, field, value, detail string)) {
	bad := func(field, v string) {
		issue(IssueBadIdentifier, field, v, "failed its check digit or shape; it is shown here and never used to match")
	}
	if v := strings.TrimSpace(string(in.ISBN10)); v != "" {
		if c, ok := ValidISBN10(v); ok {
			r.ISBN10 = c
		} else {
			bad("isbn10", v)
		}
	}
	if v := strings.TrimSpace(string(in.ISBN13)); v != "" {
		if c, ok := ValidISBN13(v); ok {
			r.ISBN13 = c
		} else {
			bad("isbn13", v)
		}
	}
	if v := strings.TrimSpace(string(in.ISBN)); v != "" {
		switch c := cleanIdentifier(v); len(c) {
		case 10:
			if x, ok := ValidISBN10(c); ok && r.ISBN10 == "" {
				r.ISBN10 = x
			} else if !ok {
				bad("isbn", v)
			}
		case 13:
			if x, ok := ValidISBN13(c); ok && r.ISBN13 == "" {
				r.ISBN13 = x
			} else if !ok {
				bad("isbn", v)
			}
		default:
			bad("isbn", v)
		}
	}
	if v := strings.TrimSpace(string(in.ASIN)); v != "" {
		asin, isbn10, ok := ClassifyASIN(v)
		switch {
		case !ok:
			bad("asin", v)
		case isbn10 != "":
			// An all-digit "ASIN" that is a valid ISBN-10 is an ISBN-10.
			if r.ISBN10 == "" {
				r.ISBN10 = isbn10
			}
		default:
			r.ASIN = asin
		}
	}
	// Both forms are stored; two forms of different editions are a conflict.
	if r.ISBN10 != "" {
		d13, _ := ISBN10To13(r.ISBN10)
		switch {
		case r.ISBN13 == "":
			r.ISBN13 = d13
		case r.ISBN13 != d13:
			issue(IssueIdentifierConflict, "isbn10", r.ISBN10,
				"the ISBN-10 is the edition "+d13+", not the ISBN-13 "+r.ISBN13)
		}
	}
}

// buildSeries reads "series" (a string, or an array of {name, position}) and
// the flat series_position.
func buildSeries(in inRow) []SeriesRef {
	var out []SeriesRef
	raw := bytes.TrimSpace(in.Series)
	switch {
	case len(raw) == 0 || string(raw) == "null":
	case raw[0] == '"':
		var name string
		if json.Unmarshal(raw, &name) == nil && strings.TrimSpace(name) != "" {
			out = append(out, SeriesRef{Name: strings.TrimSpace(name), Position: strings.TrimSpace(string(in.SeriesPos))})
		}
	case raw[0] == '[':
		var arr []struct {
			Name     string     `json:"name"`
			Position flexString `json:"position"`
		}
		if json.Unmarshal(raw, &arr) == nil {
			for _, s := range arr {
				if n := strings.TrimSpace(s.Name); n != "" {
					out = append(out, SeriesRef{Name: n, Position: strings.TrimSpace(string(s.Position))})
				}
			}
		}
	}
	return out
}

// addSource appends one provenance entry when it has anything in it, checking
// its two times.
func (r *Row) addSource(source, id, purchased, listened string, issue func(code, field, value, detail string)) {
	source = strings.ToLower(strings.TrimSpace(source))
	id = strings.TrimSpace(id)
	purchased, listened = strings.TrimSpace(purchased), strings.TrimSpace(listened)
	if source == "" && id == "" && purchased == "" && listened == "" {
		return
	}
	if source == "" {
		source = "other"
	}
	check := func(field, v string) string {
		if v == "" {
			return ""
		}
		if _, ok := ParseTime(v); !ok {
			issue(IssueBadTimestamp, field, v, "not an RFC 3339 time with a zone; it is not guessed as UTC or local time")
			return ""
		}
		return v
	}
	r.Sources = append(r.Sources, Source{Source: source, SourceID: id,
		PurchasedAt: check("purchased_at", purchased), LastListenedAt: check("last_listened_at", listened)})
}
