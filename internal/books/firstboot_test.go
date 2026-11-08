// file: internal/books/firstboot_test.go
// version: 1.0.0
// guid: 2f7c9e14-8b3a-4d61-a5e2-0c6d1b9f4a73
// last-edited: 2026-11-08

package books

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
)

// Release R1's first boot of a production store, through ABO's own boot:
// books.Module and books.Options over app.New, on disk, as serve runs it.
// The store is one HEAD (R0) left at LegacyCap with library rows in it.

// headSeed is what headStoreAt writes through books' store before it turns
// the store back into one HEAD left: a book, a setting and a preference.
type headSeed struct {
	bookID string
}

// headStoreAt writes a store at path as HEAD (R0) leaves a production store:
// library rows, a config_blob in a shape the settings load migrates, books'
// schema at LegacyCap, and no core key, sidecar or checkpoint. It writes the
// rows with this build's standalone opener and then removes what a core
// build adds, as internal/database's rewindToR0 does for that package's
// tests (that helper is unexported and its package cannot be imported by a
// test helper there without a cycle, so the R0 keys are named here: HEAD's
// on-disk format is frozen).
func headStoreAt(t *testing.T, path string) headSeed {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	store, err := database.NewPebbleStore(path)
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "Hush", FilePath: filepath.Join(filepath.Dir(path), "library", "Hush.m4b")})
	require.NoError(t, err)
	require.NoError(t, store.SetSetting("config_blob", flatEmbeddingBlob, "json", false))
	require.NoError(t, store.SetUserPreference("arkham_theme", "dark"))
	require.NoError(t, store.Close())

	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	b := db.NewBatch()
	require.NoError(t, b.DeleteRange([]byte("c/"), []byte("c0"), nil))
	require.NoError(t, b.DeleteRange([]byte("m/"), []byte("m0"), nil))
	const recordPrefix = "preference:migration_"
	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte(recordPrefix), UpperBound: []byte("preference:migration`")})
	require.NoError(t, err)
	for ok := it.First(); ok; ok = it.Next() {
		n, err := strconv.Atoi(strings.TrimPrefix(string(it.Key()), recordPrefix))
		require.NoError(t, err, "record key %q", it.Key())
		if n > database.LegacyCap {
			require.NoError(t, b.Delete(append([]byte(nil), it.Key()...), nil))
		}
	}
	require.NoError(t, it.Close())
	key := []byte("preference:db_version")
	raw, closer, err := db.Get(key)
	require.NoError(t, err, "a store a core build opened has a db_version row")
	var pref database.UserPreference
	require.NoError(t, json.Unmarshal(raw, &pref))
	require.NoError(t, closer.Close())
	dv, err := json.Marshal(database.DatabaseVersion{Version: database.LegacyCap, UpdatedAt: time.Now().UTC()})
	require.NoError(t, err)
	v := string(dv)
	pref.Value = &v
	row, err := json.Marshal(pref)
	require.NoError(t, err)
	require.NoError(t, b.Set(key, row, nil))
	require.NoError(t, b.Commit(pebble.Sync))
	require.NoError(t, db.Close())

	for _, p := range []string{path + ".storage-levels", database.StorageMigrationCheckpointPath(path), filepath.Join(filepath.Dir(path), ".mediacore-checkpoints")} {
		require.NoError(t, os.RemoveAll(p))
	}
	require.Empty(t, keysWithPrefix(rawKVs(t, path), "c/"), "the HEAD store still has core keys")
	return headSeed{bookID: book.ID}
}

// rawKVs reads every key and value of the closed store at path, read-only.
func rawKVs(t *testing.T, path string) map[string]string {
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

func keysWithPrefix(kvs map[string]string, prefix string) []string {
	var out []string
	for k := range kvs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// onDisk points the composition at the store at path, on the real
// filesystem.
func onDisk(path string) (func(*config.Config), func(*app.Options)) {
	return func(c *config.Config) {
			c.DatabasePath = path
			c.RootDir = filepath.Dir(path)
		}, func(o *app.Options) {
			o.StorageFS = storage.FS{}
		}
}

// checkpointName reads the checkpoint pointer core writes beside the store.
func checkpointName(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(database.StorageMigrationCheckpointPath(path))
	require.NoError(t, err, "the boot wrote no checkpoint pointer")
	var p struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(raw, &p))
	require.NotEmpty(t, p.Name)
	return p.Name
}

// TestCheckpointPrecedesEveryWrite on ABO's boot (05-storage §5S.10, §5S.4):
// release R1's first boot of a HEAD store takes its checkpoint before any
// write. The copy holds exactly the rows HEAD left, byte for byte, although
// the boot then wrote core's rows, books' R1 migrations, the settings
// load's writes and the boot repairs; and it holds the state files serve
// names (the encryption key).
func TestCheckpointPrecedesEveryWrite(t *testing.T) {
	path := filepath.Join(tempDir(t), "gotham.pebble")
	headStoreAt(t, path)
	before := rawKVs(t, path)

	edit, disk := onDisk(path)
	r := runComposedWith(t, edit, nil, func(o *app.Options) {
		disk(o)
		loadConfigFromDB = config.LoadConfigFromDatabase
	}, nil)
	require.NoError(t, r.stop())

	name := checkpointName(t, path)
	cp := filepath.Join(filepath.Dir(path), ".mediacore-checkpoints", name)
	copied := rawKVs(t, filepath.Join(cp, "db"))
	assert.Equal(t, before, copied, "the checkpoint is not the store HEAD left")

	after := rawKVs(t, path)
	assert.NotEmpty(t, keysWithPrefix(after, "c/"), "the boot wrote no core row: the test proves nothing")
	assert.NotEqual(t, before["setting:config_blob"], after["setting:config_blob"], "the settings load's blob migration was not committed")

	var rec struct {
		StateFiles []struct {
			Label string `json:"label"`
			Path  string `json:"path"`
		} `json:"state_files"`
		Status string `json:"status"`
	}
	raw, err := os.ReadFile(cp + ".json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &rec))
	var labels []string
	for _, f := range rec.StateFiles {
		labels = append(labels, f.Label)
	}
	assert.Contains(t, labels, "encryption_key", "the checkpoint holds no encryption key")
	// Every step ran, so Migrate completed the attempt (an open one would be
	// marked served at Run's start step (0) instead).
	assert.Equal(t, "completed", rec.Status)
}

// The strict claims are the mode in force on ABO's boot (05-storage §5S.9.4
// step 2): a write through books' store outside its claims is refused, not
// logged and allowed as the shadow dial would.
func TestClaimsModeIsStrictOnABOsBoot(t *testing.T) {
	c := bootComposed(t, false, nil, nil)
	err := c.store.DB().Set([]byte("arkham-unregistered:1"), []byte("x"), pebble.Sync)
	require.Error(t, err, "an out-of-claim write was allowed: the shadow dial is in force")
	assert.True(t, errors.Is(err, storage.ErrOutsideKeyspace), "out-of-claim write = %v, want ErrOutsideKeyspace", err)
}

// TestProjectionKeepsRecordIDOnABOsBoot (05-storage §5S.3.7): R1's first boot
// of a HEAD store leaves the legacy projection as HEAD reads it. db_version
// advances past books' R1 migrations keeping its row's ID, no
// storage_format stamp or storage_migration marker is written, and the
// .storage-format sidecar says 1.
func TestProjectionKeepsRecordIDOnABOsBoot(t *testing.T) {
	path := filepath.Join(tempDir(t), "wayne.pebble")
	headStoreAt(t, path)
	before := rawKVs(t, path)
	var dvBefore database.UserPreference
	require.NoError(t, json.Unmarshal([]byte(before["preference:db_version"]), &dvBefore))

	edit, disk := onDisk(path)
	r := runComposedWith(t, edit, nil, disk, nil)
	require.NoError(t, r.stop())

	after := rawKVs(t, path)
	var dvAfter database.UserPreference
	require.NoError(t, json.Unmarshal([]byte(after["preference:db_version"]), &dvAfter))
	assert.Equal(t, dvBefore.ID, dvAfter.ID, "db_version took a new row ID")
	require.NotNil(t, dvAfter.Value)
	var version database.DatabaseVersion
	require.NoError(t, json.Unmarshal([]byte(*dvAfter.Value), &version))
	assert.Equal(t, database.LatestMigrationVersion(), version.Version)
	for _, k := range []string{"preference:storage_format", "preference:storage_migration"} {
		assert.Equal(t, before[k], after[k], "%s changed", k)
	}
	sidecar, err := os.ReadFile(database.StorageFormatSidecarPath(path))
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprint(database.SupportedStorageFormat), strings.TrimSpace(string(sidecar)))
	assert.Equal(t, 1, database.SupportedStorageFormat, "R1 raised the legacy projection")
}
