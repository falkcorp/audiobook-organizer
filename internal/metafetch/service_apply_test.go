// file: internal/metafetch/service_apply_test.go
// version: 1.2.0
// guid: bc6eeacd-35fa-4d23-a051-ee09424676a9
// last-edited: 2026-08-29

package metafetch

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/activity"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/stretchr/testify/require"
)

// capturingActivityStore is a minimal database.ActivityStorer that keeps every
// recorded entry in memory. activity.Service takes the store interface rather
// than exposing what it recorded, so capturing here is the only way to see the
// Summary string RecordChangeHistory builds. The conformance assertion below is
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
// the assertion stable: RecordChangeHistory emits one entry per changed field
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

// Remaining ActivityStorer methods — unused by RecordChangeHistory, present
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

func (c *capturingActivityStore) Prune(time.Time, string) (int, error) { return 0, nil }

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

func (c *capturingActivityStore) MigrateSystemActivityLogs() (int, error) { return 0, nil }

func (c *capturingActivityStore) Close() error { return nil }

var _ database.ActivityStorer = (*capturingActivityStore)(nil)

// newChangeHistoryHarness wires a metafetch Service to a MockStore and an
// activity.Service backed by a capturing store, then returns both.
func newChangeHistoryHarness(t *testing.T) (*Service, *capturingActivityStore) {
	t.Helper()
	acts := &capturingActivityStore{}
	svc := NewService(&database.MockStore{})
	svc.SetActivityService(activity.NewService(acts))
	return svc, acts
}

// RecordChangeHistory_SummaryLeadsWithBookTitle is the anti-over-suppression
// case: an ordinary change with a non-empty old value must still render its
// full before/after line, now prefixed with the book title.
func TestRecordChangeHistory_SummaryLeadsWithBookTitle(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)

	book := &database.Book{
		ID:       "01J0BOOKID000000000000000",
		Title:    "The Whispering Night",
		Narrator: stringPtr("Alex Kozlowski"),
	}
	svc.RecordChangeHistory(book, metadata.BookMetadata{Narrator: "Grant Cartwright"}, "audible")

	summary := acts.summaryForField(t, "narrator")
	require.True(t, strings.HasPrefix(summary, "The Whispering Night: Applied"),
		"summary must lead with the book title followed by ': Applied', got %q", summary)
	require.Equal(t, "The Whispering Night: Applied narrator: Alex Kozlowski → Grant Cartwright", summary)
}

// RecordChangeHistory_EmptyOldValueRendersNone covers a first-ever value: the
// from-side must read "(none)" rather than leaving a dangling arrow.
func TestRecordChangeHistory_EmptyOldValueRendersNone(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)

	book := &database.Book{
		ID:                   "01J0BOOKID000000000000000",
		Title:                "The Whispering Night",
		AudiobookReleaseYear: nil, // no prior value -> oldVal is ""
	}
	svc.RecordChangeHistory(book, metadata.BookMetadata{
		PublishYear:                   2021,
		PublishYearIsAudiobookRelease: true,
	}, "audible")

	summary := acts.summaryForField(t, "audiobook_release_year")
	require.Contains(t, summary, "(none) → ", "empty old value must render as (none), got %q", summary)
	require.NotContains(t, summary, ":  → ", "summary must not contain a dangling arrow, got %q", summary)
	require.Equal(t, "The Whispering Night: Applied audiobook_release_year: (none) → 2021", summary)
}

// RecordChangeHistory_EmptyTitleFallsBackToID covers the edge case where the
// book has no title: the line must not start with a bare ": Applied ...".
func TestRecordChangeHistory_EmptyTitleFallsBackToID(t *testing.T) {
	svc, acts := newChangeHistoryHarness(t)

	book := &database.Book{
		ID:       "01J0BOOKID000000000000000",
		Title:    "",
		Narrator: stringPtr("Alex Kozlowski"),
	}
	svc.RecordChangeHistory(book, metadata.BookMetadata{Narrator: "Grant Cartwright"}, "audible")

	summary := acts.summaryForField(t, "narrator")
	require.False(t, strings.HasPrefix(summary, ": Applied"), "summary must not start with a bare ': Applied', got %q", summary)
	require.Equal(t, "01J0BOOKID000000000000000: Applied narrator: Alex Kozlowski → Grant Cartwright", summary)
}
