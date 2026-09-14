// file: internal/itunes/service/modifybook_mock_test.go
// version: 1.0.0
// guid: 7d2e4c91-3b8a-4f06-a5e1-9c0b6d8f2a47
// last-edited: 2026-09-13

package itunesservice

import (
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	"github.com/stretchr/testify/mock"
)

// expectModifyBook wires a MockStore's ModifyBook for id the way the real
// store behaves: the callback runs on a copy of row and that copy is returned
// (the unmodified copy for ErrSkipBookWrite). A non-nil failErr is returned
// without running the callback. row == nil means "the book does not exist".
func expectModifyBook(m *dbmocks.MockStore, id string, row *database.Book, failErr error) {
	m.EXPECT().ModifyBook(id, mock.Anything).RunAndReturn(func(_ string, fn func(*database.Book) error) (*database.Book, error) {
		if failErr != nil {
			return nil, failErr
		}
		if row == nil {
			return nil, nil
		}
		cp := *row
		if err := fn(&cp); err != nil {
			if errors.Is(err, database.ErrSkipBookWrite) {
				return &cp, nil
			}
			return nil, err
		}
		return &cp, nil
	})
}
