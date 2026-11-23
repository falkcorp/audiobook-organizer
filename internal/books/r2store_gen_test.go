//go:build regen_r2fixture

// file: internal/books/r2store_gen_test.go
// version: 0.1.0
// guid: 4b9d27e3-6f1a-4c58-a0d4-82e57c31f9b6
// last-edited: 2026-11-22

package books

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/tasks"
)

// The generator of testdata/r2store, the frozen store the R2 release leaves
// behind (R3-02f). It is the one place that still turns the R0 corpus into an R2
// store, by running the cutover code, and it goes with that code: R3-02e deletes
// the cutover steps and the flip, after which the fixture cannot be rebuilt and
// the committed files are the only copy. Everything that only READS the fixture
// is in r2store_fixture_test.go and imports nothing of the cutover.
//
// This file builds only under the regen_r2fixture tag, so the default build and
// staticcheck never see it; `go vet -tags regen_r2fixture ./internal/books/` is
// what keeps it compiling until e-1 deletes it.
//
// What the generator writes. The R0 corpus (testdata/r2corpus/overlay) is booted
// through the flipped composition with books' floor undeclared, which is the R2
// build: M2 runs, and so does the level step. M3 to M6 are not live until F4-08
// (#4987) and F5-18 merge, so the R2 build records none of them today; the
// generator stands each one in by copying the done record a fresh install of a
// floor-declaring build writes for it (r2StoreToppedUp lists which). The
// fixture must be regenerated with the one command in r2storeRegenCommand once
// those flips are on main, and TestR2StoreTopUpIsNotStale fails until it is.
//
// The scheduler clocks. Nothing in the R2 build runs the scheduler over the
// corpus, so the built store holds no c/tasks/ row. The generator adds the
// clocks of r2StoreClockRows through the real writer (r2StoreSchedulerClocks),
// not through M4, which R3-02e-1 deletes.
//
// Where it builds. The boot validates the directories the effective config
// names, so the store is built under a fixed synthetic directory
// (r2StoreBuildDir, the corpusBuilderDir convention): the paths the boot stores
// name that directory, never a temporary one of the machine that ran the
// generator. Rewriting the rows afterwards would leave the old values behind in
// compressed blocks, where nothing could see them.

// r2StoreBuildDir is the directory the generator builds under: fixed, synthetic,
// and removed afterwards. Two generator runs on one host cannot overlap.
const r2StoreBuildDir = "/tmp/r2store-generator"

// r2StoreKept is what the fixture keeps of a built store, relative to the build
// directory: the store, its sidecars and the two key files the store's sealed
// values need. Everything else the boots leave (the checkpoints, the logs, the
// restore journal) holds this machine's paths or times and is not a store.
var r2StoreKept = []string{
	"db/audiobooks.pebble",
	"db/audiobooks.pebble.storage-format",
	"db/audiobooks.pebble.storage-levels",
	"db/config.yaml",
	"db/library.bleve",
	"db/library.bleve.mapping",
	"db/library.bleve.rebuilding",
	"state/.encryption_key",
	"state/secrets.keyring",
}

// r2StoreWithoutFloor is the R2 build: the flipped composition with books'
// floor undeclared (R2 ships the cutover steps and no floor).
type r2StoreWithoutFloor struct{ module.Module }

func (b r2StoreWithoutFloor) Manifest() module.Manifest {
	m := b.Module.Manifest()
	m.Storage.UpgradeFloor = nil
	return m
}

// r2StoreWithFloor is a build that declares books' floor with M2 to M6 required,
// so that its fresh install records them all as done whether or not it
// registers them: the source of the records the generator tops up with.
type r2StoreWithFloor struct{ module.Module }

func (b r2StoreWithFloor) Manifest() module.Manifest {
	m := b.Module.Manifest()
	var req []storage.StepRef
	for _, id := range r2StoreSteps {
		req = append(req, storage.StepRef{Owner: database.BooksOwner, DataMigration: id})
	}
	m.Storage.UpgradeFloor = &storage.UpgradeFloor{
		Owner: database.BooksOwner, Version: database.LegacyCap, Requires: req, InstallFirst: "v0.0.0-r2store-generator",
	}
	return m
}

// r2StoreFreshRecords boots a fresh install of a floor-declaring build in its own
// directory and returns its done records for ids, keyed by row key.
func r2StoreFreshRecords(t *testing.T, ids []string) map[string]string {
	t.Helper()
	root := t.TempDir()
	work := filepath.Join(root, "fresh")
	require.NoError(t, os.MkdirAll(filepath.Join(work, "state"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(work, "db"), 0o700))
	path := storeDB(work)
	stateDir := filepath.Join(work, "state")
	edit := func(c *config.Config) { c.DatabasePath, c.RootDir = path, filepath.Dir(path) }
	mod, opts, _ := composedOptions(t, false, stateDir, edit, nil)
	opts.StorageFS = storage.FS{}
	opts.ProcessDefaults = false
	a, err := app.New(t.Context(), opts, r2StoreWithFloor{mod})
	require.NoError(t, err, "the fresh install the top-up records come from")
	require.NoError(t, a.Close(t.Context()))
	Release(mod)

	kv := rawKVs(t, path)
	out := map[string]string{}
	for _, id := range ids {
		raw, ok := kv[r2StoreStepKey(id)]
		require.True(t, ok, "the fresh install wrote no record for %s", id)
		require.Contains(t, raw, `"state":"done"`, id)
		out[r2StoreStepKey(id)] = raw
	}
	return out
}

// r2StorePutRows writes rows into the closed store at path.
func r2StorePutRows(t *testing.T, path string, rows map[string]string) {
	t.Helper()
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	for k, v := range rows {
		require.NoError(t, db.Set([]byte(k), []byte(v), pebble.Sync))
	}
	require.NoError(t, db.Close())
}

// r2StoreSchedulerClocks returns the rows of r2StoreClockRows as the real writer
// stores them. The built store is closed and cannot be opened with core/tasks
// alone, so the clocks are written with tasks.NativeStores(ks).Clocks into a
// scratch store of that one owner, and only its c/tasks/ rows are returned (the
// scratch store's own c/storage rows are its, not the fixture's). M4 does not
// run: the rows are the ones r2StoreClockRows names.
func r2StoreSchedulerClocks(t *testing.T) map[string]string {
	t.Helper()
	ctx := context.Background()
	db, path := r2StoreTasksDB(t)
	ks, ok := db.Keyspace(r2StoreTasksOwner)
	require.True(t, ok, "the scratch store has no %s keyspace", r2StoreTasksOwner)
	clocks := tasks.NativeStores(ks).Clocks
	b := ks.NewIndexedBatch()
	for _, row := range r2StoreClockRows() {
		require.NoError(t, clocks.Advance(ctx, b, row.Kind, tasks.Clock{}, false, row.Clock), row.Kind)
	}
	require.NoError(t, b.Commit(ctx, storage.Sync))
	require.NoError(t, db.Close())

	out := map[string]string{}
	for k, v := range r2StoreRows(t, path) {
		if strings.HasPrefix(k, r2StoreTasksPrefix) {
			out[k] = v
		}
	}
	require.Len(t, out, len(r2StoreClockRows()), "one row per clock under %s", r2StoreTasksPrefix)
	return out
}

// r2StoreBuilt is what buildR2Store returns.
type r2StoreBuilt struct {
	// Dir is r2StoreBuildDir: the built store, with everything the boots left.
	Dir string
	// ToppedUp are the steps among r2StoreSteps the R2 build did not record and
	// the generator stood in with a fresh install's record, in r2StoreSteps order.
	ToppedUp []string
}

// buildR2Store turns the R0 corpus into the store R2 leaves behind, under
// r2StoreBuildDir, and removes the directory when the test ends.
func buildR2Store(t *testing.T) r2StoreBuilt {
	t.Helper()
	dir := r2StoreBuildDir
	require.NoError(t, os.RemoveAll(dir), "an earlier generator run left %s", dir)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	require.NoError(t, copyCorpusTo(r0Corpus(t), dir))
	repointCorpusPaths(t, dir)

	env := func(o *app.Options) { o.Env = bootEnv(absEnv()...) }
	require.NoError(t, bootAt(t, dir, env, func(m module.Module) module.Module { return r2StoreWithoutFloor{m} }), "the R2 build")

	done := r2StoreDone(t, storeDB(dir), r2StoreSteps...)
	require.True(t, done[r2StoreSteps[0]], "the R2 build did not run M2: this is not an R2 store")
	var missing []string
	for _, id := range r2StoreSteps {
		if !done[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		r2StorePutRows(t, storeDB(dir), r2StoreFreshRecords(t, missing))
	}
	for id, ok := range r2StoreDone(t, storeDB(dir), r2StoreSteps...) {
		require.True(t, ok, "%s is still not recorded", id)
	}
	r2StorePutRows(t, storeDB(dir), r2StoreSchedulerClocks(t))
	return r2StoreBuilt{Dir: dir, ToppedUp: missing}
}

// r2StoreFiles lists every regular file of the kept set under dir, as relative
// slash paths, sorted.
func r2StoreFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, k := range r2StoreKept {
		root := filepath.Join(dir, filepath.FromSlash(k))
		fi, err := os.Stat(root)
		require.NoError(t, err, "the built store has no %s", k)
		if !fi.IsDir() {
			out = append(out, k)
			continue
		}
		require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() {
				rel, rerr := filepath.Rel(dir, p)
				if rerr != nil {
					return rerr
				}
				out = append(out, filepath.ToSlash(rel))
			}
			return nil
		}))
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// r2StoreLeak fails the test when any kept file names a path of this machine: a
// temporary directory, a home directory, or the build directory's parent
// spelled any other way than r2StoreBuildDir. It reads the files as bytes, so it
// sees a value in a table block only if the block is not compressed over it; the
// store is built under the fixed directory so that nothing needs hiding.
func r2StoreLeak(t *testing.T, dir string, files []string) {
	t.Helper()
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		require.NoError(t, err)
		for _, bad := range []string{"/var/folders/", "/Users/", "/home/", "/private/var/"} {
			require.NotContains(t, string(b), bad, "%s names a path of the machine that generated it", rel)
		}
	}
}
