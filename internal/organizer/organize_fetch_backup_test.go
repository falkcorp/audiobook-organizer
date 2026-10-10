// file: internal/organizer/organize_fetch_backup_test.go
// version: 1.0.0
// guid: 5b8e2f41-9c6d-4a37-b0e5-7d1f3a9c2e68
// last-edited: 2026-10-10

package organizer

import (
	"context"
	"sync"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// Organize with fetch_metadata_first fetches metadata for every book it was
// given, and each fetch queues a cover embed and a tag write. That is a bulk
// write, so the context each fetch receives must opt out of create_backups
// (owner decision D69), or every fetched file keeps a full .bak-* copy.
func TestPerformOrganize_FetchFirstOptsOutOfBackups(t *testing.T) {
	prev := config.Snapshot()
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	config.Mutate(func(c *config.Config) {
		c.RootDir = t.TempDir()
		c.DatabasePath = "" // no auto-backup
		c.CreateBackups = true
	})
	if !tagger.BackupWanted(context.Background()) {
		t.Fatal("precondition: create_backups on, so a plain context wants a backup")
	}

	mockDB := &database.MockStore{
		GetBookByIDFunc: func(id string) (*database.Book, error) {
			return &database.Book{ID: id, Title: "Synthetic Title"}, nil
		},
		GetAllBooksCoreFunc: func(int, int) ([]database.BookCore, error) { return nil, nil },
	}
	svc := NewService(mockDB)
	var mu sync.Mutex
	var ctxs []context.Context
	svc.FetchMetadataForBook = func(ctx context.Context, _ string) (any, error) {
		mu.Lock()
		defer mu.Unlock()
		ctxs = append(ctxs, ctx)
		return nil, nil
	}

	_ = svc.PerformOrganize(context.Background(),
		&Request{BookIDs: []string{"b1", "b2"}, FetchMetadataFirst: true},
		logger.New("test"))

	mu.Lock()
	defer mu.Unlock()
	if len(ctxs) != 2 {
		t.Fatalf("FetchMetadataForBook calls = %d, want 2", len(ctxs))
	}
	for i, ctx := range ctxs {
		if tagger.BackupWanted(ctx) {
			t.Errorf("fetch %d: the context wants a backup; organize's fetch-first loop dropped the bulk opt-out", i)
		}
	}
}
