// file: core/app/offline_config_test.go
// version: 1.0.1
// guid: 26a3e540-e2ae-454a-95cb-b251137091ff
// last-edited: 2026-11-11

package app

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/storage"
)

// recordingFS is an in-memory storage filesystem that records every call
// that would open, create, lock or list a store.
type recordingFS struct {
	vfs.FS
	mu    sync.Mutex
	calls []string
}

func (r *recordingFS) note(op, name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, op+" "+name)
}

func (r *recordingFS) Create(name string, c vfs.DiskWriteCategory) (vfs.File, error) {
	r.note("create", name)
	return r.FS.Create(name, c)
}

func (r *recordingFS) Open(name string, opts ...vfs.OpenOption) (vfs.File, error) {
	r.note("open", name)
	return r.FS.Open(name, opts...)
}

func (r *recordingFS) OpenReadWrite(name string, c vfs.DiskWriteCategory, opts ...vfs.OpenOption) (vfs.File, error) {
	r.note("open-rw", name)
	return r.FS.OpenReadWrite(name, c, opts...)
}

func (r *recordingFS) OpenDir(name string) (vfs.File, error) {
	r.note("opendir", name)
	return r.FS.OpenDir(name)
}

func (r *recordingFS) MkdirAll(dir string, perm os.FileMode) error {
	r.note("mkdir", dir)
	return r.FS.MkdirAll(dir, perm)
}

func (r *recordingFS) Lock(name string) (io.Closer, error) {
	r.note("lock", name)
	return r.FS.Lock(name)
}

func (r *recordingFS) List(dir string) ([]string, error) {
	r.note("list", dir)
	return r.FS.List(dir)
}

func (r *recordingFS) Stat(name string) (vfs.FileInfo, error) {
	r.note("stat", name)
	return r.FS.Stat(name)
}

func (r *recordingFS) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// TestConfigCheck_ListsRetiredNamesOpensNoStore (05-config §1.3.6, 06
// §6.4.5, SEC10-9): config check lists each retired name set with its
// canonical env name and file key, refused ones first, exits non-zero when
// one would refuse boot, and opens no store: the recording storage
// filesystem sees no call and the state directory is never created. With only warn-class names set it exits zero.
func TestConfigCheck_ListsRetiredNamesOpensNoStore(t *testing.T) {
	opts := offlineOpts(t)
	opts.StateDir = filepath.Join(t.TempDir(), "state")
	opts.RetiredConfigNames = []config.RetiredName{
		{Name: "GOTHAM_DB_PATH", Canonical: "core.storage.path"},
		{Name: "GOTHAM_JOURNAL_DAYS", Canonical: "core.log.journal.retention_days"},
		{Name: "GOTHAM_BAT_SIGNAL", Message: "the signal moved to the roof"},
		{Name: "GOTHAM_NOT_SET", Canonical: "core.log.journal.min_level"},
		{Name: "GOTHAM_ORACLE_SECRET", Canonical: "modules.oracle.auth.secret"},
	}
	opts.Env = []string{"GOTHAM_DB_PATH=/srv/gotham/main.pebble", "GOTHAM_JOURNAL_DAYS=30", "GOTHAM_BAT_SIGNAL=on", "GOTHAM_ORACLE_SECRET=barbara"}
	rec := &recordingFS{FS: vfs.NewMem()}
	opts.StorageFS = storage.FSOf(rec)
	check := secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check")

	var out bytes.Buffer
	err := check.Run(t.Context(), nil, &out)
	if err == nil || !strings.Contains(err.Error(), "1 retired config names would stop the service") {
		t.Errorf("config check err = %v, want a non-zero exit naming one refusing name", err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 5 {
		t.Fatalf("output has %d lines:\n%s", len(lines), out.String())
	}
	for i, want := range [][]string{
		{"env GOTHAM_DB_PATH", "STOPS THE SERVICE FROM STARTING", "GOTHAM_CORE_STORAGE_PATH", "core.storage.path"},
		{"env GOTHAM_ORACLE_SECRET", "NOT CHECKED", "GOTHAM_ORACLE_AUTH_SECRET", "modules.oracle.auth.secret"},
		{"env GOTHAM_JOURNAL_DAYS", "ignored", "GOTHAM_CORE_LOG_JOURNAL_RETENTION_DAYS", "core.log.journal.retention_days"},
		{"env GOTHAM_BAT_SIGNAL", "ignored", "removed", "the signal moved to the roof"},
		{"4 retired config names set, 1 of them would stop the service from starting, 1 not checked"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i], w) {
				t.Errorf("line %d %q lacks %q", i, lines[i], w)
			}
		}
	}
	if strings.Contains(lines[1], "ignored") {
		t.Errorf("a module key config check cannot judge is reported ignored: %q", lines[1])
	}
	if strings.Contains(out.String(), "GOTHAM_NOT_SET") || strings.Contains(out.String(), "/srv/gotham") || strings.Contains(out.String(), "barbara") {
		t.Errorf("the output lists an unset name or prints a value:\n%s", out.String())
	}
	if _, err := os.Stat(opts.StateDir); !os.IsNotExist(err) {
		t.Errorf("config check created the state directory: %v", err)
	}
	if calls := rec.seen(); len(calls) != 0 {
		t.Errorf("config check touched the store's filesystem: %v", calls)
	}

	run := func(args ...string) error {
		out.Reset()
		return secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check").Run(t.Context(), args, &out)
	}
	opts.Env = []string{"GOTHAM_ORACLE_SECRET=barbara"}
	if err := run(); err == nil || !strings.Contains(err.Error(), "not checked") {
		t.Errorf("an unchecked name only: err = %v, want a non-zero exit", err)
	}

	opts.Env = []string{"GOTHAM_JOURNAL_DAYS=30"}
	if err := run(); err != nil {
		t.Errorf("warn-only config check = %v", err)
	}
	if !strings.Contains(out.String(), "1 retired config names set, 0 of them") {
		t.Errorf("warn-only output:\n%s", out.String())
	}

	opts.Env = []string{"GOTHAM_CORE_LOG_JOURNAL_RETENTION_DAYS=thirty"}
	if err := run(); err == nil {
		t.Error("config check accepted a value that does not decode")
	}

	// The service's flags, by config path, decode as a start decodes them.
	opts.Env = nil
	if err := run("--set", "core.log.journal.retention_days=thirty"); err == nil || !strings.Contains(err.Error(), "retention_days") {
		t.Errorf("a flag that does not decode: err = %v", err)
	}
	if err := run("--set", "core.log.journal.retention_days=30"); err != nil {
		t.Errorf("a flag that decodes: err = %v", err)
	}
	if err := run("--set", "core.batcave.nope=1"); err == nil {
		t.Error("a flag naming no key was accepted")
	}
	for _, bad := range [][]string{{"--set", "retention_days"}, {"--set", "core.log.journal.retention_days=1", "--set", "core.log.journal.retention_days=2"}} {
		if err := run(bad...); err == nil {
			t.Errorf("config check %v was accepted", bad)
		}
	}

	opts.Env = nil
	opts.RetiredConfigNames = nil
	out.Reset()
	if err := secretsCmd(t, OfflineCommands(opts, offlineKeeper()), "config check").Run(t.Context(), nil, &out); err != nil ||
		!strings.Contains(out.String(), "No retired config names are set") {
		t.Errorf("clean config check = %v, output %q", err, out.String())
	}
	if calls := rec.seen(); len(calls) != 0 {
		t.Errorf("config check touched the store's filesystem: %v", calls)
	}

	// The control: a boot over the same filesystem is recorded.
	served(t, opts, offlineKeeper())
	if len(rec.seen()) == 0 {
		t.Error("the recording filesystem records nothing, so its silence above proves nothing")
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
