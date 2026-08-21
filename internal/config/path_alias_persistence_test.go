// file: internal/config/path_alias_persistence_test.go
// version: 1.0.0
// guid: 3f7a2c9e-6b1d-4e5f-8a0c-2d9b6e4f1a7c
// last-edited: 2026-08-21

// Controller ruling for Task 2: SeedPathAliases and ValidatePathAliases are
// defined and unit tested (path_alias_test.go), but the brief never wired
// ValidatePathAliases into a production code path, so the drift guard would
// never actually run. This file pins that LoadConfigFromDatabase calls
// ValidatePathAliases after seeding and logs a contradiction at error level —
// without failing config load, because a config contradiction here is a
// display defect, not a data-integrity threat.

package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// TestLoadConfigFromDatabaseLogsPathAliasContradiction is the positive case:
// a stored config whose path_aliases disagree with itunes.path_mappings must
// produce a logged error, proving ValidatePathAliases is actually invoked on
// load rather than merely defined and unit tested in isolation.
func TestLoadConfigFromDatabaseLogsPathAliasContradiction(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	// path_aliases claims Z: for /library/books; itunes.path_mappings claims
	// W: for the same root — the exact contradiction shape ValidatePathAliases
	// is built to catch.
	store := storeWithBlob(t, `{
		"path_aliases": [{"root": "/library/books", "windows": "Z:"}],
		"itunes": {"path_mappings": [{"from": "W:", "to": "/library/books"}]}
	}`)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase must not fail load on a path-alias contradiction: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "path_aliases contradicts itunes.path_mappings") {
		t.Fatalf("a path-alias/itunes.path_mappings contradiction must be logged at error level; got:\n%s", out)
	}
	if !strings.Contains(out, "/library/books") {
		t.Errorf("the logged error should name the offending root; got:\n%s", out)
	}

	// Load must succeed regardless of the contradiction: this is a display
	// defect, never a reason to abort config load.
	got := Snapshot()
	if len(got.PathAliases) != 1 || got.PathAliases[0].Windows != "Z:" {
		t.Errorf("the stored (contradictory) alias must still be loaded, not discarded; got %+v", got.PathAliases)
	}
}

// TestLoadConfigFromDatabaseNoLogWhenPathAliasesAgree is the discriminating
// half: agreement must not produce a spurious error line, or the log becomes
// noise nobody trusts.
func TestLoadConfigFromDatabaseNoLogWhenPathAliasesAgree(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	store := storeWithBlob(t, `{
		"path_aliases": [{"root": "/library/books", "windows": "W:"}],
		"itunes": {"path_mappings": [{"from": "W:", "to": "/library/books"}]}
	}`)

	if err := LoadConfigFromDatabase(store); err != nil {
		t.Fatalf("LoadConfigFromDatabase: %v", err)
	}

	if strings.Contains(buf.String(), "path_aliases contradicts itunes.path_mappings") {
		t.Errorf("agreeing path_aliases and itunes.path_mappings must not log a contradiction; got:\n%s", buf.String())
	}
}
