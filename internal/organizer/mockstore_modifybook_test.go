// file: internal/organizer/mockstore_modifybook_test.go
// version: 1.0.0
// guid: 3f6b1c2e-7d84-4b5a-9e01-c2a5d8f4b716
// last-edited: 2026-09-14

package organizer

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/mock"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/database/mocks"
)

// newMockStore returns a mockery store whose ModifyBook composes the mock's
// own GetBookByID and UpdateBook, so a test written as
// On("GetBookByID") + On("UpdateBook") keeps stubbing and observing the
// organizer's book writes now that they go through ModifyBook. A GetBookByID
// error or a nil row is returned as ModifyBook would return it; the callback's
// ErrSkipBookWrite skips UpdateBook. The expectation is optional (Maybe), so a
// test whose path never writes a book is unaffected.
func newMockStore(t *testing.T) *mocks.MockStore {
	t.Helper()
	m := mocks.NewMockStore(t)
	m.EXPECT().ModifyBook(mock.Anything, mock.Anything).
		RunAndReturn(func(id string, fn func(*database.Book) error) (*database.Book, error) {
			b, err := m.GetBookByID(id)
			if err != nil || b == nil {
				return nil, err
			}
			if err := fn(b); err != nil {
				if errors.Is(err, database.ErrSkipBookWrite) {
					return b, nil
				}
				return nil, err
			}
			return m.UpdateBook(id, b)
		}).Maybe()
	return m
}
