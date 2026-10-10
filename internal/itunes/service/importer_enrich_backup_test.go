// file: internal/itunes/service/importer_enrich_backup_test.go
// version: 1.0.0
// guid: 13467322-3d42-4906-96a6-f205648e8c09
// last-edited: 2026-10-10

package itunesservice

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/tagger"
)

// backupCtxFetcher records, per call, whether the ctx handed to
// FetchMetadataForBook would still ask the tag writer for a backup.
type backupCtxFetcher struct {
	*fakeMetadataFetcher
	mu     sync.Mutex
	wanted []bool
}

func (f *backupCtxFetcher) FetchMetadataForBook(ctx context.Context, id string) (*metafetch.FetchMetadataResponse, error) {
	f.mu.Lock()
	f.wanted = append(f.wanted, tagger.BackupWanted(ctx))
	f.mu.Unlock()
	return f.fakeMetadataFetcher.FetchMetadataForBook(ctx, id)
}

// The import enriches every book it created, a bulk tag write: with
// create_backups on, the ctx each fetch receives must carry the opt-out so
// the queued file work keeps no .bak-* sibling (owner decision D69).
func TestEnrichImportedBooks_OptsOutOfBackups(t *testing.T) {
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = true })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
	require.True(t, tagger.BackupWanted(context.Background()), "fixture error: create_backups is off")

	const n = 4
	_, store := buildEnrichFixture(t, n)
	fetcher := &backupCtxFetcher{fakeMetadataFetcher: newFakeMetadataFetcher(nil)}
	imp := &Importer{store: store, mfs: fetcher, enrichConcurrencyOverride: 2}

	imp.enrichImportedBooks(context.Background(), fixtureBookIDs(n), &itunesImportStatus{}, logger.New("test-enrich-backup"))

	require.Len(t, fetcher.wanted, n)
	for i, w := range fetcher.wanted {
		require.False(t, w, "fetch %d would keep a backup", i)
	}
}
