// file: internal/books/flip_fresh_store_test.go
// version: 0.1.0
// guid: b37d8afc-3f17-470e-aaad-eb39c4bbef9f
// last-edited: 2026-11-21

package books

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestFlipFreshStore: a flipped boot of a store with no settings blob (a new
// install before its first save).
//
// The bootstrap key lifetime is at core's default, 8h, which
// bootstrap_key_ttl holds and bootstrap_key_ttl_days (whole days) cannot. Both
// HEAD rows name core.auth.bootstrap.key_ttl, so the value reaches HEAD's
// Config through bootstrap_key_ttl and nothing is lost: the boot logs no error
// for it and lists no unrepresentable setting.
//
// The wizard's first save of the library root writes root_dir into a blob that
// did not exist (root_dir is a residual row until M6), and the projection then
// holds it.
func TestFlipFreshStore(t *testing.T) {
	work := copyR2Store(t)
	r2StoreSeed(t, work, func(s *database.PebbleStore) {
		require.NoError(t, s.DeleteSetting("config_blob"))
	})
	lib := filepath.Join(tempDir(t), "gotham-library")

	r2StoreRun(t, work, func(c *composed) {
		snap := config.Snapshot()
		require.Equal(t, "8h", snap.BootstrapKeyTTL, "the default bootstrap key lifetime is not projected")
		require.Zero(t, snap.BootstrapKeyTTLDays)
		for line := range strings.SplitSeq(c.newLog.String(), "\n") {
			if strings.Contains(line, "bootstrap_key_ttl") && strings.Contains(strings.ToUpper(line), "ERROR") {
				t.Errorf("a boot at the default bootstrap key lifetime logged an error: %s", line)
			}
		}
		in := c.mod.gapFillIn.Load()
		require.NotNil(t, in, "the boot bound no Facade")
		blob, err := c.store.GetSetting("config_blob")
		require.True(t, err != nil || blob == nil, "the boot wrote a settings blob before any save: the cell no longer saves into nothing")
		require.Empty(t, snap.RootDir)

		_, err, _ = putUpdate(t, config.Current(), map[string]any{"root_dir": lib, "setup_complete": true})
		require.NoError(t, err)
		require.Equal(t, lib, config.Snapshot().RootDir, "the first root save is not the projected root")
		require.Equal(t, strconv.Quote(lib), string(blobMember(t, c.store, "root_dir")), "the first root save did not create the blob")
	})
}
