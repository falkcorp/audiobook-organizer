// file: internal/books/checkpoint_health_test.go
// version: 1.0.0
// guid: 960c292a-4e60-4e6a-b105-7f67930597b1
// last-edited: 2026-11-08

package books

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	system "github.com/falkcorp/audiobook-organizer/internal/server/handlers/system"
	"github.com/falkcorp/mediacore/storage"
)

// TestCheckpointPosture (05-storage §5S.3.12; 05-health §5H.3.1): every
// checkpoint raises storage_checkpoint_retained with its delete_after; the
// live store's rollback and every scratch entry raise storage_checkpoint_open
// with their age; every loosened path raises storage_checkpoint_mode, with
// one entry counting the paths the sweep did not list; nothing raises
// nothing; and a health read that fails still raises retained.
func TestCheckpointPosture(t *testing.T) {
	now := time.Date(2026, time.March, 14, 15, 9, 26, 0, time.UTC)
	due := now.Add(7 * 24 * time.Hour)
	h := storage.CheckpointHealth{
		Checkpoints: []storage.CheckpointInfo{
			{Name: "20260307T000000Z-c0b1e001", Status: "completed", TakenAt: now.Add(-7 * 24 * time.Hour), DeleteAfter: &due},
			{Name: "20260314T140926Z-0ac1e002", Status: "served", TakenAt: now.Add(-time.Hour), Rollback: true},
		},
		Scratch:    []storage.ScratchInfo{{Path: "/batcave/.mediacore-checkpoints/scratch/rehearsal-robin", ModTime: now.Add(-2 * time.Hour)}},
		Loose:      []string{"/batcave/.mediacore-checkpoints/20260307T000000Z-c0b1e001/db"},
		LooseTotal: 3,
	}
	got := checkpointPosture(h, nil, now)
	want := []system.PostureToken{
		{Token: system.DegradedStorageCheckpointRetained, Subject: "20260307T000000Z-c0b1e001", DeleteAfter: &due},
		{Token: system.DegradedStorageCheckpointRetained, Subject: "20260314T140926Z-0ac1e002"},
		{Token: system.DegradedStorageCheckpointOpen, Subject: "20260314T140926Z-0ac1e002", AgeSeconds: 3600},
		{Token: system.DegradedStorageCheckpointOpen, Subject: "/batcave/.mediacore-checkpoints/scratch/rehearsal-robin", AgeSeconds: 7200},
		{Token: system.DegradedStorageCheckpointMode, Subject: "/batcave/.mediacore-checkpoints/20260307T000000Z-c0b1e001/db"},
		{Token: system.DegradedStorageCheckpointMode, Subject: "2 more paths not listed"},
	}
	assert.Equal(t, want, got)

	assert.Empty(t, checkpointPosture(storage.CheckpointHealth{}, nil, now))

	failed := checkpointPosture(storage.CheckpointHealth{}, errors.New("bane locked the vault"), now)
	require.Len(t, failed, 1)
	assert.Equal(t, system.DegradedStorageCheckpointRetained, failed[0].Token)
}
