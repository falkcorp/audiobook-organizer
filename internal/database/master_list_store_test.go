// file: internal/database/master_list_store_test.go
// version: 1.0.0
// guid: d7f0b394-f709-439a-b110-47286050c881
// last-edited: 2026-10-23

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
}
