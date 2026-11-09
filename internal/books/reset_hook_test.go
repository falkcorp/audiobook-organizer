// file: internal/books/reset_hook_test.go
// version: 1.0.0
// guid: 8ff8611d-2192-4368-9d9a-fbe5c21995ba
// last-edited: 2026-11-09

package books

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/backup"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// osFiles is a storage.StateFiles over the OS for the hook's unit tests:
// kept paths are refused with ErrKeptPath (the kernel's confined
// filesystem does that for real) and broken ones fail.
type osFiles struct {
	mu     sync.Mutex
	kept   map[string]bool
	broken map[string]error
}

func (f *osFiles) ReadDir(p string) ([]fs.DirEntry, error) { return os.ReadDir(p) }

func (f *osFiles) RemoveAll(p string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.kept[p] {
		return fmt.Errorf("%w: %s", storage.ErrKeptPath, p)
	}
	if err := f.broken[p]; err != nil {
		return err
	}
	return os.RemoveAll(p)
}

// hookRun is a ResetRun over files whose Done records each call.
func hookRun(t *testing.T, args resetArgs, wipe bool, files storage.StateFiles) (storage.ResetRun, *[][]string) {
	t.Helper()
	raw, err := json.Marshal(args)
	require.NoError(t, err)
	var calls [][]string
	return storage.ResetRun{
		ID: "r-test", Args: raw, DeleteLibraryFiles: wipe, Files: files,
		ExtendStart: func(string) {},
		Done: func(_ context.Context, failed []string) error {
			calls = append(calls, slices.Clone(failed))
			return nil
		},
	}, &calls
}

// gotham is a library root with three top-level entries and an Open
// Library dump directory inside it.
func gotham(t *testing.T) (root, dump string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "library")
	dump = filepath.Join(root, "openlibrary-dumps")
	for _, d := range []string{filepath.Join(root, "Year One"), filepath.Join(root, "The Killing Joke"), dump} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "Hush.m4b"), []byte("hush"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dump, "authors.jsonl"), []byte("penguin"), 0o600))
	return root, dump
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// TestPostResetHook_RemoveErrorsStillDone (05-storage §5S.3.15 rule (1);
// OPS8-5): a per-entry failure is recorded and the hook goes on, a kept
// entry is skipped without a failure, the library root itself stays, Done
// is called exactly once with the failures, and Run returns nil. Hook
// arguments that do not parse are a failure recorded through Done too.
func TestPostResetHook_RemoveErrorsStillDone(t *testing.T) {
	root, dump := gotham(t)
	require.NoError(t, os.MkdirAll(filepath.Join(root, "batcave"), 0o700))
	files := &osFiles{
		kept:   map[string]bool{filepath.Join(root, "batcave"): true},
		broken: map[string]error{filepath.Join(root, "Hush.m4b"): errors.New("the joker glued it shut")},
	}
	rr, calls := hookRun(t, resetArgs{DumpDir: dump, RootDir: root}, true, files)
	require.NoError(t, (&booksModule{}).runReset(t.Context(), rr))

	require.Len(t, *calls, 1, "Done must be called exactly once")
	failed := (*calls)[0]
	require.Len(t, failed, 1, "failed = %v", failed)
	assert.Contains(t, failed[0], "Hush.m4b")
	assert.Contains(t, failed[0], "the joker glued it shut")
	for _, gone := range []string{dump, filepath.Join(root, "Year One"), filepath.Join(root, "The Killing Joke")} {
		assert.False(t, exists(gone), "%s survived the hook", gone)
	}
	for _, kept := range []string{root, filepath.Join(root, "batcave"), filepath.Join(root, "Hush.m4b")} {
		assert.True(t, exists(kept), "%s was removed", kept)
	}

	t.Run("hook arguments that do not parse", func(t *testing.T) {
		rr, calls := hookRun(t, resetArgs{}, false, &osFiles{})
		rr.Args = []byte("{the riddler")
		require.NoError(t, (&booksModule{}).runReset(t.Context(), rr))
		require.Len(t, *calls, 1)
		require.Len(t, (*calls)[0], 1)
		assert.Contains(t, (*calls)[0][0], "hook_args")
	})
	t.Run("the timeout records what it did not reach", func(t *testing.T) {
		root, dump := gotham(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		rr, calls := hookRun(t, resetArgs{DumpDir: dump, RootDir: root}, true, &osFiles{})
		require.NoError(t, (&booksModule{}).runReset(ctx, rr))
		require.Len(t, *calls, 1)
		assert.Len(t, (*calls)[0], 5, "the dump directory and the four library entries were not reached: %v", (*calls)[0])
		assert.True(t, exists(filepath.Join(root, "Hush.m4b")))
	})
	t.Run("without the wipe the library stays", func(t *testing.T) {
		root, dump := gotham(t)
		rr, calls := hookRun(t, resetArgs{DumpDir: dump, RootDir: root}, false, &osFiles{})
		require.NoError(t, (&booksModule{}).runReset(t.Context(), rr))
		require.Equal(t, [][]string{nil}, *calls)
		assert.False(t, exists(dump))
		assert.True(t, exists(filepath.Join(root, "Hush.m4b")))
	})
	t.Run("a dangerous root is only skipped", func(t *testing.T) {
		rr, calls := hookRun(t, resetArgs{RootDir: "/"}, true, &osFiles{broken: map[string]error{}})
		require.NoError(t, (&booksModule{}).runReset(t.Context(), rr))
		require.Len(t, *calls, 1)
		require.Len(t, (*calls)[0], 1)
		assert.Contains(t, (*calls)[0][0], "skipped")
	})
}

// TestFactoryResetReadOnlyRootDirCompletes (05-storage §5S.3.15 rule (1);
// OPS8-5): a library root whose entries cannot be removed (a read-only
// directory) records each entry as failed and still calls Done; the hook
// never fails.
func TestFactoryResetReadOnlyRootDirCompletes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes entries from a read-only directory")
	}
	root, _ := gotham(t)
	require.NoError(t, os.Chmod(root, 0o500))
	t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
	rr, calls := hookRun(t, resetArgs{RootDir: root}, true, &osFiles{})
	require.NoError(t, (&booksModule{}).runReset(t.Context(), rr))
	require.Len(t, *calls, 1)
	assert.Len(t, (*calls)[0], 4, "every top-level entry is a recorded failure: %v", (*calls)[0])
	assert.True(t, exists(filepath.Join(root, "Hush.m4b")))
}

// TestResetPlanRefusals (05-storage §5S.3.15 "Staging" (2)): without the
// store's settings a wipe is refused; a dump directory that is a protected
// directory or holds the library root is refused whatever the request; a
// plain reset records the dump directory and no library root.
func TestResetPlanRefusals(t *testing.T) {
	root, dump := gotham(t)
	factory := storage.ResetRequest{Kind: restorejournal.ResetFactory, Confirm: storage.ResetConfirm}
	wipe := factory
	wipe.DeleteLibraryFiles, wipe.LibraryRoot, wipe.LibraryFileCount = true, root, 3

	withConfig(t, config.Config{RootDir: root})
	m := &booksModule{}
	_, err := m.planReset(t.Context(), wipe)
	assert.ErrorIs(t, err, storage.ErrResetRefused, "a wipe without the stored settings")

	plan, err := m.planReset(t.Context(), factory)
	require.NoError(t, err)
	var args resetArgs
	require.NoError(t, json.Unmarshal(plan.Hook, &args))
	assert.Equal(t, resetArgs{DumpDir: dump}, args)
	assert.Equal(t, []string{dump}, plan.Paths)
	assert.Empty(t, plan.LibraryRoot)

	for name, d := range map[string]string{"protected": "/", "holds the root": filepath.Dir(root), "is the root": root} {
		withConfig(t, config.Config{RootDir: root, OpenLibraryDumpDir: d})
		_, err := m.planReset(t.Context(), factory)
		assert.ErrorIs(t, err, storage.ErrResetRefused, "dump directory %s (%s)", d, name)
	}

	plan, err = m.planReset(t.Context(), storage.ResetRequest{Kind: restorejournal.ResetStore, Confirm: storage.ResetConfirm})
	require.NoError(t, err)
	assert.Equal(t, storage.ResetPlan{}, plan, "a store reset has no plan")
}

// offlineABO is ABO on disk with its service stopped: a store booted once
// through books over app.New, a library root beside it with three
// top-level entries and an Open Library dump directory, and the offline
// commands as cmd builds them.
type offlineABO struct {
	t           *testing.T
	path, state string
	root, dump  string
	edit        func(*config.Config)
	disk        func(*app.Options)
	mod         *booksModule
	opts        app.Options
	cmds        map[string]app.Command
	marker      string
}

func newOfflineABO(t *testing.T) *offlineABO {
	t.Helper()
	h := &offlineABO{t: t, state: filepath.Join(t.TempDir(), "state")}
	h.path = filepath.Join(tempDir(t), "gotham.pebble")
	h.root, h.dump = gotham(t)
	h.edit = func(c *config.Config) { c.DatabasePath, c.RootDir = h.path, h.root }
	h.disk = func(o *app.Options) { o.StorageFS = storage.FS{} }
	h.marker = filepath.Join(filepath.Dir(h.path), "hnsw", "hnsw.clean-stop")
	h.boot()
	return h
}

// boot runs ABO's serve boot over the store once and stops it. Its
// Register stores a config blob naming the library root, as a settings
// save would (the composition stubs the settings load).
func (h *offlineABO) boot() {
	h.t.Helper()
	pre := func(store *database.PebbleStore) {
		if s, err := store.GetSetting("config_blob"); err == nil && s != nil {
			return
		}
		blob, err := json.Marshal(map[string]string{"root_dir": h.root})
		require.NoError(h.t, err)
		require.NoError(h.t, store.SetSetting("config_blob", string(blob), "json", false))
	}
	r := runComposedIn(h.t, h.state, h.edit, pre, h.disk, nil)
	require.NoError(h.t, r.stop())
	h.offline()
}

// offline builds the offline commands over the stopped store.
func (h *offlineABO) offline() {
	h.t.Helper()
	withConfig(h.t, func() config.Config { c := config.AppConfig; h.edit(&c); return c }())
	h.mod = Module().(*booksModule)
	opts, err := OfflineOptions()
	require.NoError(h.t, err)
	h.disk(&opts)
	h.opts = opts
	h.cmds = map[string]app.Command{}
	for _, c := range app.OfflineCommands(opts, h.mod) {
		h.cmds[c.Name] = c
	}
}

// run runs one offline command.
func (h *offlineABO) run(name string, args ...string) (string, error) {
	h.t.Helper()
	c, ok := h.cmds[name]
	require.True(h.t, ok, "no offline command %q", name)
	var out bytes.Buffer
	err := c.Run(h.t.Context(), args, &out)
	return out.String(), err
}

// setStoredRoot rewrites the stored config blob's root_dir through a
// read-write offline open.
func (h *offlineABO) setStoredRoot(root string) {
	h.t.Helper()
	o, err := app.OpenOffline(h.t.Context(), h.opts, false, h.mod)
	require.NoError(h.t, err)
	defer func() { require.NoError(h.t, o.Close()) }()
	sh, err := o.Storage(ID)
	require.NoError(h.t, err)
	raw, err := sh.Legacy.Get(h.t.Context(), []byte("setting:config_blob"))
	require.NoError(h.t, err, "the boot stored no config blob")
	var row map[string]any
	require.NoError(h.t, json.Unmarshal(raw, &row))
	var blob map[string]any
	require.NoError(h.t, json.Unmarshal([]byte(row["value"].(string)), &blob))
	blob["root_dir"] = root
	b, err := json.Marshal(blob)
	require.NoError(h.t, err)
	row["value"] = string(b)
	raw, err = json.Marshal(row)
	require.NoError(h.t, err)
	require.NoError(h.t, sh.Legacy.Set(h.t.Context(), []byte("setting:config_blob"), raw))
}

func (h *offlineABO) slot() restorejournal.SlotState {
	h.t.Helper()
	st, err := restorejournal.ReadSlot(vfs.Default, h.state)
	require.NoError(h.t, err)
	return st
}

// TestStageFactoryReset_DangerousRootRefused (05-storage §5S.3.15 "Staging"
// (2); COR8-4): a stored library root that is a protected system directory
// is refused when the wipe is requested (storage.ErrResetRefused, the
// route's 400), before any count is compared, and nothing is staged; the
// same root without the wipe stages, because nothing under it is touched.
func TestStageFactoryReset_DangerousRootRefused(t *testing.T) {
	h := newOfflineABO(t)
	h.setStoredRoot("/")
	_, err := h.run("storage reset", "--kind", "factory", "--confirm", "RESET",
		"--delete-library-files", "--library-root", "/", "--library-file-count", "1")
	require.ErrorIs(t, err, storage.ErrResetRefused)
	require.False(t, h.slot().Busy(), "a refused reset staged %+v", h.slot())

	out, err := h.run("storage reset", "--kind", "factory", "--confirm", "RESET")
	require.NoError(t, err, out)
	require.NotNil(t, h.slot().Pending)
}

// TestStageFactoryReset_StoreDirectoryRootRefused (R1-25 review SF5): a
// confirmed wipe of a library root that is the database's directory (its
// backups and checkpoints sit beside the store) or the secure state
// directory is refused before anything is staged, and every entry stays;
// rule (4) keeps those directories only as a whole.
func TestStageFactoryReset_StoreDirectoryRootRefused(t *testing.T) {
	h := newOfflineABO(t)
	dbDir := filepath.Dir(h.path)
	require.NoError(t, os.MkdirAll(filepath.Join(dbDir, "backups"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dbDir, "backups", "arkham.tar.gz"), []byte("cell"), 0o600))
	for _, root := range []string{dbDir, h.state} {
		h.setStoredRoot(root)
		entries, err := os.ReadDir(root)
		require.NoError(t, err)
		_, err = h.run("storage reset", "--kind", "factory", "--confirm", "RESET",
			"--delete-library-files", "--library-root", root, "--library-file-count", fmt.Sprint(len(entries)))
		require.ErrorIs(t, err, storage.ErrResetRefused, "a wipe of %s", root)
		require.False(t, h.slot().Busy(), "a refused reset staged %+v", h.slot())
		after, err := os.ReadDir(root)
		require.NoError(t, err)
		require.Len(t, after, len(entries), "a refused reset changed %s", root)
	}
	require.True(t, exists(filepath.Join(dbDir, "backups", "arkham.tar.gz")))
}

// TestFactoryResetPreflightCountsWhatRunRemoves (05-storage §5S.3.15 rule
// (4)): a library root that holds the state directory, as an install with
// its data inside the library folder does, counts the entries the wipe
// will remove and not the state directory, which the confined filesystem
// keeps at Run: the preflight reads the kernel's kept rule
// (module.ResetPlanKept), so the count the operator confirms is the one
// Run can remove.
func TestFactoryResetPreflightCountsWhatRunRemoves(t *testing.T) {
	h := newOfflineABO(t)
	root := filepath.Dir(h.state)
	require.NoError(t, os.WriteFile(filepath.Join(root, "Hush.m4b"), []byte("hush"), 0o600))
	h.setStoredRoot(root)
	_, err := h.run("storage reset", "--kind", "factory", "--confirm", "RESET", "--delete-library-files")
	lce, ok := errors.AsType[*storage.LibraryConfirmError](err)
	require.True(t, ok, "an unconfirmed wipe = %v", err)
	assert.Equal(t, root, lce.Root)
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	assert.Equal(t, len(entries)-1, lce.Files, "the count includes the state directory the wipe keeps (entries %v)", entries)
	require.False(t, h.slot().Busy())
}

// TestFactoryResetOffline_R1 (06 §6.10; 05-storage §5S.3.15): with the
// service stopped, storage reset --kind factory --confirm RESET stages a
// ResetFactory in the one slot whose hook_args name the dump directory,
// removes the HNSW clean-stop marker, and leaves the live store and its
// settings blob untouched; a wipe is first answered with the fresh root and
// count, read from the stored settings over the loaded config. The next
// start applies it: the old store set aside, books' reset hook removes the
// Open Library dumps and keeps the library, and the journal reads
// hook_done with nothing in flight. A confirmed wipe then empties the
// library root and keeps it.
func TestFactoryResetOffline_R1(t *testing.T) {
	h := newOfflineABO(t)
	elsewhere := filepath.Join(t.TempDir(), "arkham")
	withConfig(t, func() config.Config { c := config.AppConfig; c.RootDir = elsewhere; return c }())

	out, err := h.run("storage reset", "--kind", "factory", "--confirm", "RESET", "--delete-library-files")
	lce, ok := errors.AsType[*storage.LibraryConfirmError](err)
	require.True(t, ok, "an unconfirmed wipe = %v", err)
	assert.Equal(t, h.root, lce.Root, "the root is the stored one, not the loaded config's")
	assert.Equal(t, 4, lce.Files, "Year One, The Killing Joke, Hush.m4b and openlibrary-dumps")
	assert.Contains(t, out, "--library-file-count 4")
	require.False(t, h.slot().Busy())

	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, nil, 0o600))
	before := rawKVs(t, h.path)
	out, err = h.run("storage reset", "--kind", "factory", "--confirm", "RESET")
	require.NoError(t, err, out)
	st := h.slot()
	require.NotNil(t, st.Pending)
	require.Equal(t, restorejournal.KindReset, st.Pending.Kind)
	require.Equal(t, restorejournal.ResetFactory, st.Pending.Reset)
	id := st.Pending.ID
	raw, err := os.ReadFile(restorejournal.PendingPath(h.state))
	require.NoError(t, err)
	var in restorejournal.Intent
	require.NoError(t, json.Unmarshal(raw, &in))
	var args resetArgs
	require.NoError(t, json.Unmarshal(in.HookArgs, &args))
	assert.Equal(t, resetArgs{DumpDir: h.dump}, args)
	assert.False(t, in.DeleteLibraryFiles)
	assert.Equal(t, before, rawKVs(t, h.path), "staging wrote the live store")
	assert.False(t, exists(h.marker), "the HNSW clean-stop marker survived the staging")

	h.boot()
	recs := journalOf(t, h.state, id)
	require.Equal(t, "hook_done", recs[len(recs)-1].Step, "journal %+v", recs)
	assert.Empty(t, recs[len(recs)-1].Failed)
	require.False(t, h.slot().Busy(), "slot after the reset boot = %+v", h.slot())
	assert.True(t, exists(restorejournal.SetAsideDest(h.state, h.path, id, h.path)), "the old store was not set aside")
	assert.False(t, exists(h.dump), "the Open Library dumps survived the reset")
	assert.True(t, exists(filepath.Join(h.root, "Hush.m4b")), "a reset without the wipe removed a library file")

	out, err = h.run("storage reset", "--kind", "factory", "--confirm", "RESET", "--delete-library-files",
		"--library-root", h.root, "--library-file-count", "3")
	require.NoError(t, err, out)
	h.boot()
	for _, gone := range []string{"Year One", "The Killing Joke", "Hush.m4b"} {
		assert.False(t, exists(filepath.Join(h.root, gone)), "%s survived the confirmed wipe", gone)
	}
	assert.True(t, exists(h.root), "the wipe removed the library root itself")
	require.False(t, h.slot().Busy())
}

// storedRoot reads the stored config blob's root_dir through a read-only
// offline open.
func (h *offlineABO) storedRoot() string {
	h.t.Helper()
	o, err := app.OpenOffline(h.t.Context(), h.opts, true, h.mod)
	require.NoError(h.t, err)
	defer func() { require.NoError(h.t, o.Close()) }()
	sh, err := o.Storage(ID)
	require.NoError(h.t, err)
	raw, err := sh.Legacy.Get(h.t.Context(), []byte("setting:config_blob"))
	require.NoError(h.t, err)
	var row struct {
		Value string `json:"value"`
	}
	require.NoError(h.t, json.Unmarshal(raw, &row))
	var blob struct {
		RootDir string `json:"root_dir"`
	}
	require.NoError(h.t, json.Unmarshal([]byte(row.Value), &blob))
	return blob.RootDir
}

// TestRestoreOfflineArchive_R1 (05-storage §5S.3.12; 06 §6.4.6): with the
// service stopped, storage restore-offline --archive puts HEAD's backup
// archive of the store back in this process: it needs --accept-loss, the
// HNSW clean-stop marker of the replaced store goes, the restored store
// holds the archive's settings, and the next serve boot opens it with
// nothing in flight.
func TestRestoreOfflineArchive_R1(t *testing.T) {
	h := newOfflineABO(t)
	info, err := backup.CreateBackup(h.path, "pebble", backup.BackupConfig{BackupDir: t.TempDir(), MaxBackups: 5})
	require.NoError(t, err)
	elsewhere := filepath.Join(t.TempDir(), "arkham")
	h.setStoredRoot(elsewhere)
	require.Equal(t, elsewhere, h.storedRoot())

	_, err = h.run("storage restore-offline", "--archive", info.Path)
	require.Error(t, err, "an archive restore without --accept-loss")
	require.Equal(t, elsewhere, h.storedRoot(), "a refused restore changed the store")

	require.NoError(t, os.MkdirAll(filepath.Dir(h.marker), 0o700))
	require.NoError(t, os.WriteFile(h.marker, nil, 0o600))
	out, err := h.run("storage restore-offline", "--archive", info.Path, "--accept-loss")
	require.NoError(t, err, out)
	assert.Contains(t, out, "Restored archive "+info.Path)
	assert.False(t, exists(h.marker), "the HNSW clean-stop marker survived the restore")
	require.Equal(t, h.root, h.storedRoot(), "the restored store does not hold the archive's settings")
	require.False(t, h.slot().Busy(), "slot after the offline restore = %+v", h.slot())

	h.boot()
	require.Equal(t, h.root, h.storedRoot(), "the serve boot after the restore")
	require.False(t, h.slot().Busy())
}

// journalOf reads the swap id's journal (JSON Lines).
func journalOf(t *testing.T, state, id string) []restorejournal.Record {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(restorejournal.JournalDir(state, id), restorejournal.JournalName))
	require.NoError(t, err)
	var out []restorejournal.Record
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var r restorejournal.Record
		require.NoError(t, json.Unmarshal([]byte(line), &r))
		out = append(out, r)
	}
	return out
}
