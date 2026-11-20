// file: internal/books/options_updater_test.go
// version: 1.0.0
// guid: 4e7a1c93-b605-4d28-9f4e-83a02d6c17b9
// last-edited: 2026-11-18

package books

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	coreupdater "github.com/falkcorp/mediacore/updater"
)

// TestOfflineOptions_FetchReleaseAsksForThePublishedAsset (F8-02 review M1):
// `updater fetch-release` reads app.Options.Updater, and the asset it looks
// for must be the one releases publish, not the name the gin apply asks for.
func TestOfflineOptions_FetchReleaseAsksForThePublishedAsset(t *testing.T) {
	books := filepath.Join(tempDir(t), "books.pebble")
	withConfig(t, config.Config{DatabaseType: "pebble", DatabasePath: books})
	t.Setenv(storagePathEnv, books)
	opts, err := OfflineOptions(BootInputs{})
	require.NoError(t, err)
	require.NotNil(t, opts.Updater, "the offline options carry no release source")
	require.NotNil(t, opts.Updater.AssetName, "the fetch falls back to core's default asset name, which no release publishes")
	assert.Equal(t, "audiobook-organizer_Linux_x86_64", opts.Updater.AssetName(coreupdater.Release{}, "linux", "amd64"))
	assert.Equal(t, "audiobook-organizer_Linux_arm64", opts.Updater.AssetName(coreupdater.Release{}, "linux", "arm64"))
}
