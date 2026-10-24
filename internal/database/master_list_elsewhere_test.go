// file: internal/database/master_list_elsewhere_test.go
// version: 1.0.0
// guid: 0e8c41d7-6a52-4b93-8f1d-72a9c0b5e3d4
// last-edited: 2026-10-23

package database

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testElsewhere(id, user string) MasterListElsewhere {
	return MasterListElsewhere{
		ID: id, UserID: user, ListID: "list-1", UploadedBy: "admin", Lines: []int{4, 9},
		Title: "A Test Title", Authors: []string{"Test Author"}, ISBN13: "9780000000002", ASIN: "B000000000",
		Sources: []MasterListElsewhereSource{{Source: "itunes", SourceID: "PID1"}},
		Status:  "finished", FinishedAt: "2024-03-05T10:00:00Z", Issues: []string{"bad_timestamp"},
	}
}

func TestMasterListElsewhere_CreateGetListDelete(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	clock := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	st.now = func() time.Time { clock = clock.Add(time.Hour); return clock }

	a := testElsewhere("a", "u1")
	require.NoError(t, st.CreateElsewhere(a))
	require.NoError(t, st.CreateElsewhere(testElsewhere("b", "u2")))
	require.NoError(t, st.CreateElsewhere(testElsewhere("c", "u1")))

	got, err := st.GetElsewhere("a")
	require.NoError(t, err)
	a.CreatedAt = got.CreatedAt
	assert.Equal(t, a, *got)
	assert.False(t, got.CreatedAt.IsZero())

	all, err := st.ListElsewhere("")
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, []string{"c", "b", "a"}, []string{all[0].ID, all[1].ID, all[2].ID}, "newest first")

	mine, err := st.ListElsewhere("u1")
	require.NoError(t, err)
	require.Len(t, mine, 2)
	assert.Equal(t, "c", mine[0].ID)

	require.NoError(t, st.DeleteElsewhere("a"))
	_, err = st.GetElsewhere("a")
	assert.ErrorIs(t, err, ErrElsewhereNotFound)
	assert.ErrorIs(t, st.DeleteElsewhere("a"), ErrElsewhereNotFound)
}

func TestMasterListElsewhere_CreateIsIdempotentAndNeverOverwrites(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	first := testElsewhere("a", "u1")
	require.NoError(t, st.CreateElsewhere(first))

	second := testElsewhere("a", "u1")
	second.Title = "Changed"
	assert.ErrorIs(t, st.CreateElsewhere(second), ErrElsewhereExists)
	got, err := st.GetElsewhere("a")
	require.NoError(t, err)
	assert.Equal(t, "A Test Title", got.Title)

	assert.Error(t, st.CreateElsewhere(MasterListElsewhere{UserID: "u1", ListID: "l"}), "id required")
	assert.Error(t, st.CreateElsewhere(MasterListElsewhere{ID: "x", ListID: "l"}), "user required")
	assert.Error(t, st.CreateElsewhere(MasterListElsewhere{ID: "x", UserID: "u1"}), "list required")
}

func TestMasterListElsewhere_ConcurrentCreateMakesOne(t *testing.T) {
	st, _ := openTestMasterListStore(t)
	var created, exists atomic.Int32
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := st.CreateElsewhere(testElsewhere("same", "u1")); err {
			case nil:
				created.Add(1)
			case ErrElsewhereExists:
				exists.Add(1)
			default:
				t.Errorf("unexpected: %v", err)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, created.Load())
	assert.EqualValues(t, 15, exists.Load())
}

func TestElsewhereID_DeterministicPerUserAndRow(t *testing.T) {
	a := ElsewhereID("u1", "row-1")
	assert.Equal(t, a, ElsewhereID("u1", "row-1"))
	assert.NotEqual(t, a, ElsewhereID("u2", "row-1"))
	assert.NotEqual(t, a, ElsewhereID("u1", "row-2"))
	assert.NotEqual(t, ElsewhereID("u", "1row"), ElsewhereID("u1", "row"), "separator keeps the pairs apart")
	assert.Len(t, a, 32)
}

func TestSameElsewhere_IgnoresOnlyCreatedAt(t *testing.T) {
	a := testElsewhere("a", "u1")
	b := a
	b.CreatedAt = time.Now()
	assert.True(t, SameElsewhere(a, b))
	b.Title = "other"
	assert.False(t, SameElsewhere(a, b))
}
