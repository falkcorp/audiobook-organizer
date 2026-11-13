// file: core/log/logtest/journal.go
// version: 1.0.2
// guid: 5d7c1e94-0b3a-4e68-a2f7-9c4b8d6e1a30
// last-edited: 2026-11-13

// Package logtest is the test kit for implementations of package log's journal
// interfaces: JournalStoreSuite, the contract of log.JournalStore and its
// log.Importer (05-logging §10; one suite per interface and its importer,
// 03 §3.3). It imports log and the standard library only.
package logtest

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// Subject is one fresh, empty journal under test: the store and the importer
// over the same records. Both are required, because the importer's cases read
// back through the store.
type Subject struct {
	Store    log.JournalStore
	Importer log.Importer
}

// The case names, one subtest each, so a failure points at the rule.
const (
	CaseRoundTrip         = "append_query_round_trip"
	CaseNewestFirst       = "newest_first_equal_time_reverse_append"
	CaseFilters           = "filters"
	CaseCursor            = "cursor_pages_without_gap_or_repeat"
	CaseCursorWithFilter  = "cursor_with_filter"
	CasePrune             = "prune_bounded_oldest_first"
	CasePruneBoundary     = "prune_before_is_exclusive"
	CaseSinceUntilInMs    = "since_inclusive_until_exclusive_within_one_millisecond"
	CaseImportRoundTrip   = "import_round_trip"
	CaseImportIdempotent  = "import_twice_leaves_one_each"
	CaseImportOverlap     = "import_overlapping_batches"
	CaseImportDistinctIDs = "import_distinct_source_ids_same_time"
	CaseImportSortsByTime = "imported_sort_by_their_own_time_among_native"
	CaseEmptyAppend       = "empty_append_and_import"
	CaseCancelledContext  = "cancelled_context_writes_nothing"
)

// JournalStoreSuite runs the contract over the journal open returns, which it
// calls once per case (every case starts on an empty journal):
//
//   - Append then Query round-trips level, module, subsystem, message, time and
//     attributes (an attribute inside a group comes back under its dotted key;
//     numbers come back as numbers, strings as strings, bools as bools);
//   - Query is newest first by Record.Time, records with an equal Time in the
//     reverse of the order they were appended, Since is inclusive and Until
//     exclusive (each judged on the record's own Time, to the microsecond, not
//     on a rounded one), MinLevel and Module and Text filter, and a cursor pages the
//     result without a gap or a repeat, filtered or not;
//   - Prune deletes at most limit records older than before (exclusive),
//     oldest first, and reports whether older ones remain;
//   - Import writes each record once however often it is repeated: importing
//     the same ImportedRecords twice leaves one record each, two records of
//     one Time and different SourceIDs are two records, and imported records
//     sort by their own Time among native ones;
//   - a call whose context is already cancelled writes nothing.
func JournalStoreSuite(t *testing.T, open func(t testing.TB) Subject) {
	t.Helper()
	runSuite(tAdapter{t}, func(tt tester) Subject { return open(tt.tb()) })
}

// tester is the part of *testing.T the suite reports through, so the self
// test can run the suite against a recorder and assert which case failed.
type tester interface {
	Helper()
	Errorf(format string, args ...any)
	Run(name string, f func(tester)) bool
	tb() testing.TB
}

type tAdapter struct{ t *testing.T }

func (a tAdapter) Helper()                           { a.t.Helper() }
func (a tAdapter) Errorf(format string, args ...any) { a.t.Helper(); a.t.Errorf(format, args...) }
func (a tAdapter) Run(name string, f func(tester)) bool {
	return a.t.Run(name, func(t *testing.T) { f(tAdapter{t}) })
}
func (a tAdapter) tb() testing.TB { return a.t }

// epoch is the fixtures' clock: every record time is epoch() plus an offset.
func epoch() time.Time { return time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC) }

func at(d time.Duration) time.Time { return epoch().Add(d) }

func rec(d time.Duration, lv slog.Level, module, sub, msg string, attrs ...slog.Attr) log.Record {
	return log.Record{Time: at(d), Level: lv, Module: module, Subsystem: sub, Message: msg, Attrs: attrs}
}

func runSuite(tt tester, open func(tester) Subject) {
	tt.Helper()
	ctx := context.Background()
	cases := []struct {
		name string
		run  func(tt tester, ctx context.Context, s Subject)
	}{
		{CaseRoundTrip, caseRoundTrip},
		{CaseNewestFirst, caseNewestFirst},
		{CaseFilters, caseFilters},
		{CaseCursor, caseCursor},
		{CaseCursorWithFilter, caseCursorWithFilter},
		{CasePrune, casePrune},
		{CasePruneBoundary, casePruneBoundary},
		{CaseSinceUntilInMs, caseSinceUntilInMs},
		{CaseImportRoundTrip, caseImportRoundTrip},
		{CaseImportIdempotent, caseImportIdempotent},
		{CaseImportOverlap, caseImportOverlap},
		{CaseImportDistinctIDs, caseImportDistinctIDs},
		{CaseImportSortsByTime, caseImportSortsByTime},
		{CaseEmptyAppend, caseEmptyAppend},
		{CaseCancelledContext, caseCancelledContext},
	}
	for _, c := range cases {
		tt.Run(c.name, func(tt tester) {
			s := open(tt)
			if s.Store == nil || s.Importer == nil {
				tt.Errorf("open returned a Subject without a Store and an Importer")
				return
			}
			c.run(tt, ctx, s)
		})
	}
}

// all is a query that sees every level: the zero MinLevel is LevelInfo.
func all() log.JournalQuery { return log.JournalQuery{MinLevel: log.LevelTrace, Limit: 1000} }

// drain pages q to the end and returns every record in the order served.
func drain(tt tester, ctx context.Context, st log.JournalStore, q log.JournalQuery) []log.Record {
	tt.Helper()
	var out []log.Record
	for range 10000 {
		p, err := st.Query(ctx, q)
		if err != nil {
			tt.Errorf("Query: %v", err)
			return out
		}
		out = append(out, p.Records...)
		if p.Next == "" {
			return out
		}
		q.Cursor = p.Next
	}
	tt.Errorf("Query never reached the end of the journal")
	return out
}

// messages are the records' messages, in order.
func messages(recs []log.Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Message
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func wantMessages(tt tester, what string, recs []log.Record, want ...string) {
	tt.Helper()
	if got := messages(recs); !sameStrings(got, want) {
		tt.Errorf("%s: got %q, want %q", what, got, want)
	}
}

// attrString renders an attribute as key=kind:value, a number as a number
// whatever its slog kind, so a store that keeps 3 as an int64 or a float64
// passes and one that turns it into text does not.
func attrString(a slog.Attr) string {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindInt64:
		return fmt.Sprintf("%s=num:%v", a.Key, float64(v.Int64()))
	case slog.KindUint64:
		return fmt.Sprintf("%s=num:%v", a.Key, float64(v.Uint64()))
	case slog.KindFloat64:
		return fmt.Sprintf("%s=num:%v", a.Key, v.Float64())
	case slog.KindBool:
		return fmt.Sprintf("%s=bool:%v", a.Key, v.Bool())
	case slog.KindString:
		return fmt.Sprintf("%s=str:%s", a.Key, v.String())
	default:
		return fmt.Sprintf("%s=other(%v):%v", a.Key, v.Kind(), v.Any())
	}
}

func attrStrings(attrs []slog.Attr) []string {
	out := make([]string, len(attrs))
	for i, a := range attrs {
		out[i] = attrString(a)
	}
	return out
}

// sameRecord reports the first difference between a stored record and what was
// appended, naming the field.
func sameRecord(got, want log.Record, wantAttrs []string) string {
	switch {
	case !got.Time.Equal(want.Time):
		return fmt.Sprintf("Time %v, want %v", got.Time, want.Time)
	case got.Level != want.Level:
		return fmt.Sprintf("Level %v, want %v", got.Level, want.Level)
	case got.Module != want.Module:
		return fmt.Sprintf("Module %q, want %q", got.Module, want.Module)
	case got.Subsystem != want.Subsystem:
		return fmt.Sprintf("Subsystem %q, want %q", got.Subsystem, want.Subsystem)
	case got.Message != want.Message:
		return fmt.Sprintf("Message %q, want %q", got.Message, want.Message)
	case got.TraceID != want.TraceID:
		return fmt.Sprintf("TraceID %q, want %q", got.TraceID, want.TraceID)
	case got.SpanID != want.SpanID:
		return fmt.Sprintf("SpanID %q, want %q", got.SpanID, want.SpanID)
	}
	if g := attrStrings(got.Attrs); !sameStrings(g, wantAttrs) {
		return fmt.Sprintf("Attrs %q, want %q", g, wantAttrs)
	}
	return ""
}

func caseRoundTrip(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	// Nanosecond precision survives: the ULID of a native key holds only the
	// millisecond, so the time itself must be stored.
	t0 := epoch().Add(123456789 * time.Nanosecond)
	in := []log.Record{
		{Time: t0, Level: log.LevelTrace, Module: "core", Subsystem: "tap", Message: "the tap is lit"},
		{Time: t0.Add(time.Second), Level: log.LevelDebug, Module: "core", Subsystem: "kernel", Message: "boot step"},
		{Time: t0.Add(2 * time.Second), Level: log.LevelInfo, Module: "books", Subsystem: "scanner", Message: "scan started",
			Attrs:   []slog.Attr{slog.String("path", "/gotham/library"), slog.Int("files", 42), slog.Bool("dry_run", true), slog.Float64("ratio", 0.5)},
			TraceID: "0af7651916cd43dd8448eb211c80319c", SpanID: "b7ad6b7169203331"},
		{Time: t0.Add(3 * time.Second), Level: log.LevelWarn, Module: "books", Subsystem: "scanner", Message: "slow disk",
			Attrs: []slog.Attr{slog.Group("http", slog.Int("status", 503), slog.Group("retry", slog.Bool("final", false)))}},
		{Time: t0.Add(4 * time.Second), Level: log.LevelError, Module: "", Message: "no module, no subsystem"},
	}
	if err := s.Store.Append(ctx, in); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	got := drain(tt, ctx, s.Store, all())
	if len(got) != len(in) {
		tt.Errorf("Query returned %d records, want %d", len(got), len(in))
		return
	}
	// Newest first: got[0] is the last record appended.
	wantAttrs := [][]string{
		nil,
		nil,
		{"path=str:/gotham/library", "files=num:42", "dry_run=bool:true", "ratio=num:0.5"},
		{"http.status=num:503", "http.retry.final=bool:false"},
		nil,
	}
	for i := range in {
		g := got[len(in)-1-i]
		if d := sameRecord(g, in[i], wantAttrs[i]); d != "" {
			tt.Errorf("record %q: %s", in[i].Message, d)
		}
	}
}

func caseNewestFirst(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	if err := s.Store.Append(ctx, []log.Record{
		rec(2*time.Second, log.LevelInfo, "core", "", "second"),
		rec(0, log.LevelInfo, "core", "", "first"),
		rec(time.Second, log.LevelInfo, "core", "", "middle-a"),
		rec(time.Second, log.LevelInfo, "core", "", "middle-b"),
	}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	// A later Append at an instant already stored comes back before it.
	if err := s.Store.Append(ctx, []log.Record{rec(time.Second, log.LevelInfo, "core", "", "middle-c")}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	wantMessages(tt, "newest first", drain(tt, ctx, s.Store, all()), "second", "middle-c", "middle-b", "middle-a", "first")
}

func caseFilters(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	if err := s.Store.Append(ctx, []log.Record{
		rec(0, log.LevelDebug, "core", "kernel", "boot DEBUG step"),
		rec(1*time.Second, log.LevelInfo, "books", "scanner", "Scan started"),
		rec(2*time.Second, log.LevelWarn, "books", "scanner", "disk is slow"),
		rec(3*time.Second, log.LevelError, "core", "tasks", "the run FAILED"),
		rec(4*time.Second, log.LevelInfo, "core", "tasks", "run scan done"),
	}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	q := func(f func(*log.JournalQuery)) []log.Record {
		jq := all()
		f(&jq)
		return drain(tt, ctx, s.Store, jq)
	}
	wantMessages(tt, "no filter", q(func(*log.JournalQuery) {}),
		"run scan done", "the run FAILED", "disk is slow", "Scan started", "boot DEBUG step")
	wantMessages(tt, "MinLevel warn", q(func(j *log.JournalQuery) { j.MinLevel = log.LevelWarn }), "the run FAILED", "disk is slow")
	wantMessages(tt, "zero MinLevel is info", drain(tt, ctx, s.Store, log.JournalQuery{Limit: 1000}),
		"run scan done", "the run FAILED", "disk is slow", "Scan started")
	wantMessages(tt, "Module books", q(func(j *log.JournalQuery) { j.Module = "books" }), "disk is slow", "Scan started")
	wantMessages(tt, "Text is a case-insensitive substring", q(func(j *log.JournalQuery) { j.Text = "SCAN" }), "run scan done", "Scan started")
	wantMessages(tt, "Module and Text and MinLevel together",
		q(func(j *log.JournalQuery) { j.Module = "core"; j.Text = "run"; j.MinLevel = log.LevelError }), "the run FAILED")
	wantMessages(tt, "Since is inclusive", q(func(j *log.JournalQuery) { j.Since = at(3 * time.Second) }), "run scan done", "the run FAILED")
	wantMessages(tt, "Until is exclusive", q(func(j *log.JournalQuery) { j.Until = at(2 * time.Second) }), "Scan started", "boot DEBUG step")
	wantMessages(tt, "Since and Until", q(func(j *log.JournalQuery) { j.Since = at(time.Second); j.Until = at(4 * time.Second) }),
		"the run FAILED", "disk is slow", "Scan started")
	wantMessages(tt, "no match", q(func(j *log.JournalQuery) { j.Text = "joker" }))
}

// pageSet is 25 records one second apart, "r00" oldest to "r24" newest, every
// third one a warning.
func pageSet() []log.Record {
	var in []log.Record
	for i := range 25 {
		lv := log.LevelInfo
		if i%3 == 0 {
			lv = log.LevelWarn
		}
		in = append(in, rec(time.Duration(i)*time.Second, lv, "books", "scanner", fmt.Sprintf("r%02d", i)))
	}
	return in
}

func newestFirst(in []log.Record, keep func(log.Record) bool) []string {
	var out []string
	for _, r := range slices.Backward(in) {
		if keep == nil || keep(r) {
			out = append(out, r.Message)
		}
	}
	return out
}

func caseCursor(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	in := pageSet()
	if err := s.Store.Append(ctx, in); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	q := all()
	q.Limit = 10
	var sizes []int
	var got []string
	for range 10 {
		p, err := s.Store.Query(ctx, q)
		if err != nil {
			tt.Errorf("Query: %v", err)
			return
		}
		sizes = append(sizes, len(p.Records))
		got = append(got, messages(p.Records)...)
		if p.Next == "" {
			break
		}
		q.Cursor = p.Next
	}
	if !sameStrings(got, newestFirst(in, nil)) {
		tt.Errorf("pages joined: got %q, want %q", got, newestFirst(in, nil))
	}
	if len(sizes) != 3 || sizes[0] != 10 || sizes[1] != 10 || sizes[2] != 5 {
		tt.Errorf("page sizes = %v, want [10 10 5]", sizes)
	}
	// An exact multiple of the limit ends with an empty Next, not an empty page.
	q = all()
	q.Limit = 25
	p, err := s.Store.Query(ctx, q)
	if err != nil || len(p.Records) != 25 || p.Next != "" {
		tt.Errorf("Limit equal to the count: %d records, next %q, err %v; want 25 records and no next", len(p.Records), p.Next, err)
	}
	// A cursor taken before a newer record arrives still continues from where
	// it was: nothing older is skipped or repeated.
	q = all()
	q.Limit = 10
	first, err := s.Store.Query(ctx, q)
	if err != nil || first.Next == "" {
		tt.Errorf("first page: next %q, err %v", first.Next, err)
		return
	}
	if err := s.Store.Append(ctx, []log.Record{rec(time.Hour, log.LevelInfo, "books", "scanner", "late arrival")}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	q.Cursor = first.Next
	rest := drain(tt, ctx, s.Store, q)
	wantMessages(tt, "after the first page", rest, newestFirst(in, nil)[10:]...)
}

func caseCursorWithFilter(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	in := pageSet()
	if err := s.Store.Append(ctx, in); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	// 9 of the 25 are warnings; pages of 4 must still join to all 9.
	q := all()
	q.MinLevel = log.LevelWarn
	q.Limit = 4
	got := drain(tt, ctx, s.Store, q)
	want := newestFirst(in, func(r log.Record) bool { return r.Level >= log.LevelWarn })
	wantMessages(tt, "warnings paged by 4", got, want...)
	q = all()
	q.Text = "r1"
	q.Limit = 3
	wantMessages(tt, "text r1 paged by 3", drain(tt, ctx, s.Store, q),
		newestFirst(in, func(r log.Record) bool { return strings.Contains(r.Message, "r1") })...)
}

func casePrune(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	var in []log.Record
	for i := range 10 {
		in = append(in, rec(time.Duration(i)*time.Minute, log.LevelInfo, "core", "", fmt.Sprintf("old%02d", i)))
	}
	for i := range 5 {
		in = append(in, rec(time.Hour+time.Duration(i)*time.Minute, log.LevelInfo, "core", "", fmt.Sprintf("new%02d", i)))
	}
	if err := s.Store.Append(ctx, in); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	cut := at(30 * time.Minute)
	// Bounded and oldest first: 4 of the 10 old ones, the 4 oldest.
	n, more, err := s.Store.Prune(ctx, cut, 4)
	if err != nil || n != 4 || !more {
		tt.Errorf("Prune(limit 4) = %d, %v, %v; want 4, true, nil", n, more, err)
	}
	rest := messages(drain(tt, ctx, s.Store, all()))
	if want := []string{"new04", "new03", "new02", "new01", "new00", "old09", "old08", "old07", "old06", "old05", "old04"}; !sameStrings(rest, want) {
		tt.Errorf("after the first prune: %q, want %q (the four oldest gone)", rest, want)
	}
	n, more, err = s.Store.Prune(ctx, cut, 4)
	if err != nil || n != 4 || !more {
		tt.Errorf("second Prune(limit 4) = %d, %v, %v; want 4, true, nil", n, more, err)
	}
	// The last two: fewer than limit, nothing older remains.
	n, more, err = s.Store.Prune(ctx, cut, 4)
	if err != nil || n != 2 || more {
		tt.Errorf("third Prune(limit 4) = %d, %v, %v; want 2, false, nil", n, more, err)
	}
	n, more, err = s.Store.Prune(ctx, cut, 4)
	if err != nil || n != 0 || more {
		tt.Errorf("a Prune with nothing older = %d, %v, %v; want 0, false, nil", n, more, err)
	}
	wantMessages(tt, "the new ones survive", drain(tt, ctx, s.Store, all()), "new04", "new03", "new02", "new01", "new00")
	// A limit that holds exactly what is left reports no more.
	n, more, err = s.Store.Prune(ctx, at(24*time.Hour), 5)
	if err != nil || n != 5 || more {
		tt.Errorf("Prune(limit == remaining) = %d, %v, %v; want 5, false, nil", n, more, err)
	}
}

func casePruneBoundary(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	// Three records inside one millisecond: a key built from the millisecond
	// cannot tell them apart, so before is judged on the record's own time.
	base := epoch().Add(10 * time.Millisecond)
	if err := s.Store.Append(ctx, []log.Record{
		{Time: base.Add(100 * time.Microsecond), Level: log.LevelInfo, Module: "core", Message: "before"},
		{Time: base.Add(500 * time.Microsecond), Level: log.LevelInfo, Module: "core", Message: "exactly"},
		{Time: base.Add(900 * time.Microsecond), Level: log.LevelInfo, Module: "core", Message: "after"},
		rec(time.Hour, log.LevelInfo, "core", "", "much later"),
	}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	n, more, err := s.Store.Prune(ctx, base.Add(500*time.Microsecond), 100)
	if err != nil || n != 1 || more {
		tt.Errorf("Prune(before = exactly) = %d, %v, %v; want 1, false, nil", n, more, err)
	}
	wantMessages(tt, "before is exclusive", drain(tt, ctx, s.Store, all()), "much later", "after", "exactly")
	if n, more, err = s.Store.Prune(ctx, at(0), 100); err != nil || n != 0 || more {
		tt.Errorf("Prune(before the oldest) = %d, %v, %v; want 0, false, nil", n, more, err)
	}
	if n, more, err = s.Store.Prune(ctx, at(24*time.Hour), 0); err != nil || n != 0 || !more {
		tt.Errorf("Prune(limit 0) = %d, %v, %v; want 0, true, nil: it deletes nothing and says records remain", n, more, err)
	}
}

func caseSinceUntilInMs(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	// Three records inside one millisecond, one a millisecond either side: a
	// filter that compared milliseconds could not tell the three apart, so Since
	// and Until are judged on the record's own time.
	base := epoch().Add(20 * time.Millisecond)
	if err := s.Store.Append(ctx, []log.Record{
		{Time: base.Add(-time.Millisecond), Level: log.LevelInfo, Module: "core", Message: "early"},
		{Time: base.Add(100 * time.Microsecond), Level: log.LevelInfo, Module: "core", Message: "before"},
		{Time: base.Add(500 * time.Microsecond), Level: log.LevelInfo, Module: "core", Message: "exactly"},
		{Time: base.Add(900 * time.Microsecond), Level: log.LevelInfo, Module: "core", Message: "after"},
		{Time: base.Add(time.Millisecond), Level: log.LevelInfo, Module: "core", Message: "late"},
	}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	q := func(f func(*log.JournalQuery)) log.JournalQuery { j := all(); f(&j); return j }
	mid := base.Add(500 * time.Microsecond)
	wantMessages(tt, "Since is inclusive, to the microsecond",
		drain(tt, ctx, s.Store, q(func(j *log.JournalQuery) { j.Since = mid })), "late", "after", "exactly")
	wantMessages(tt, "Until is exclusive, to the microsecond",
		drain(tt, ctx, s.Store, q(func(j *log.JournalQuery) { j.Until = mid })), "before", "early")
	wantMessages(tt, "Since and Until inside one millisecond",
		drain(tt, ctx, s.Store, q(func(j *log.JournalQuery) {
			j.Since = base.Add(100 * time.Microsecond)
			j.Until = base.Add(900 * time.Microsecond)
		})), "exactly", "before")
}

func imported(src string, r log.Record) log.ImportedRecord {
	return log.ImportedRecord{Record: r, SourceID: src}
}

func caseImportRoundTrip(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	in := []log.ImportedRecord{
		imported("src:a", log.Record{Time: epoch().Add(7*time.Second + 123456789), Level: log.LevelWarn, Module: "books", Subsystem: "scanner", Message: "imported warning",
			Attrs: []slog.Attr{slog.String("user_id", "bruce-wayne")}}),
		imported("src:b", rec(8*time.Second, log.LevelInfo, "books", "organizer", "imported info")),
	}
	if err := s.Importer.Import(ctx, in); err != nil {
		tt.Errorf("Import: %v", err)
		return
	}
	got := drain(tt, ctx, s.Store, all())
	if len(got) != 2 {
		tt.Errorf("Query returned %d records after importing 2, want 2", len(got))
		return
	}
	if d := sameRecord(got[1], in[0].Record, []string{"user_id=str:bruce-wayne"}); d != "" {
		tt.Errorf("imported record %q: %s", in[0].Message, d)
	}
	if d := sameRecord(got[0], in[1].Record, nil); d != "" {
		tt.Errorf("imported record %q: %s", in[1].Message, d)
	}
	// An imported record is an ordinary one to Query's filters.
	q := all()
	q.Module = "books"
	q.Text = "WARNING"
	wantMessages(tt, "filter over an imported record", drain(tt, ctx, s.Store, q), "imported warning")
}

func caseImportIdempotent(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	in := []log.ImportedRecord{
		imported("src:1", rec(1*time.Second, log.LevelInfo, "books", "scanner", "one")),
		imported("src:2", rec(2*time.Second, log.LevelWarn, "books", "scanner", "two")),
		imported("src:3", rec(3*time.Second, log.LevelError, "books", "scanner", "three")),
	}
	for i := range 3 {
		if err := s.Importer.Import(ctx, in); err != nil {
			tt.Errorf("Import #%d: %v", i+1, err)
			return
		}
	}
	wantMessages(tt, "after importing the same records three times", drain(tt, ctx, s.Store, all()), "three", "two", "one")
}

func caseImportOverlap(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	a := imported("src:a", rec(1*time.Second, log.LevelInfo, "books", "", "a"))
	b := imported("src:b", rec(2*time.Second, log.LevelInfo, "books", "", "b"))
	c := imported("src:c", rec(3*time.Second, log.LevelInfo, "books", "", "c"))
	// A step that crashed between an import and its deletes re-imports part of
	// what it wrote.
	for _, batch := range [][]log.ImportedRecord{{a, b}, {b, c}} {
		if err := s.Importer.Import(ctx, batch); err != nil {
			tt.Errorf("Import: %v", err)
			return
		}
	}
	wantMessages(tt, "overlapping batches", drain(tt, ctx, s.Store, all()), "c", "b", "a")
}

func caseImportDistinctIDs(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	// Two rows written in one instant by two sources are two records: the key
	// is derived from the time and the source ID, not the time alone.
	if err := s.Importer.Import(ctx, []log.ImportedRecord{
		imported("src:t:scanner", rec(time.Second, log.LevelInfo, "books", "scanner", "from the scanner")),
		imported("src:t:organizer", rec(time.Second, log.LevelInfo, "books", "organizer", "from the organizer")),
	}); err != nil {
		tt.Errorf("Import: %v", err)
		return
	}
	got := messages(drain(tt, ctx, s.Store, all()))
	if len(got) != 2 {
		tt.Errorf("two sources at one instant gave %q, want both records", got)
	}
}

func caseImportSortsByTime(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	if err := s.Store.Append(ctx, []log.Record{
		rec(10*time.Second, log.LevelInfo, "core", "", "native 10"),
		rec(30*time.Second, log.LevelInfo, "core", "", "native 30"),
	}); err != nil {
		tt.Errorf("Append: %v", err)
		return
	}
	if err := s.Importer.Import(ctx, []log.ImportedRecord{
		imported("src:5", rec(5*time.Second, log.LevelInfo, "books", "", "imported 5")),
		imported("src:20", rec(20*time.Second, log.LevelInfo, "books", "", "imported 20")),
		imported("src:40", rec(40*time.Second, log.LevelInfo, "books", "", "imported 40")),
	}); err != nil {
		tt.Errorf("Import: %v", err)
		return
	}
	// Imported after the native ones were written, yet sorted by their own time.
	wantMessages(tt, "imported among native", drain(tt, ctx, s.Store, all()),
		"imported 40", "native 30", "imported 20", "native 10", "imported 5")
	// Prune sees an imported record's own time too.
	n, _, err := s.Store.Prune(ctx, at(15*time.Second), 10)
	if err != nil || n != 2 {
		tt.Errorf("Prune(before 15s) = %d, %v; want the two records older than that (one native, one imported)", n, err)
	}
	wantMessages(tt, "after pruning", drain(tt, ctx, s.Store, all()), "imported 40", "native 30", "imported 20")
}

func caseEmptyAppend(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	if err := s.Store.Append(ctx, nil); err != nil {
		tt.Errorf("Append(nil): %v", err)
	}
	if err := s.Importer.Import(ctx, nil); err != nil {
		tt.Errorf("Import(nil): %v", err)
	}
	p, err := s.Store.Query(ctx, all())
	if err != nil || len(p.Records) != 0 || p.Next != "" {
		tt.Errorf("Query of an empty journal = %d records, next %q, err %v; want none", len(p.Records), p.Next, err)
	}
}

func caseCancelledContext(tt tester, ctx context.Context, s Subject) {
	tt.Helper()
	dead, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Store.Append(dead, []log.Record{rec(0, log.LevelInfo, "core", "", "never written")}); err == nil {
		tt.Errorf("Append with a cancelled context returned nil")
	}
	if err := s.Importer.Import(dead, []log.ImportedRecord{imported("src:x", rec(0, log.LevelInfo, "books", "", "never imported"))}); err == nil {
		tt.Errorf("Import with a cancelled context returned nil")
	}
	wantMessages(tt, "after cancelled writes", drain(tt, ctx, s.Store, all()))
}
