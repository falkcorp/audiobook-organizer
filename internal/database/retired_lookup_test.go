// file: internal/database/retired_lookup_test.go
// version: 1.1.0
// guid: 84b42cd5-8a89-4667-bfe1-8cb62dd1527c
// last-edited: 2026-10-26

package database

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// liveOnlyStore has the plain reads and nothing else: no retired reader, like a
// decorator that was never taught to forward the capability. Its live subset
// could be hiding retired rows.
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

// A store that does not resolve to the retired reader must not be answered from
// its live subset: every IncludingRetired helper returns
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
	_, err = RetiredVersionsFromBook(s, "shell")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
	_, err = BookFilesHoldingHash(liveOnlyStore{}, "h")
	require.NoError(t, err, "a store that implements BookFilesWithHash answers for itself")
	_, err = BookFilesHoldingHash(struct{}{}, "h")
	require.ErrorIs(t, err, ErrBookFilesWithHashUnavailable)
}

// An opted-in decorator is looked through to the reader underneath.
type wrappedMock struct{ Store }

func (w wrappedMock) Unwrap() Store { return w.Store }

// A MockStore implements the reader as its plain reads (it never holds a
// retired row), directly and through an opted-in decorator, and a decorator
// without Unwrap fails closed like any other store.
func TestIncludingRetiredHelpers_MockStoreIsTheReader(t *testing.T) {
	m := &MockStore{}
	m.GetBookFilesFunc = func(string) ([]BookFile, error) { return []BookFile{{ID: "live"}}, nil }
	for name, s := range map[string]any{"direct": m, "decorated": wrappedMock{Store: m}} {
		t.Run(name, func(t *testing.T) {
			files, err := BookFilesIncludingRetired(s, "b")
			require.NoError(t, err)
			require.Equal(t, "live", files[0].ID)
			vs, err := RetiredVersionsFromBook(s, "shell")
			require.NoError(t, err)
			require.Empty(t, vs)
		})
	}
	_, err := BookFilesIncludingRetired(struct{ Store }{Store: m}, "b")
	require.ErrorIs(t, err, ErrRetiredReaderUnavailable)
}

// BookFilesHoldingHash on a MockStore keeps BookFilesWithHash's contract: the
// unstubbed default stays the fail-closed refusal (it is never turned into an
// empty answer), a stubbed answer is returned as given, and any other error is
// not swallowed.
func TestBookFilesHoldingHash_MockStore(t *testing.T) {
	m := &MockStore{}
	_, err := BookFilesHoldingHash(m, "h")
	require.ErrorIs(t, err, ErrBookFilesWithHashUnavailable)

	m.BookFilesWithHashFunc = func(string) ([]BookFile, error) {
		return []BookFile{{ID: "r", Retired: true}}, nil
	}
	rows, err := BookFilesHoldingHash(m, "h")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].Retired)

	boom := errors.New("boom")
	m.BookFilesWithHashFunc = func(string) ([]BookFile, error) { return nil, boom }
	_, err = BookFilesHoldingHash(m, "h")
	require.ErrorIs(t, err, boom)
}

// RetiredHoldersOfHash answers "none" for a MockStore without a BookFilesWithHash
// stub: a MockStore has no retired version (HasRetiredVersions is false), so a
// scanner test that imports a hashed file reaches no retired content.
func TestRetiredHoldersOfHash_MockStoreHasNoRetiredVersions(t *testing.T) {
	retired, liveHolds, err := RetiredHoldersOfHash(&MockStore{}, "h")
	require.NoError(t, err)
	require.Empty(t, retired)
	require.False(t, liveHolds)
}
