// file: tools/cmd/orphan-nonprimary-census/main_test.go
// version: 1.1.0
// guid: 9d4b8a2e-1f6c-4a3d-8e5b-2c7f0a9d3e61
// last-edited: 2026-08-23
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func ptrStr(s string) *string        { return &s }
func ptrBool(b bool) *bool           { return &b }
func ptrTime(t time.Time) *time.Time { return &t }

// TestSelectsOnlyExplicitlyNonPrimaryUngrouped is table-driven over in-memory
// book rows, asserting the predicate matches ONLY (VersionGroupID nil/empty
// AND IsPrimaryVersion != nil AND *IsPrimaryVersion == false) and in
// particular does NOT match a book with IsPrimaryVersion == nil (which
// internal/audiobooks/service_filtering.go:864 treats as primary) nor one
// that has a version group. The predicate is extracted into a testable
// function (isOrphanNonPrimary) so the census logic is verifiable without a
// live store.
func TestSelectsOnlyExplicitlyNonPrimaryUngrouped(t *testing.T) {
	cases := []struct {
		name string
		b    book
		want bool
	}{
		{
			name: "target population: no group, explicitly false",
			b:    book{ID: "1", VersionGroupID: nil, IsPrimaryVersion: ptrBool(false)},
			want: true,
		},
		{
			name: "target population: empty-string group, explicitly false",
			b:    book{ID: "2", VersionGroupID: ptrStr(""), IsPrimaryVersion: ptrBool(false)},
			want: true,
		},
		{
			name: "target population: whitespace-only group counts as empty",
			b:    book{ID: "3", VersionGroupID: ptrStr("   "), IsPrimaryVersion: ptrBool(false)},
			want: true,
		},
		{
			name: "excluded: nil flag is treated as primary elsewhere, must NOT count as non-primary",
			b:    book{ID: "4", VersionGroupID: nil, IsPrimaryVersion: nil},
			want: false,
		},
		{
			name: "excluded: has a version group, even though explicitly false",
			b:    book{ID: "5", VersionGroupID: ptrStr("grp-123"), IsPrimaryVersion: ptrBool(false)},
			want: false,
		},
		{
			name: "excluded: explicitly true and no group",
			b:    book{ID: "6", VersionGroupID: nil, IsPrimaryVersion: ptrBool(true)},
			want: false,
		},
		{
			name: "excluded: has a group and nil flag",
			b:    book{ID: "7", VersionGroupID: ptrStr("grp-456"), IsPrimaryVersion: nil},
			want: false,
		},
		{
			name: "excluded: has a group and explicitly true",
			b:    book{ID: "8", VersionGroupID: ptrStr("grp-789"), IsPrimaryVersion: ptrBool(true)},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isOrphanNonPrimary(tc.b)
			if got != tc.want {
				t.Errorf("isOrphanNonPrimary(%+v) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

// TestCensusMatchesExactSubset builds a fixture containing a known mix
// (explicit-false + ungrouped, explicit-false + grouped, explicit-true,
// nil-flagged) and asserts censusMatches reports exactly the expected
// subset of book IDs — proving the tool can find something real rather than
// silently returning nothing on a broken predicate. A census that returns 0
// on a broken query looks identical to a clean library, so this asserts the
// positive case explicitly by ID, not just by count.
func TestCensusMatchesExactSubset(t *testing.T) {
	now := time.Now()
	books := []book{
		{ID: "orphan-1", Title: "Orphan One", VersionGroupID: nil, IsPrimaryVersion: ptrBool(false), CreatedAt: ptrTime(now)},
		{ID: "orphan-2", Title: "Orphan Two", VersionGroupID: ptrStr(""), IsPrimaryVersion: ptrBool(false), CreatedAt: ptrTime(now)},
		{ID: "grouped-nonprimary", Title: "Grouped Non-Primary", VersionGroupID: ptrStr("grp-1"), IsPrimaryVersion: ptrBool(false)},
		{ID: "grouped-primary", Title: "Grouped Primary", VersionGroupID: ptrStr("grp-1"), IsPrimaryVersion: ptrBool(true)},
		{ID: "explicit-primary", Title: "Explicit Primary", VersionGroupID: nil, IsPrimaryVersion: ptrBool(true)},
		{ID: "nil-flagged", Title: "Nil Flagged", VersionGroupID: nil, IsPrimaryVersion: nil},
		{ID: "nil-flagged-grouped", Title: "Nil Flagged Grouped", VersionGroupID: ptrStr("grp-2"), IsPrimaryVersion: nil},
	}

	got := censusMatches(books)

	wantIDs := map[string]bool{"orphan-1": true, "orphan-2": true}
	if len(got) != len(wantIDs) {
		t.Fatalf("censusMatches returned %d matches, want %d: %+v", len(got), len(wantIDs), got)
	}
	for _, b := range got {
		if !wantIDs[b.ID] {
			t.Errorf("censusMatches unexpectedly matched book %q", b.ID)
		}
		delete(wantIDs, b.ID)
	}
	if len(wantIDs) != 0 {
		t.Errorf("censusMatches missed expected IDs: %v", wantIDs)
	}
}

// TestCheckPositiveControl asserts the scan fails loudly when it examined
// suspiciously few books, rather than letting a broken fetch masquerade as
// a truthful "0 orphans found" on a clean library.
func TestCheckPositiveControl(t *testing.T) {
	t.Run("below threshold fails loudly", func(t *testing.T) {
		err := checkPositiveControl(3, 1000)
		if err == nil {
			t.Fatal("expected an error when examined count is far below min-expected, got nil")
		}
		if !strings.Contains(err.Error(), "examined only 3") {
			t.Errorf("error message does not name the examined count: %q", err.Error())
		}
	})

	t.Run("at or above threshold passes", func(t *testing.T) {
		if err := checkPositiveControl(1000, 1000); err != nil {
			t.Errorf("expected no error at exactly the threshold, got: %v", err)
		}
		if err := checkPositiveControl(50000, 1000); err != nil {
			t.Errorf("expected no error well above the threshold, got: %v", err)
		}
	})

	t.Run("disabled when min-expected is zero or negative", func(t *testing.T) {
		if err := checkPositiveControl(0, 0); err != nil {
			t.Errorf("expected the guard to be disabled at min-expected=0, got: %v", err)
		}
		if err := checkPositiveControl(0, -5); err != nil {
			t.Errorf("expected the guard to be disabled at negative min-expected, got: %v", err)
		}
	})
}

// TestCheckFieldPresence asserts the wire-format regression guard: it fails
// loudly only when is_primary_version was nil on every single examined book
// (the signature of the tri-state field silently failing to round-trip),
// and stays quiet for a normal mix, including the case where NO book has a
// version_group_id (which is legitimate — most books have no group — unlike
// is_primary_version being universally nil).
func TestCheckFieldPresence(t *testing.T) {
	t.Run("all nil is_primary_version fails loudly", func(t *testing.T) {
		books := []book{
			{ID: "1", IsPrimaryVersion: nil, VersionGroupID: nil},
			{ID: "2", IsPrimaryVersion: nil, VersionGroupID: ptrStr("grp-1")},
		}
		c := countFieldPresence(books)
		err := checkFieldPresence(c)
		if err == nil {
			t.Fatal("expected an error when is_primary_version is nil on every examined book, got nil")
		}
		if !strings.Contains(err.Error(), "all 2 examined books") {
			t.Errorf("error message does not name the examined count: %q", err.Error())
		}
	})

	t.Run("a real mix passes even with zero grouped books", func(t *testing.T) {
		books := []book{
			{ID: "1", IsPrimaryVersion: ptrBool(true), VersionGroupID: nil},
			{ID: "2", IsPrimaryVersion: ptrBool(false), VersionGroupID: nil},
			{ID: "3", IsPrimaryVersion: nil, VersionGroupID: nil},
		}
		c := countFieldPresence(books)
		if c.withVersionGroup != 0 {
			t.Fatalf("fixture setup error: expected 0 grouped books, got %d", c.withVersionGroup)
		}
		if err := checkFieldPresence(c); err != nil {
			t.Errorf("expected no error for a real mix of is_primary_version values, got: %v", err)
		}
	})

	t.Run("zero examined books is checkPositiveControl's job, not an error here", func(t *testing.T) {
		c := countFieldPresence(nil)
		if err := checkFieldPresence(c); err != nil {
			t.Errorf("expected no error on zero examined books (positive control handles that), got: %v", err)
		}
	})
}

// TestCountFieldPresence asserts the presence tally counts non-nil pointers
// regardless of their pointed-to value (an explicit empty-string
// VersionGroupID or explicit-false IsPrimaryVersion both count as present).
func TestCountFieldPresence(t *testing.T) {
	books := []book{
		{ID: "1", VersionGroupID: nil, IsPrimaryVersion: nil},
		{ID: "2", VersionGroupID: ptrStr(""), IsPrimaryVersion: ptrBool(false)},
		{ID: "3", VersionGroupID: ptrStr("grp-1"), IsPrimaryVersion: ptrBool(true)},
	}
	c := countFieldPresence(books)
	if c.examined != 3 {
		t.Errorf("examined = %d, want 3", c.examined)
	}
	if c.withVersionGroup != 2 {
		t.Errorf("withVersionGroup = %d, want 2", c.withVersionGroup)
	}
	if c.withPrimaryFlag != 2 {
		t.Errorf("withPrimaryFlag = %d, want 2", c.withPrimaryFlag)
	}
}

// TestCSVOutputColumns asserts the CSV writer emits exactly the documented
// columns and formats pointer fields as empty strings when nil, so a reader
// correlating CreatedAt/UpdatedAt against job runs gets a stable schema.
func TestCSVOutputColumns(t *testing.T) {
	var buf strings.Builder
	created := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	matches := []book{
		{ID: "b1", Title: "Book One", CreatedAt: ptrTime(created), UpdatedAt: nil, VersionGroupID: nil, IsPrimaryVersion: ptrBool(false)},
	}
	if err := writeCSV(&buf, matches); err != nil {
		t.Fatalf("writeCSV: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "book_id,title,created_at,updated_at,version_group_id,is_primary_version\n") {
		t.Fatalf("unexpected CSV header: %q", out)
	}
	if !strings.Contains(out, "b1,Book One,2026-08-20T12:00:00Z,,,false\n") {
		t.Fatalf("unexpected CSV row: %q", out)
	}
}

// fakeLibrary serves n books over offset-paginated pages of size pageSize,
// reporting a deliberately WRONG count, and records the query each page was
// fetched with.
type fakeLibrary struct {
	total       int
	pageSize    int
	lyingCount  int
	gotQueries  []string
	repeatFirst bool // serve page 2's first row again, simulating window drift
}

func (f *fakeLibrary) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.gotQueries = append(f.gotQueries, r.URL.RawQuery)
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 50 // mirrors the server default when `limit` is absent
		}
		if limit > f.pageSize {
			limit = f.pageSize
		}
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))

		var items []book
		for i := offset; i < offset+limit && i < f.total; i++ {
			id := i
			if f.repeatFirst && offset > 0 && i == offset {
				id = offset - 1 // re-serve the previous page's last row
			}
			items = append(items, book{
				ID:               fmt.Sprintf("book-%04d", id),
				Title:            fmt.Sprintf("Title %d", id),
				IsPrimaryVersion: ptrBool(false),
			})
		}
		_ = json.NewEncoder(w).Encode(listData{
			Items: items, Count: f.lyingCount, Limit: limit, Offset: offset,
		})
	}
}

// TestFetchAllBooksIgnoresLyingCount pins the defect that would have silently
// truncated the census. The production list endpoint reports `count` from
// CountPrimaryBooks() -- primary, non-deleted books only -- while the item
// stream is not primary-filtered at all. Measured 2026-08-23: count said
// 41,741 against a stream of 56,727. A loop that breaks on
// `len(all) >= count` stops 14,986 books early and looks perfectly healthy
// doing it, because -min-expected only guards the low end.
//
// The fake reports a count barely over half the true total; the fetch must
// still return every book and terminate only on the empty page.
func TestFetchAllBooksIgnoresLyingCount(t *testing.T) {
	f := &fakeLibrary{total: 250, pageSize: 100, lyingCount: 130}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	books, dupes, err := fetchAllBooks(srv.Client(), srv.URL, "k", 100, 0, 0, false)
	if err != nil {
		t.Fatalf("fetchAllBooks: %v", err)
	}
	if len(books) != 250 {
		t.Errorf("got %d books, want 250 -- the loop trusted the count field "+
			"(%d) and truncated the census", len(books), f.lyingCount)
	}
	if dupes != 0 {
		t.Errorf("got %d duplicates, want 0", dupes)
	}
}

// TestFetchAllBooksSendsLimitAndShowQuarantined pins the two query-parameter
// fixes. `page_size` is ignored by the handler (measured: page_size=5 returned
// 50 items, limit=5 returned 5), so sending it made -page-size inert. Omitting
// show_quarantined hid quarantined rows, which are real rows that can carry
// the anomaly.
func TestFetchAllBooksSendsLimitAndShowQuarantined(t *testing.T) {
	f := &fakeLibrary{total: 10, pageSize: 100, lyingCount: 10}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	if _, _, err := fetchAllBooks(srv.Client(), srv.URL, "k", 5, 0, 0, false); err != nil {
		t.Fatalf("fetchAllBooks: %v", err)
	}
	if len(f.gotQueries) == 0 {
		t.Fatal("no requests recorded")
	}
	for i, q := range f.gotQueries {
		if !strings.Contains(q, "limit=5") {
			t.Errorf("request %d query %q: want limit=5 (the param the handler reads)", i, q)
		}
		if strings.Contains(q, "page_size=") {
			t.Errorf("request %d query %q: still sends page_size, which the handler ignores", i, q)
		}
		if !strings.Contains(q, "show_quarantined=true") {
			t.Errorf("request %d query %q: want show_quarantined=true", i, q)
		}
	}
}

// TestFetchAllBooksCountsDuplicates verifies that a row served twice by
// offset paging over a shifting list is de-duplicated AND reported. A repeat
// means the window moved, so some other row was skipped -- the count must
// surface as a lower bound rather than silently inflating the total.
func TestFetchAllBooksCountsDuplicates(t *testing.T) {
	f := &fakeLibrary{total: 300, pageSize: 100, lyingCount: 300, repeatFirst: true}
	srv := httptest.NewServer(f.handler())
	defer srv.Close()

	books, dupes, err := fetchAllBooks(srv.Client(), srv.URL, "k", 100, 0, 0, false)
	if err != nil {
		t.Fatalf("fetchAllBooks: %v", err)
	}
	if dupes == 0 {
		t.Error("got 0 duplicates, want > 0 -- repeated rows went uncounted")
	}
	seen := map[string]bool{}
	for _, b := range books {
		if seen[b.ID] {
			t.Fatalf("duplicate %s survived de-duplication", b.ID)
		}
		seen[b.ID] = true
	}
}
