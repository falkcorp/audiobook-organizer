// file: internal/books/firstboot_test.go
// version: 1.0.1
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
	"github.com/falkcorp/audiobook-organizer/internal/operations/state"
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
// on-disk format is frozen). extra, when set, writes more rows before the
// rewind.
func headStoreAt(t *testing.T, path string, extra func(*database.PebbleStore)) headSeed {
	t.Helper()
	return headStoreAtVersion(t, path, database.LegacyCap, extra)
}

// headStoreAtVersion is headStoreAt with books' schema at version (at most
// LegacyCap): the records above it deleted and db_version set to it.
func headStoreAtVersion(t *testing.T, path string, version int, extra func(*database.PebbleStore)) headSeed {
	t.Helper()
	require.LessOrEqual(t, version, database.LegacyCap)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	store, err := database.NewPebbleStore(path)
	require.NoError(t, err)
	book, err := store.CreateBook(&database.Book{Title: "Hush", FilePath: filepath.Join(filepath.Dir(path), "library", "Hush.m4b")})
	require.NoError(t, err)
	require.NoError(t, store.SetSetting("config_blob", flatEmbeddingBlob, "json", false))
	require.NoError(t, store.SetUserPreference("arkham_theme", "dark"))
	if extra != nil {
		extra(store)
	}
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
		if n > version {
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
	dv, err := json.Marshal(database.DatabaseVersion{Version: version, UpdatedAt: time.Now().UTC()})
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
	headStoreAt(t, path, nil)
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
	c.expectClaimViolations()
	err := c.store.DB().Set([]byte("arkham-unregistered:1"), []byte("x"), pebble.Sync)
	require.Error(t, err, "an out-of-claim write was allowed: the shadow dial is in force")
	assert.True(t, errors.Is(err, storage.ErrOutsideKeyspace), "out-of-claim write = %v, want ErrOutsideKeyspace", err)
}

// TestShadowModeBootsABO (R1-23 review S3): ABO boots with the emergency dial
// ABO_CORE_STORAGE_CLAIMS_MODE=shadow. An out-of-claim write through books'
// store is allowed and counted as shadow, its Warn is rate-limited (the 1st
// and 2nd of three accesses log, the 3rd does not), and a scan over the
// unclaimed family still returns nothing: shadow rescues point accesses,
// never scans, because iterators stay clipped to books' claims.
func TestShadowModeBootsABO(t *testing.T) {
	c := bootComposedWith(t, false, "", nil, nil, func(o *app.Options) {
		o.Env = []string{"ABO_CORE_STORAGE_CLAIMS_MODE=shadow"}
	})
	c.expectClaimViolations()
	db := c.store.DB()
	for i := 1; i <= 3; i++ {
		require.NoError(t, db.Set([]byte(fmt.Sprintf("arkham-unregistered:%d", i)), []byte("x"), pebble.Sync),
			"shadow refused an out-of-claim write")
	}
	v, closer, err := db.Get([]byte("arkham-unregistered:2"))
	require.NoError(t, err, "shadow refused an out-of-claim read")
	assert.Equal(t, "x", string(v))
	require.NoError(t, closer.Close())

	it, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte("arkham-unregistered:"), UpperBound: []byte("arkham-unregistered;")})
	require.NoError(t, err)
	assert.False(t, it.First(), "a scan reached an unclaimed family under shadow: iterators must stay clipped to the claims")
	require.NoError(t, it.Close())

	got := c.app.StorageAdmin().ClaimViolations()
	assert.Equal(t, uint64(3), got[storage.ClaimViolation{Owner: database.BooksOwner, Op: "write", Mode: "shadow"}])
	assert.Equal(t, uint64(1), got[storage.ClaimViolation{Owner: database.BooksOwner, Op: "read", Mode: "shadow"}])
	writes := 0
	for _, l := range strings.Split(c.newLog.String(), "\n") {
		if strings.Contains(l, "allowed by core.storage.claims.mode=shadow") && strings.Contains(l, "op=set") {
			writes++
		}
	}
	assert.Equal(t, 2, writes, "shadow Warns for 3 writes (want the 1st and 2nd only): %s", c.newLog.String())
}

// TestProjectionKeepsRecordIDOnABOsBoot (05-storage §5S.3.7): R1's first boot
// of a HEAD store leaves the legacy projection as HEAD reads it. db_version
// advances past books' R1 migrations keeping its row's ID, the
// storage_format stamp HEAD wrote is left as it was, no storage_migration
// marker is written, and the .storage-format sidecar says 1.
func TestProjectionKeepsRecordIDOnABOsBoot(t *testing.T) {
	path := filepath.Join(tempDir(t), "wayne.pebble")
	headStoreAt(t, path, nil)
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

// TestStampWrittenAfterTheCheckpoint (R1-23 review B2): a HEAD store without
// the storage_format stamp gets it at 1 from books' boot repairs, after the
// pre-migration checkpoint: the checkpoint holds no stamp and the store holds
// 1 in HEAD's row format. A present stamp is never rewritten
// (TestProjectionKeepsRecordIDOnABOsBoot).
func TestStampWrittenAfterTheCheckpoint(t *testing.T) {
	path := filepath.Join(tempDir(t), "blackgate.pebble")
	headStoreAt(t, path, nil)
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	require.NoError(t, db.Delete([]byte("preference:storage_format"), pebble.Sync))
	require.NoError(t, db.Close())

	edit, disk := onDisk(path)
	r := runComposedWith(t, edit, nil, disk, nil)
	require.NoError(t, r.stop())

	cp := filepath.Join(filepath.Dir(path), ".mediacore-checkpoints", checkpointName(t, path))
	_, inCheckpoint := rawKVs(t, filepath.Join(cp, "db"))["preference:storage_format"]
	assert.False(t, inCheckpoint, "the checkpoint holds a stamp: it was written before the checkpoint")
	raw, ok := rawKVs(t, path)["preference:storage_format"]
	require.True(t, ok, "the boot wrote no storage_format stamp")
	var pref database.UserPreference
	require.NoError(t, json.Unmarshal([]byte(raw), &pref))
	require.NotNil(t, pref.Value)
	assert.NotZero(t, pref.ID, "the stamp has no preference ID")
	var v database.DatabaseVersion
	require.NoError(t, json.Unmarshal([]byte(*pref.Value), &v))
	assert.Equal(t, 1, v.Version)
}

// TestFirstBootRefusesForeignKeys on ABO's boot (05-storage §5S.10): a HEAD
// store that already holds keys under c/ or m/ but no install marker (a
// store some other build wrote into) halts books' first R1 boot with
// ErrForeignKeys before the server exists, and the boot writes nothing: not
// a row, not a checkpoint.
func TestFirstBootRefusesForeignKeys(t *testing.T) {
	path := filepath.Join(tempDir(t), "arkham.pebble")
	headStoreAt(t, path, nil)
	db, err := pebble.Open(path, &pebble.Options{FormatMajorVersion: database.PebbleFormatMajorVersion})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("c/rogue"), []byte("x"), pebble.Sync))
	require.NoError(t, db.Set([]byte("m/scarecrow/toxin"), []byte("y"), pebble.Sync))
	require.NoError(t, db.Close())
	before := rawKVs(t, path)

	edit, disk := onDisk(path)
	mod, opts, _ := composedOptions(t, false, "", edit, nil)
	disk(&opts)
	err = bootFails(t, mod, opts)
	assert.ErrorIs(t, err, storage.ErrForeignKeys)
	assert.Equal(t, before, rawKVs(t, path), "a refused first boot wrote")
	_, err = os.Stat(database.StorageMigrationCheckpointPath(path))
	assert.True(t, os.IsNotExist(err), "a refused first boot wrote a checkpoint pointer: %v", err)
}

// TestR1BootRepairsOpsAndEmails (05-storage §5S.10, §5S.9.3; COR5-7,
// XFILE-10): on books' first R1 boot of a HEAD store holding an opv3 record
// an older binary left stale and a user row it wrote without an email
// source, books' repairs start hook repairs both before books serves: app.New
// (books' constructor) leaves them as they were, and when Run opens the
// listener the record matches its opv2 row and the row is stamped.
func TestR1BootRepairsOpsAndEmails(t *testing.T) {
	path := filepath.Join(tempDir(t), "blackgate.pebble")
	const runID = "01JBATCAVE0000000000000001"
	var userID string
	headStoreAt(t, path, func(s *database.PebbleStore) {
		now := time.Now().UTC().Truncate(time.Second)
		require.NoError(t, s.InsertOperationV2(database.OperationV2Row{ID: runID, DefID: "test.rollback", Plugin: "test", Status: "queued", QueuedAt: now, Params: `{}`}))
		// An older binary ran it to completed with no opv3 write.
		row, err := s.GetOperationV2(runID)
		require.NoError(t, err)
		done := now.Add(time.Minute)
		row.Status, row.StartedAt, row.CompletedAt = "completed", &now, &done
		raw, err := json.Marshal(row)
		require.NoError(t, err)
		require.NoError(t, s.DB().Set([]byte("opv2:op:"+runID), raw, pebble.Sync))

		// And rewrote a user row, dropping the email source it does not know.
		u, err := s.CreateUser("nightwing", "nightwing@example.invalid", "argon2id", "x", []string{"viewer"}, "active")
		require.NoError(t, err)
		userID = u.ID
		key := []byte("u:" + u.ID)
		cur, closer, err := s.DB().Get(key)
		require.NoError(t, err)
		var fields map[string]any
		require.NoError(t, json.Unmarshal(cur, &fields))
		require.NoError(t, closer.Close())
		delete(fields, "email_source")
		raw, err = json.Marshal(fields)
		require.NoError(t, err)
		require.NoError(t, s.DB().Set(key, raw, pebble.Sync))
	})

	stale := func(c *composed) {
		rec, err := c.store.GetRunV3(runID)
		require.NoError(t, err)
		require.NotNil(t, rec)
		assert.NotEqual(t, state.V3Succeeded, rec.State, "the opv3 record was repaired before the start hooks")
		u, err := c.store.GetUserByID(userID)
		require.NoError(t, err)
		assert.Empty(t, u.EmailSource, "the email row was stamped before the start hooks")
	}
	edit, disk := onDisk(path)
	r := runComposedWith(t, edit, nil, disk, stale)

	rec, err := r.store.GetRunV3(runID)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, state.V3Succeeded, rec.State, "books' start hook left the opv3 record stale")
	u, err := r.store.GetUserByID(userID)
	require.NoError(t, err)
	assert.NotEmpty(t, u.EmailSource, "books' start hook left the email row unstamped")
	require.NoError(t, r.stop())
}
