// file: internal/books/reset_hook.go
// version: 1.0.0
// guid: 9eda415d-9fbc-4638-90d2-e8b516f9dfdf
// last-edited: 2026-11-08

package books

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// resetLog logs books' reset hook.
var resetLog = logger.New("reset")

// resetBatch is how many entries the hook removes between two ExtendStart
// calls (05-storage §5S.3.15 rule (3): it asks for start time per batch).
const resetBatch = 64

// resetArgs is books' ResetPlan.Hook, recorded at staging as the intent's
// hook_args and handed back to Run: the Open Library dump directory and,
// only with the wipe, the library root the operator confirmed. The kernel
// never parses it; ResetPlan.Paths carries the same directories for the
// confinement.
type resetArgs struct {
	DumpDir string `json:"dump_dir,omitempty"`
	RootDir string `json:"root_dir,omitempty"`
}

// resetHook is books' Manifest.ResetHook (05-storage §5S.3.15 "The app's
// reset hook"; ResetFactory only): HEAD's FactoryReset file effects, moved
// out of the request and into the boot that applies the reset. The store
// and the settings are reset by the swap itself (the store is set aside
// and the boot is a fresh install), so the hook only removes files.
func (m *booksModule) resetHook() *storage.ResetHook {
	return &storage.ResetHook{Plan: m.planReset, Run: m.runReset}
}

// planReset is the hook's Plan. For ResetFactory it resolves the Open
// Library dump directory and the library root the service runs with
// (resetDirs) and, with req.DeleteLibraryFiles, runs the preflight: a
// missing or dangerous library root is refused (storage.ErrResetRefused,
// nothing staged, where HEAD refused only after its store was wiped), and
// LibraryFiles is the number of top-level entries the wipe would remove:
// the kept ones, which the confined filesystem refuses at Run, are not
// counted (preflight). A dump directory that is a dangerous root, or that
// is or holds the library root, is refused whatever the request: removing
// it would remove the library folder itself, which neither the plain reset
// nor the wipe does.
func (m *booksModule) planReset(ctx context.Context, req storage.ResetRequest) (storage.ResetPlan, error) {
	if req.Kind != restorejournal.ResetFactory {
		return storage.ResetPlan{}, nil
	}
	root, dump, err := resetDirs(ctx)
	if err != nil && req.DeleteLibraryFiles {
		return storage.ResetPlan{}, fmt.Errorf("%w: the library root cannot be read from the stored settings: %w", storage.ErrResetRefused, err)
	}
	if dump != "" {
		switch {
		case !filepath.IsAbs(dump):
			return storage.ResetPlan{}, fmt.Errorf("%w: the Open Library dump directory %q is not absolute", storage.ErrResetRefused, dump)
		case library.IsDangerousRoot(dump):
			return storage.ResetPlan{}, fmt.Errorf("%w: the Open Library dump directory %s is a protected system directory", storage.ErrResetRefused, dump)
		case root != "" && restorejournal.InTree(root, dump):
			return storage.ResetPlan{}, fmt.Errorf("%w: the Open Library dump directory %s holds the library root %s; set openlibrary_dump_dir to a directory of its own", storage.ErrResetRefused, dump, root)
		}
	}
	args := resetArgs{DumpDir: dump}
	var paths []string
	if dump != "" {
		paths = append(paths, filepath.Clean(dump))
	}
	plan := storage.ResetPlan{}
	if req.DeleteLibraryFiles {
		kept, _ := module.ResetPlanKept(ctx)
		n, err := preflight(root, kept)
		if err != nil {
			return storage.ResetPlan{}, err
		}
		args.RootDir = filepath.Clean(root)
		paths = append(paths, args.RootDir)
		plan.LibraryRoot, plan.LibraryFiles = args.RootDir, n
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return storage.ResetPlan{}, err
	}
	plan.Hook, plan.Paths = raw, paths
	return plan, nil
}

// preflight checks the library root the wipe would empty and counts the
// top-level entries it would remove: one directory read, never a walk. A
// root that does not exist has nothing to remove. kept is the kernel's
// rule (4) (module.ResetPlanKept), the one the confined filesystem applies
// at Run: an entry it reports (one that holds the store or the state
// directory, say) is never removed, so it is not counted. nil counts every
// entry.
func preflight(root string, kept func(path string) bool) (int, error) {
	switch {
	case root == "":
		return 0, fmt.Errorf("%w: no library root is configured, so there is nothing to wipe", storage.ErrResetRefused)
	case !filepath.IsAbs(root):
		return 0, fmt.Errorf("%w: the library root %q is not absolute", storage.ErrResetRefused, root)
	case library.IsDangerousRoot(root):
		return 0, fmt.Errorf("%w: the library root %s is a protected system directory", storage.ErrResetRefused, root)
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("books: read the library root %s: %w", root, err)
	}
	n := 0
	for _, e := range entries {
		if kept == nil || !kept(filepath.Join(root, e.Name())) {
			n++
		}
	}
	return n, nil
}

// resetDirs is the library root and the Open Library dump directory the
// service runs with, layered as the settings boot layers them: the stored
// settings (the config blob, else HEAD's per-key rows) over the loaded
// config (config.LoadConfigFromDatabase), then a root_dir from the
// environment or the config file over both (config.RootDirOverride); the
// default dump directory is <root>/openlibrary-dumps. The stored settings are read through the
// read-only storage the kernel hands Plan (module.ResetPlanStorage). When
// the kernel could not open the store, or the stored settings do not read,
// the loaded config's values are returned with the error, and Plan refuses
// a wipe on it.
func resetDirs(ctx context.Context) (root, dump string, err error) {
	snap := config.Snapshot()
	root, dump = snap.RootDir, snap.OpenLibraryDumpDir
	h, ok := module.ResetPlanStorage(ctx)
	if !ok || h.Legacy == (storage.LegacyRange{}) {
		return root, defaultDump(root, dump), errors.New("the store did not open, so its settings were not read")
	}
	stored, found, err := storedDirs(ctx, h.Legacy)
	if err != nil {
		return root, defaultDump(root, dump), err
	}
	if found.root {
		root = stored.RootDir
	}
	if found.dump {
		dump = stored.DumpDir
	}
	// The environment or the config file overrides the stored root, as the
	// settings boot's SyncConfigFromEnv does after the load.
	if v, ok := config.RootDirOverride(); ok {
		root = v
	}
	return root, defaultDump(root, dump), nil
}

// defaultDump is config's default Open Library dump directory: the
// configured one, else <root>/openlibrary-dumps, else none.
func defaultDump(root, dump string) string {
	if dump == "" && root != "" {
		return filepath.Join(root, "openlibrary-dumps")
	}
	return dump
}

// storedSetting is the JSON a "setting:" row holds (database.Setting).
type storedSetting struct {
	Value    string `json:"value"`
	IsSecret bool   `json:"is_secret"`
}

// storedDirs reads root_dir and openlibrary_dump_dir from the stored
// settings: the config_blob row when there is one (a blob that does not
// parse is an error, as LoadConfigFromDatabase refuses it), else HEAD's
// per-key rows. found says which the store set.
func storedDirs(ctx context.Context, r storage.LegacyRange) (dirs resetArgs, found struct{ root, dump bool }, err error) {
	if v, ok, err := settingRow(ctx, r, "config_blob"); err != nil {
		return dirs, found, err
	} else if ok {
		var blob map[string]json.RawMessage
		if err := json.Unmarshal([]byte(v), &blob); err != nil {
			return dirs, found, fmt.Errorf("the stored config_blob does not parse: %w", err)
		}
		for key, dst := range map[string]*string{"root_dir": &dirs.RootDir, "openlibrary_dump_dir": &dirs.DumpDir} {
			raw, ok := blob[key]
			if !ok {
				continue
			}
			if err := json.Unmarshal(raw, dst); err != nil {
				return dirs, found, fmt.Errorf("the stored config_blob's %s is not a string: %w", key, err)
			}
			if key == "root_dir" {
				found.root = true
			} else {
				found.dump = true
			}
		}
		return dirs, found, nil
	}
	for key, dst := range map[string]*string{"root_dir": &dirs.RootDir, "openlibrary_dump_dir": &dirs.DumpDir} {
		v, ok, err := settingRow(ctx, r, key)
		if err != nil {
			return dirs, found, err
		}
		if ok {
			*dst = v
			if key == "root_dir" {
				found.root = true
			} else {
				found.dump = true
			}
		}
	}
	return dirs, found, nil
}

// settingRow reads one non-secret "setting:" row's value.
func settingRow(ctx context.Context, r storage.LegacyRange, key string) (string, bool, error) {
	raw, err := r.Get(ctx, []byte("setting:"+key))
	if errors.Is(err, storage.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read setting %s: %w", key, err)
	}
	var s storedSetting
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false, fmt.Errorf("setting %s does not parse: %w", key, err)
	}
	if s.IsSecret {
		return "", false, fmt.Errorf("setting %s is stored as a secret", key)
	}
	return s.Value, true, nil
}

// runReset is the hook's Run (05-storage §5S.3.15 rules (1)-(4)): it removes
// the intent's delete entries, the recorded Open Library dump directory
// (with the Open Library store closed, as HEAD's deleteOLDataLocked did)
// and, only when the intent's delete_library_files is set, every top-level
// entry of the recorded library root, keeping the root itself. A per-entry
// failure is logged and recorded, never returned; a kept entry is skipped
// (the confined filesystem refuses it, and the kernel records it as kept);
// a dangerous root is re-checked only to skip the wipe; the context's end
// (the reset hook timeout) records the entries not reached as failed. It
// calls Done once every action has been attempted.
func (m *booksModule) runReset(ctx context.Context, rr storage.ResetRun) error {
	var failed []string
	fail := func(p string, err error) {
		resetLog.Warn("Factory reset could not remove %s: %v", p, err)
		failed = append(failed, fmt.Sprintf("%s: %v", p, err))
	}
	remove := func(p string) {
		if err := rr.Files.RemoveAll(p); err != nil && !errors.Is(err, storage.ErrKeptPath) {
			fail(p, err)
		}
	}
	var args resetArgs
	if len(rr.Args) > 0 {
		if err := json.Unmarshal(rr.Args, &args); err != nil {
			fail("hook_args", err)
			return rr.Done(context.WithoutCancel(ctx), failed)
		}
	}
	for _, p := range rr.Delete {
		if ctx.Err() != nil {
			fail(p, ctx.Err())
			continue
		}
		remove(p)
	}
	if args.DumpDir != "" {
		if err := ctx.Err(); err != nil {
			fail(args.DumpDir, err)
		} else if err := m.withOpenLibraryClosed(func() error { remove(args.DumpDir); return nil }); err != nil {
			fail(args.DumpDir, err)
		}
	}
	if rr.DeleteLibraryFiles && args.RootDir != "" {
		m.wipeLibrary(ctx, rr, args.RootDir, remove, fail)
	}
	return rr.Done(context.WithoutCancel(ctx), failed)
}

// wipeLibrary removes every top-level entry of root, asking for start time
// per batch; entries not reached before the context ends are failed.
func (m *booksModule) wipeLibrary(ctx context.Context, rr storage.ResetRun, root string,
	remove func(string), fail func(string, error)) {
	if library.IsDangerousRoot(root) {
		fail(root, errors.New("a protected system directory; the library wipe was skipped"))
		return
	}
	entries, err := rr.Files.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		fail(root, err)
		return
	}
	for i, e := range entries {
		p := filepath.Join(root, e.Name())
		if err := ctx.Err(); err != nil {
			fail(p, err)
			continue
		}
		if i%resetBatch == 0 && rr.ExtendStart != nil {
			rr.ExtendStart("factory reset: removing library entries")
		}
		remove(p)
	}
	resetLog.Info("Factory reset removed the library entries under %s", root)
}

// withOpenLibraryClosed runs fn with the served Open Library store closed
// and its service locked (server.WithOpenLibraryClosed), so no import opens
// it again in between; with no server (an offline command) fn runs alone.
func (m *booksModule) withOpenLibraryClosed(fn func() error) error {
	if srv := m.srv.Load(); srv != nil {
		return srv.WithOpenLibraryClosed(fn)
	}
	return fn()
}
