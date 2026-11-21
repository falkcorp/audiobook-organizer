// file: internal/books/flip_older_builds_test.go
// version: 1.0.0
// guid: 3f7a1c58-9d24-4e60-b8a3-0c5e72d91b46
// last-edited: 2026-11-20

package books

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// flipBootOn boots the flipped composition on the corpus copy at work and returns
// app.New's error: M2 and the level steps run and the boot succeeds (the app is
// closed). With failM2 M2's step fails once the level step has run, which leaves
// the store a boot killed between the two would leave.
func flipBootOn(t *testing.T, work string, failM2 bool) error {
	t.Helper()
	repointCorpusPaths(t, work)
	var wrap []func(module.Module) module.Module
	if failM2 {
		wrap = append(wrap, func(m module.Module) module.Module { return m2Killed{m} })
	}
	return bootAt(t, work, func(o *app.Options) {
		o.Env = append([]string{storageEnv()}, absEnv()...)
	}, wrap...)
}

// m2Killed wraps the flipped module so M2's step fails when it runs: the level
// step has raised the stamps by then (M2 is After it), so the store is what a
// boot killed between the two leaves. M2's preflight still passes, so the boot
// reaches the step.
type m2Killed struct{ module.Module }

func (f m2Killed) Manifest() module.Manifest {
	m := f.Module.Manifest()
	inner := m.Cutover
	m.Cutover = func(imp module.Importers) []datamigrate.DataMigration {
		steps := inner(imp)
		for i := range steps {
			steps[i].Step = func(context.Context, storage.KV, *datamigrate.StepWriter) error {
				return errors.New("the boot was killed after the level step")
			}
		}
		return steps
	}
	return m
}

// preFlip wraps the books module as the build that predates the flip ships it:
// the same storage declaration and migrations with no cutover step, no store
// rewrite and no raised level. It is the pre-freeze release's module, which the
// raised stamps must keep out.
type preFlip struct{ module.Module }

func (p preFlip) Manifest() module.Manifest {
	m := p.Module.Manifest()
	plain := (&booksModule{}).Manifest()
	m.Storage, m.Cutover, m.Rewrite = plain.Storage, nil, nil
	return m
}

// corpusBuilderDir is the directory the corpus' builder ran in (a scratch
// checkout of stage 1): the stored path settings name it.
const corpusBuilderDir = "/tmp/r2corpus/stage1"

// repointCorpusPaths rewrites the stored config blob's builder-relative paths
// to the copy at work, so the boot validates directories that exist (the
// flipped boot validates the effective config at registration; the builder's
// scratch directory is gone by then). A store whose blob names no builder path
// is left alone.
func repointCorpusPaths(t *testing.T, work string) {
	t.Helper()
	st, err := database.OpenStandalone(storeDB(work), database.StandaloneOptions{})
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	s, err := st.GetSetting("config_blob")
	require.NoError(t, err)
	if s == nil || !strings.Contains(s.Value, corpusBuilderDir) {
		return
	}
	require.NoError(t, st.SetSetting("config_blob", strings.ReplaceAll(s.Value, corpusBuilderDir, work), s.Type, false))
}

// bootAt boots the composition on the corpus copy at work with opt applied and
// returns app.New's error; a boot that succeeds is closed.
func bootAt(t *testing.T, work string, opt func(*app.Options), wrap ...func(module.Module) module.Module) error {
	t.Helper()
	edit, base := cutoverBootConfig(t, work)
	mod, opts, _ := composedOptions(t, false, filepath.Join(work, "state"), edit, nil)
	base(&opts)
	opts.StateDir = filepath.Join(work, "state")
	// Options asks for the process defaults, which app.New installs early and only
	// InstallProcessDefaults' restore (or a failed New) puts back: a boot that
	// succeeds and is merely closed would leave the process holding them, and
	// every later test's InstallProcessDefaults would answer ErrProcessDefaultsTaken.
	// These boots read the store, they do not need the globals.
	opts.ProcessDefaults = false
	opt(&opts)
	var m module.Module = mod
	for _, w := range wrap {
		m = w(m)
	}
	a, err := app.New(t.Context(), opts, m)
	if err == nil {
		require.NoError(t, a.Close(t.Context()))
	}
	Release(mod)
	return err
}

// levelsAt reads the level sidecar the store's boot wrote.
func levelsAt(t *testing.T, work string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(storeDB(work) + ".storage-levels")
	require.NoError(t, err)
	out := map[string]string{}
	for _, f := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(f), " "); ok {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

// dirImage is every file under dir by relative path and content, so a boot that
// wrote nothing can be told from one that rewrote a sidecar.
func dirImage(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// Pebble's own files (tables, manifest, WAL, lock) are rewritten by any open:
		// the refusal can come after the open, and a compaction replaces tables
		// without changing a row. The rows are compared by rawKVs; this image is for
		// the sidecars and everything else beside the store.
		if strings.Contains(p, ".pebble"+string(filepath.Separator)) {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out[rel] = string(b)
		return nil
	}))
	return out
}

// TestCutoverRefusedByOlderBuilds (05-storage 5S.3.7; 14 K7): once the flip's
// level step has run, a build that predates it refuses the store, with exit 78 and
// nothing written, whether the cutover finished or the boot was killed after the
// level step and before M2 completed. Two older builds are modelled: this
// codebase's pre-flip build (the dark module, which is what the pre-freeze release
// ships) over the same core, which reads the raised level stamps and answers
// *storage.LevelTooNewError; and R0's own storage-format check, which compares the
// legacy stamp books projects (legacyMaxFormat + 1) with the format it supports.
// Both owners' stamps (books and core) are asserted raised to this build's levels.
// The mid-M3 cell is F4-08's.
func TestCutoverRefusedByOlderBuilds(t *testing.T) {
	flipLevel := flipOn().Manifest().Storage.Level
	require.Positive(t, flipLevel, "the flipped manifest declares no raised books level: the test would check nothing")

	for _, tc := range []struct {
		name        string
		bootFailure bool
	}{
		{"after the cutover finished", false},
		{"killed after the level step, before M2 finished", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := copyCorpus(t, r0Corpus(t))
			err := flipBootOn(t, work, tc.bootFailure)
			if tc.bootFailure {
				require.Error(t, err, "the boot whose M2 step fails completed M2")
				require.Contains(t, err.Error(), "config-cutover")
			} else {
				require.NoError(t, err)
			}

			// Both owners' stamps are raised to this build's levels.
			levels := levelsAt(t, work)
			require.Equal(t, itoa(flipLevel), levels["books"], "books' stamp (levels %v)", levels)
			require.Equal(t, itoa(kernel.CoreCutoverLevel), levels["core"], "core's stamp (levels %v)", levels)

			// R0's check: books' legacy projection is above what R0 supports.
			sidecar, err := os.ReadFile(storeDB(work) + ".storage-format")
			require.NoError(t, err)
			require.Greater(t, atoi(t, strings.TrimSpace(string(sidecar))), database.SupportedStorageFormat,
				"the storage-format sidecar R0 and `make rollback` read is not above R0's supported format")

			// The pre-flip build refuses, exit 78, and writes nothing.
			beforeRows, beforeFiles := rawKVs(t, storeDB(work)), dirImage(t, work)
			err = bootAt(t, work, func(o *app.Options) {}, func(m module.Module) module.Module { return preFlip{m} })
			require.Error(t, err, "a pre-flip build booted a store the flip raised")
			var tooNew *storage.LevelTooNewError
			require.True(t, errors.As(err, &tooNew), "%v", err)
			require.Greater(t, tooNew.Store, tooNew.Supported, "%v", err)
			require.Equal(t, kernel.ExitConfig, kernel.ExitCode(err), "%v", err)
			require.Equal(t, beforeRows, rawKVs(t, storeDB(work)), "the refused boot wrote rows")
			require.Empty(t, imageDiff(beforeFiles, dirImage(t, work)), "the refused boot wrote files")
		})
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func atoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	require.NoError(t, err, s)
	return n
}

// imageDiff names the files that differ between two dirImages (names only: the
// contents are large and a failure message must stay readable).
func imageDiff(before, after map[string]string) []string {
	var out []string
	for k, v := range before {
		if w, ok := after[k]; !ok {
			out = append(out, "removed "+k)
		} else if w != v {
			out = append(out, "changed "+k)
		}
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			out = append(out, "added "+k)
		}
	}
	slices.Sort(out)
	return out
}
