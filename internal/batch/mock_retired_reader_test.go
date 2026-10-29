// file: internal/batch/mock_retired_reader_test.go
// version: 1.0.1
// guid: 4e8b2d61-9c35-4a07-b1f8-6d3a70c95e24
// last-edited: 2026-10-29

package batch

import "github.com/falkcorp/audiobook-organizer/internal/database"

// MockBookStore holds no book_file rows at all (its GetBookFiles answers nil), so
// nothing in it is retired either. It still has to answer the retired-version
// reads, because merge.RestoreFromTrashLocked refuses a restore it cannot vet:
// database.RetiredRowsHeldFor on a store that is not a
// database.RetiredBookFileReader fails closed with
// database.ErrRetiredReaderUnavailable, and the restore returns that error
// before it writes anything. A restore test against this fixture therefore never
// reached the write it set out to exercise (the gated-action panic test saw no
// panic) until the fixture said "nothing is retired", which is true of it.
var _ database.RetiredBookFileReader = (*MockBookStore)(nil)

func (m *MockBookStore) GetBookFilesIncludingRetired(string) ([]database.BookFile, error) {
	return nil, nil
}

func (m *MockBookStore) GetRetiredBookFiles(string) ([]database.BookFile, error) { return nil, nil }

func (m *MockBookStore) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return nil, nil
}

func (m *MockBookStore) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, nil
}

func (m *MockBookStore) GetBookFileByPIDIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}

func (m *MockBookStore) GetBookFileByPathIncludingRetired(string) (*database.BookFile, error) {
	return nil, nil
}
