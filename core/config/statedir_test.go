// file: core/config/statedir_test.go
// version: 1.0.2
// guid: 3c1e5b4a-8f0d-4d6e-9a2b-7e5c1f0a9d42
// last-edited: 2026-11-10

package config_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/config"
)

// headEnsureSecureStateDir is HEAD's EnsureSecureStateDir
// (internal/config/state_dir.go at 954f7a35a, lines 118-155) with its two
// inputs made parameters: env is the raw value of the app's state-directory variable, def the
// DefaultSecureStateDir constant, dbPath AppConfig.DatabasePath. warned
// reports the fallback's slog.Warn. It is the oracle, copied, not called:
// ABO's file now delegates to config.ResolveStateDir.
func headEnsureSecureStateDir(env, def, dbPath string, mkdir func(string) error) (dir string, warned bool, err error) {
	dir = def
	if v := strings.TrimSpace(env); v != "" {
		dir = filepath.Clean(v)
	}
	err = mkdir(dir)
	if err == nil {
		return dir, false, nil
	}
	if strings.TrimSpace(env) != "" {
		return dir, false, err
	}
	fallback := filepath.Dir(strings.TrimSpace(dbPath))
	if fallback == "" || fallback == "." || !filepath.IsAbs(fallback) {
		return dir, false, err
	}
	if ferr := mkdir(fallback); ferr != nil {
		return dir, false, err
	}
	return fallback, true, nil
}

// fakeFS is a mkdir over a set of creatable directories; each refusal is a
// distinct error naming its path, so a test can tell whose error came back.
type fakeFS struct {
	ok    map[string]bool
	calls []string
}

func (f *fakeFS) mkdir(d string) error {
	f.calls = append(f.calls, d)
	if f.ok[d] {
		return nil
	}
	return fmt.Errorf("mkdir %s: permission denied", d)
}

// TestStateDirFallbackMatchesHEAD (05-config §1.11): explicit set or not ×
// default creatable or not × DB path absolute (creatable or not) / relative /
// blank / padded: ResolveStateDir returns HEAD's directory, HEAD's fallback
// decision and HEAD's error, row by row.
func TestStateDirFallbackMatchesHEAD(t *testing.T) {
	const def, dbDir = "/var/lib/gotham", "/srv/gotham/db"
	explicits := []string{"", "   ", "/opt/gotham", "  /opt/gotham/../gotham/ ", "/opt/arkham"}
	defOK := []bool{true, false}
	dbPaths := []string{dbDir + "/main.pebble", " " + dbDir + "/main.pebble ", "/srv/arkham/main.pebble", "db/main.pebble", "main.pebble", "", "/main.pebble"}
	rows := 0
	for _, ex := range explicits {
		for _, dok := range defOK {
			for _, db := range dbPaths {
				rows++
				ok := map[string]bool{"/opt/gotham": true, dbDir: true, "/": true}
				ok[def] = dok
				head, mine := &fakeFS{ok: ok}, &fakeFS{ok: ok}
				hd, hw, herr := headEnsureSecureStateDir(ex, def, db, head.mkdir)
				md, mf, merr := config.ResolveStateDir(ex, def, db, mine.mkdir)
				name := fmt.Sprintf("explicit=%q defaultCreatable=%v db=%q", ex, dok, db)
				if md != hd || mf != hw || fmt.Sprint(merr) != fmt.Sprint(herr) {
					t.Errorf("%s:\n  HEAD   (%q, fellBack %v, %v)\n  config (%q, fellBack %v, %v)", name, hd, hw, herr, md, mf, merr)
				}
				if !slices.Equal(mine.calls, head.calls) {
					t.Errorf("%s: mkdir calls %q, HEAD %q", name, mine.calls, head.calls)
				}
				if strings.TrimSpace(ex) != "" && mf {
					t.Errorf("%s: an explicit directory fell back", name)
				}
			}
		}
	}
	if rows != 70 {
		t.Fatalf("%d rows", rows)
	}
	// No default declared and nothing explicit: an error, never "".
	if d, fb, err := config.ResolveStateDir("", "", "relative/db", (&fakeFS{}).mkdir); err == nil || fb || d != "" {
		t.Errorf("no default: (%q, %v, %v)", d, fb, err)
	}
}

// TestStateDirFallbackMatchesHEAD_AppResolver: core/app.ResolveStateDir
// delegates to config.ResolveStateDir, so on a real filesystem the two agree
// on every row: the same directory (on an error too, the directory that could
// not be used), the same fallback, and an error from both or neither. Only the
// error text differs: app wraps it with the env name that chooses a
// directory. This guards the delegation; a resolver of app's own fails here.
func TestStateDirFallbackMatchesHEAD_AppResolver(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	creatable := filepath.Join(root, "state")
	uncreatable := filepath.Join(blocker, "state") // under a file: ENOTDIR
	dbOK := filepath.Join(root, "db", "main.pebble")
	dbBad := filepath.Join(blocker, "db", "main.pebble")
	type row struct {
		name, explicit, def, db string
	}
	var rows []row
	for _, ex := range []struct{ n, v string }{{"unset", ""}, {"blank", "   "}, {"set", filepath.Join(root, "explicit")}, {"padded", "  " + filepath.Join(root, "explicit", "..", "explicit") + "/ "}, {"uncreatable", filepath.Join(blocker, "explicit")}} {
		for _, d := range []struct{ n, v string }{{"creatable", creatable}, {"uncreatable", uncreatable}, {"none", ""}} {
			for _, db := range []struct{ n, v string }{{"abs", dbOK}, {"uncreatableAbs", dbBad}, {"relative", "db/main.pebble"}, {"blank", ""}} {
				rows = append(rows, row{fmt.Sprintf("explicit=%s default=%s db=%s", ex.n, d.n, db.n), ex.v, d.v, db.v})
			}
		}
	}
	if len(rows) != 60 {
		t.Fatalf("%d rows", len(rows))
	}
	mkdir := func(d string) error { return os.MkdirAll(d, 0o700) }
	for _, r := range rows {
		env := []string{}
		if r.explicit != "" {
			env = append(env, "GOTHAM_CORE_PATHS_STATE_DIR="+r.explicit)
		}
		if r.db != "" {
			env = append(env, "GOTHAM_CORE_STORAGE_PATH="+r.db)
		}
		capture := &warnCount{}
		ad, aerr := app.ResolveStateDir(app.Options{AppID: "gotham", EnvPrefix: "GOTHAM", StateDir: r.def, Env: env, Logger: slog.New(capture)})
		cd, cfb, cerr := config.ResolveStateDir(r.explicit, r.def, r.db, mkdir)
		if ad != cd || (aerr != nil) != (cerr != nil) || capture.n != boolInt(cfb) {
			t.Errorf("%s: core/app (%q, %d warnings, %v) disagrees with config (%q, fellBack %v, %v)", r.name, ad, capture.n, aerr, cd, cfb, cerr)
		}
		if aerr != nil && !strings.Contains(aerr.Error(), "GOTHAM_CORE_PATHS_STATE_DIR") {
			t.Errorf("%s: core/app's error does not name GOTHAM_CORE_PATHS_STATE_DIR: %v", r.name, aerr)
		}
	}
}

// warnCount is a slog.Handler that counts warnings: core/app's fallback
// warning is its only output, so the count is its fellBack.
type warnCount struct{ n int }

func (w *warnCount) Enabled(context.Context, slog.Level) bool { return true }
func (w *warnCount) Handle(_ context.Context, r slog.Record) error {
	if r.Level == slog.LevelWarn {
		w.n++
	}
	return nil
}
func (w *warnCount) WithAttrs([]slog.Attr) slog.Handler { return w }
func (w *warnCount) WithGroup(string) slog.Handler      { return w }

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestStateDir_OneResolverYAMLDBPath (the config half, 05-config §1.11,
// OPS4-8, OPS5-13): a database path given only in the operator file plus an
// uncreatable default resolves, through Store.Operator before Boot, to that
// path's directory, and Boot reads the boot record from exactly there. The
// restore intent, HEAD's key file and the keyring are owed by their rows
// (F3-03 and backup); see the seams file.
func TestStateDir_OneResolverYAMLDBPath(t *testing.T) {
	type storageFix struct {
		Path string `json:"path" reload:"restart" protect:"operator"`
	}
	root := t.TempDir()
	blocker := filepath.Join(root, "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dbDir := filepath.Join(root, "db")
	dbPath := filepath.Join(dbDir, "main.pebble")
	def := filepath.Join(blocker, "state")

	// Step 0: the kernel reads core.storage.path from every pre-boot layer,
	// the file included, before Boot.
	probe := newRig(t, rigOpts{file: map[config.Path]string{"core.storage.path": mustJSON(t, dbPath)}})
	if _, err := config.Register(probe.st.Registry(), config.Define("core.storage", storageFix{})); err != nil {
		t.Fatal(err)
	}
	v, ok, err := probe.st.Operator("core.storage.path")
	if err != nil || !ok || v.Src != config.SourceFile {
		t.Fatalf("Operator = %+v, %v, %v", v, ok, err)
	}
	var fromFile string
	if err := json.Unmarshal(v.Value, &fromFile); err != nil {
		t.Fatal(err)
	}
	dir, fellBack, err := config.ResolveStateDir("", def, fromFile, func(d string) error { return os.MkdirAll(d, 0o700) })
	if err != nil || !fellBack || dir != dbDir {
		t.Fatalf("ResolveStateDir = (%q, %v, %v), want %q by fallback", dir, fellBack, err, dbDir)
	}
	// The kernel registers core.paths with that directory as the default and
	// opens the record's root there.
	r := newRig(t, rigOpts{stateDir: dir, dir: dbDir})
	r.start()
	if !slices.Equal(r.roots, []string{dbDir}) {
		t.Errorf("BootRecordRoot called with %q, want [%s]", r.roots, dbDir)
	}
	if _, err := r.apply(admin(), map[config.Path]string{kMode: `"day"`}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dbDir, "config.boot.json")); err != nil {
		t.Errorf("the boot record is not beside the database: %v", err)
	}
}
