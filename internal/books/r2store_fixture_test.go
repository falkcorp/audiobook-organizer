// file: internal/books/r2store_fixture_test.go
// version: 0.1.0
// guid: 91c3e5a0-27d8-4f6b-b1a9-6e04d7c2853f
// last-edited: 2026-11-22

package books

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
)

// testdata/r2store is the store the R2 release leaves behind, frozen as files
// (R3-02f). Tests that need "a store this build accepts past the R3 upgrade
// floor" copy it with copyR2Store instead of booting the R0 corpus through the
// cutover: the cutover code is deleted in R3-02e, after which nothing can build
// an R2 store from the corpus. This file reads the fixture and imports nothing
// of the cutover; r2store_gen_test.go is what writes it.
//
// The directory holds a store as serve lays it out, db/ beside state/: the
// Pebble store and its sidecars, the search index's marker files, the key
// the store's sealed values need and the keyring. It holds no library or import
// files, no logs and no checkpoints.

const (
	// r2StoreDirRel and r2StoreManifestRel are the fixture and its manifest,
	// relative to this package.
	r2StoreDirRel      = "testdata/r2store"
	r2StoreManifestRel = "testdata/r2store.json"
)

// r2StoreManifest is testdata/r2store.json, which the generator writes beside
// the fixture.
type r2StoreManifest struct {
	// Command regenerates the fixture.
	Command string `json:"command"`
	// BuildDir is the synthetic directory the generator built under: the paths
	// the store holds name it.
	BuildDir string `json:"build_dir"`
	// BooksVersion is the applied books version the generator found.
	BooksVersion int `json:"books_version"`
	// ToppedUp are the floor-required steps the R2 build did not record at
	// generation time, stood in with a fresh install's done record. Empty when
	// M3 to M6 are live.
	ToppedUp []string `json:"topped_up"`
	// Files lists each fixture file, by slash path under r2store/, with its sha256,
	// sorted by path. It is a list of path/sha256 pairs and not a path-to-hash
	// map because a map would put a ".encryption_key" JSON key beside a 64-hex
	// value, which a secret scanner reads as an API key: it is the checksum of
	// the fixture's synthetic key file, nothing a reader could use.
	Files []r2StoreFileSum `json:"files"`
}

// r2StoreFileSum is one entry of r2StoreManifest.Files.
type r2StoreFileSum struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// byPath is the manifest's file sums keyed by path.
func (m r2StoreManifest) byPath() map[string]string {
	out := make(map[string]string, len(m.Files))
	for _, f := range m.Files {
		out[f.Path] = f.SHA256
	}
	return out
}

func readR2StoreManifest(t *testing.T) r2StoreManifest {
	t.Helper()
	raw, err := os.ReadFile(filepath.FromSlash(r2StoreManifestRel))
	require.NoError(t, err, "testdata/r2store.json is missing: regenerate the fixture with %s", r2storeRegenCommand)
	var m r2StoreManifest
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

// r2storeRegenCommand is the one command that rewrites the fixture.
const r2storeRegenCommand = "go test -count=1 -tags regen_r2fixture -run TestRegenerateR2Fixture ./internal/books/"

// r2StoreSteps are the data steps a store must have done to pass books' R3
// floor: M2 to M6, copies of internal/books/cutover's stepids.go. The IDs are
// recorded in every cutover store, so they never change.
var r2StoreSteps = []string{
	"books.config-cutover",
	"books.identity-cutover",
	"books.tasks-cutover",
	"books.log-cutover",
	"books.integrations-cutover",
}

// r2StoreStepKey is the row a data step's state record lives in: core
// storage's keyspace, then the step's key.
func r2StoreStepKey(id string) string {
	return "c/storage/" + string(storage.DataMigrationKey(id))
}

// r2StoreRows reads every key and value of the closed store at path, read-only.
func r2StoreRows(t *testing.T, path string) map[string]string {
	t.Helper()
	db, err := pebble.Open(path, &pebble.Options{ReadOnly: true})
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	it, err := db.NewIter(nil)
	require.NoError(t, err)
	out := map[string]string{}
	for ok := it.First(); ok; ok = it.Next() {
		out[string(it.Key())] = string(it.Value())
	}
	require.NoError(t, it.Error())
	require.NoError(t, it.Close())
	return out
}

// r2StoreDone reports which of ids have a done record in the closed store.
func r2StoreDone(t *testing.T, path string, ids ...string) map[string]bool {
	t.Helper()
	kv := r2StoreRows(t, path)
	out := map[string]bool{}
	for _, id := range ids {
		raw, ok := kv[r2StoreStepKey(id)]
		out[id] = ok && strings.Contains(raw, `"state":"done"`)
	}
	return out
}

// r2StoreSum is the lowercase hex sha256 of a file.
func r2StoreSum(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// copyR2Store copies the frozen R2 store into a new temporary directory and
// returns it: db/audiobooks.pebble (see r2StoreDB) beside state/. The store is
// real copies of the committed files, so a test may boot it, write to it and
// leave it.
func copyR2Store(t *testing.T) string {
	t.Helper()
	dst := t.TempDir()
	require.NoError(t, copyR2StoreTree(filepath.FromSlash(r2StoreDirRel), dst))
	// A checkout gives every file 0644; core refuses a keyring or key file that
	// others can read, so the copy takes the modes a serving install has.
	state := r2StoreState(dst)
	require.NoError(t, os.Chmod(state, 0o700))
	for _, name := range []string{".encryption_key", "secrets.keyring"} {
		require.NoError(t, os.Chmod(filepath.Join(state, name), 0o600))
	}
	return dst
}

// r2StoreDB is the Pebble store's path in a copy.
func r2StoreDB(dir string) string { return filepath.Join(dir, "db", "audiobooks.pebble") }

// r2StoreState is the state directory of a copy.
func r2StoreState(dir string) string { return filepath.Join(dir, "state") }

// copyR2StoreTree copies the regular files and directories of src into dst.
func copyR2StoreTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type().IsRegular():
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, in); err != nil {
				out.Close()
				return err
			}
			return out.Close()
		}
		return nil
	})
}

// r2StoreInstallKey is the row core storage keeps its install marker in.
const r2StoreInstallKey = "c/storage/install\x00\x01"

// r2StoreWithForeignKeys turns the closed store at path into one holding keys
// under c/ and m/ and no install marker, which every boot refuses with
// storage.ErrForeignKeys: the fixture's marker deleted, and rows another build
// might have written added.
func r2StoreWithForeignKeys(t *testing.T, path string, rogue map[string]string) {
	t.Helper()
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	_, closer, err := db.Get([]byte(r2StoreInstallKey))
	require.NoError(t, err, "the fixture holds no install marker at %q", r2StoreInstallKey)
	require.NoError(t, closer.Close())
	require.NoError(t, db.Delete([]byte(r2StoreInstallKey), pebble.Sync))
	for k, v := range rogue {
		require.NoError(t, db.Set([]byte(k), []byte(v), pebble.Sync))
	}
	require.NoError(t, db.Close())
}

// r2StoreSeed runs seed over the closed store of a copy at work, opened with
// this build's standalone options.
func r2StoreSeed(t *testing.T, work string, seed func(*database.PebbleStore)) {
	t.Helper()
	st, err := database.OpenStandalone(r2StoreDB(work), StandaloneOptions(database.StandaloneOptions{}))
	require.NoError(t, err)
	seed(st)
	require.NoError(t, st.Close())
}

// r2StoreRun boots the composition on a copy at work, waits for ready, calls
// atReady (when set) with the booted composition, and stops it cleanly. Run, not
// only boot: the repairs hook runs at start. The environment is the process's,
// so a test sets an operator layer with t.Setenv before calling it.
func r2StoreRun(t *testing.T, work string, atReady func(*composed)) {
	t.Helper()
	edit, boot := r2StoreBootConfig(t, work)
	r := runComposedIn(t, r2StoreState(work), edit, nil, boot, nil)
	deadline := time.Now().Add(2 * time.Minute)
	for {
		ready, err := shapedReady(r.composed)
		require.NoError(t, err)
		if ready {
			break
		}
		require.True(t, time.Now().Before(deadline), "/health never reported ready")
		time.Sleep(50 * time.Millisecond)
	}
	if atReady != nil {
		// Deleted by the config copy-on-write lane; see quiesceBackgroundForFlipPUT.
		quiesceBackgroundForFlipPUT(t, r.composed)
		atReady(r.composed)
	}
	require.NoError(t, r.stop())
}

// The embedding width and model the fixture's own settings give a boot (the
// defaults the R0 corpus carried through M2). A clean-stop marker planted for a
// boot of the fixture must record them, or the boot reconciles the snapshot.
const (
	r2StoreEmbeddingDims  = 3072
	r2StoreEmbeddingModel = "text-embedding-3-large"
)

// r2StoreBootConfig returns the config edit and options that boot the
// composition on a copy at work as serve boots a store R2 left: on disk, the
// state directory beside it, the copy's own key, and the ABS settings the R0
// binary that wrote the corpus served (the stored settings say so, and a boot
// that names it differently is refused). The directories the stored path
// settings name belong to the generator's synthetic build directory, so the
// environment layer, which outranks the store, points them at the copy; the
// store itself is not edited.
func r2StoreBootConfig(t *testing.T, work string) (func(*config.Config), func(*app.Options)) {
	t.Helper()
	stateDir := r2StoreState(work)
	key, err := os.ReadFile(database.EncryptionKeyPath(stateDir))
	require.NoError(t, err, "the fixture holds no settings key")
	library := filepath.Join(work, "library")
	playlists := filepath.Join(work, "playlists")
	dumps := filepath.Join(library, "openlibrary-dumps")
	t.Setenv("ABO_BOOKS_PLAYLIST_DIR", playlists)
	t.Setenv("ABO_BOOKS_OPENLIBRARY_DUMP_DIR", dumps)
	edit := func(c *config.Config) {
		c.DatabasePath = r2StoreDB(work)
		c.RootDir = library
		c.PlaylistDir = playlists
		c.OpenLibraryDumpDir = dumps
		c.ABSAPIEnabled = true
		c.ABSAuthModes = "jwt"
		c.ABSJWTSecret = "synthetic-r2-store-secret-0123456789abcdef"
	}
	opt := func(o *app.Options) {
		o.StorageFS = storage.FS{}
		// sandboxStateDirAt wrote a fixed key; the store's secrets are sealed
		// under the fixture's own.
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(stateDir), key, 0o600))
	}
	return edit, opt
}

// TestR2StoreFixtureIsIntact: the committed files are the ones the generator
// wrote. A file edited by hand, one the generator did not write, or one missing
// fails here, naming the file; regenerate with r2storeRegenCommand.
func TestR2StoreFixtureIsIntact(t *testing.T) {
	m := readR2StoreManifest(t)
	require.NotEmpty(t, m.Files, "the manifest names no file")
	onDisk := map[string]string{}
	root := filepath.FromSlash(r2StoreDirRel)
	require.NoError(t, filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		onDisk[filepath.ToSlash(rel)] = r2StoreSum(t, p)
		return nil
	}))
	var problems []string
	listed := m.byPath()
	for rel, want := range listed {
		got, ok := onDisk[rel]
		switch {
		case !ok:
			problems = append(problems, "missing "+rel)
		case got != want:
			problems = append(problems, "changed "+rel)
		}
	}
	for rel := range onDisk {
		if _, ok := listed[rel]; !ok {
			problems = append(problems, "unlisted "+rel)
		}
	}
	sort.Strings(problems)
	assert.Empty(t, problems, "the fixture is not what the generator wrote; regenerate it: %s", r2storeRegenCommand)
}

// TestR2StoreFixtureIsAnR2Store: the fixture is a store that passes books'
// floor. Books' applied version is at least LegacyCap, every step a floor
// would require is done, and the level sidecar the R2 build wrote is there. The
// done records are read from the store, never from the manifest.
func TestR2StoreFixtureIsAnR2Store(t *testing.T) {
	m := readR2StoreManifest(t)
	dir := copyR2Store(t)
	assert.GreaterOrEqual(t, m.BooksVersion, database.LegacyCap)
	done := r2StoreDone(t, r2StoreDB(dir), r2StoreSteps...)
	for _, id := range r2StoreSteps {
		assert.True(t, done[id], "%s is not done in the fixture", id)
	}
	for _, id := range m.ToppedUp {
		assert.Contains(t, r2StoreSteps, id, "the manifest tops up a step no floor requires")
	}
	_, err := os.Stat(r2StoreDB(dir) + ".storage-levels")
	assert.NoError(t, err, "the fixture has no level sidecar")
}
