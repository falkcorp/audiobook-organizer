// file: core/app/offline_config_test.go
// version: 1.0.0
// guid: 26a3e540-e2ae-454a-95cb-b251137091ff
// last-edited: 2026-11-10

package app

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
)

// TestConfigCheck_ListsRetiredNamesOpensNoStore (05-config §1.3.6, 06
// §6.4.5, SEC10-9): config check lists each retired name set with its
// canonical env name and file key, refused ones first, exits non-zero when
// one would refuse boot, and opens no store: the state directory is never
// created. With only warn-class names set it exits zero.
func TestConfigCheck_ListsRetiredNamesOpensNoStore(t *testing.T) {
	opts := offlineOpts(t)
	opts.StateDir = filepath.Join(t.TempDir(), "state")
	opts.RetiredConfigNames = []config.RetiredName{
		{Name: "GOTHAM_DB_PATH", Canonical: "core.storage.path"},
		{Name: "GOTHAM_JOURNAL_DAYS", Canonical: "core.log.journal.retention_days"},
		{Name: "GOTHAM_BAT_SIGNAL", Message: "the signal moved to the roof"},
		{Name: "GOTHAM_NOT_SET", Canonical: "core.log.journal.min_level"},
	}
	opts.Env = []string{"GOTHAM_DB_PATH=/srv/gotham/main.pebble", "GOTHAM_JOURNAL_DAYS=30", "GOTHAM_BAT_SIGNAL=on"}
	check := secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check")

	var out bytes.Buffer
	err := check.Run(t.Context(), nil, &out)
	if err == nil || !strings.Contains(err.Error(), "1 retired config names would stop the service") {
		t.Errorf("config check err = %v, want a non-zero exit naming one refusing name", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("output has %d lines:\n%s", len(lines), out.String())
	}
	for i, want := range [][]string{
		{"env GOTHAM_DB_PATH", "STOPS THE SERVICE FROM STARTING", "GOTHAM_CORE_STORAGE_PATH", "core.storage.path"},
		{"env GOTHAM_JOURNAL_DAYS", "ignored", "GOTHAM_CORE_LOG_JOURNAL_RETENTION_DAYS", "core.log.journal.retention_days"},
		{"env GOTHAM_BAT_SIGNAL", "ignored", "removed", "the signal moved to the roof"},
		{"3 retired config names set, 1 of them"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i], w) {
				t.Errorf("line %d %q lacks %q", i, lines[i], w)
			}
		}
	}
	if strings.Contains(out.String(), "GOTHAM_NOT_SET") || strings.Contains(out.String(), "/srv/gotham") {
		t.Errorf("the output lists an unset name or prints a value:\n%s", out.String())
	}
	if _, err := os.Stat(opts.StateDir); !os.IsNotExist(err) {
		t.Errorf("config check created the state directory: %v", err)
	}

	opts.Env = []string{"GOTHAM_JOURNAL_DAYS=30"}
	out.Reset()
	if err := secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check").Run(t.Context(), nil, &out); err != nil {
		t.Errorf("warn-only config check = %v", err)
	}
	if !strings.Contains(out.String(), "1 retired config names set, 0 of them") {
		t.Errorf("warn-only output:\n%s", out.String())
	}

	opts.Env = []string{"GOTHAM_CORE_LOG_JOURNAL_RETENTION_DAYS=thirty"}
	out.Reset()
	if err := secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check").Run(t.Context(), nil, &out); err == nil {
		t.Error("config check accepted a value that does not decode")
	}

	opts.Env = nil
	opts.RetiredConfigNames = nil
	out.Reset()
	if err := secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check").Run(t.Context(), nil, &out); err != nil ||
		!strings.Contains(out.String(), "No retired config names are set") {
		t.Errorf("clean config check = %v, output %q", err, out.String())
	}
}

// TestOptions_ConfigOptionsCopies (06 §6.4.5): configOptions carries the
// app's identity, env, file and both F3 lists, copied so the caller's slices
// stay its own.
func TestOptions_ConfigOptionsCopies(t *testing.T) {
	opts := Options{
		AppID: "gotham", EnvPrefix: "GCPD", ConfigFile: "/etc/gotham.yaml",
		Env:                []string{"GCPD_CORE_LOG_JOURNAL_RETENTION_DAYS=7"},
		RetiredConfigNames: []config.RetiredName{{Name: "GCPD_DAYS", Canonical: "core.log.journal.retention_days"}},
		ConfigEnvBindings:  []config.EnvName{{Name: "OTEL_EXPORTER_OTLP_ENDPOINT", Path: "core.tracing.otlp.endpoint"}},
	}
	o := opts.configOptions()
	if o.AppID != "gotham" || o.EnvPrefix != "GCPD" || o.FilePath != "/etc/gotham.yaml" || o.Logger == nil ||
		!slices.Equal(o.Environ, opts.Env) || !slices.Equal(o.Retired, opts.RetiredConfigNames) ||
		!slices.Equal(o.EnvBindings, opts.ConfigEnvBindings) {
		t.Fatalf("configOptions = %+v", o)
	}
	o.Environ[0], o.Retired[0].Name, o.EnvBindings[0].Name = "X=1", "X", "X"
	if opts.Env[0] == "X=1" || opts.RetiredConfigNames[0].Name == "X" || opts.ConfigEnvBindings[0].Name == "X" {
		t.Error("configOptions shares the caller's slices")
	}
	t.Setenv("GOTHAM_BATCAVE", "1")
	if env := (Options{AppID: "gotham"}).configOptions().Environ; !slices.Contains(env, "GOTHAM_BATCAVE=1") {
		t.Error("a nil Options.Env does not fall back to os.Environ")
	}
}
