// file: core/examples/hello/state_hook_test.go
// version: 0.1.0
// guid: e8a14f62-0b3d-47c9-9a5e-3d71b2c860f4
// last-edited: 2026-11-22

package hello_test

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/coretest/crashmatrix"
	"github.com/falkcorp/mediacore/examples/hello"
	"github.com/falkcorp/mediacore/module"
)

// The labels of the cleaner module's two writes: a module's writer-table rows
// are the app's, so they live in the scenario that boots the module, not in
// core's crashmatrix.Rows.
const (
	cleanerKeyLabel   = "hello.cleaner.stale_key"
	cleanerIndexLabel = "hello.cleaner.stale_index"
)

// cleaner is hello plus a start hook that does what an app's legacy-file
// cleanup does (R3-03a): it deletes a stale file of the state directory and a
// stale directory tree beside the store, through Deps.State under one crash
// label each, logging and carrying on when a deletion fails so the next boot
// retries it.
type cleaner struct {
	*hello.Module
	state string
}

func (c cleaner) paths() (key, index string) {
	return filepath.Join(c.state, ".joker_key"), filepath.Join(c.state, "storage", "riddler.idx")
}

func (c cleaner) Register(r *module.Registrar) error {
	if err := c.Module.Register(r); err != nil {
		return err
	}
	st, log := r.Deps().State, slog.New(slog.DiscardHandler)
	key, index := c.paths()
	r.Hook(module.Hook{Name: string(hello.ID) + ".legacy-cleanup", Start: func(ctx context.Context) error {
		if err := st.RemoveAll(ctx, cleanerIndexLabel, index); err != nil {
			log.Warn("legacy index not removed; the next boot retries", "err", err)
		}
		if err := st.Remove(ctx, cleanerKeyLabel, key); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Warn("legacy key not removed; the next boot retries", "err", err)
		}
		return nil
	}})
	return r.Err()
}

// cleanerScenario is the matrix over one fixture holding what the cleaner
// deletes: the key, and a tree two levels deep beside the store.
func cleanerScenario(t *testing.T) crashmatrix.Scenario {
	t.Helper()
	f := crashmatrix.Fixture{Name: "stale-files", StateDir: t.TempDir()}
	mod := cleaner{Module: hello.New(), state: f.StateDir}
	key, index := mod.paths()
	for _, p := range []string{key, filepath.Join(index, "store", "root.db"), filepath.Join(index, "meta.json")} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("arkham"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return crashmatrix.Scenario{
		Build:    build(mod),
		Fixtures: []crashmatrix.Fixture{f},
		Rows: []crashmatrix.Row{
			{Phase: "start hooks", Writer: "hello.legacy-cleanup: the stale key of the state directory (Deps.State.Remove)", Labels: []string{cleanerKeyLabel}, Retried: true},
			{Phase: "start hooks", Writer: "hello.legacy-cleanup: the stale index tree beside the store (Deps.State.RemoveAll)", Labels: []string{cleanerIndexLabel}, Retried: true},
		},
	}
}

// TestModuleStateFSLabelsWrites: a module start hook's deletions through
// Deps.State reach the crash-matrix kit under the labels the module gave, and
// only those: the kit fails a run that records an unlabelled operation, a
// label no row lists, or a write on the real disk (the fixture's own files
// are never touched), and the census sees both of the module's rows reached.
func TestModuleStateFSLabelsWrites(t *testing.T) {
	s := cleanerScenario(t)
	observed := crashmatrix.Observed(t, s)
	for _, l := range []string{cleanerKeyLabel, cleanerIndexLabel} {
		if !observed[l] {
			t.Errorf("label %s: the kit never saw the module's labelled write; observed %v", l, labelsOf(observed))
		}
	}
	missing, stale := crashmatrix.Unobserved(s.Rows, observed, nil)
	if len(missing) != 0 || len(stale) != 0 {
		t.Errorf("the module's rows and the labels the kit observed disagree: missing %v, stale %v", missing, stale)
	}
	key, index := cleaner{Module: hello.New(), state: s.Fixtures[0].StateDir}.paths()
	for _, p := range []string{key, filepath.Join(index, "meta.json")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the census removed the real file %s: %v", p, err)
		}
	}
}

// TestModuleStateHookCrashMatrix: the cleaner's boot broken at every durable
// operation of both deletions, by a kill and by ENOSPC and EACCES, then
// rebooted: the next boot reaches readiness (I5) and finishes the cleanup
// whatever state the break left.
func TestModuleStateHookCrashMatrix(t *testing.T) {
	crashmatrix.Run(t, cleanerScenario(t))
}

// meddler is hello plus a start hook that reaches for what core owns: the open
// store, a file inside it, the keyring and the config boot record, each through
// Deps.State. It records what the handle answered.
type meddler struct {
	*hello.Module
	state string
	got   *map[string]error
}

func (m meddler) targets() map[string]string {
	db := filepath.Join(m.state, "storage", "main.pebble")
	return map[string]string{
		"the store":         db,
		"a file in it":      filepath.Join(db, "MANIFEST-000001"),
		"the keyring":       filepath.Join(m.state, "secrets.keyring"),
		"the boot record":   filepath.Join(m.state, "config.boot.json"),
		"the checkpoints":   filepath.Join(m.state, "storage", ".mediacore-checkpoints"),
		"the swap lock":     filepath.Join(m.state, "swap.lock"),
		"the store's level": db + ".storage-levels",
	}
}

func (m meddler) Register(r *module.Registrar) error {
	if err := m.Module.Register(r); err != nil {
		return err
	}
	st := r.Deps().State
	r.Hook(module.Hook{Name: string(hello.ID) + ".meddle", Start: func(ctx context.Context) error {
		got := map[string]error{}
		for name, p := range m.targets() {
			got[name] = st.RemoveAll(ctx, "hello.meddler.remove", p)
		}
		*m.got = got
		return nil
	}})
	return r.Err()
}

// TestModuleStateRefusesCoreOwnedPaths: through a real boot, a start hook that
// removes the open store, a file inside it, the keyring, the boot record, the
// checkpoints or the swap lock through Deps.State is refused with
// ErrStateCoreOwned, nothing is removed, and the next boot opens the same
// store: the kernel hands modules a handle that carries the core-owned rule.
func TestModuleStateRefusesCoreOwnedPaths(t *testing.T) {
	f := crashmatrix.Fixture{Name: "meddled", StateDir: t.TempDir()}
	var got map[string]error
	mod := meddler{Module: hello.New(), state: f.StateDir, got: &got}
	boot(t, f, mod)
	if len(got) != len(mod.targets()) {
		t.Fatalf("the hook ran %d of %d removals (did it run at all?)", len(got), len(mod.targets()))
	}
	for name, err := range got {
		if !errors.Is(err, module.ErrStateCoreOwned) {
			t.Errorf("%s: %v, want ErrStateCoreOwned", name, err)
		}
	}
	for _, p := range []string{filepath.Join(f.StateDir, "secrets.keyring"), filepath.Join(f.StateDir, "storage", "main.pebble")} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("the refused hook removed %s: %v", p, err)
		}
	}
	got = nil
	boot(t, f, mod) // the store is intact: it opens, and the hook is refused again
	if len(got) == 0 {
		t.Fatal("the hook did not run at the second boot")
	}
}

func labelsOf(m map[string]bool) []string {
	var out []string
	for l := range m {
		out = append(out, l)
	}
	slices.Sort(out)
	return out
}
