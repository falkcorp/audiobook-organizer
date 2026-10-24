// file: internal/masterlist/masterlist_test.go
// version: 1.3.0
// guid: 9a9b54c7-a9db-4d6e-ab51-67572f9cef60
// last-edited: 2026-10-24

package masterlist

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// All identifiers here are synthetic: the ISBNs are the textbook examples
// (checksums valid), the ASINs are fake.

func TestValidISBN10(t *testing.T) {
	for _, ok := range []string{"0306406152", "0-306-40615-2", " 0306 4061 52 ", "080442957X", "0-8044-2957-x"} {
		_, got := ValidISBN10(ok)
		assert.True(t, got, ok)
	}
	for _, bad := range []string{"0306406153", "030640615", "03064061522", "X306406152", "0306406X52", "", "B000000000"} {
		_, got := ValidISBN10(bad)
		assert.False(t, got, bad)
	}
}

func TestValidISBN13(t *testing.T) {
	for _, ok := range []string{"9780306406157", "978-0-306-40615-7", "9780804429573", "9791034347667"} {
		_, got := ValidISBN13(ok)
		assert.True(t, got, ok)
	}
	for _, bad := range []string{"9780306406158", "9770306406150", "978030640615", "97803064061570", "978030640615X", ""} {
		_, got := ValidISBN13(bad)
		assert.False(t, got, bad)
	}
}

func TestISBN10To13(t *testing.T) {
	got, ok := ISBN10To13("0306406152")
	require.True(t, ok)
	assert.Equal(t, "9780306406157", got)
	got, ok = ISBN10To13("0-8044-2957-X")
	require.True(t, ok)
	assert.Equal(t, "9780804429573", got)
	_, ok = ISBN10To13("0306406153")
	assert.False(t, ok, "a bad ISBN-10 does not convert")
	assert.Equal(t, "9780306406157", NormalizeISBN13("0306406152"))
	assert.Equal(t, "9780306406157", NormalizeISBN13("978-0-306-40615-7"))
	assert.Empty(t, NormalizeISBN13("9780306406158"))
}

func TestClassifyASIN(t *testing.T) {
	asin, isbn, ok := ClassifyASIN("b000000000")
	assert.True(t, ok)
	assert.Equal(t, "B000000000", asin)
	assert.Empty(t, isbn)

	// All digits and a valid ISBN-10: it is an ISBN-10.
	asin, isbn, ok = ClassifyASIN("0306406152")
	assert.True(t, ok)
	assert.Empty(t, asin)
	assert.Equal(t, "0306406152", isbn)

	// All digits and not an ISBN-10: it keeps the ASIN shape.
	asin, isbn, ok = ClassifyASIN("1234567890")
	assert.True(t, ok)
	assert.Equal(t, "1234567890", asin)
	assert.Empty(t, isbn)

	for _, bad := range []string{"B00000000", "B0000000000", "B00000000!", ""} {
		_, _, ok = ClassifyASIN(bad)
		assert.False(t, ok, bad)
	}
}

func TestParseJSON_ValidatesAndKeepsIssues(t *testing.T) {
	rows, err := ParseJSON(strings.NewReader(`{"rows":[
	  {"title":"Example One","authors":["Jane Doe"],"asin":"b000000000","isbn10":"0-306-40615-2","status":"Finished",
	   "finished_at":"2026-01-02T03:04:05Z","runtime_min":"612","progress_pct":100,
	   "sources":[{"source":"Audible","source_id":"B000000000","purchased_at":"2025-12-01T00:00:00+02:00"}]},
	  {"title":"Example Two","isbn13":"9780306406158","asin":"short","finished_at":"2026-01-02 03:04:05","status":"done","progress_pct":140},
	  {"authors":"A; B | C"},
	  {"isbn":"0306406152"}
	]}`))
	require.NoError(t, err)
	require.Len(t, rows, 4)

	r := rows[0]
	assert.Equal(t, 1, r.Line)
	assert.Equal(t, "B000000000", r.ASIN)
	assert.Equal(t, "0306406152", r.ISBN10)
	assert.Equal(t, "9780306406157", r.ISBN13, "an ISBN-10 is also stored as its ISBN-13")
	assert.Equal(t, StatusFinished, r.Status)
	assert.Equal(t, 612, r.RuntimeMin)
	assert.Equal(t, 100, r.ProgressPct)
	assert.Empty(t, r.Issues)
	require.Len(t, r.Sources, 1)
	assert.Equal(t, "audible", r.Sources[0].Source)

	r = rows[1]
	assert.Empty(t, r.ISBN13, "a bad ISBN is never kept as an identifier")
	assert.Empty(t, r.ASIN)
	assert.Empty(t, r.FinishedAt, "a time with no zone is dropped, not guessed")
	assert.Empty(t, r.Status)
	codes := map[string]string{}
	for _, is := range r.Issues {
		codes[is.Code+":"+is.Field] = is.Value
	}
	assert.Equal(t, "9780306406158", codes["bad_identifier:isbn13"])
	assert.Equal(t, "short", codes["bad_identifier:asin"])
	assert.Equal(t, "2026-01-02 03:04:05", codes["bad_timestamp:finished_at"])
	assert.Equal(t, "done", codes["bad_status:status"])
	assert.Equal(t, "140", codes["bad_number:progress_pct"])
	assert.True(t, r.HasIdentity(), "it still has a title")

	r = rows[2]
	assert.Equal(t, []string{"A", "B", "C"}, r.Authors)
	assert.False(t, r.HasIdentity(), "no title and no identifier is invalid")

	r = rows[3]
	assert.Equal(t, "0306406152", r.ISBN10)
	assert.True(t, r.HasIdentity())
}

func TestParseJSON_ISBNConflictAndShapes(t *testing.T) {
	rows, err := ParseJSON(strings.NewReader(`[{"title":"X","isbn10":"0306406152","isbn13":"9780804429573"}]`))
	require.NoError(t, err)
	require.True(t, rows[0].HasIssue(IssueIdentifierConflict))
	assert.Equal(t, "9780804429573", rows[0].ISBN13, "the explicit ISBN-13 is kept")
	assert.Equal(t, "0306406152", rows[0].ISBN10)

	_, err = ParseJSON(strings.NewReader(`[]`))
	assert.ErrorIs(t, err, ErrNoRows)
	_, err = ParseJSON(strings.NewReader(`{"rows": 3}`))
	assert.Error(t, err)
	_, err = ParseJSON(strings.NewReader(`not json`))
	assert.Error(t, err)
	// A byte order mark in front is skipped.
	rows, err = ParseJSON(strings.NewReader("\xef\xbb\xbf" + `[{"title":"X"}]`))
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

func TestParseCSV(t *testing.T) {
	csv := "\xef\xbb\xbfTitle,Authors,ISBN-13,ASIN,Source,Source ID,Status,Progress Pct,Last Listened At\n" +
		"Example One,\"Doe, Jane; Roe, Rick\",978-0-306-40615-7,B000000000,audible,B000000000,in_progress,40,2026-02-03T04:05:06Z\n" +
		"\n" +
		"Example Two,,,,itunes,ABCDEF0123456789,,,\n"
	rows, err := ParseCSV(strings.NewReader(csv))
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, 2, rows[0].Line, "the header is line 1")
	assert.Equal(t, 4, rows[1].Line, "a blank line still counts")
	assert.Equal(t, []string{"Doe, Jane", "Roe, Rick"}, rows[0].Authors)
	assert.Equal(t, "9780306406157", rows[0].ISBN13)
	assert.Equal(t, StatusInProgress, rows[0].Status)
	assert.Equal(t, 40, rows[0].ProgressPct)
	require.Len(t, rows[1].Sources, 1)
	assert.Equal(t, "ABCDEF0123456789", rows[1].Sources[0].SourceID)

	_, err = ParseCSV(strings.NewReader("foo,bar\n1,2\n"))
	assert.ErrorIs(t, err, ErrNoColumns)
	_, err = ParseCSV(strings.NewReader("title\n"))
	assert.ErrorIs(t, err, ErrNoRows)
	_, err = ParseCSV(strings.NewReader(""))
	assert.ErrorIs(t, err, ErrNoRows)
}

func TestParse_RowCap(t *testing.T) {
	var b strings.Builder
	b.WriteString("title\n")
	for i := 0; i <= MaxRows; i++ {
		b.WriteString("t\n")
	}
	_, err := Parse(FormatCSV, strings.NewReader(b.String()))
	assert.ErrorIs(t, err, ErrTooManyRow)
	_, err = Parse("xml", strings.NewReader(""))
	assert.Error(t, err)
}

func TestFutureTimestamps(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	r := Row{Line: 3, FinishedAt: "2026-10-23T12:05:00Z", Sources: []Source{{LastListenedAt: "2027-01-01T00:00:00Z"}}}
	got := r.FutureTimestamps(now, 10*time.Minute)
	require.Len(t, got, 1, "five minutes ahead is inside the slack")
	assert.Equal(t, "last_listened_at", got[0].Field)
	assert.Equal(t, IssueFutureTimestamp, got[0].Code)
}

func TestCrossListKeys(t *testing.T) {
	r := Row{ASIN: "B000000000", ISBN13: "9780306406157", Sources: []Source{
		{Source: UnlabelledSource, SourceID: "17"}, {Source: "audible", SourceID: "XYZ"}, {Source: "itunes"}}}
	assert.Equal(t, []string{"asin:B000000000", "isbn13:9780306406157", "src:other:17", "src:audible:XYZ"}, IdentityKeys(r),
		"inside one list an unlabelled source id still joins rows")
	assert.Equal(t, []string{"asin:B000000000", "isbn13:9780306406157", "src:audible:XYZ"}, CrossListKeys(r),
		"across lists it does not: two lists' 17 are not one book")
	assert.Empty(t, CrossListKeys(Row{Sources: []Source{{Source: UnlabelledSource, SourceID: "17"}}}))
}

func TestNaturalKey(t *testing.T) {
	assert.Equal(t, "asin:B000000000", NaturalKey(Row{ASIN: "B000000000", ISBN13: "9780306406157"}))
	assert.Equal(t, "isbn13:9780306406157", NaturalKey(Row{ISBN13: "9780306406157"}))
	assert.Equal(t, "src:itunes:AB12", NaturalKey(Row{Sources: []Source{{Source: "itunes", SourceID: "AB12"}}}))
	assert.Empty(t, NaturalKey(Row{Title: "x"}))
}

func TestCollapse_JoinsOnSharedIdentifiers(t *testing.T) {
	rows := []Row{
		{Line: 2, Title: "Example One", ASIN: "B000000000", Status: StatusInProgress, ProgressPct: 30,
			Sources: []Source{{Source: "audible", SourceID: "B000000000", LastListenedAt: "2026-03-01T00:00:00Z"}}},
		{Line: 3, Title: "Other Book", ASIN: "B000000001"},
		// Same book as line 2 through the ISBN-10 of line 5 and the source id here.
		{Line: 4, Title: "Example One (iTunes)", ISBN13: "9780306406157", ISBN10: "0306406152",
			Sources: []Source{{Source: "itunes", SourceID: "PID1"}}, Status: StatusFinished, FinishedAt: "2026-04-01T00:00:00Z"},
		{Line: 5, ISBN13: "9780306406157", Sources: []Source{{Source: "audible", SourceID: "B000000000"}}},
	}
	groups := Collapse(rows)
	require.Len(t, groups, 2)
	assert.Equal(t, []int{0, 2, 3}, groups[0].Members, "transitive: the source id of line 5 joins lines 2 and 4")
	assert.Equal(t, []int{1}, groups[1].Members)

	g := groups[0].Row
	assert.Equal(t, 2, g.Line)
	assert.Equal(t, "Example One", g.Title, "the first title in input order")
	assert.Equal(t, "B000000000", g.ASIN)
	assert.Equal(t, "9780306406157", g.ISBN13)
	assert.Equal(t, StatusFinished, g.Status, "finished beats everything")
	assert.Equal(t, "2026-04-01T00:00:00Z", g.FinishedAt)
	assert.Equal(t, 30, g.ProgressPct, "progress is the maximum")
	assert.Len(t, g.Sources, 3, "every source is kept as provenance")
	assert.False(t, g.HasIssue(IssueListConflict))
}

func TestCollapse_NoJoinOnTitleAlone(t *testing.T) {
	groups := Collapse([]Row{
		{Line: 2, Title: "Same Title", Authors: []string{"A B"}},
		{Line: 3, Title: "Same Title", Authors: []string{"A B"}},
	})
	assert.Len(t, groups, 2, "two editions look like this; they are not auto-collapsed")
}

func TestCollapse_ConflictingIdentifiers(t *testing.T) {
	groups := Collapse([]Row{
		{Line: 2, ASIN: "B000000000", ISBN13: "9780306406157"},
		{Line: 3, ASIN: "B000000001", ISBN13: "9780306406157"},
	})
	require.Len(t, groups, 1)
	assert.True(t, groups[0].Row.HasIssue(IssueListConflict))
	assert.Equal(t, "B000000000", groups[0].Row.ASIN)
}

func TestCombineStatus(t *testing.T) {
	older := Source{Source: "audible", LastListenedAt: "2026-01-01T00:00:00Z"}
	newer := Source{Source: "itunes", LastListenedAt: "2026-02-01T00:00:00Z"}

	g := Combine([]Row{
		{Line: 1, Status: StatusInProgress, ProgressPct: 50, Sources: []Source{older}},
		{Line: 2, Status: StatusAbandoned, ProgressPct: 20, Sources: []Source{newer}},
	})
	assert.Equal(t, StatusAbandoned, g.Status, "the latest listen decides when nothing is finished")
	assert.Equal(t, 50, g.ProgressPct)

	g = Combine([]Row{
		{Line: 1, Status: StatusFinished, FinishedAt: "2026-05-01T00:00:00Z"},
		{Line: 2, Status: StatusFinished, FinishedAt: "2026-03-01T00:00:00Z"},
		{Line: 3, Status: StatusInProgress, Sources: []Source{newer}},
	})
	assert.Equal(t, StatusFinished, g.Status)
	assert.Equal(t, "2026-03-01T00:00:00Z", g.FinishedAt, "the earliest finish is the finish")

	g = Combine([]Row{{Line: 1, Status: StatusUnstarted}, {Line: 2, Status: StatusInProgress}})
	assert.Equal(t, StatusInProgress, g.Status, "with no times the further-along status wins")

	assert.Empty(t, Combine([]Row{{Line: 1}, {Line: 2}}).Status)
}

// strconv.ParseFloat reads "NaN" and "Inf" as numbers, and every comparison
// with NaN is false, so a range check written as "below 0 or above max" lets
// NaN through (and int(NaN+0.5) is the minimum int64 on amd64). Neither a
// non-finite CSV cell nor a non-finite numeric JSON string is a number here.
func TestNonFiniteNumbersAreBadNumbers(t *testing.T) {
	for _, bad := range []string{"NaN", "nan", "Inf", "+Inf", "-Inf", "Infinity", "-infinity"} {
		rows, err := ParseCSV(strings.NewReader("title,runtime_min,progress_pct,position_sec\nSynthetic Title," + bad + "," + bad + "," + bad + "\n"))
		require.NoError(t, err, bad)
		require.Len(t, rows, 1, bad)
		r := rows[0]
		got := map[string]bool{}
		for _, is := range r.Issues {
			if is.Code == IssueBadNumber {
				got[is.Field] = true
			}
		}
		assert.Equal(t, map[string]bool{"runtime_min": true, "progress_pct": true, "position_sec": true}, got, bad)
		assert.Zero(t, r.RuntimeMin, bad)
		assert.Zero(t, r.ProgressPct, bad)
		assert.Zero(t, r.PositionSec, bad)

		rows, err = ParseJSON(strings.NewReader(`[{"title":"Synthetic Title","runtime_min":"` + bad + `","progress_pct":"` + bad + `","position_sec":"` + bad + `"}]`))
		require.NoError(t, err, bad)
		require.Len(t, rows, 1, bad)
		got = map[string]bool{}
		for _, is := range rows[0].Issues {
			if is.Code == IssueBadNumber {
				got[is.Field] = true
			}
		}
		assert.Equal(t, map[string]bool{"runtime_min": true, "progress_pct": true, "position_sec": true}, got, "json "+bad)
		assert.Zero(t, rows[0].RuntimeMin, "json "+bad)
	}
	// A bare JSON number beyond float64 is not a number either.
	rows, err := ParseJSON(strings.NewReader(`[{"title":"Synthetic Title","runtime_min":1e999}]`))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.True(t, rows[0].HasIssue(IssueBadNumber))
	// An ordinary number is untouched.
	assert.Equal(t, 90, mustCSVRow(t, "title,runtime_min\nSynthetic Title,89.6\n").RuntimeMin)
}

func mustCSVRow(t *testing.T, csv string) Row {
	t.Helper()
	rows, err := ParseCSV(strings.NewReader(csv))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	return rows[0]
}

// An ISBN-10 whose check character is X is an ISBN-10 in the ASIN column too.
func TestClassifyASIN_ISBN10WithXCheckDigit(t *testing.T) {
	for _, in := range []string{"080442957X", "0-8044-2957-x"} {
		asin, isbn, ok := ClassifyASIN(in)
		assert.True(t, ok, in)
		assert.Empty(t, asin, in)
		assert.Equal(t, "080442957X", isbn, in)
	}
	// Nine digits and an X that fails the check sum is not an ISBN-10: it keeps
	// the ASIN shape.
	asin, isbn, ok := ClassifyASIN("030640615X")
	assert.True(t, ok)
	assert.Equal(t, "030640615X", asin)
	assert.Empty(t, isbn)
	// An X anywhere but last never makes an ISBN-10.
	asin, isbn, ok = ClassifyASIN("0306X06152")
	assert.True(t, ok)
	assert.Equal(t, "0306X06152", asin)
	assert.Empty(t, isbn)

	rows, err := ParseJSON(strings.NewReader(`[{"title":"X","asin":"080442957X"}]`))
	require.NoError(t, err)
	assert.Empty(t, rows[0].ASIN)
	assert.Equal(t, "080442957X", rows[0].ISBN10)
	assert.Equal(t, "9780804429573", rows[0].ISBN13)
	assert.Empty(t, rows[0].Issues)
}

// A generic isbn column that names another edition than the isbn10 / isbn13
// column of the same row is a finding, not a silent drop; the same edition
// spelled two ways is not.
func TestParseJSON_GenericISBNConflict(t *testing.T) {
	cases := []struct {
		name     string
		row      string
		conflict bool
		field    string
		isbn10   string
		isbn13   string
	}{
		{"generic 10 vs typed 10", `{"title":"X","isbn10":"0306406152","isbn":"080442957X"}`, true, "isbn", "0306406152", "9780306406157"},
		{"generic 13 vs typed 13", `{"title":"X","isbn13":"9780306406157","isbn":"9780804429573"}`, true, "isbn", "", "9780306406157"},
		{"generic 13 vs typed 10", `{"title":"X","isbn10":"0306406152","isbn":"9780804429573"}`, true, "isbn10", "0306406152", "9780804429573"},
		{"generic 10 vs typed 13", `{"title":"X","isbn13":"9780306406157","isbn":"080442957X"}`, true, "isbn10", "080442957X", "9780306406157"},
		{"asin column ISBN-10 vs typed 10", `{"title":"X","isbn10":"0306406152","asin":"080442957X"}`, true, "asin", "0306406152", "9780306406157"},
		{"generic 13 is the typed 10's edition", `{"title":"X","isbn10":"0306406152","isbn":"978-0-306-40615-7"}`, false, "", "0306406152", "9780306406157"},
		{"generic 10 is the typed 13's edition", `{"title":"X","isbn13":"9780306406157","isbn":"0-306-40615-2"}`, false, "", "0306406152", "9780306406157"},
		{"generic repeats the typed value", `{"title":"X","isbn10":"0306406152","isbn":"0306406152"}`, false, "", "0306406152", "9780306406157"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := ParseJSON(strings.NewReader("[" + tc.row + "]"))
			require.NoError(t, err)
			r := rows[0]
			assert.Equal(t, tc.isbn10, r.ISBN10)
			assert.Equal(t, tc.isbn13, r.ISBN13)
			if !tc.conflict {
				assert.False(t, r.HasIssue(IssueIdentifierConflict), "%+v", r.Issues)
				return
			}
			require.True(t, r.HasIssue(IssueIdentifierConflict), "%+v", r.Issues)
			var fields []string
			for _, is := range r.Issues {
				if is.Code == IssueIdentifierConflict {
					fields = append(fields, is.Field)
				}
			}
			assert.Equal(t, []string{tc.field}, fields)
		})
	}
}

// One row with a value of the wrong JSON type is a finding on that row; the
// file and the row's other fields survive.
func TestParseJSON_PerRowTypeErrors(t *testing.T) {
	rows, err := ParseJSON(strings.NewReader(`[
	  {"title":"Good","asin":"B000000000"},
	  {"title":"Bad authors","asin":"B000000001","authors":7,"sources":{"source":"x"}},
	  "not an object",
	  42,
	  {"title":{"x":1},"isbn13":"9780306406157"}
	]`))
	require.NoError(t, err)
	require.Len(t, rows, 5)

	assert.Empty(t, rows[0].Issues)

	r := rows[1]
	assert.Equal(t, "B000000001", r.ASIN, "the good identifier is kept")
	assert.Equal(t, "Bad authors", r.Title)
	assert.Empty(t, r.Authors)
	var bad []string
	for _, is := range r.Issues {
		require.Equal(t, IssueBadField, is.Code)
		require.Equal(t, 2, is.Line)
		bad = append(bad, is.Field)
	}
	assert.Equal(t, []string{"authors", "sources"}, bad, "both bad fields are named, in a stable order")

	for i, line := range map[int]int{2: 3, 3: 4} {
		r = rows[i]
		assert.Equal(t, line, r.Line)
		require.Len(t, r.Issues, 1)
		assert.Equal(t, IssueBadRow, r.Issues[0].Code)
		assert.False(t, r.HasIdentity(), "an entry that is not an object names no book")
	}

	r = rows[4]
	assert.Equal(t, "9780306406157", r.ISBN13)
	assert.Empty(t, r.Title)
	require.Len(t, r.Issues, 1)
	assert.Equal(t, IssueBadField, r.Issues[0].Code)
	assert.Equal(t, "title", r.Issues[0].Field)
	assert.Equal(t, 5, r.Issues[0].Line)

	// A file that is not a list at all still fails as a whole.
	_, err = ParseJSON(strings.NewReader(`{"rows": 3}`))
	assert.Error(t, err)
}
