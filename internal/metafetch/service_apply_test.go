// file: internal/metafetch/service_apply_test.go
// version: 1.6.0
// guid: bc6eeacd-35fa-4d23-a051-ee09424676a9
// last-edited: 2026-09-13

package metafetch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/stretchr/testify/require"
)

// capturingActivityStore is a minimal database.ActivityStorer that keeps every
// recorded entry in memory. activity.Service takes the store interface rather
// than exposing what it recorded, so capturing here is the only way to see the
// Summary string RecordApplyHistory builds. The conformance assertion below is
// what proves this type satisfies the interface.
type capturingActivityStore struct {
	mu      sync.Mutex
	entries []database.ActivityEntry
}

func (c *capturingActivityStore) Record(e database.ActivityEntry) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	return int64(len(c.entries)), nil
}

// summaryForField returns the Summary of the single captured entry whose
// Details["field"] equals field. Selecting by field rather than by index keeps
// the assertion stable: RecordApplyHistory emits one entry per changed field
// and the set of changed fields differs between the cases below.
func (c *capturingActivityStore) summaryForField(t *testing.T, field string) string {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var found []string
	for _, e := range c.entries {
		if e.Details != nil && e.Details["field"] == field {
			found = append(found, e.Summary)
		}
	}
	require.Len(t, found, 1, "expected exactly one activity entry for field %q, captured %d entries total", field, len(c.entries))
	return found[0]
}

// Remaining ActivityStorer methods — unused by RecordApplyHistory, present
// only to satisfy the interface.
func (c *capturingActivityStore) Query(context.Context, database.ActivityFilter) ([]database.ActivityEntry, int, error) {
	return nil, 0, nil
}

func (c *capturingActivityStore) Summarize(context.Context, time.Time, string) (int, error) {
	return 0, nil
}

func (c *capturingActivityStore) GetDistinctSources(context.Context, database.ActivityFilter) ([]database.SourceCount, error) {
	return nil, nil
}

func (c *capturingActivityStore) Prune(context.Context, time.Time, string) (int, error) {
	return 0, nil
}

func (c *capturingActivityStore) WipeAllActivity(context.Context) (int64, error) { return 0, nil }

func (c *capturingActivityStore) CompactByDay(context.Context, time.Time) (database.CompactResult, error) {
	return database.CompactResult{}, nil
}

func (c *capturingActivityStore) RepairActivityIndexes(context.Context) (database.ActivityIndexRepairResult, error) {
	return database.ActivityIndexRepairResult{}, nil
}

func (c *capturingActivityStore) RecompactDigests(context.Context) (database.RecompactResult, error) {
	return database.RecompactResult{}, nil
}

func (c *capturingActivityStore) OptimizeStatistics(context.Context) (database.ActivityOptimizeResult, error) {
	return database.ActivityOptimizeResult{}, nil
}

func (c *capturingActivityStore) MigrateSystemActivityLogs() (int, error) { return 0, nil }

func (c *capturingActivityStore) Close() error { return nil }

var _ database.ActivityStorer = (*capturingActivityStore)(nil)

// newChangeHistoryHarness wires a metafetch Service to a MockStore and an
// activity.Service backed by a capturing store, then returns both.
// An owner-reviewed override is applied past legs the certainty gate refused;
// its change-history rows are the only record to audit or revert it from.
// History is recorded after the commit (CommitApply), so a failed history
// write cannot stop the write; it must instead come back with the response so
// the op reports and counts it. An unreviewed apply keeps main's behaviour:
// logged, the apply stands, no error.
func TestApplyCandidate_HistoryFailureIsReturnedForOwnerReviewedApply(t *testing.T) {
	for _, tc := range []struct {
		name    string
		opts    ApplyOptions
		wantErr bool
	}{
		{"owner-reviewed", ApplyOptions{OwnerReviewed: true, GateOverride: "score_below_floor"}, true},
		// An empty summary must not turn a reviewed apply into an ordinary
		// one: OwnerReviewed alone requires the history and adds the note.
		{"owner-reviewed, empty summary", ApplyOptions{OwnerReviewed: true}, true},
		{"unreviewed", ApplyOptions{}, false},
		// A label without the flag is not a review.
		{"label only", ApplyOptions{GateOverride: "score_below_floor"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pebble, err := database.NewPebbleStore(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = pebble.Close() })
			store := &historyFailStore{PebbleStore: pebble, failFetched: true}
			svc := NewService(store)
			book, err := store.CreateBook(&database.Book{Title: "track01", FilePath: "/library/a.m4b", Format: "m4b"})
			require.NoError(t, err)

			resp, err := svc.ApplyMetadataCandidateWithOptions(book.ID, MetadataCandidate{Title: "New Title", Source: "Audible"}, nil, tc.opts)
			require.NotNil(t, resp, "the write committed, so the response is returned either way")
			if tc.wantErr {
				require.ErrorIs(t, err, ErrApplyHistoryIncomplete)
				require.Contains(t, err.Error(), "change history not recorded")
			} else {
				require.NoError(t, err)
			}
			got, gerr := store.GetBookByID(book.ID)
			require.NoError(t, gerr)
			require.Equal(t, "New Title", got.Title, "the write stands; only its history is missing")
			hasNote := got.VersionNotes != nil && strings.Contains(*got.VersionNotes, "owner_reviewed ")
			require.Equal(t, tc.opts.OwnerReviewed, hasNote, "owner_reviewed note, notes %v", got.VersionNotes)
		})
	}
}

// The history label names the override for every reviewed apply, and falls
// back to owner_reviewed rather than to the plain source when no reasons came.
func TestApplyOptionsHistorySource(t *testing.T) {
	require.Equal(t, "Audible", ApplyOptions{}.historySource("Audible"))
	require.Equal(t, "Audible", ApplyOptions{GateOverride: "x"}.historySource("Audible"))
	require.Equal(t, "Audible (owner-reviewed; certainty gate overridden: owner_reviewed)",
		ApplyOptions{OwnerReviewed: true}.historySource("Audible"))
	require.Equal(t, "Audible (owner-reviewed; certainty gate overridden: score_below_floor)",
		ApplyOptions{OwnerReviewed: true, GateOverride: "score_below_floor"}.historySource("Audible"))
}

func newChangeHistoryHarness(t *testing.T) (*Service, *capturingActivityStore) {
	t.Helper()
	acts := &capturingActivityStore{}
	svc := NewService(&database.MockStore{})
	svc.SetActivityService(activity.NewService(acts))
	return svc, acts
}

// RecordApplyHistory_SummaryLeadsWithBookTitle is the anti-over-suppression
// case: an ordinary change with a non-empty old value must still render its
// full before/after line, now prefixed with the book title.
func TestRecordApplyHistory_SummaryLeadsWithBookTitle(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)

	book := &database.Book{
		ID:       "01J0BOOKID000000000000000",
		Title:    "The Whispering Night",
		Narrator: new("Alex Kozlowski"),
	}
	after := *book
	after.Narrator = new("Grant Cartwright")
	svc.RecordApplyHistory(book, &after, nil, "audible")

	summary := acts.summaryForField(t, "narrator")
	require.True(t, strings.HasPrefix(summary, "The Whispering Night: Applied"),
		"summary must lead with the book title followed by ': Applied', got %q", summary)
	require.Equal(t, "The Whispering Night: Applied narrator: Alex Kozlowski → Grant Cartwright", summary)
}

// RecordApplyHistory_EmptyOldValueRendersNone covers a first-ever value: the
// from-side must read "(none)" rather than leaving a dangling arrow.
func TestRecordApplyHistory_EmptyOldValueRendersNone(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)

	book := &database.Book{
		ID:                   "01J0BOOKID000000000000000",
		Title:                "The Whispering Night",
		AudiobookReleaseYear: nil, // no prior value -> oldVal is ""
	}
	after := *book
	after.AudiobookReleaseYear = new(2021)
	svc.RecordApplyHistory(book, &after, nil, "audible")

	summary := acts.summaryForField(t, "audiobook_release_year")
	require.Contains(t, summary, "(none) → ", "empty old value must render as (none), got %q", summary)
	require.NotContains(t, summary, ":  → ", "summary must not contain a dangling arrow, got %q", summary)
	require.Equal(t, "The Whispering Night: Applied audiobook_release_year: (none) → 2021", summary)
}

// RecordApplyHistory_EmptyTitleFallsBackToID covers the edge case where the
// book has no title: the line must not start with a bare ": Applied ...".
func TestRecordApplyHistory_EmptyTitleFallsBackToID(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)

	book := &database.Book{
		ID:       "01J0BOOKID000000000000000",
		Title:    "",
		Narrator: new("Alex Kozlowski"),
	}
	after := *book
	after.Narrator = new("Grant Cartwright")
	svc.RecordApplyHistory(book, &after, nil, "audible")

	summary := acts.summaryForField(t, "narrator")
	require.False(t, strings.HasPrefix(summary, ": Applied"), "summary must not start with a bare ': Applied', got %q", summary)
	require.Equal(t, "01J0BOOKID000000000000000: Applied narrator: Alex Kozlowski → Grant Cartwright", summary)
}

// RecordApplyHistory_RecordsEveryWrittenField: an apply (an owner-reviewed
// one above all) must leave a history row for every column the apply body
// writes, not only the original nine. Before 2026-09-13 an apply that
// replaced a book's ASIN, ISBNs or description left no old value to undo from.
// RecordApplyHistory diffs the whole row, so this pins the columns an
// owner-reviewed apply most often replaces.
func TestRecordApplyHistory_RecordsEveryWrittenField(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)
	abridged := false
	book := &database.Book{
		ID:                "01J0BOOKID000000000000000",
		Title:             "Big Cats",
		ASIN:              new("B00OLD"),
		ISBN13:            new("9780000000001"),
		Description:       new("Old blurb."),
		PageCount:         new(100),
		AudibleRuntimeMin: new(300),
	}
	after := *book
	after.ASIN = new("B00NEW")
	after.ISBN13 = new("9780000000002")
	after.ISBN10 = new("0000000002")
	after.Description = new("New blurb.")
	after.Genre = new("Fantasy")
	after.Subtitle = new("A Tale")
	after.Abridged = &abridged
	after.PageCount = new(240)
	after.SeriesSecondary = new("Cats Universe")
	after.SeriesSecondaryPosition = new("2")
	after.AudibleRuntimeMin = new(600)
	_, err := svc.RecordApplyHistory(book, &after, nil, "Audible (owner-reviewed; certainty gate overridden: sequence_missing_on_candidate)")
	require.NoError(t, err)

	want := map[string]string{
		"asin":                      "B00OLD → B00NEW",
		"isbn13":                    "9780000000001 → 9780000000002",
		"isbn10":                    "(none) → 0000000002",
		"description":               "Old blurb. → New blurb.",
		"genre":                     "(none) → Fantasy",
		"subtitle":                  "(none) → A Tale",
		"abridged":                  "(none) → false",
		"page_count":                "100 → 240",
		"series_secondary":          "(none) → Cats Universe",
		"series_secondary_position": "(none) → 2",
		"audible_runtime_min":       "300 → 600",
	}
	for field, change := range want {
		summary := acts.summaryForField(t, field)
		require.True(t, strings.HasSuffix(summary, field+": "+change), "field %s: summary %q, want suffix %q", field, summary, change)
	}
}

// An owner-reviewed note is added on EVERY override, never deduplicated into
// the first: each is a separate decision.
func TestAppendOwnerReviewedNote_AddsOnEveryOverride(t *testing.T) {
	book := &database.Book{VersionNotes: new("audio_confirmed")}
	t1 := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	appendOwnerReviewedNote(book, "sequence_missing_on_candidate", t1)
	appendOwnerReviewedNote(book, "sequence_missing_on_candidate", t1)
	appendOwnerReviewedNote(book, "runtime_unknown_on_overwrite", t1.Add(time.Hour))
	lines := strings.Split(*book.VersionNotes, "\n")
	require.Equal(t, []string{
		"audio_confirmed",
		"owner_reviewed 2026-09-13T10:00:00Z: sequence_missing_on_candidate",
		"owner_reviewed 2026-09-13T10:00:00Z: sequence_missing_on_candidate",
		"owner_reviewed 2026-09-13T11:00:00Z: runtime_unknown_on_overwrite",
	}, lines)
}
