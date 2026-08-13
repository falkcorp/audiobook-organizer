// file: internal/database/pebble_summaries_fallback_filter_test.go
// version: 1.0.0
// guid: 4f7c1d92-8b3e-4a56-9d07-2e6b5a1c8f43
// last-edited: 2026-08-13

package database

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This file pins the contract that GetAllBookSummariesFiltered's Pebble
// fallback must honor EVERY predicate on the BookSummaryFilter, not just the
// two it currently implements.
//
// Why it matters: memdb is not merely a startup-warmup optimization that a
// slow-but-correct fallback covers for. Two states leave reads on Pebble
// indefinitely:
//
//  1. UseMemDB=false — configuration, permanent for the process.
//  2. memPendingAbandoned — memdb_sync.go marks warmup abandoned when the
//     pending-write buffer overflows, and refuses to publish for the rest of
//     the process lifetime. Its own comment states the intent this file
//     tests: "Reads then stay on Pebble — slower, but they cannot lie."
//
// They can lie today. On production 2026-08-13 08:50:29, during the 115,971 ms
// warmup, GET /api/v1/audiobooks?title=Skills returned count=63870 with
// non-matching rows; the same query post-warmup returned the correct 25. The
// fallback honors only IsPrimaryVersion and ExcludeQuarantined and silently
// discards Predicate (which carries every field-filter: title, author,
// narrator, ...), LibraryState, ReviewStatus, RestrictToIDs, and
// MarkedForDeletion.
//
// The oracle below is computed from the seed fixtures directly rather than by
// comparing the two backends against each other, so a defect present in BOTH
// paths still fails the test. The memdb path is included as a control: it is
// expected to pass at HEAD, which is what makes a Pebble-path failure
// attributable to the fallback rather than to a bad expectation.

// fallbackFixture is a self-describing seed row. The test persists it via
// CreateBook and evaluates the expected filter result against these same
// fields, so the expectation never depends on the code under test.
type fallbackFixture struct {
	id           string // assigned by CreateBook
	title        string
	libraryState string
	reviewStatus string
	primary      bool
	quarantined  bool
	deleted      bool
}

// seedFallbackBooks creates a small deterministic corpus spanning every
// filterable dimension the BookSummaryFilter carries.
func seedFallbackBooks(t *testing.T, p *PebbleStore) []fallbackFixture {
	t.Helper()

	// Titles are chosen so that a substring predicate for "Skills" matches a
	// strict, known subset — mirroring the production ?title=Skills query.
	fixtures := []fallbackFixture{
		{title: "Skills of the Trade", libraryState: "organized", reviewStatus: "matched", primary: true},
		{title: "Advanced Skills", libraryState: "imported", reviewStatus: "no_match", primary: true},
		{title: "Skills", libraryState: "organized", reviewStatus: "matched", primary: false},
		{title: "The Hobbit", libraryState: "organized", reviewStatus: "matched", primary: true},
		{title: "Dune", libraryState: "imported", reviewStatus: "no_match", primary: true},
		{title: "Neuromancer", libraryState: "suspicious", reviewStatus: "matched", primary: true},
		{title: "Skills Quarantined", libraryState: "organized", reviewStatus: "matched", primary: true, quarantined: true},
		{title: "Skills Deleted", libraryState: "organized", reviewStatus: "matched", primary: true, deleted: true},
	}

	for i := range fixtures {
		f := &fixtures[i]
		ls := f.libraryState
		rs := f.reviewStatus
		primary := f.primary
		b := &Book{
			Title:                f.title,
			FilePath:             fmt.Sprintf("/tmp/fallbackfilter_%02d.m4b", i),
			LibraryState:         &ls,
			MetadataReviewStatus: &rs,
			IsPrimaryVersion:     &primary,
		}
		if f.quarantined {
			now := time.Now().UTC()
			b.QuarantinedAt = &now
		}
		if f.deleted {
			del := true
			b.MarkedForDeletion = &del
		}
		created, err := p.CreateBook(b)
		require.NoError(t, err)
		require.NotNil(t, created)
		f.id = created.ID
	}
	return fixtures
}

// titlePredicate builds the same shape the service layer pushes down for a
// ?title= field-filter: a case-insensitive substring match evaluated against
// the full *Book.
func titlePredicate(substr string) func(*Book) bool {
	return func(b *Book) bool {
		return strings.Contains(strings.ToLower(b.Title), strings.ToLower(substr))
	}
}

// idsOf collects and sorts the IDs from a summary slice for set comparison.
func idsOf(summaries []BookSummary) []string {
	out := make([]string, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, s.ID)
	}
	sort.Strings(out)
	return out
}

// expectIDs collects the IDs of fixtures satisfying keep, sorted.
func expectIDs(fixtures []fallbackFixture, keep func(fallbackFixture) bool) []string {
	out := make([]string, 0, len(fixtures))
	for _, f := range fixtures {
		if keep(f) {
			out = append(out, f.id)
		}
	}
	sort.Strings(out)
	return out
}

// TestGetAllBookSummariesFiltered_PebbleFallbackHonorsEveryPredicate runs one
// filter matrix through BOTH the memdb-delegation path (UseMemDB=true, the
// production default) and the Pebble scan-fallback path (UseMemDB=false),
// asserting each against an oracle derived from the seed fixtures.
func TestGetAllBookSummariesFiltered_PebbleFallbackHonorsEveryPredicate(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()

	fixtures := seedFallbackBooks(t, p)

	cases := []struct {
		name   string
		filter BookSummaryFilter
		// want is evaluated against the fixtures to build the expected ID set.
		want func(fallbackFixture) bool
	}{
		{
			// The production symptom: ?title=Skills returned the whole
			// library. Deleted rows are excluded by default; quarantined rows
			// are NOT excluded unless ExcludeQuarantined is set.
			name:   "TitlePredicate",
			filter: BookSummaryFilter{Predicate: titlePredicate("Skills")},
			want: func(f fallbackFixture) bool {
				return strings.Contains(f.title, "Skills") && !f.deleted
			},
		},
		{
			// A predicate matching nothing must return nothing. The prod log
			// showed zzqqxx→0 only AFTER warmup published; during warmup this
			// is the case that returns the entire corpus.
			name:   "PredicateMatchesNothing",
			filter: BookSummaryFilter{Predicate: titlePredicate("zzqqxx")},
			want:   func(fallbackFixture) bool { return false },
		},
		{
			name:   "LibraryState",
			filter: BookSummaryFilter{LibraryState: "imported"},
			want: func(f fallbackFixture) bool {
				return f.libraryState == "imported" && !f.deleted
			},
		},
		{
			name:   "ReviewStatus",
			filter: BookSummaryFilter{ReviewStatus: "no_match"},
			want: func(f fallbackFixture) bool {
				return f.reviewStatus == "no_match" && !f.deleted
			},
		},
		{
			// Predicate AND LibraryState must both apply — a fallback that
			// honors one and drops the other still over-returns.
			name: "PredicateAndLibraryState",
			filter: BookSummaryFilter{
				Predicate:    titlePredicate("Skills"),
				LibraryState: "organized",
			},
			want: func(f fallbackFixture) bool {
				return strings.Contains(f.title, "Skills") &&
					f.libraryState == "organized" && !f.deleted
			},
		},
		{
			// MarkedForDeletion=true is the inverse of the default: it must
			// return ONLY deleted rows. getAllBookSummariesFull drops every
			// deleted row unconditionally before the filter is consulted, so
			// the fallback returns the empty set for this filter today.
			name:   "MarkedForDeletionTrue",
			filter: BookSummaryFilter{MarkedForDeletion: boolPtr(true)},
			want:   func(f fallbackFixture) bool { return f.deleted },
		},
		{
			// CONTROL: IsPrimaryVersion is one of the two predicates the
			// fallback already implements. It must stay green on both paths —
			// if this reddens, the fix over-reached.
			name:   "IsPrimaryVersionControl",
			filter: BookSummaryFilter{IsPrimaryVersion: boolPtr(false)},
			want: func(f fallbackFixture) bool {
				return !f.primary && !f.deleted
			},
		},
		{
			// CONTROL: ExcludeQuarantined is the other already-implemented
			// predicate.
			name:   "ExcludeQuarantinedControl",
			filter: BookSummaryFilter{ExcludeQuarantined: true},
			want: func(f fallbackFixture) bool {
				return !f.quarantined && !f.deleted
			},
		},
	}

	for _, tc := range cases {
		for _, useMemDB := range []bool{true, false} {
			backend := "MemDBPath"
			if !useMemDB {
				backend = "PebbleFallbackPath"
			}
			t.Run(tc.name+"/"+backend, func(t *testing.T) {
				p.UseMemDB = useMemDB
				want := expectIDs(fixtures, tc.want)

				got, err := p.GetAllBookSummariesFiltered(0, 0, tc.filter)
				require.NoError(t, err)
				require.Equal(t, want, idsOf(got),
					"%s returned the wrong row set for filter %s", backend, tc.name)

				// The count path has the identical fallback and is what made
				// the production response report count=63870. Assert it
				// separately — it delegates today, but a fix that repairs
				// only the row path would leave the count lying.
				n, err := p.CountBookSummariesFiltered(tc.filter)
				require.NoError(t, err)
				require.Equal(t, len(want), n,
					"%s returned the wrong count for filter %s", backend, tc.name)
			})
		}
	}
}

// TestGetAllBookSummariesFiltered_FallbackPaginatesPostFilter pins that
// offset/limit are applied to the POST-filter set on the Pebble fallback, the
// same as memdb does. A fallback that filters correctly but paginates the
// pre-filter set would return short or empty pages — the failure mode a
// previous pagination bug in this same area already produced once.
func TestGetAllBookSummariesFiltered_FallbackPaginatesPostFilter(t *testing.T) {
	p := setupTestPebbleStore(t)
	p.WaitForWarmup()

	fixtures := seedFallbackBooks(t, p)
	filter := BookSummaryFilter{Predicate: titlePredicate("Skills")}
	want := expectIDs(fixtures, func(f fallbackFixture) bool {
		return strings.Contains(f.title, "Skills") && !f.deleted
	})
	require.Len(t, want, 4, "fixture guard: expected 4 non-deleted Skills books")

	for _, useMemDB := range []bool{true, false} {
		backend := "MemDBPath"
		if !useMemDB {
			backend = "PebbleFallbackPath"
		}
		t.Run(backend, func(t *testing.T) {
			p.UseMemDB = useMemDB

			// Walk the whole matched set one page at a time and assert the
			// pages PARTITION it — every matched ID appears exactly once
			// across pages, and nothing unmatched appears at all. Asserting
			// per-page length alone would pass even if paging over the
			// pre-filter set silently dropped rows.
			const pageSize = 2
			seen := make([]string, 0, len(want))
			for offset := 0; offset < len(want); offset += pageSize {
				page, err := p.GetAllBookSummariesFiltered(pageSize, offset, filter)
				require.NoError(t, err)
				require.NotEmpty(t, page,
					"%s: page at offset %d was empty but %d matches remain",
					backend, offset, len(want)-offset)
				seen = append(seen, idsOf(page)...)
			}
			sort.Strings(seen)
			require.Equal(t, want, seen,
				"%s: paged results must partition the matched set exactly", backend)
		})
	}
}
