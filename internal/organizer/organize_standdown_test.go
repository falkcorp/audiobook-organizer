// file: internal/organizer/organize_standdown_test.go
// version: 1.0.0
// guid: 2e9c4a70-5d18-4b63-8f07-c1a6e3d9b254
// last-edited: 2026-09-12

package organizer

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// A canceled or stood-down run returns right after the auto-backup instead of
// going on to load books and fetch metadata for them. Before the fix
// PerformOrganize ignored the canceled backup and walked into the
// FetchMetadataFirst loop.
func TestPerformOrganize_StopsAfterCanceledAutoBackup(t *testing.T) {
	prev := config.AppConfig
	t.Cleanup(func() { config.AppConfig = prev })
	config.AppConfig.RootDir = t.TempDir()

	var reads atomic.Int32
	mockDB := &database.MockStore{
		GetBookByIDFunc: func(string) (*database.Book, error) {
			reads.Add(1)
			return nil, nil
		},
	}
	svc := NewService(mockDB)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := svc.PerformOrganize(ctx,
		&Request{BookIDs: []string{"b1"}, FetchMetadataFirst: true},
		logger.New("test"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PerformOrganize on a canceled run: want context.Canceled, got %v", err)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("organize loaded %d books after the run was canceled; it must stop after the backup", n)
	}
}
