// file: internal/seriesdenumber/report.go
// version: 1.0.0
// guid: b5c91d38-7e24-4a06-8f1b-d3a6e02c97f4
// last-edited: 2026-11-16

package seriesdenumber

import (
	"strconv"
	"strings"
)

// Row kinds of the report.
const (
	KindPlan          = "plan"           // one numbered series and its verdict
	KindBook          = "book"           // one book move, planned or done
	KindHeldBook      = "held_book"      // one book left where it is, with the reason
	KindCreatedSeries = "created_series" // a base series the run made
	KindDeletedSeries = "deleted_series" // an emptied numbered series the run removed
)

// ReportHeader is the first line of the report. The column list is the contract
// of a file a person reads, sorts and greps: one row per numbered series
// (kind plan, columns 2-10) followed by one row per book decision (kind book or
// held_book, columns 11-15).
const ReportHeader = "kind\tfrom_series_id\tfrom_name\tinto_name\tposition\tbooks\tshape\tconfidence\teligible\treason\t" +
	"book_id\told_series_sequence\tnew_series_id\tnew_series_sequence\tstatus"

// ReportRow is one report line. Fields a kind does not use stay empty.
type ReportRow struct {
	Kind         string
	FromSeriesID int
	FromName     string
	IntoName     string
	Position     int
	Books        int
	Shape        string
	Confidence   string
	Eligible     bool
	Reason       string

	BookID string
	// OldSeq is the book's position before ("" when it had none).
	OldSeq string
	// NewSeriesID is the series the book lands in; "" before the base series
	// exists (a created_series row of the applied report names it).
	NewSeriesID string
	NewSeq      string
	Status      string
}

// RenderReport renders rows under ReportHeader. Tabs and line breaks inside a
// value are replaced with spaces: one of them would shift every column after it
// and misattribute the rest of the row.
func RenderReport(rows []ReportRow) string {
	var b strings.Builder
	b.WriteString(ReportHeader)
	b.WriteByte('\n')
	for _, r := range rows {
		cols := []string{
			r.Kind,
			strconv.Itoa(r.FromSeriesID),
			clean(r.FromName),
			clean(r.IntoName),
			strconv.Itoa(r.Position),
			strconv.Itoa(r.Books),
			r.Shape,
			r.Confidence,
			strconv.FormatBool(r.Eligible),
			clean(r.Reason),
			clean(r.BookID),
			r.OldSeq,
			r.NewSeriesID,
			r.NewSeq,
			clean(r.Status),
		}
		b.WriteString(strings.Join(cols, "\t"))
		b.WriteByte('\n')
	}
	return b.String()
}

// ReportColumns is the number of columns of every row.
const ReportColumns = 15

var cleaner = strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")

func clean(s string) string { return cleaner.Replace(s) }

// SeqString renders a position for a report column: "" for none.
func SeqString(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}
