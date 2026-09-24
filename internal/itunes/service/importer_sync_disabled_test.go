// file: internal/itunes/service/importer_sync_disabled_test.go
// version: 1.0.0
// guid: 8d1f4c7a-2b9e-4e30-a6d5-71c3e0f9b248
// last-edited: 2026-09-24

package itunesservice

import (
	"context"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// enableSyncForTest turns itunes.sync_enabled on for one test: Sync refuses
// while it is off, and the zero-value test config has it off.
func enableSyncForTest(t *testing.T) {
	t.Helper()
	prev := config.AppConfig.ITunes.SyncEnabled
	config.AppConfig.ITunes.SyncEnabled = true
	t.Cleanup(func() { config.AppConfig.ITunes.SyncEnabled = prev })
}

// With itunes.sync_enabled off, Sync refuses before reading the library, so
// it can never repoint a library row back at the iTunes file.
func TestSync_DisabledRefusesBeforeReading(t *testing.T) {
	prev := config.AppConfig.ITunes.SyncEnabled
	config.AppConfig.ITunes.SyncEnabled = false
	t.Cleanup(func() { config.AppConfig.ITunes.SyncEnabled = prev })

	imp := &Importer{} // no store: any read past the gate would panic
	err := imp.Sync(context.Background(), "/nonexistent/iTunes Library.xml", nil, nil, logger.New("test"))
	if !errors.Is(err, ErrSyncDisabled) {
		t.Fatalf("Sync with sync disabled: got %v, want ErrSyncDisabled", err)
	}
}
