// file: internal/database/retired_lookup_test.go
// version: 1.0.0
// guid: 84b42cd5-8a89-4667-bfe1-8cb62dd1527c
// last-edited: 2026-10-23

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// liveOnlyStore has the plain reads and nothing else: no retired reader and no
// NoRetiredBookFiles declaration, like a decorator that was never taught to
// forward the capability. Its live subset could be hiding retired rows.
type liveOnlyStore struct{}

func (liveOnlyStore) GetAllBookFilesCore() ([]BookFileCore, error) {
	return []BookFileCore{{ID: "live"}}, nil
}
func (liveOnlyStore) GetBookFiles(string) ([]BookFile, error)     { return []BookFile{{ID: "live"}}, nil }
func (liveOnlyStore) GetBookFileByPID(string) (*BookFile, error)  { return &BookFile{ID: "live"}, nil }
func (liveOnlyStore) GetBookFileByPath(string) (*BookFile, error) { return &BookFile{ID: "live"}, nil }
func (liveOnlyStore) BookFilesWithHash(string) ([]BookFile, error) {
	return []BookFile{{ID: "live"}}, nil
}

// declaredCompleteStore is liveOnlyStore that says it never holds a retired row.
type declaredCompleteStore struct{ liveOnlyStore }

func (declaredCompleteStore) HoldsNoRetiredBookFiles() bool { return true }

// A store that neither reads retired rows nor declares it never has any must
// not be answered from its live subset: every IncludingRetired helper returns
// ErrRetiredReaderUnavailable, so a retired version's path, PID or file is never
// read as free.
func TestIncludingRetiredHelpers_FailClosedForUnknownStore(t *testing.T) {
	var s any = liveOnlyStore{}

	_, err := AllBookFilesCoreIncludingRetired(s)
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	_, err = BookFilesIncludingRetired(s, "b")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	_, err = BookFileByPIDIncludingRetired(s, "pid")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	_, err = BookFileByPathIncludingRetired(s, "/p")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	_, err = BookFilesHoldingHash(liveOnlyStore{}, "h")
	require.NoError(t, err, "a store that implements BookFilesWithHash answers for itself")
	_, err = BookFilesHoldingHash(struct{}{}, "h")
	require.ErrorIs(t, err, ErrBookFilesWithHashUnavailable)
}

// A declared-complete store, and a MockStore, take the plain read: they hold no
// retired rows, so the plain read is the whole answer.
func TestIncludingRetiredHelpers_FallBackForDeclaredCompleteStores(t *testing.T) {
	for name, s := range map[string]any{
		"declared": declaredCompleteStore{},
		"mock":     &MockStore{},
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, plainReadsAreComplete(s))
		})
	}

	var s any = declaredCompleteStore{}
	all, err := AllBookFilesCoreIncludingRetired(s)
	require.NoError(t, err)
	require.Equal(t, "live", all[0].ID)
	files, err := BookFilesIncludingRetired(s, "b")
	require.NoError(t, err)
	require.Equal(t, "live", files[0].ID)
	pid, err := BookFileByPIDIncludingRetired(s, "pid")
	require.NoError(t, err)
	require.Equal(t, "live", pid.ID)
	path, err := BookFileByPathIncludingRetired(s, "/p")
	require.NoError(t, err)
	require.Equal(t, "live", path.ID)
}

// An opted-in decorator is looked through to the declaration underneath.
type wrappedMock struct{ Store }

func (w wrappedMock) Unwrap() Store { return w.Store }

func TestPlainReadsAreComplete_LooksThroughDecorators(t *testing.T) {
	require.True(t, plainReadsAreComplete(wrappedMock{Store: &MockStore{}}))
	require.False(t, plainReadsAreComplete(liveOnlyStore{}))
	require.False(t, plainReadsAreComplete(nil))
}

// BookFilesHoldingHash on a MockStore: the unstubbed default (an
// ErrBookFilesWithHashUnavailable) is "no retired rows", a stubbed answer is
// returned as given, and any other error is not swallowed.
func TestBookFilesHoldingHash_MockStore(t *testing.T) {
	m := &MockStore{}
	rows, err := BookFilesHoldingHash(m, "h")
	require.NoError(t, err)
	require.Empty(t, rows)

	m.BookFilesWithHashFunc = func(string) ([]BookFile, error) {
		return []BookFile{{ID: "r", Retired: true}}, nil
	}
	rows, err = BookFilesHoldingHash(m, "h")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Retired)

	boom := errors.New("boom")
	m.BookFilesWithHashFunc = func(string) ([]BookFile, error) { return nil, boom }
	_, err = BookFilesHoldingHash(m, "h")
	require.ErrorIs(t, err, boom)
}
