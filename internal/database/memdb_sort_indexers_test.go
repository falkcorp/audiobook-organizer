// file: internal/database/memdb_sort_indexers_test.go
// version: 1.0.0
// guid: 2c8a6f31-9b07-4de5-a142-70e3d95cb864
// last-edited: 2026-08-09
//
// Tests for the sorted secondary indexes.
//
// The high-value test here is TestSortIndexOrderMatchesComparator: the
// indexed walk and the materialise-and-sort path must produce the SAME
// order, because which one runs depends on whether a filter happened to be
// active. If they diverge, "sort by duration" silently returns a different
// order depending on unrelated query parameters — the kind of bug that is
// nearly impossible to spot by hand.

package database

import (
	"bytes"
	"sort"
	"testing"
	"time"
)

// enableAllSortIndexes turns on every sort index for the duration of a test
// and restores the previous set afterwards. Required because the indexes are
// opt-in: memdbSchema() reads the enabled set when the store is built, so
// this must run BEFORE NewMemStore.
func enableAllSortIndexes(t *testing.T) {
	t.Helper()
	fields := make([]string, 0, len(sortIndexForField))
	for f := range sortIndexForField {
		fields = append(fields, f)
	}
	if unknown := SetEnabledSortIndexes(fields); len(unknown) != 0 {
		t.Fatalf("SetEnabledSortIndexes reported unknown fields %v for its own map", unknown)
	}
	t.Cleanup(func() { SetEnabledSortIndexes(nil) })
}

func TestEncodeSortableInt64_IsOrderPreserving(t *testing.T) {
	// The whole point of the sign-bit flip. As raw two's-complement
	// big-endian bytes, every negative would sort ABOVE every positive.
	values := []int64{
		-9223372036854775808, -1000000, -2, -1, 0, 1, 2, 1000000,
		9223372036854775807,
	}
	for i := 1; i < len(values); i++ {
		lo := encodeSortableInt64(values[i-1])
		hi := encodeSortableInt64(values[i])
		if bytes.Compare(lo, hi) >= 0 {
			t.Errorf("encode(%d) should sort before encode(%d), got %x >= %x",
				values[i-1], values[i], lo, hi)
		}
	}
}

func TestMissingSortsAfterEveryPresentValue(t *testing.T) {
	missing := missingSortKey()

	for _, v := range []int64{-9223372036854775808, -1, 0, 1, 9223372036854775807} {
		if bytes.Compare(encodeSortableInt64(v), missing) >= 0 {
			t.Errorf("present value %d must sort before missing", v)
		}
	}

	// Strings too — including ones above "~" (0x7E), which is exactly the
	// case the old "~" sentinel could not order correctly.
	for _, s := range []string{"", "a", "zzz", "~tilde", "\u00e9clair", "\uffef"} {
		enc := encodeSortableString(s)
		if s == "" {
			if !bytes.Equal(enc, missing) {
				t.Errorf("empty string should encode to the missing key")
			}
			continue
		}
		if bytes.Compare(enc, missing) >= 0 {
			t.Errorf("present string %q must sort before missing, got %x", s, enc)
		}
	}
}

func TestEncodeSortableString_CaseInsensitive(t *testing.T) {
	// Mirrors the comparators, which all use strings.ToLower.
	if !bytes.Equal(encodeSortableString("Asimov"), encodeSortableString("asimov")) {
		t.Error("string sort key must be case-insensitive")
	}
	if bytes.Compare(encodeSortableString("Asimov"), encodeSortableString("banks")) >= 0 {
		t.Error("Asimov should sort before banks case-insensitively")
	}
}

func TestBookYearSortValue_MirrorsComparator(t *testing.T) {
	// The "year" comparator prefers AudiobookReleaseYear, falls back to
	// PrintYear, and treats a zero in the first as absent.
	tests := []struct {
		name        string
		release     *int
		print       *int
		wantVal     int64
		wantPresent bool
	}{
		{"release wins", ptrInt_mem(2001), ptrInt_mem(1999), 2001, true},
		{"zero release falls back to print", ptrInt_mem(0), ptrInt_mem(1999), 1999, true},
		{"nil release falls back to print", nil, ptrInt_mem(1999), 1999, true},
		{"both absent", nil, nil, 0, false},
		{"both zero is absent", ptrInt_mem(0), ptrInt_mem(0), 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := &Book{AudiobookReleaseYear: tc.release, PrintYear: tc.print}
			got, present := bookYearSortValue(b)
			if got != tc.wantVal || present != tc.wantPresent {
				t.Errorf("= (%d, %v), want (%d, %v)", got, present, tc.wantVal, tc.wantPresent)
			}
		})
	}
}

func TestZeroValueIsPresentNotMissing(t *testing.T) {
	// A 0-second duration is a real measurement (a broken file), not an
	// unknown. It must sort with the numbers, not at the end with unknowns.
	zero := 0
	b := &Book{Duration: &zero}
	v, present := bookDurationSortValue(b)
	if !present || v != 0 {
		t.Fatalf("explicit zero duration = (%d, %v), want (0, true)", v, present)
	}

	var nilDur *int
	v, present = bookDurationSortValue(&Book{Duration: nilDur})
	if present {
		t.Fatalf("nil duration should be absent, got (%d, %v)", v, present)
	}
}

// CanPushDownSort must track the ENABLED set, not the known set. If it said
// true for a field whose index was never registered, the query path would
// call txn.Get with an unknown index name and error at runtime.
func TestCanPushDownSort_FollowsEnabledSet(t *testing.T) {
	SetEnabledSortIndexes(nil)
	t.Cleanup(func() { SetEnabledSortIndexes(nil) })

	// Title is always indexed and is not configurable.
	if !CanPushDownSort("title") {
		t.Error("title must always be pushdownable")
	}
	for _, f := range []string{"author", "duration", "year"} {
		if CanPushDownSort(f) {
			t.Errorf("CanPushDownSort(%q) = true with nothing enabled — would query a missing index", f)
		}
	}

	// Enabling one field must not enable its neighbours.
	if unknown := SetEnabledSortIndexes([]string{"duration"}); len(unknown) != 0 {
		t.Fatalf("unexpected unknown fields: %v", unknown)
	}
	if !CanPushDownSort("duration") {
		t.Error("duration was enabled but is not pushdownable")
	}
	// Alias spelling shares the index but is a distinct key: it must be
	// enabled explicitly, so that config says what it means.
	if CanPushDownSort("duration_seconds") {
		t.Error("duration_seconds should not be enabled by enabling duration")
	}
	if CanPushDownSort("author") {
		t.Error("enabling duration must not enable author")
	}
}

func TestCanPushDownSort(t *testing.T) {
	enableAllSortIndexes(t)
	// Indexed — must be pushdownable, or the index is built and never used.
	for _, f := range []string{
		"title", "author", "narrator", "series", "year",
		"created_at", "updated_at", "duration", "file_size", "bitrate",
		"duration_seconds", "bitrate_kbps", "file_size_bytes",
	} {
		if !CanPushDownSort(f) {
			t.Errorf("CanPushDownSort(%q) = false, want true — index exists but is unused", f)
		}
	}
	// Deliberately excluded. If one of these becomes true without an index
	// being added, the query path will ask memdb for an index that is not
	// in the schema.
	for _, f := range []string{
		"library_state", "quality", "genre", "language", "publisher",
		"format", "codec", "edition", "sample_rate_hz", "", "nonsense",
	} {
		if CanPushDownSort(f) {
			t.Errorf("CanPushDownSort(%q) = true, but no index is registered for it", f)
		}
	}
}

// Every sortIndexForField target must exist in the schema, or the query path
// asks memdb for an unknown index and errors at runtime.
func TestSortIndexNamesExistInSchema(t *testing.T) {
	enableAllSortIndexes(t)
	schema := memdbSchema()
	books, ok := schema.Tables[memTableBooks]
	if !ok {
		t.Fatal("books table missing from schema")
	}
	for field, idxName := range sortIndexForField {
		if _, ok := books.Indexes[idxName]; !ok {
			t.Errorf("sort field %q maps to index %q, which is not in the schema",
				field, idxName)
		}
	}
	if _, ok := books.Indexes[memIdxTitle]; !ok {
		t.Error("title index missing from schema")
	}
}

// TestSortIndexOrderMatchesComparator is the important one. The indexed walk
// and the in-memory comparator sort must agree; which path runs depends on
// unrelated query parameters, so a divergence shows up as an order that
// changes when you add a filter.
func TestSortIndexOrderMatchesComparator(t *testing.T) {
	enableAllSortIndexes(t)
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}

	t1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	books := []Book{
		{ID: "b1", Title: "One", Narrator: ptrString_mem("Zoe"), Duration: ptrInt_mem(300),
			FileSize: ptrInt64_mem(9000), Bitrate: ptrInt_mem(128),
			AudiobookReleaseYear: ptrInt_mem(2001), CreatedAt: &t1, UpdatedAt: &t2},
		{ID: "b2", Title: "Two", Narrator: ptrString_mem("adam"), Duration: ptrInt_mem(100),
			FileSize: ptrInt64_mem(100), Bitrate: ptrInt_mem(320),
			AudiobookReleaseYear: ptrInt_mem(1999), CreatedAt: &t2, UpdatedAt: &t1},
		// No values at all — must still appear, sorted last ascending.
		{ID: "b3", Title: "Three"},
		{ID: "b4", Title: "Four", Narrator: ptrString_mem("Mabel"), Duration: ptrInt_mem(0),
			FileSize: ptrInt64_mem(0), Bitrate: ptrInt_mem(0),
			PrintYear: ptrInt_mem(1888)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	cases := []struct {
		field string
		key   func(*Book) (int64, bool)
		str   func(*Book) string
	}{
		{field: "narrator", str: bookNarratorSortValue},
		{field: "duration", key: bookDurationSortValue},
		{field: "file_size", key: bookFileSizeSortValue},
		{field: "bitrate", key: bookBitrateSortValue},
		{field: "year", key: bookYearSortValue},
		{field: "created_at", key: bookCreatedAtSortValue},
		{field: "updated_at", key: bookUpdatedAtSortValue},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			got, err := m.GetBookSummaries(0, 0, BookSummaryFilter{
				SortBy: tc.field, SortAscending: true,
			})
			if err != nil {
				t.Fatalf("GetBookSummaries(%s): %v", tc.field, err)
			}
			if len(got) != len(books) {
				t.Fatalf("indexed walk returned %d books, want %d — a book is "+
					"missing from the %s index and would vanish from the library page",
					len(got), len(books), tc.field)
			}

			// Independently compute the expected order from the same
			// accessors, using a stable sort with the missing-last rule.
			want := make([]Book, len(books))
			copy(want, books)
			sort.SliceStable(want, func(i, j int) bool {
				if tc.str != nil {
					a, b := encodeSortableString(tc.str(&want[i])), encodeSortableString(tc.str(&want[j]))
					return bytes.Compare(a, b) < 0
				}
				av, ap := tc.key(&want[i])
				bv, bp := tc.key(&want[j])
				switch {
				case !ap && !bp:
					return false
				case !ap:
					return false // missing sorts last
				case !bp:
					return true
				default:
					return av < bv
				}
			})

			for i := range want {
				if got[i].ID != want[i].ID {
					gotIDs := make([]string, len(got))
					for k := range got {
						gotIDs[k] = got[k].ID
					}
					wantIDs := make([]string, len(want))
					for k := range want {
						wantIDs[k] = want[k].ID
					}
					t.Fatalf("sort by %s:\n  indexed walk = %v\n  expected     = %v",
						tc.field, gotIDs, wantIDs)
				}
			}
		})
	}
}

// A field that is KNOWN but NOT ENABLED must fall back to an unsorted walk,
// not ask memdb for an index that was never registered.
//
// This caught a real bug during development: memdb_summaries.go mapped
// straight through sortIndexForField (every known field) instead of checking
// the enabled set, so `sort_by=duration` with duration disabled would have
// called txn.Get with "sort_duration" — an index absent from the schema —
// and failed the whole library query at runtime.
func TestDisabledSortFieldFallsBackInsteadOfErroring(t *testing.T) {
	SetEnabledSortIndexes(nil)
	t.Cleanup(func() { SetEnabledSortIndexes(nil) })

	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "A", Duration: ptrInt_mem(30)},
		{ID: "b2", Title: "B", Duration: ptrInt_mem(10)},
	}
	seedMemStore(t, m, books, nil, nil, nil)

	got, err := m.GetBookSummaries(0, 0, BookSummaryFilter{
		SortBy: "duration", SortAscending: true,
	})
	if err != nil {
		t.Fatalf("disabled sort field must fall back, not error: %v", err)
	}
	if len(got) != len(books) {
		t.Fatalf("fallback returned %d books, want %d", len(got), len(books))
	}
	// title stays available even with everything else disabled.
	if _, err := m.GetBookSummaries(0, 0, BookSummaryFilter{
		SortBy: "title", SortAscending: true,
	}); err != nil {
		t.Fatalf("title must always work: %v", err)
	}
}

func TestSortIndexDescendingReversesAscending(t *testing.T) {
	enableAllSortIndexes(t)
	m, err := NewMemStore()
	if err != nil {
		t.Fatalf("NewMemStore: %v", err)
	}
	books := []Book{
		{ID: "b1", Title: "A", Duration: ptrInt_mem(10)},
		{ID: "b2", Title: "B", Duration: ptrInt_mem(30)},
		{ID: "b3", Title: "C", Duration: ptrInt_mem(20)},
		{ID: "b4", Title: "D"}, // missing
	}
	seedMemStore(t, m, books, nil, nil, nil)

	asc, err := m.GetBookSummaries(0, 0, BookSummaryFilter{SortBy: "duration", SortAscending: true})
	if err != nil {
		t.Fatalf("asc: %v", err)
	}
	desc, err := m.GetBookSummaries(0, 0, BookSummaryFilter{SortBy: "duration", SortAscending: false})
	if err != nil {
		t.Fatalf("desc: %v", err)
	}
	if len(asc) != len(books) || len(desc) != len(books) {
		t.Fatalf("asc=%d desc=%d, want %d each", len(asc), len(desc), len(books))
	}
	for i := range asc {
		if asc[i].ID != desc[len(desc)-1-i].ID {
			t.Fatalf("descending is not the reverse of ascending:\n asc=%v\n desc=%v",
				ids(asc), ids(desc))
		}
	}
}

func ids(bs []BookSummary) []string {
	out := make([]string, len(bs))
	for i := range bs {
		out[i] = bs[i].ID
	}
	return out
}
