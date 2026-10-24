// file: internal/database/retired_mock_store.go
// version: 1.0.0
// guid: 2e8b4d90-6f13-4c75-a9d2-b05c71e3f482
// last-edited: 2026-10-23

package database

// The hand-written MockStore is an in-memory library that has never had a row
// retired, so it implements the retired-row reader as the plain reads: every
// row it holds is live and nothing is retired. That is what lets the ops that
// resolve RetiredBookFileReader (and fail closed without it) run against it.
//
// It does not implement RetiredBookFileWriter: a test that retires rows uses a
// real store.

var _ RetiredBookFileReader = (*MockStore)(nil)

// GetBookFilesIncludingRetired is GetBookFiles: nothing is retired.
func (m *MockStore) GetBookFilesIncludingRetired(bookID string) ([]BookFile, error) {
	return m.GetBookFiles(bookID)
}

// GetRetiredBookFiles returns nothing: nothing is retired.
func (m *MockStore) GetRetiredBookFiles(string) ([]BookFile, error) { return nil, nil }

// GetAllBookFilesCoreIncludingRetired is GetAllBookFilesCore: nothing is retired.
func (m *MockStore) GetAllBookFilesCoreIncludingRetired() ([]BookFileCore, error) {
	return m.GetAllBookFilesCore()
}

// GetRetiredVersionsFromBook returns nothing: nothing is retired.
func (m *MockStore) GetRetiredVersionsFromBook(string) ([]BookVersion, error) { return nil, nil }

// GetBookFileByPIDIncludingRetired is GetBookFileByPID: nothing is retired.
func (m *MockStore) GetBookFileByPIDIncludingRetired(itunesPID string) (*BookFile, error) {
	return m.GetBookFileByPID(itunesPID)
}

// GetBookFileByPathIncludingRetired is GetBookFileByPath: nothing is retired.
func (m *MockStore) GetBookFileByPathIncludingRetired(path string) (*BookFile, error) {
	return m.GetBookFileByPath(path)
}
