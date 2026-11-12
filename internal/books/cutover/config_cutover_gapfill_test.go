// file: internal/books/cutover/config_cutover_gapfill_test.go
// version: 1.1.0
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

// managedFileExact is managedFile with every key in the case HEAD's gap-fill
// reads it (it matches key names exactly; the strip matches case-insensitively).
func managedFileExact() string {
	return strings.NewReplacer("Database_Path:", "database_path:", "ORGANIZATION_STRATEGY:", "organization_strategy:", "Google_Books_API_Key:", "google_books_api_key:").Replace(managedFile())
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

func (f *faultFiles) Resolve(p string) (string, error) {
	if err := f.step("resolve"); err != nil {
		return "", err
	}
	return osFiles{}.Resolve(p)
}

func (f *faultFiles) Stat(p string) (FileMeta, bool, error) {
	if err := f.step("stat"); err != nil {
		return FileMeta{}, false, err
	}
	return osFiles{}.Stat(p)
}

func (f *faultFiles) WriteTempFile(dir, pattern string, data []byte, meta FileMeta) (string, error) {
	if err := f.step("write"); err != nil {
		return "", err
	}
	return osFiles{}.WriteTempFile(dir, pattern, data, meta)
}

func (f *faultFiles) WriteInPlace(p string, data []byte) error {
	if err := f.step("write-in-place"); err != nil {
		return err
	}
	return osFiles{}.WriteInPlace(p, data)
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

func (f *faultFiles) ReadDir(d string) ([]string, error) {
	if err := f.step("readdir"); err != nil {
		return nil, err
	}
	return osFiles{}.ReadDir(d)
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

// holdAll and holdNone are holders that say every destination holds a value,
// or none does.
func holdAll(context.Context, string) (bool, error)  { return true, nil }
func holdNone(context.Context, string) (bool, error) { return false, nil }

// holdOnly holds the named keys.
func holdOnly(keys ...string) GapFillHolder {
	return func(_ context.Context, k string) (bool, error) { return slices.Contains(keys, k), nil }
}

func TestStripGapFill(t *testing.T) {
	ctx := context.Background()
	write := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	t.Run("absent", func(t *testing.T) {
		rep, err := StripGapFill(ctx, nil, filepath.Join(t.TempDir(), "config.yaml"), holdAll)
		if err != nil || rep.Result != StripAbsent {
			t.Errorf("= %v, %v; want absent", rep.Result, err)
		}
	})

	t.Run("a file with none of the keys is not rewritten", func(t *testing.T) {
		p := write(t, "operator_note: keep-me\n# a comment\n")
		before, _ := os.ReadFile(p)
		rep, err := StripGapFill(ctx, nil, p, holdAll)
		after, _ := os.ReadFile(p)
		if err != nil || rep.Result != StripClean || !bytes.Equal(before, after) {
			t.Errorf("= %v, %v, file changed %v; want clean and untouched", rep.Result, err, !bytes.Equal(before, after))
		}
	})

	t.Run("the fifteen managed keys go, case-insensitively, and nothing else", func(t *testing.T) {
		p := write(t, managedFile())
		rep, err := StripGapFill(ctx, nil, p, holdAll)
		if err != nil || rep.Result != StripStripped || len(rep.Kept) != 0 {
			t.Fatalf("= %+v, %v; want stripped, nothing kept", rep, err)
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
		if err != nil || st.Mode().Perm() != 0o644 {
			t.Errorf("mode %v (err %v), want the file's own 0644 (HEAD's writeFileAtomic keeps the mode)", st.Mode().Perm(), err)
		}
		if names := dirNames(t, filepath.Dir(p)); !slices.Equal(names, []string{"config.yaml"}) {
			t.Errorf("the directory holds %v, want only config.yaml", names)
		}
		// A second strip finds nothing.
		if rep, err := StripGapFill(ctx, nil, p, holdAll); err != nil || rep.Result != StripClean {
			t.Errorf("second strip = %v, %v; want clean", rep.Result, err)
		}
	})

	t.Run("a file of nothing else is removed", func(t *testing.T) {
		p := write(t, "root_dir: "+fxFileRoot+"\nOPENAI_API_KEY: "+fxFileOpenAI+"\nlog_level: info\n")
		rep, err := StripGapFill(ctx, nil, p, holdAll)
		if err != nil || rep.Result != StripDeleted {
			t.Fatalf("= %v, %v; want deleted", rep.Result, err)
		}
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("the file is still there (stat err %v)", err)
		}
		if rep, err := StripGapFill(ctx, nil, p, holdAll); err != nil || rep.Result != StripAbsent {
			t.Errorf("second strip = %v, %v; want absent", rep.Result, err)
		}
	})

	t.Run("a file that is not YAML is left alone", func(t *testing.T) {
		p := write(t, "root_dir: [unclosed\n\topenai_api_key: "+fxFileOpenAI+"\n")
		before, _ := os.ReadFile(p)
		rep, err := StripGapFill(ctx, nil, p, holdAll)
		after, _ := os.ReadFile(p)
		if err != nil || rep.Result != StripUnparsable || !bytes.Equal(before, after) {
			t.Errorf("= %v, %v, changed %v; want unparsable and untouched", rep.Result, err, !bytes.Equal(before, after))
		}
	})

	t.Run("a failure at any file operation leaves the file whole or stripped, and a rerun finishes", func(t *testing.T) {
		probe := &faultFiles{}
		p := write(t, managedFile())
		if _, err := StripGapFill(ctx, probe, p, holdAll); err != nil {
			t.Fatal(err)
		}
		ops := len(probe.ops)
		if ops < 6 {
			t.Fatalf("a strip made %d file operations %v, expected read, resolve, readdir, stat, write, rename, sync", ops, probe.ops)
		}
		for failAt := 1; failAt <= ops; failAt++ {
			p := write(t, managedFile())
			ff := &faultFiles{failAt: failAt}
			_, err := StripGapFill(ctx, ff, p, holdAll)
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
			if rep, err := StripGapFill(ctx, nil, p, holdAll); err != nil || (rep.Result != StripStripped && rep.Result != StripClean) {
				t.Errorf("fail at op %d: rerun = %v, %v", failAt, rep.Result, err)
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
		ff := &faultFiles{failAt: 4} // read, resolve, readdir, remove
		if _, err := StripGapFill(ctx, ff, p, holdAll); err == nil {
			t.Fatal("no error from a failing remove")
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("the file went although the remove failed: %v", err)
		}
		if rep, err := StripGapFill(ctx, nil, p, holdAll); err != nil || rep.Result != StripDeleted {
			t.Errorf("rerun = %v, %v; want deleted", rep.Result, err)
		}
	})

	t.Run("a temp file an interrupted strip left is removed by the next run, clean file or not", func(t *testing.T) {
		for _, content := range []string{managedFile(), "operator_note: keep-me\n"} {
			p := write(t, content)
			stray := filepath.Join(filepath.Dir(p), ".config.yaml.strip-123456")
			if err := os.WriteFile(stray, []byte("operator_note: keep-me\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(filepath.Dir(p), ".other.yaml.strip-1")
			if err := os.WriteFile(other, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			rep, err := StripGapFill(ctx, nil, p, holdAll)
			if err != nil || rep.TempsRemoved != 1 {
				t.Fatalf("= %+v, %v; want one stray temp removed", rep, err)
			}
			if names := dirNames(t, filepath.Dir(p)); !slices.Equal(names, []string{".other.yaml.strip-1", "config.yaml"}) {
				t.Errorf("the directory holds %v: the stray temp stays, or another file's temp went", names)
			}
		}
	})
}

// TestStripGapFill_VerifiesTheDestinationBeforeDeleting: the strip is
// one-way, so a gap-fill value whose destination holds nothing is the only
// copy and stays. The three secrets, root_dir and enable_ai_parsing are the
// only keys HEAD reads back from the file; each is checked alone.
func TestStripGapFill_VerifiesTheDestinationBeforeDeleting(t *testing.T) {
	ctx := context.Background()
	file := func(t *testing.T) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(managedFileExact()), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	for _, key := range headGapFillKeys {
		t.Run(key+" with no destination value stays", func(t *testing.T) {
			p := file(t)
			var held []string
			for _, k := range headGapFillKeys {
				if k != key {
					held = append(held, k)
				}
			}
			rep, err := StripGapFill(ctx, nil, p, holdOnly(held...))
			if err != nil || rep.Result != StripStripped || !slices.Equal(rep.Kept, []string{key}) {
				t.Fatalf("= %+v, %v; want stripped with %s kept", rep, err, key)
			}
			got := yamlKeys(t, p)
			for _, k := range headManagedFileKeys {
				_, in := got[k]
				if in != (k == key) {
					t.Errorf("%s in the file after the strip = %v, want %v", k, in, k == key)
				}
			}
			// Once the destination holds it, the next run strips it.
			if rep, err := StripGapFill(ctx, nil, p, holdAll); err != nil || rep.Result != StripDeleted && rep.Result != StripStripped || len(rep.Kept) != 0 {
				t.Errorf("rerun with the destination held = %+v, %v", rep, err)
			}
			if got := yamlKeys(t, p); len(got) != 2 {
				t.Errorf("the file holds %v after the rerun", slices.Sorted(keysOf(got)))
			}
		})
	}

	t.Run("no holder keeps all five and strips the ten HEAD never reads back", func(t *testing.T) {
		p := file(t)
		rep, err := StripGapFill(ctx, nil, p, nil)
		if err != nil || rep.Result != StripStripped || len(rep.Kept) != 5 {
			t.Fatalf("= %+v, %v", rep, err)
		}
		got := yamlKeys(t, p)
		for _, k := range headGapFillKeys {
			if _, in := got[k]; !in {
				t.Errorf("%s was stripped with nothing to show it is held", k)
			}
		}
		for _, k := range []string{"database_path", "playlist_dir", "setup_complete", "organization_strategy", "log_level"} {
			if _, in := got[k]; in {
				t.Errorf("%s stayed: HEAD never reads it back from the file", k)
			}
		}
	})

	t.Run("a key with no usable value needs no destination", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte("openai_api_key: \"\"\nenable_ai_parsing: false\nOpenAI_API_KEY: wrong-case-HEAD-never-read\noperator_note: keep-me\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rep, err := StripGapFill(ctx, nil, p, holdNone)
		if err != nil || rep.Result != StripStripped || len(rep.Kept) != 0 {
			t.Fatalf("= %+v, %v; want stripped, nothing kept", rep, err)
		}
		if got := yamlKeys(t, p); len(got) != 1 {
			t.Errorf("left %v, want only operator_note", slices.Sorted(keysOf(got)))
		}
	})

	t.Run("a holder that fails fails the strip and the file is as it was", func(t *testing.T) {
		p := file(t)
		before, _ := os.ReadFile(p)
		_, err := StripGapFill(ctx, nil, p, func(context.Context, string) (bool, error) { return false, errors.New("the store is down") })
		after, _ := os.ReadFile(p)
		if err == nil || !bytes.Equal(before, after) {
			t.Errorf("err = %v, file changed %v; want an error and an untouched file", err, !bytes.Equal(before, after))
		}
	})
}

// TestStripGapFill_WritesLikeHEAD: HEAD's writeFileAtomic follows a symlink,
// keeps the file's mode and owner, and falls back to an in-place write when the
// directory cannot be written. A strip that renamed over a symlink would sever
// it and leave the plaintext secret in the file it pointed at.
func TestStripGapFill_WritesLikeHEAD(t *testing.T) {
	ctx := context.Background()

	t.Run("a symlink survives and its target is stripped", func(t *testing.T) {
		dir := t.TempDir()
		managed := filepath.Join(dir, "managed")
		if err := os.MkdirAll(managed, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(managed, "target.yaml")
		if err := os.WriteFile(target, []byte("keep: 1\nopenai_api_key: "+fxFileOpenAI+"\n"), 0o640); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "config.yaml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		rep, err := StripGapFill(ctx, nil, link, holdAll)
		if err != nil || rep.Result != StripStripped {
			t.Fatalf("= %+v, %v", rep, err)
		}
		li, err := os.Lstat(link)
		if err != nil || li.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("the symlink was replaced by a regular file (err %v)", err)
		}
		raw, err := os.ReadFile(target)
		if err != nil || bytes.Contains(raw, []byte(fxFileOpenAI)) {
			t.Errorf("the target still holds the secret (err %v)", err)
		}
		if got := yamlKeys(t, target); len(got) != 1 || got["keep"] != 1 {
			t.Errorf("the target holds %v, want only keep", got)
		}
		if st, _ := os.Stat(target); st.Mode().Perm() != 0o640 {
			t.Errorf("the target's mode is %v, want its own 0640", st.Mode().Perm())
		}
		if names := dirNames(t, managed); !slices.Equal(names, []string{"target.yaml"}) {
			t.Errorf("the target's directory holds %v", names)
		}
	})

	t.Run("a symlink that would end up empty is kept, not removed", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.yaml")
		if err := os.WriteFile(target, []byte("openai_api_key: "+fxFileOpenAI+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, "config.yaml")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		rep, err := StripGapFill(ctx, nil, link, holdAll)
		if err != nil || rep.Result != StripStripped {
			t.Fatalf("= %+v, %v", rep, err)
		}
		if _, err := os.Stat(link); err != nil {
			t.Errorf("the link dangles after the strip: %v", err)
		}
		if got := yamlKeys(t, link); len(got) != 0 {
			t.Errorf("the file holds %v, want nothing", got)
		}
	})

	t.Run("a dangling symlink is an absent file", func(t *testing.T) {
		dir := t.TempDir()
		link := filepath.Join(dir, "config.yaml")
		if err := os.Symlink(filepath.Join(dir, "gone.yaml"), link); err != nil {
			t.Fatal(err)
		}
		if rep, err := StripGapFill(ctx, nil, link, holdAll); err != nil || rep.Result != StripAbsent {
			t.Errorf("= %v, %v; want absent", rep.Result, err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Errorf("the dangling link was touched: %v", err)
		}
	})

	t.Run("the file keeps its mode", func(t *testing.T) {
		for _, mode := range []fs.FileMode{0o600, 0o640, 0o664} {
			p := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(p, []byte(managedFile()), mode); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(p, mode); err != nil {
				t.Fatal(err)
			}
			if _, err := StripGapFill(ctx, nil, p, holdAll); err != nil {
				t.Fatal(err)
			}
			if st, _ := os.Stat(p); st.Mode().Perm() != mode {
				t.Errorf("mode %v after the strip, want %v", st.Mode().Perm(), mode)
			}
		}
	})

	t.Run("the temp file takes the original's owner where the process may", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(managedFile()), 0o600); err != nil {
			t.Fatal(err)
		}
		meta, ok, err := osFiles{}.Stat(p)
		if err != nil || !ok {
			t.Fatalf("stat: %v %v", ok, err)
		}
		if runtimeHasOwner() && !meta.HasOwner {
			t.Error("Stat reported no owner on a platform that has one")
		}
		if _, err := StripGapFill(ctx, nil, p, holdAll); err != nil {
			t.Fatal(err)
		}
		after, _, _ := osFiles{}.Stat(p)
		if meta.HasOwner && (after.UID != meta.UID || after.GID != meta.GID) {
			t.Errorf("owner %d:%d after the strip, want %d:%d", after.UID, after.GID, meta.UID, meta.GID)
		}
	})

	t.Run("the temp write is handed the original's mode and owner", func(t *testing.T) {
		// A chown to one's own owner changes nothing, so the file alone cannot
		// show whether the owner was passed on: the spy sees what the write got.
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(managedFile()), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o640); err != nil {
			t.Fatal(err)
		}
		want, ok, err := osFiles{}.Stat(p)
		if err != nil || !ok {
			t.Fatalf("stat: %v %v", ok, err)
		}
		spy := &metaSpy{}
		if _, err := StripGapFill(ctx, spy, p, holdAll); err != nil {
			t.Fatal(err)
		}
		if !spy.called {
			t.Fatal("the temp file was never written")
		}
		if spy.meta != want {
			t.Errorf("WriteTempFile got %+v, want the original's %+v", spy.meta, want)
		}
		if runtimeHasOwner() && !spy.meta.HasOwner {
			t.Error("the owner was dropped on a platform that reports one")
		}
	})

	t.Run("a directory the process cannot write gets an in-place write, and says so", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root can write a read-only directory")
		}
		dir := t.TempDir()
		p := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(p, []byte(managedFile()), 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
		rep, err := StripGapFill(ctx, nil, p, holdAll)
		if err != nil || rep.Result != StripStripped || !rep.InPlace {
			t.Fatalf("= %+v, %v; want stripped in place", rep, err)
		}
		if got := yamlKeys(t, p); len(got) != 2 {
			t.Errorf("the file holds %v", slices.Sorted(keysOf(got)))
		}
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o640 {
			t.Errorf("mode %v, want 0640", st.Mode().Perm())
		}
	})

	t.Run("a permission failure to create the temp file falls back; one to write in place is an error", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p, []byte(managedFile()), 0o600); err != nil {
			t.Fatal(err)
		}
		denied := &denyTemp{}
		rep, err := StripGapFill(ctx, denied, p, holdAll)
		if err != nil || !rep.InPlace || !denied.inPlace {
			t.Fatalf("= %+v, %v, in place %v", rep, err, denied.inPlace)
		}
		if got := yamlKeys(t, p); len(got) != 2 {
			t.Errorf("the file holds %v", slices.Sorted(keysOf(got)))
		}

		p2 := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(p2, []byte(managedFile()), 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(p2)
		denied2 := &denyTemp{failInPlace: true}
		_, err = StripGapFill(ctx, denied2, p2, holdAll)
		after, _ := os.ReadFile(p2)
		if err == nil || !bytes.Equal(before, after) {
			t.Errorf("err = %v, file changed %v; want an error and an untouched file", err, !bytes.Equal(before, after))
		}
	})

	t.Run("a file with more than one YAML document is refused whole", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "config.yaml")
		content := "a: 1\nopenai_api_key: " + fxFileOpenAI + "\n---\nb: 2\n"
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := StripGapFill(ctx, nil, p, holdAll)
		if !errors.Is(err, errConfigStep) || !strings.Contains(err.Error(), "more than one YAML document") {
			t.Fatalf("err = %v, want the multi-document refusal", err)
		}
		if strings.Contains(err.Error(), fxFileOpenAI) {
			t.Error("the refusal carries a value")
		}
		after, _ := os.ReadFile(p)
		if string(after) != content {
			t.Errorf("the file was rewritten: later documents would be dropped")
		}
	})
}

// denyTemp is osFiles whose WriteTempFile is denied by permission, as in a
// directory the process cannot write.
type denyTemp struct {
	failInPlace bool
	inPlace     bool
}

func (d *denyTemp) ReadFile(p string) ([]byte, error)     { return osFiles{}.ReadFile(p) }
func (d *denyTemp) Resolve(p string) (string, error)      { return osFiles{}.Resolve(p) }
func (d *denyTemp) Stat(p string) (FileMeta, bool, error) { return osFiles{}.Stat(p) }
func (d *denyTemp) Rename(a, b string) error              { return osFiles{}.Rename(a, b) }
func (d *denyTemp) Remove(p string) error                 { return osFiles{}.Remove(p) }
func (d *denyTemp) SyncDir(p string) error                { return osFiles{}.SyncDir(p) }
func (d *denyTemp) ReadDir(p string) ([]string, error)    { return osFiles{}.ReadDir(p) }
func (d *denyTemp) WriteTempFile(string, string, []byte, FileMeta) (string, error) {
	return "", &fs.PathError{Op: "open", Path: "dir", Err: fs.ErrPermission}
}
func (d *denyTemp) WriteInPlace(p string, data []byte) error {
	if d.failInPlace {
		return &fs.PathError{Op: "open", Path: "file", Err: fs.ErrPermission}
	}
	d.inPlace = true
	return osFiles{}.WriteInPlace(p, data)
}

// metaSpy is osFiles that records the metadata its temp write was handed.
type metaSpy struct {
	osFiles
	called bool
	meta   FileMeta
}

func (m *metaSpy) WriteTempFile(dir, pattern string, data []byte, meta FileMeta) (string, error) {
	m.called, m.meta = true, meta
	return m.osFiles.WriteTempFile(dir, pattern, data, meta)
}

// runtimeHasOwner reports whether this platform reports a file's owner.
func runtimeHasOwner() bool {
	st, err := os.Stat(os.TempDir())
	if err != nil {
		return false
	}
	_, _, ok := fileOwner(st)
	return ok
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
