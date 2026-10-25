// file: internal/plugins/maintenance/retired_reader_fake_test.go
// version: 1.0.0
// guid: c6a10507-b500-4753-9187-089b0fd6deb3
// last-edited: 2026-10-24

package maintenance

import (
	"errors"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// errFakeReadNotModelled is what noRetiredRows answers for a retired-reader read
// whose live twin the embedding fake does not model, so a test that reaches one
// fails loudly instead of reading an empty answer as "nothing there".
var errFakeReadNotModelled = errors.New("test fake does not model this retired-reader read; implement it on the fake")

// noRetiredRows lets a hand-written fake store satisfy
// database.RetiredBookFileReader, which database.AllBookFilesCoreIncludingRetired
// and database.BookFileByPathIncludingRetired require and, without it, answer
// database.ErrRetiredReaderUnavailable (they fail closed on purpose; there is no
// live-rows fallback to lean on).
//
// Embed it in a fake that holds no retired rows, and add the one method that
// cannot be shared because it needs the fake's own rows:
//
//	func (f *myFake) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
//		return f.GetAllBookFilesCore()
//	}
//
// With nothing retired the including-retired read IS the plain read. A fake that
// does hold retired rows (rewriteFakeStore's fileCores) implements that method
// to return them.
//
// What it answers itself:
//   - GetRetiredBookFiles and GetRetiredVersionsFromBook: nothing is retired, so
//     nil is the true answer.
//   - GetBookFilesIncludingRetired, GetBookFileByPIDIncludingRetired and
//     GetBookFileByPathIncludingRetired: errFakeReadNotModelled. Their live twins
//     (GetBookFiles, GetBookFileByPID, GetBookFileByPath) differ per fake and
//     most fakes have none of them, so a shared nil would be a wrong answer.
//     A fake whose op reaches one of them overrides it with its own plain read.
type noRetiredRows struct{}

var _ database.RetiredBookFileReader = (*fakeWithRetiredReader)(nil)

// fakeWithRetiredReader is the compile-time proof that noRetiredRows plus the one
// per-fake method is a complete database.RetiredBookFileReader.
type fakeWithRetiredReader struct{ noRetiredRows }

func (fakeWithRetiredReader) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return nil, nil
}

func (noRetiredRows) GetBookFilesIncludingRetired(string) ([]database.BookFile, error) {
	return nil, errFakeReadNotModelled
}

func (noRetiredRows) GetRetiredBookFiles(string) ([]database.BookFile, error) { return nil, nil }

func (noRetiredRows) GetRetiredVersionsFromBook(string) ([]database.BookVersion, error) {
	return nil, nil
}

func (noRetiredRows) GetBookFileByPIDIncludingRetired(string) (*database.BookFile, error) {
	return nil, errFakeReadNotModelled
}

func (noRetiredRows) GetBookFileByPathIncludingRetired(string) (*database.BookFile, error) {
	return nil, errFakeReadNotModelled
}
