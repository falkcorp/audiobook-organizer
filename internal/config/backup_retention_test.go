// file: internal/config/backup_retention_test.go
// version: 1.0.0
// guid: c2a94f17-6b3d-4e80-9d15-7a0e8b3f6c42
// last-edited: 2026-10-09

package config

import "testing"

func TestEffectiveBackupRetentionDays(t *testing.T) {
	for _, tc := range []struct {
		name         string
		backup, soft int
		want         int
	}{
		{"key set wins over soft-delete", 10, 30, 10},
		{"key unset falls back to soft-delete", 0, 30, 30},
		{"key set, soft-delete unset", 10, 0, 10},
		{"both unset falls back to 30", 0, 0, 30},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prev := AppConfig
			t.Cleanup(func() { AppConfig = prev })
			AppConfig.BackupRetentionDays = tc.backup
			AppConfig.PurgeSoftDeletedAfterDays = tc.soft
			if got := EffectiveBackupRetentionDays(); got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}
