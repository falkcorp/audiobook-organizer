// file: internal/activity/service.go
// version: 1.4.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-08-11

package activity

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// Service wraps an ActivityStorer and provides business-level methods
// for recording and querying unified activity log entries.
type Service struct {
	store database.ActivityStorer
}

// NewService creates a new Service backed by the given store.
func NewService(store database.ActivityStorer) *Service {
	return &Service{store: store}
}

// Record inserts an activity entry into the store. Automatically enriches entry
// with derived tags (op:, book:, outcome:, source:, action:, scope:) before
// storing. The entry ID is discarded; callers that need it should call the
// store directly.
func (s *Service) Record(entry database.ActivityEntry) error {
	EnrichTags(&entry)
	_, err := s.store.Record(entry)
	return err
}

// Query returns entries matching the filter plus the total matching count.
// Callers on a request path must pass the request's context: the scan aborts
// as soon as it is cancelled, which is what stops an abandoned request from
// scanning the whole log after the client has disconnected.
func (s *Service) Query(ctx context.Context, filter database.ActivityFilter) ([]database.ActivityEntry, int, error) {
	return s.store.Query(ctx, filter)
}

// Summarize collapses old entries in the given tier that are older than olderThan.
// Returns the count of original rows deleted.
func (s *Service) Summarize(ctx context.Context, olderThan time.Time, tier string) (int, error) {
	return s.store.Summarize(ctx, olderThan, tier)
}

// Prune hard-deletes all entries of the given tier older than olderThan.
// Returns the number of rows deleted.
func (s *Service) Prune(olderThan time.Time, tier string) (int, error) {
	return s.store.Prune(olderThan, tier)
}

// CompactByDay groups old change/debug entries by UTC day into digest rows.
func (s *Service) CompactByDay(ctx context.Context, olderThan time.Time) (database.CompactResult, error) {
	return s.store.CompactByDay(ctx, olderThan)
}

// GetDistinctSources returns all unique sources with their entry counts,
// narrowed by the given filter's tier/level/since/until/search fields.
// As with Query, request-path callers must pass the request's context.
func (s *Service) GetDistinctSources(ctx context.Context, filter database.ActivityFilter) ([]database.SourceCount, error) {
	return s.store.GetDistinctSources(ctx, filter)
}

// RecompactDigests re-derives type, tier, and tags on all stored daily-digest
// entries that were compacted before tag enrichment was added (2026-05-20).
// Returns the count of digests touched and skipped.
func (s *Service) RecompactDigests(ctx context.Context) (database.RecompactResult, error) {
	return s.store.RecompactDigests(ctx)
}

// Store returns the underlying ActivityStorer (e.g. for close or direct access).
func (s *Service) Store() database.ActivityStorer {
	return s.store
}
