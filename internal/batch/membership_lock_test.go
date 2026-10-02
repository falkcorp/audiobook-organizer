// file: internal/batch/membership_lock_test.go
// version: 1.0.0
// guid: 3e8b1f6a-7c2d-4a95-9e04-b6d1c8f2a573
// last-edited: 2026-10-02

package batch

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary"
	"github.com/falkcorp/audiobook-organizer/internal/versionprimary/vptest"
)

// A batch edit that moves a book out of version group g waits for g's
// hand-off lock: a reader holding it (itunes.regroup's apply-time recheck
// through its moves) sees no member leave until it releases. The same holds
// for the group the book joins.
func TestBatchUpdate_GroupChangeWaitsForGroupLocks(t *testing.T) {
	for _, held := range []string{"g", "h"} {
		t.Run("holding "+held, func(t *testing.T) {
			f := vptest.New(t)
			prev := config.AppConfig.RootDir
			config.AppConfig.RootDir = f.Root
			t.Cleanup(func() { config.AppConfig.RootDir = prev })
			f.Book(t, vptest.Spec{ID: "inc", Group: "g", Primary: "true"})
			mover := f.Book(t, vptest.Spec{ID: "mover", Group: "g", Primary: "false"})

			unlock := versionprimary.LockGroup(held)
			done := make(chan *BatchResponse, 1)
			go func() {
				done <- NewBatchService(f.S).UpdateAudiobooks(&BatchUpdateRequest{
					IDs: []string{mover}, Updates: map[string]any{"version_group_id": "h"},
				})
			}()
			select {
			case <-done:
				unlock()
				t.Fatalf("batch group change wrote while group %q's lock was held", held)
			case <-time.After(300 * time.Millisecond):
			}
			b, err := f.S.GetBookByID(mover)
			require.NoError(t, err)
			require.Equal(t, "g", *b.VersionGroupID, "book left the group while its lock was held")
			unlock()

			select {
			case resp := <-done:
				require.Equal(t, 1, resp.Success, "errors: %+v", resp.Results)
			case <-time.After(10 * time.Second):
				t.Fatal("batch group change still blocked after the lock was released")
			}
			b, err = f.S.GetBookByID(mover)
			require.NoError(t, err)
			require.Equal(t, "h", *b.VersionGroupID)
		})
	}
}
