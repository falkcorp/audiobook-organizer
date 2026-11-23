// file: internal/books/firstboot_test.go
// version: 1.0.2
// guid: 2f7c9e14-8b3a-4d61-a5e2-0c6d1b9f4a73
// last-edited: 2026-11-09

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

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
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

// flatEmbeddingBlob is a config_blob in the shape before the nested
// embedding object, as an older HEAD left it.
const flatEmbeddingBlob = `{"embedding_enabled":true,"embedding_model":"batcomputer"}`

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
		o.Env = bootEnv("ABO_CORE_STORAGE_CLAIMS_MODE=shadow")
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
	for l := range strings.SplitSeq(c.newLog.String(), "\n") {
		if strings.Contains(l, "allowed by core.storage.claims.mode=shadow") && strings.Contains(l, "op=set") {
			writes++
		}
	}
	assert.Equal(t, 2, writes, "shadow Warns for 3 writes (want the 1st and 2nd only): %s", c.newLog.String())
}

// TestFirstBootRefusesForeignKeys on ABO's boot (05-storage §5S.10): a store
// that already holds keys under c/ or m/ but no install marker (a store some
// other build wrote into) halts books' boot with ErrForeignKeys before the
// server exists, and the boot writes nothing: not a row, not a checkpoint.
// The store is the frozen one R2 leaves (testdata/r2store) with its marker
// deleted, so the refusal is the one a store past the upgrade floor reaches.
func TestFirstBootRefusesForeignKeys(t *testing.T) {
	work := copyR2Store(t)
	path := r2StoreDB(work)
	r2StoreWithForeignKeys(t, path, map[string]string{"c/rogue": "x", "m/scarecrow/toxin": "y"})
	before := rawKVs(t, path)

	edit, boot := r2StoreBootConfig(t, work)
	mod, opts, _ := composedOptions(t, false, r2StoreState(work), edit, nil)
	boot(&opts)
	err := bootFails(t, mod, opts)
	assert.ErrorIs(t, err, storage.ErrForeignKeys)
	assert.Equal(t, before, rawKVs(t, path), "a refused first boot wrote")
	_, err = os.Stat(database.StorageMigrationCheckpointPath(path))
	assert.True(t, os.IsNotExist(err), "a refused first boot wrote a checkpoint pointer: %v", err)
}

// TestR1BootRepairsOpsAndEmails (05-storage §5S.10, §5S.9.3; COR5-7,
// XFILE-10): on books' boot of a store holding an opv3 record an older binary
// left stale and a user row it wrote without an email source, books' repairs
// start hook repairs both before books serves: app.New (books' constructor)
// leaves them as they were, and when Run opens the listener the record matches
// its opv2 row and the row is stamped. The store is the frozen one R2 leaves
// (testdata/r2store), which the rows are written into.
func TestR1BootRepairsOpsAndEmails(t *testing.T) {
	work := copyR2Store(t)
	const runID = "01JBATCAVE0000000000000001"
	var userID string
	r2StoreSeed(t, work, func(s *database.PebbleStore) {
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
	edit, boot := r2StoreBootConfig(t, work)
	r := runComposedIn(t, r2StoreState(work), edit, nil, boot, stale)

	rec, err := r.store.GetRunV3(runID)
	require.NoError(t, err)
	require.NotNil(t, rec)
	assert.Equal(t, state.V3Succeeded, rec.State, "books' start hook left the opv3 record stale")
	u, err := r.store.GetUserByID(userID)
	require.NoError(t, err)
	assert.NotEmpty(t, u.EmailSource, "books' start hook left the email row unstamped")
	require.NoError(t, r.stop())
}
