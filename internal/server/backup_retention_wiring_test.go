// file: internal/server/backup_retention_wiring_test.go
// version: 1.0.0
// guid: 6a2f9c13-8e47-4b05-a1d6-3c7e50b92f84
// last-edited: 2026-10-13

package server

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// TestServerBackupRetentionDays pins that the server hands the backup sweep
// config.EffectiveBackupRetentionDays, not the raw backup_retention_days key.
// The plugin floors a non-positive answer at 30, so reading the raw key would
// silently lose the purge_soft_deleted_after_days fallback that 04-P3a unified.
func TestServerBackupRetentionDays(t *testing.T) {
	for _, tc := range []struct {
		name         string
		backup, soft int
		want         int
	}{
		{"the backup key wins", 10, 30, 10},
		{"unset backup key falls back to the soft-delete window", 0, 10, 10},
		{"both unset falls back to 30", 0, 0, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := config.AppConfig
			t.Cleanup(func() { config.AppConfig = prev })
			config.AppConfig.BackupRetentionDays = tc.backup
			config.AppConfig.PurgeSoftDeletedAfterDays = tc.soft

			require.Equal(t, tc.want, (&Server{}).BackupRetentionDays())
		})
	}
}
