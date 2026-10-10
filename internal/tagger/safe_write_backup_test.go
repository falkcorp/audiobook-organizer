// file: internal/tagger/safe_write_backup_test.go
// version: 1.0.0
// guid: fbc0620d-3437-4d08-b716-9d12968f8297
// last-edited: 2026-10-10

package tagger

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
)

// setCreateBackups sets create_backups for one test and restores the previous
// configuration on cleanup. Tests using it must not run in parallel.
func setCreateBackups(t *testing.T, on bool) {
	t.Helper()
	prev := config.Snapshot()
	config.Mutate(func(c *config.Config) { c.CreateBackups = on })
	t.Cleanup(func() { config.Mutate(func(c *config.Config) { *c = prev }) })
}

// The create_backups setting drives KeepBackup, and a WithoutBackup context
// overrides it. Both the HashStore and the nil-HashStore branch of hashOptions
// are covered: the nil branch returns an empty options value, so a flag set
// only inside the other branch would be lost there.
func TestWriteOptions_CreateBackupsAndOptOut(t *testing.T) {
	withStore := SafeWriteDeps{BookFileID: "bf-1", HashStore: &pathRecorder{rows: map[string]string{}}}
	noStore := SafeWriteDeps{}
	cases := []struct {
		name    string
		setting bool
		optOut  bool
		want    bool
	}{
		{"setting on", true, false, true},
		{"setting on, bulk opt-out", true, true, false},
		{"setting off", false, false, false},
		{"setting off, bulk opt-out", false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setCreateBackups(t, tc.setting)
			ctx := context.Background()
			if tc.optOut {
				ctx = WithoutBackup(ctx)
			}
			for depsName, deps := range map[string]SafeWriteDeps{"with HashStore": withStore, "nil HashStore": noStore} {
				if got := deps.writeOptions(ctx, "/library/a.m4b", "/library/a.m4b").KeepBackup; got != tc.want {
					t.Errorf("%s: KeepBackup = %v, want %v", depsName, got, tc.want)
				}
			}
			if got := BackupWanted(ctx); got != tc.want {
				t.Errorf("BackupWanted = %v, want %v", got, tc.want)
			}
		})
	}
}

// The opt-out survives derived contexts: a bulk op wraps its ctx once and then
// derives cancel and value contexts from it before the writes.
func TestWithoutBackup_SurvivesDerivedContexts(t *testing.T) {
	setCreateBackups(t, true)
	ctx, cancel := context.WithCancel(WithoutBackup(context.Background()))
	defer cancel()
	type otherKey struct{}
	ctx = context.WithValue(ctx, otherKey{}, "x")
	if BackupWanted(ctx) {
		t.Fatal("a context derived from WithoutBackup must still opt out")
	}
}

// A nil context means "no opt-out" rather than a panic.
func TestBackupWanted_NilContext(t *testing.T) {
	setCreateBackups(t, true)
	var nilCtx context.Context
	if !BackupWanted(nilCtx) {
		t.Fatal("nil ctx with create_backups on: want a backup")
	}
}
