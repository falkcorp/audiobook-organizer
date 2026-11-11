// file: internal/books/cutover/config_cutover_gapfill_test.go
// version: 1.0.0
// guid: c47a9e03-5b18-4d62-a0f7-83e1d6b25c94
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"gopkg.in/yaml.v3"
)

// File-held values of the gap-fill fixture: Batman-themed, fake.
const (
	fxFileOpenAI = "Gordon-file-openai-token-0101"
	fxFileGoogle = "Gordon-file-google-token-0102"
	fxFileHardc  = "Gordon-file-hardcover-token-0103"
	fxFileRoot   = "/batcave/library-from-file"
)

// TestConfigCutover_GapFillStripped: M2a consumes what config.yaml gap-fills
// (the three secrets, root_dir, enable_ai_parsing) without touching the file;
// the strip M2b performs removes the fifteen managed keys, case-insensitively,
// keeps everything else, and survives a failure at every file operation.
func TestConfigCutover_GapFillStripped(t *testing.T) {
	t.Run("M2a consumes the gap-fill and leaves the file alone", func(t *testing.T) {
		r := newRig(t, "stage1", func(r *rig) {
			r.mutate = func(t testing.TB, lr storage.LegacyRange) {
				ctx := context.Background()
				for _, n := range []string{"basic_auth_password", "google_books_api_key", "hardcover_api_token", "openai_api_key"} {
					if err := lr.Delete(ctx, settingKey(n)); err != nil {
						t.Fatal(err)
					}
				}
				rewriteBlob(t, lr, func(tree map[string]any) {
					tree["root_dir"] = ""
					tree["enable_ai_parsing"] = false
				})
			}
			file := "openai_api_key: " + fxFileOpenAI + "\ngoogle_books_api_key: " + fxFileGoogle + "\nhardcover_api_token: " + fxFileHardc +
				"\nroot_dir: " + fxFileRoot + "\nenable_ai_parsing: true\nlog_level: debug\n"
			if err := os.WriteFile(r.confFile, []byte(file), 0o600); err != nil {
				t.Fatal(err)
			}
		})
		fileBefore, err := os.ReadFile(r.confFile)
		if err != nil {
			t.Fatal(err)
		}
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		for name, want := range map[string]string{
			"integrations.ai.openai.api_key":             fxFileOpenAI,
			"integrations.metadata.google-books.api_key": fxFileGoogle,
			"integrations.metadata.hardcover.api_token":  fxFileHardc,
		} {
			if got := snap.Secrets[name]; got != want {
				t.Errorf("secret %s did not take config.yaml's value (readable: %v)", name, got != "")
			}
		}
		if _, ok := snap.Secrets["core.http.basic_gate.password"]; ok {
			t.Error("a gate password appeared from nowhere")
		}
		if root, _ := blobTree(t, snap.Legacy)["root_dir"].(string); root != fxFileRoot {
			t.Errorf("root_dir in the blob is %q, want config.yaml's", root)
		}
		// The key is staged with the file's true, whether or not that equals
		// the key's default (the registry then stores nothing for a default).
		row, _ := Table().Row("enable_ai_parsing")
		staged := false
		for _, c := range snap.Calls {
			if c.Path == row.Targets[0].Path {
				staged = strings.TrimSpace(c.Value) == "true"
			}
		}
		if !staged {
			t.Errorf("enable_ai_parsing: %s was not staged as true from config.yaml", row.Targets[0].Path)
		}
		fileAfter, err := os.ReadFile(r.confFile)
		if err != nil || !bytes.Equal(fileBefore, fileAfter) {
			t.Errorf("M2a rewrote config.yaml (err %v): stripping it is M2b's, after the migration committed", err)
		}
	})

	t.Run("the migration's done record is what releases the strip", func(t *testing.T) {
		r := newRig(t, "stage1", seedKnown)
		var before, after bool
		r.preMigrate = func(ctx context.Context, e *bootEnv) error {
			sks, _ := e.db.Keyspace("core/storage")
			var err error
			before, err = ConfigCutoverDone(ctx, sks)
			return err
		}
		err := r.boot(context.Background(), r.newMem(t), []datamigrate.DataMigration{r.migration()}, func(ctx context.Context, e *bootEnv) error {
			sks, _ := e.db.Keyspace("core/storage")
			var err error
			after, err = ConfigCutoverDone(ctx, sks)
			return err
		})
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if before || !after {
			t.Errorf("ConfigCutoverDone before = %v, after = %v; want false, true", before, after)
		}
	})
}

// managedFile is a config.yaml holding all fifteen managed keys, some in the
// wrong case, and keys the strip must keep.
func managedFile() string {
	return `root_dir: ` + fxFileRoot + `
Database_Path: /batcave/db
playlist_dir: /batcave/playlists
setup_complete: true
ORGANIZATION_STRATEGY: copy
scan_on_startup: false
auto_organize: true
folder_naming_pattern: '{author}/{title}'
file_naming_pattern: '{title}'
enable_ai_parsing: true
concurrent_scans: 4
log_level: debug
openai_api_key: ` + fxFileOpenAI + `
Google_Books_API_Key: ` + fxFileGoogle + `
hardcover_api_token: ` + fxFileHardc + `
operator_note: keep-me
tuning:
  gotham_threads: 7
`
}

// faultFiles is osFiles with a failure injected at the nth operation (1-based;
// 0 never fails).
type faultFiles struct {
	mu     sync.Mutex
	failAt int
	n      int
	ops    []string
}

var errInjected = errors.New("injected file failure: " + fxFileOpenAI) // text a leak would show

func (f *faultFiles) step(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	f.ops = append(f.ops, op)
	if f.failAt == f.n {
		return errInjected
	}
	return nil
}

func (f *faultFiles) ReadFile(p string) ([]byte, error) {
	if err := f.step("read"); err != nil {
		return nil, err
	}
	return osFiles{}.ReadFile(p)
}

func (f *faultFiles) WriteTempFile(dir, pattern string, data []byte, mode fs.FileMode) (string, error) {
	if err := f.step("write"); err != nil {
		return "", err
	}
	return osFiles{}.WriteTempFile(dir, pattern, data, mode)
}

func (f *faultFiles) Rename(a, b string) error {
	if err := f.step("rename"); err != nil {
		return err
	}
	return osFiles{}.Rename(a, b)
}

func (f *faultFiles) Remove(p string) error {
	if err := f.step("remove"); err != nil {
		return err
	}
	return osFiles{}.Remove(p)
}

func (f *faultFiles) SyncDir(d string) error {
	if err := f.step("sync"); err != nil {
		return err
	}
	return osFiles{}.SyncDir(d)
}

func yamlKeys(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(raw, &m); err != nil {
		t.Fatalf("config.yaml no longer parses: %v", err)
	}
	return m
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func TestStripGapFill(t *testing.T) {
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("absent", func(t *testing.T) {
		res, err := StripGapFill(nil, filepath.Join(t.TempDir(), "config.yaml"))
		if err != nil || res != StripAbsent {
			t.Errorf("= %v, %v; want absent", res, err)
		}
	})

	t.Run("a file with none of the keys is not rewritten", func(t *testing.T) {
		p := write(t, "operator_note: keep-me\n# a comment\n")
		before, _ := os.ReadFile(p)
		res, err := StripGapFill(nil, p)
		after, _ := os.ReadFile(p)
		if err != nil || res != StripClean || !bytes.Equal(before, after) {
			t.Errorf("= %v, %v, file changed %v; want clean and untouched", res, err, !bytes.Equal(before, after))
		}
	})

	t.Run("the fifteen managed keys go, case-insensitively, and nothing else", func(t *testing.T) {
		p := write(t, managedFile())
		res, err := StripGapFill(nil, p)
		if err != nil || res != StripStripped {
			t.Fatalf("= %v, %v; want stripped", res, err)
		}
		got := yamlKeys(t, p)
		if len(got) != 2 || got["operator_note"] != "keep-me" {
			t.Errorf("left %v, want only operator_note and tuning", slices.Sorted(keysOf(got)))
		}
		if tuning, _ := got["tuning"].(map[string]any); tuning["gotham_threads"] != 7 {
			t.Errorf("the nested key was not kept: %v", got["tuning"])
		}
		raw, _ := os.ReadFile(p)
		for _, v := range []string{fxFileOpenAI, fxFileGoogle, fxFileHardc} {
			if bytes.Contains(raw, []byte(v)) {
				t.Error("a managed secret is still in the file")
			}
		}
		st, err := os.Stat(p)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Errorf("mode %v (err %v), want 0600", st.Mode().Perm(), err)
		}
		if names := dirNames(t, filepath.Dir(p)); !slices.Equal(names, []string{"config.yaml"}) {
			t.Errorf("the directory holds %v, want only config.yaml", names)
		}
		// A second strip finds nothing.
		if res, err := StripGapFill(nil, p); err != nil || res != StripClean {
			t.Errorf("second strip = %v, %v; want clean", res, err)
		}
	})

	t.Run("a file of nothing else is removed", func(t *testing.T) {
		p := write(t, "root_dir: "+fxFileRoot+"\nOPENAI_API_KEY: "+fxFileOpenAI+"\nlog_level: info\n")
		res, err := StripGapFill(nil, p)
		if err != nil || res != StripDeleted {
			t.Fatalf("= %v, %v; want deleted", res, err)
		}
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the file is still there (stat err %v)", err)
		}
		if res, err := StripGapFill(nil, p); err != nil || res != StripAbsent {
			t.Errorf("second strip = %v, %v; want absent", res, err)
		}
	})

	t.Run("a file that is not YAML is left alone", func(t *testing.T) {
		p := write(t, "root_dir: [unclosed\n\topenai_api_key: "+fxFileOpenAI+"\n")
		before, _ := os.ReadFile(p)
		res, err := StripGapFill(nil, p)
		after, _ := os.ReadFile(p)
		if err != nil || res != StripUnparsable || !bytes.Equal(before, after) {
			t.Errorf("= %v, %v, changed %v; want unparsable and untouched", res, err, !bytes.Equal(before, after))
		}
	})

	t.Run("a failure at any file operation leaves the file whole or stripped, and a rerun finishes", func(t *testing.T) {
		probe := &faultFiles{}
		p := write(t, managedFile())
		if _, err := StripGapFill(probe, p); err != nil {
			t.Fatal(err)
		}
		ops := len(probe.ops)
		if ops < 4 {
			t.Fatalf("a strip made %d file operations %v, expected read, write, rename, sync", ops, probe.ops)
		}
		for failAt := 1; failAt <= ops; failAt++ {
			p := write(t, managedFile())
			ff := &faultFiles{failAt: failAt}
			_, err := StripGapFill(ff, p)
			if err == nil {
				t.Errorf("fail at op %d (%s): no error", failAt, probe.ops[failAt-1])
				continue
			}
			if strings.Contains(err.Error(), fxFileOpenAI) {
				t.Errorf("fail at op %d: the error carries the injected text, which could carry a value: %v", failAt, err)
			}
			m := yamlKeys(t, p)
			_, hasRoot := m["root_dir"]
			_, hasKey := m["openai_api_key"]
			if hasRoot != hasKey {
				t.Errorf("fail at op %d (%s): the file is half stripped", failAt, probe.ops[failAt-1])
			}
			if res, err := StripGapFill(nil, p); err != nil || (res != StripStripped && res != StripClean) {
				t.Errorf("fail at op %d: rerun = %v, %v", failAt, res, err)
			}
			final := yamlKeys(t, p)
			if len(final) != 2 {
				t.Errorf("fail at op %d: after the rerun the file holds %v", failAt, slices.Sorted(keysOf(final)))
			}
			if names := dirNames(t, filepath.Dir(p)); !slices.Equal(names, []string{"config.yaml"}) {
				t.Errorf("fail at op %d: the directory holds %v after the rerun", failAt, names)
			}
		}
	})

	t.Run("a failed delete leaves the file, a rerun removes it", func(t *testing.T) {
		p := write(t, "root_dir: "+fxFileRoot+"\n")
		ff := &faultFiles{failAt: 2} // read, then remove
		if _, err := StripGapFill(ff, p); err == nil {
			t.Fatal("no error from a failing remove")
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the file went although the remove failed: %v", err)
		}
		if res, err := StripGapFill(nil, p); err != nil || res != StripDeleted {
			t.Errorf("rerun = %v, %v; want deleted", res, err)
		}
	})
}

func keysOf(m map[string]any) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
