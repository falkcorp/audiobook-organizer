// file: internal/database/retired_mock_store.go
// version: 1.3.0
// guid: 2e8b4d90-6f13-4c75-a9d2-b05c71e3f482
// last-edited: 2026-10-26

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

// HasRetiredVersions answers for the rows the test gave the mock. The mock
// holds no rows of its own, so the only way it can hold a retired one is through
// the complete reads a test stubs: BookFilesWithHashFunc and BookFilesAtPathFunc
// are where a test puts the retired rows it wants the code under test to see.
//
//   - HasRetiredVersionsFunc, when set, is the answer.
//   - Otherwise it is true exactly when one of those two reads is stubbed, so
//     RetiredHoldersOfHash and BookFileRowsAtPathForWrite go on to that read and
//     a stubbed retired row reaches the retired-content check instead of being
//     short-circuited to "none".
//   - With neither stubbed the mock holds no row at all, so it is false, which
//     is what lets those two lookups answer "none" for a MockStore without
//     stubs, where BookFilesWithHash would otherwise fail closed.
//
// Stubbing BookFilesAtPathFunc also opts BookFileRowsAtPathForWrite into the
// strict lookup, so a stub that returns ErrBookFilesAtPathUnavailable makes it
// answer ErrOwnershipLookupIncomplete instead of falling back to the live rows.
// A test that stubs BookFilesAtPathFunc only to feed a read path will see the
// write-side lookup go strict too.
//
// It answered false unconditionally until 2026-10-26, and every MockStore
// scanner test then skipped the retired check whatever it had stubbed.
func (m *MockStore) HasRetiredVersions() (bool, error) {
	if m.HasRetiredVersionsFunc != nil {
		return m.HasRetiredVersionsFunc()
	}
	return m.BookFilesWithHashFunc != nil || m.BookFilesAtPathFunc != nil, nil
}
