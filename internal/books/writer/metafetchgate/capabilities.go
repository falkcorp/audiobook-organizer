// file: internal/books/writer/metafetchgate/capabilities.go
// version: 1.1.0
// guid: 5be0f2c9-71a4-4c38-9d6e-c2a41f0b7e83
// last-edited: 2026-11-19

package metafetchgate

import (
	"fmt"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// This file is the READ capabilities the wrapper forwards.
//
// Store has no Unwrap (see the package doc and TestStoreCapabilitiesAreClassified),
// so database.AsCapability sees only the wrapper's own method set. Code the
// fetch service hands its store to resolves optional capabilities through that
// function, and a capability the wrapper does not implement is, to that code, a
// store without it. For a WRITE capability that is the point (it fails closed:
// the write is skipped, never made past the gate). For a READ capability it
// silently changes what the code does: the apply's author resolution
// (authorcredit.ResolveBook) gets less evidence through the gate than on the
// bare store and can mint a combined author row the bare store would not.
//
// So every read capability that code reaches through the wrapper is forwarded
// here, ungated (a read stamps nothing), and resolved against the inner store
// with the same function the consumer would have used on it, so an inner store
// that lacks the capability is reported the way the consumer already handles a
// miss:
//
//   - the optional author-resolution lookups (alias, series listing, author
//     history, by-author book listing) return the empty answer the consumer
//     reads as "no evidence", with no error;
//   - the retired-row reader and the file-hash holder lookup return the same
//     error the database helpers return for a store without them
//     (ErrRetiredReaderUnavailable, ErrNoFileHashHolderLookup), because their
//     callers fail closed on a miss.
//
// A hidden WRITE capability is not always "the write is skipped": the consumer
// takes its own fallback, and that fallback is not always equivalent.
// backup.Checkpointable, hidden, makes the organizer's auto-backup take the
// live-directory walk instead of a checkpoint (racy against a writer); the
// fetch path never reaches that (autoBackup runs only from PerformOrganizeStats),
// and a path that does must say so in the seam (F5-18b.md).
//
// Two places the wrapper cannot match the bare store exactly, both reads:
//
//   - A consumer that tells "no capability" from "capability, empty answer"
//     (authorcredit adds its by-author evidence source when the capability is
//     present) gets the empty source instead of an absent one. An empty
//     listing yields no evidence, which is what the absent source contributes.
//   - authorcredit.TitleSource is SYNTHESIZED: the wrapper always has
//     GetAllSeries (above) and the promoted GetAllBooksCore, so the wrapper is
//     a TitleSource even over an inner store that has none. The bare store
//     would then have no title index, so the title check is skipped; through
//     the wrapper titlesOf builds a books-only index and the check runs. Not
//     an empty answer standing for absence: a partial answer standing for
//     none. PebbleStore has GetAllSeries (TestStoreCapabilitiesAreClassified),
//     so production does not see it; a test fake without it does.

// seriesLister is the series half of authorcredit.TitleSource; the book half,
// GetAllBooksCore, is already a metafetch.Store read.
type seriesLister interface {
	GetAllSeries() ([]database.Series, error)
}

// The wrapper offers authorcredit's own interfaces (exported for this), so a
// change to what authorcredit asks of a store is a compile error here instead of
// a capability the wrapper silently stops satisfying.
var (
	_ authorcredit.TitleSource       = (*Store)(nil)
	_ authorcredit.HistorySource     = (*Store)(nil)
	_ authorcredit.AliasFinder       = (*Store)(nil)
	_ authorcredit.AuthorBooksSource = (*Store)(nil)
	_ authorcredit.TitleCacheKeyer   = (*Store)(nil)
)

// FindAuthorByAlias is the inner store's alias lookup; (nil, nil), a miss, when
// it has none.
func (s *Store) FindAuthorByAlias(aliasName string) (*database.Author, error) {
	f, ok := database.AsCapability[authorcredit.AliasFinder](s.Store)
	if !ok {
		return nil, nil
	}
	return f.FindAuthorByAlias(aliasName)
}

// GetAllSeries is the inner store's series listing; empty when it has none.
func (s *Store) GetAllSeries() ([]database.Series, error) {
	l, ok := database.AsCapability[seriesLister](s.Store)
	if !ok {
		return nil, nil
	}
	return l.GetAllSeries()
}

// GetMetadataChangeHistory is the inner store's metadata change history for a
// field; empty when it has none.
func (s *Store) GetMetadataChangeHistory(bookID string, field string, limit int) ([]database.MetadataChangeRecord, error) {
	h, ok := database.AsCapability[authorcredit.HistorySource](s.Store)
	if !ok {
		return nil, nil
	}
	return h.GetMetadataChangeHistory(bookID, field, limit)
}

// GetBooksByAuthorIDWithRoleCore is the inner store's by-author book listing;
// empty when it has none.
func (s *Store) GetBooksByAuthorIDWithRoleCore(authorID int) ([]database.BookCore, error) {
	l, ok := database.AsCapability[authorcredit.AuthorBooksSource](s.Store)
	if !ok {
		return nil, nil
	}
	return l.GetBooksByAuthorIDWithRoleCore(authorID)
}

// --- the file-work reads -------------------------------------------------

// retired resolves the inner store's retired-row reader, or the error the
// database helpers return for a store without it.
func (s *Store) retired(what string) (database.RetiredBookFileReader, error) {
	r, ok := database.AsCapability[database.RetiredBookFileReader](s.Store)
	if !ok {
		return nil, fmt.Errorf("%s: %w", what, database.ErrRetiredReaderUnavailable)
	}
	return r, nil
}

func (s *Store) GetBookFilesIncludingRetired(bookID string) ([]database.BookFile, error) {
	r, err := s.retired("book_files of " + bookID)
	if err != nil {
		return nil, err
	}
	return r.GetBookFilesIncludingRetired(bookID)
}

func (s *Store) GetRetiredBookFiles(holderID string) ([]database.BookFile, error) {
	r, err := s.retired("retired book_files of " + holderID)
	if err != nil {
		return nil, err
	}
	return r.GetRetiredBookFiles(holderID)
}

func (s *Store) GetAllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	r, err := s.retired("all book_file rows")
	if err != nil {
		return nil, err
	}
	return r.GetAllBookFilesCoreIncludingRetired()
}

func (s *Store) GetRetiredVersionsFromBook(shellID string) ([]database.BookVersion, error) {
	r, err := s.retired("retired versions of " + shellID)
	if err != nil {
		return nil, err
	}
	return r.GetRetiredVersionsFromBook(shellID)
}

func (s *Store) GetBookFileByPIDIncludingRetired(itunesPID string) (*database.BookFile, error) {
	r, err := s.retired("book_file by PID " + itunesPID)
	if err != nil {
		return nil, err
	}
	return r.GetBookFileByPIDIncludingRetired(itunesPID)
}

func (s *Store) GetBookFileByPathIncludingRetired(path string) (*database.BookFile, error) {
	r, err := s.retired(fmt.Sprintf("book_file at %q", path))
	if err != nil {
		return nil, err
	}
	return r.GetBookFileByPathIncludingRetired(path)
}

var _ database.RetiredBookFileReader = (*Store)(nil)

// BooksByFileHash is the inner store's holder listing for a file hash, or
// ErrNoFileHashHolderLookup for a store without it (database.BooksHoldingFileHash
// fails closed on that, never answering "no holders").
func (s *Store) BooksByFileHash(hash string) ([]*database.Book, error) {
	l, ok := database.AsCapability[database.FileHashHolderLookup](s.Store)
	if !ok {
		return nil, database.ErrNoFileHashHolderLookup
	}
	return l.BooksByFileHash(hash)
}

var _ database.FileHashHolderLookup = (*Store)(nil)

// TitleCacheKey is the identity authorcredit caches the title index under: the
// key of the layer a bare store resolves authorcredit.TitleSource at (that
// layer's own key when it names one, else that layer), so a decorator in front
// of it does not change which entry is used. The wrapper is built per run, and
// cached under its own value it would leave one library-sized index behind per
// run; named like this every wrapper over one store shares the entry the bare
// store would have had.
//
// The key is an authorcredit.TitleKey: opaque, holding the store in an
// unexported field with no method that reads it back. Returning the store
// itself (as `any`) would let any caller assert it to database.Store and write
// through it with no stamp, an Unwrap under another name
// (TestNoMethodHandsBackTheInnerStore).
func (s *Store) TitleCacheKey() authorcredit.TitleKey {
	if s.Store == nil {
		return authorcredit.TitleKeyOf(s)
	}
	layer := any(s.Store)
	if ts, ok := database.AsCapability[authorcredit.TitleSource](s.Store); ok {
		layer = ts
	}
	if k, ok := layer.(authorcredit.TitleCacheKeyer); ok {
		return k.TitleCacheKey()
	}
	return authorcredit.TitleKeyOf(layer)
}
