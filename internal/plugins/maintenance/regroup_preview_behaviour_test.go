// file: internal/plugins/maintenance/regroup_preview_behaviour_test.go
// version: 1.0.0
// guid: 646f265d-ef3c-42a4-b9c8-1764018f6b26
// last-edited: 2026-10-18

package maintenance

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	itunesservice "github.com/falkcorp/audiobook-organizer/internal/itunes/service"
)

// countingReviewStore counts the review-queue writes the regroup op makes.
type countingReviewStore struct {
	database.Store
	upserts atomic.Int64
	deletes atomic.Int64
}

func (c *countingReviewStore) UpsertReviewItem(it database.ReviewItem) (database.ReviewItem, error) {
	c.upserts.Add(1)
	return c.Store.UpsertReviewItem(it)
}

func (c *countingReviewStore) DeleteReviewItem(id string) error {
	c.deletes.Add(1)
	return c.Store.DeleteReviewItem(id)
}

// TestPreviewBehaviour_RegroupShatteredAI runs the registered op with the
// params the /api/v3/ops start path would queue for each mode over a library
// that has one regroup candidate and one stale pending hold, so a live run
// both upserts and deletes. A preview must call neither and leave the queue as
// it found it; a live run must write.
func TestPreviewBehaviour_RegroupShatteredAI(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		wantWrite bool
	}{
		{"preview", false},
		{"live", true},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			s := regroupStore(t)
			base := "/lib/Example Author/Example Book"
			for i := 1; i <= 6; i++ {
				seedShatterBook(t, s, base+"/Example Book - "+itoa(i)+"/01.mp3")
			}
			staleKey := regroupDedupKey(itunesservice.KindAnthology, "/lib/Example Author/Gone Book")
			seedHold(t, s, itunesservice.KindAnthology, staleKey, "/lib/Example Author/Gone Book")

			store := &countingReviewStore{Store: s}
			p := &Plugin{deps: fakeDeps{store: store}}
			def := p.regroupShatteredAIDef()

			params, err := def.Preview.Resolve(json.RawMessage(`{}`), tc.mode)
			if err != nil {
				t.Fatalf("Resolve(%s): %v", tc.mode, err)
			}
			rep := &fakeReporter{}
			if err := def.Run(context.Background(), params, rep); err != nil {
				t.Fatalf("Run(%s) with %s: %v", tc.mode, params, err)
			}

			upserts, deletes := store.upserts.Load(), store.deletes.Load()
			keys := remainingKeys(t, s)
			if !tc.wantWrite {
				if upserts != 0 || deletes != 0 {
					t.Fatalf("preview wrote the review queue: %d upserts, %d deletes", upserts, deletes)
				}
				if len(keys) != 1 || !keys[staleKey] {
					t.Fatalf("preview changed the queue, want only the seeded stale hold: %v", keys)
				}
				logs := strings.Join(rep.logs, "\n")
				if !strings.Contains(logs, "would upsert 1 review holds") || !strings.Contains(logs, "delete 1 stale pending holds") {
					t.Fatalf("preview did not report what it would write:\n%s", logs)
				}
				return
			}
			if upserts < 1 || deletes < 1 {
				t.Fatalf("live run made %d upserts and %d deletes, want at least one of each", upserts, deletes)
			}
			if keys[staleKey] || len(keys) != 1 {
				t.Fatalf("live run did not replace the stale hold with the candidate: %v", keys)
			}
		})
	}
}

// Naming both spellings with different values is refused, not guessed.
func TestRegroupShatteredAI_ConflictingModeRefused(t *testing.T) {
	p := &Plugin{deps: fakeDeps{store: regroupStore(t)}}
	err := p.runRegroupShatteredAI(context.Background(), json.RawMessage(`{"dry_run":false,"dryRun":true}`), &fakeReporter{})
	if err == nil || !strings.Contains(err.Error(), "disagree") {
		t.Fatalf("conflicting dry_run/dryRun must be refused, got %v", err)
	}
}
