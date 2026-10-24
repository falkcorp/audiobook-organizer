// file: internal/database/master_list_store_test.go
// version: 1.3.0
// guid: d7f0b394-f709-439a-b110-47286050c881
// last-edited: 2026-10-24

package database

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openTestMasterListStore(t *testing.T) (*MasterListStore, *pebble.DB) {
	t.Helper()
	db, err := pebble.Open("mlist.pebble", &pebble.Options{FS: vfs.NewMem()})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return NewMasterListStore(NewGatedDB(db)), db
}

func TestMasterListStore_RoundTripKeepsInputOrder(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	// More rows than one write batch, so chunking is exercised; the padded row
	// number keeps the prefix scan in input order past 9, 99, 999 rows.
	n := masterListChunk*2 + 37
	rows := make([][]byte, n)
	for i := range rows {
		rows[i] = fmt.Appendf(nil, `{"line":%d}`, i+1)
	}
	meta, err := st.PutList(MasterListMeta{Filename: "list.csv", Format: "csv", UploadedBy: "u1", Bytes: 99, SHA256: "ab"}, rows)
	require.NoError(t, err)
	assert.NotEmpty(t, meta.ID)
	assert.Equal(t, n, meta.RowCount)
	assert.False(t, meta.UploadedAt.IsZero())

	got, err := st.GetList(meta.ID)
	require.NoError(t, err)
	assert.Equal(t, meta, *got)

	next := 0
	require.NoError(t, st.ScanRows(meta.ID, func(i int, raw []byte) error {
		next++
		assert.Equal(t, next, i)
		assert.Equal(t, fmt.Sprintf(`{"line":%d}`, i), string(raw))
		return nil
	}))
	assert.Equal(t, n, next)
}

func TestMasterListStore_ListNewestFirstAndNotFound(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { clock = clock.Add(time.Hour); return clock }
	a, err := st.PutList(MasterListMeta{Format: "json"}, [][]byte{[]byte(`{}`)})
	require.NoError(t, err)
	b, err := st.PutList(MasterListMeta{Format: "csv"}, [][]byte{[]byte(`{}`), []byte(`{}`)})
	require.NoError(t, err)
	all, err := st.ListLists()
	require.NoError(t, err)
	require.Len(t, all, 2)
	assert.Equal(t, b.ID, all[0].ID)
	assert.Equal(t, a.ID, all[1].ID)

	_, err = st.GetList("nope")
	assert.ErrorIs(t, err, ErrMasterListNotFound)
	assert.ErrorIs(t, st.ScanRows("nope", func(int, []byte) error { return nil }), ErrMasterListNotFound)

	_, err = st.PutList(MasterListMeta{}, nil)
	assert.Error(t, err)
}

func TestMasterListStore_ScanRowsFailsOnMissingRow(t *testing.T) {
	st, db := openTestMasterListStore(t)
	meta, err := st.PutList(MasterListMeta{Format: "csv"}, [][]byte{[]byte(`{"line":1}`), []byte(`{"line":2}`)})
	require.NoError(t, err)
	require.NoError(t, db.Delete(masterListRowKey(meta.ID, 2), pebble.Sync))
	err = st.ScanRows(meta.ID, func(int, []byte) error { return nil })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stored 1 rows but the list says 2")
}

func TestMasterListStore_CallbackErrorStopsScan(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	meta, err := st.PutList(MasterListMeta{Format: "csv"}, [][]byte{[]byte(`1`), []byte(`2`), []byte(`3`)})
	require.NoError(t, err)
	calls := 0
	err = st.ScanRows(meta.ID, func(int, []byte) error { calls++; return fmt.Errorf("stop") })
	assert.EqualError(t, err, "stop")
	assert.Equal(t, 1, calls)
}

func TestMasterListStore_KeyFamiliesRegistered(t *testing.T) {
	have := map[string]string{}
	for _, f := range KeyFamilies() {
		have[f.Prefix] = f.Owner
	}
	for _, p := range []string{masterListPrefix, masterListRowPrefix} {
		owner, ok := have[p]
		require.True(t, ok, "%s is not registered in keyfamilies.go", p)
		assert.True(t, strings.HasSuffix(owner, "master_list_store.go"))
	}
	owner, ok := have[masterListElsewherePrefix]
	require.True(t, ok, "%s is not registered in keyfamilies.go", masterListElsewherePrefix)
	assert.True(t, strings.HasSuffix(owner, "master_list_elsewhere.go"))
}

// putTestList stores a one-row list for uploader and returns its id.
func putTestList(t *testing.T, st *MasterListStore, uploader string, rows int) string {
	t.Helper()
	raw := make([][]byte, rows)
	for i := range raw {
		raw[i] = fmt.Appendf(nil, `{"line":%d}`, i+1)
	}
	meta, err := st.PutList(MasterListMeta{Format: "csv", UploadedBy: uploader}, raw)
	require.NoError(t, err)
	return meta.ID
}

func countKeys(t *testing.T, db *pebble.DB, prefix string) int {
	t.Helper()
	iter, err := db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: prefixUpperBound([]byte(prefix))})
	require.NoError(t, err)
	defer iter.Close()
	n := 0
	for iter.First(); iter.Valid(); iter.Next() {
		n++
	}
	return n
}

func TestMasterListStore_DeleteListRemovesMetaAndRowsOnly(t *testing.T) {
	st, db := openTestMasterListStore(t)
	keep := putTestList(t, st, "u1", 3)
	gone := putTestList(t, st, "u1", 4)
	require.NoError(t, st.CreateElsewhere(MasterListElsewhere{ID: "e1", UserID: "u1", ListID: gone}))

	require.NoError(t, st.DeleteList(gone))

	_, err := st.GetList(gone)
	assert.ErrorIs(t, err, ErrMasterListNotFound)
	assert.ErrorIs(t, st.ScanRows(gone, func(int, []byte) error { return nil }), ErrMasterListNotFound)
	assert.Equal(t, 3, countKeys(t, db, masterListRowPrefix), "only the kept list's rows remain")
	require.NoError(t, st.ScanRows(keep, func(int, []byte) error { return nil }))
	// A record made from the list outlives it: it is the owner's data, not the upload's.
	_, err = st.GetElsewhere("e1")
	assert.NoError(t, err)

	assert.ErrorIs(t, st.DeleteList(gone), ErrMasterListNotFound, "second delete")
	assert.ErrorIs(t, st.DeleteList("nope"), ErrMasterListNotFound)
}

// A list whose metadata does not decode can still be deleted, rows and all.
func TestMasterListStore_DeleteListRemovesAListWhoseMetadataDoesNotDecode(t *testing.T) {
	st, db := openTestMasterListStore(t)
	keep := putTestList(t, st, "u1", 2)
	bad := putTestList(t, st, "u1", 3)
	require.NoError(t, db.Set([]byte(masterListPrefix+bad), []byte("{not json"), pebble.Sync))
	_, err := st.GetList(bad)
	require.Error(t, err, "the fixture is a list GetList cannot decode")
	require.NotErrorIs(t, err, ErrMasterListNotFound)

	require.NoError(t, st.DeleteList(bad))

	assert.Equal(t, 2, countKeys(t, db, masterListRowPrefix), "only the kept list's rows remain")
	_, closer, gerr := db.Get([]byte(masterListPrefix + bad))
	assert.ErrorIs(t, gerr, pebble.ErrNotFound)
	if closer != nil {
		_ = closer.Close()
	}
	require.NoError(t, st.ScanRows(keep, func(int, []byte) error { return nil }))
	assert.ErrorIs(t, st.DeleteList(bad), ErrMasterListNotFound, "second delete")
}

func TestMasterListStore_DeleteListMetadataFirstLeavesNoVisibleBrokenList(t *testing.T) {
	st, db := openTestMasterListStore(t)
	id := putTestList(t, st, "u1", 2)
	// What a crash between the two deletes leaves: rows with no metadata.
	require.NoError(t, db.Delete([]byte(masterListPrefix+id), pebble.Sync))
	all, err := st.ListLists()
	require.NoError(t, err)
	assert.Empty(t, all)
	assert.ErrorIs(t, st.ScanRows(id, func(int, []byte) error { return nil }), ErrMasterListNotFound)
}

func TestMasterListStore_PruneListsKeepsNewestPerUploader(t *testing.T) {
	st, db := openTestMasterListStore(t)
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { clock = clock.Add(time.Hour); return clock }

	a1 := putTestList(t, st, "alice", 1)
	b1 := putTestList(t, st, "bob", 1)
	a2 := putTestList(t, st, "alice", 1)
	n1 := putTestList(t, st, "", 1) // no recorded uploader: its own group
	a3 := putTestList(t, st, "alice", 1)
	a4 := putTestList(t, st, "alice", 1)

	deleted, err := st.PruneLists("alice", 2, "")
	require.NoError(t, err)
	assert.Equal(t, []string{a1, a2}, deleted, "oldest first")

	all, err := st.ListLists()
	require.NoError(t, err)
	var ids []string
	for _, m := range all {
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []string{a4, a3, n1, b1}, ids)
	assert.Equal(t, 4, countKeys(t, db, masterListRowPrefix))

	// Nothing over the limit: nothing deleted.
	deleted, err = st.PruneLists("alice", 2, "")
	require.NoError(t, err)
	assert.Empty(t, deleted)

	// The no-uploader group prunes on its own and never touches the others.
	n2 := putTestList(t, st, "", 1)
	deleted, err = st.PruneLists("", 1, "")
	require.NoError(t, err)
	assert.Equal(t, []string{n1}, deleted)
	_, err = st.GetList(n2)
	assert.NoError(t, err)
	_, err = st.GetList(b1)
	assert.NoError(t, err)
}

func TestMasterListStore_PruneListsRefusesKeepBelowOne(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	id := putTestList(t, st, "u1", 1)
	for _, keep := range []int{0, -1} {
		deleted, err := st.PruneLists("u1", keep, id)
		require.Error(t, err)
		assert.Empty(t, deleted)
	}
	_, err := st.GetList(id)
	assert.NoError(t, err, "a bad setting must not delete the list just stored")
}

// The list just stored is exempt from the retention however its UploadedAt
// compares: a clock that stepped back makes it the "oldest", and it must still
// be kept while an older-looking-newer list goes.
func TestMasterListStore_PruneListsKeepsTheProtectedListWhateverItsTime(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	st.now = func() time.Time { return clock }
	old := putTestList(t, st, "alice", 1)
	clock = clock.Add(-48 * time.Hour) // the clock steps back before the next upload
	fresh := putTestList(t, st, "alice", 1)

	deleted, err := st.PruneLists("alice", 1, fresh)
	require.NoError(t, err)
	assert.Equal(t, []string{old}, deleted)
	_, err = st.GetList(fresh)
	require.NoError(t, err, "the list just stored survives")
	_, err = st.GetList(old)
	assert.ErrorIs(t, err, ErrMasterListNotFound)

	// An unknown id protects nothing and costs nothing.
	deleted, err = st.PruneLists("alice", 1, "no-such-list")
	require.NoError(t, err)
	assert.Empty(t, deleted)
}
