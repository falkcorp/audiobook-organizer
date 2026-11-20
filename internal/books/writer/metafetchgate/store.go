// file: internal/books/writer/metafetchgate/store.go
// version: 1.0.1
// guid: 7a1d93e6-48c2-4f5b-b0e7-3d9c15a8f264
// last-edited: 2026-11-19

// Package metafetchgate gates the store writes of metafetch's apply.
//
// metafetch.Service takes its store as a metafetch.Store and writes through it
// directly, so a native kind that runs the fetch (resolve-production-authors,
// auto-match-transcribed) writes outside the typed writers and, until now,
// outside the fence. Store wraps a metafetch.Store so that every write method
// is one gated call (writer.Gate.Do) stamped with the method's whole row. The
// fetch service is then built over the wrapped store per run, over the run's
// own writer: its writes cannot go around the fence.
//
// A method that only reads is promoted from the embedded store. That is the
// hole this design has to close by test rather than by type: a write method
// added to metafetch.Store later would be promoted ungated. The tests list
// every method of metafetch.Store as gated or read, so a new method fails until
// a person classifies it, and they call every gated method over a nil inner
// store to prove none of them reaches it past the gate.
package metafetchgate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// Store is a metafetch.Store whose writes are gated.
type Store struct {
	metafetch.Store // reads, and nothing that writes (see the package doc)
	ctx             context.Context
	gate            *writer.Gate
}

var _ metafetch.Store = (*Store)(nil)

// Rows is the row table Store stamps from: the shared table, metafetch's own
// rows and the raw key families, merged. It fails when two of them disagree.
func Rows() (writer.Rows, error) {
	return writer.Merge(writer.SharedRows(), writer.MetafetchApplyRows(), writer.RawRows())
}

// Wrap gates inner's writes through gate. ctx is the run's context: the fence
// checks it (a stopped run's writes are refused).
func Wrap(ctx context.Context, inner metafetch.Store, gate *writer.Gate) *Store {
	return &Store{Store: inner, ctx: ctx, gate: gate}
}

func book(id string) entity.Ref { return tasks.ResID(writer.Module, string(ops.ResBooks), id) }
func file(id string) entity.Ref { return tasks.ResID(writer.Module, string(ops.ResBookFiles), id) }
func of(r ops.Resource, id string) entity.Ref {
	return tasks.ResID(writer.Module, string(r), id)
}

// do is one gated call whose store call returns only an error.
func (s *Store) do(method string, subject entity.Ref, call func() error) error {
	return s.gate.Do(s.ctx, method, subject, func(context.Context) ([]tasks.Change, error) { return nil, call() })
}

// rawMethod classifies a raw key into its qualified method name and the
// resource its family is, or refuses.
func (s *Store) rawMethod(method, key string) (string, ops.Resource, error) {
	for _, f := range writer.RawFamilies() {
		if strings.HasPrefix(key, f.Prefix) {
			return writer.RawMethod(method, f.Prefix), f.Resource, nil
		}
	}
	return "", "", fmt.Errorf("%w: %w: %s on key %q is in no raw family", tasks.ErrOutsideEffects, writer.ErrNoRow, method, key)
}

// --- books ---------------------------------------------------------------

func (s *Store) CreateBook(b *database.Book) (out *database.Book, err error) {
	id := ""
	if b != nil {
		id = b.ID
	}
	err = s.do("CreateBook", book(id), func() (e error) { out, e = s.Store.CreateBook(b); return })
	return out, err
}

func (s *Store) UpdateBook(id string, b *database.Book) (out *database.Book, err error) {
	err = s.do("UpdateBook", book(id), func() (e error) { out, e = s.Store.UpdateBook(id, b); return })
	return out, err
}

func (s *Store) ModifyBook(id string, fn func(*database.Book) error) (out *database.Book, err error) {
	err = s.do("ModifyBook", book(id), func() (e error) { out, e = s.Store.ModifyBook(id, fn); return })
	return out, err
}

func (s *Store) DeleteBook(id string) error {
	return s.do("DeleteBook", book(id), func() error { return s.Store.DeleteBook(id) })
}

func (s *Store) FlagMetadataHashDuplicate(primaryID, duplicateID string) error {
	return s.do("FlagMetadataHashDuplicate", book(duplicateID), func() error { return s.Store.FlagMetadataHashDuplicate(primaryID, duplicateID) })
}

func (s *Store) SetLastWrittenAt(id string, t time.Time) error {
	return s.do("SetLastWrittenAt", book(id), func() error { return s.Store.SetLastWrittenAt(id, t) })
}

func (s *Store) MarkNeedsRescan(id string) error {
	return s.do("MarkNeedsRescan", book(id), func() error { return s.Store.MarkNeedsRescan(id) })
}

func (s *Store) AddBookTagWithSource(bookID, tag, source string) error {
	return s.do("AddBookTagWithSource", book(bookID), func() error { return s.Store.AddBookTagWithSource(bookID, tag, source) })
}

func (s *Store) RemoveBookTagsByPrefix(bookID, prefix, source string) error {
	return s.do("RemoveBookTagsByPrefix", book(bookID), func() error { return s.Store.RemoveBookTagsByPrefix(bookID, prefix, source) })
}

func (s *Store) RecordPathChange(c *database.BookPathChange) error {
	id := ""
	if c != nil {
		id = c.BookID
	}
	return s.do("RecordPathChange", book(id), func() error { return s.Store.RecordPathChange(c) })
}

func (s *Store) SetBookNarrators(bookID string, n []database.BookNarrator) error {
	return s.do("SetBookNarrators", book(bookID), func() error { return s.Store.SetBookNarrators(bookID, n) })
}

// --- credits and series --------------------------------------------------

func (s *Store) CreateAuthor(name string) (out *database.Author, err error) {
	err = s.do("CreateAuthor", of(ops.ResAuthors, name), func() (e error) { out, e = s.Store.CreateAuthor(name); return })
	return out, err
}

func (s *Store) SetBookAuthors(bookID string, a []database.BookAuthor) error {
	return s.do("SetBookAuthors", book(bookID), func() error { return s.Store.SetBookAuthors(bookID, a) })
}

func (s *Store) ModifyBookAuthors(bookID string, fn func([]database.BookAuthor) ([]database.BookAuthor, error)) (out []database.BookAuthor, err error) {
	err = s.do("ModifyBookAuthors", book(bookID), func() (e error) { out, e = s.Store.ModifyBookAuthors(bookID, fn); return })
	return out, err
}

func (s *Store) CreateSeries(name string, authorID *int) (out *database.Series, err error) {
	err = s.do("CreateSeries", of(ops.ResSeries, name), func() (e error) { out, e = s.Store.CreateSeries(name, authorID); return })
	return out, err
}

// --- file rows -----------------------------------------------------------

func (s *Store) CreateBookFile(f *database.BookFile) error {
	id := ""
	if f != nil {
		id = f.ID
	}
	return s.do("CreateBookFile", file(id), func() error { return s.Store.CreateBookFile(f) })
}

func (s *Store) BatchCreateBookFiles(fs []*database.BookFile) error {
	return s.do("BatchCreateBookFiles", tasks.Res(writer.Module, string(ops.ResBookFiles)), func() error { return s.Store.BatchCreateBookFiles(fs) })
}

func (s *Store) UpdateBookFile(id string, f *database.BookFile) error {
	return s.do("UpdateBookFile", file(id), func() error { return s.Store.UpdateBookFile(id, f) })
}

// UpdateBookFileHashes takes the FILE row's id first (the store's
// UpdateBookFileHashes(id, originalHash, postMetadataHash, fileHash)), so the
// subject is file(id); there is no book id argument.
func (s *Store) UpdateBookFileHashes(id, originalHash, postMetadataHash, fileHash string) error {
	return s.do("UpdateBookFileHashes", file(id), func() error {
		return s.Store.UpdateBookFileHashes(id, originalHash, postMetadataHash, fileHash)
	})
}

func (s *Store) PatchBookFileFields(bookID, fileID string, p database.BookFileFieldPatch) (before, after *database.BookFile, err error) {
	err = s.do("PatchBookFileFields", file(fileID), func() (e error) { before, after, e = s.Store.PatchBookFileFields(bookID, fileID, p); return })
	return before, after, err
}

// --- metadata state, cache and history -----------------------------------

func (s *Store) PutMetadataCache(entry *database.MetadataCandidateCache) error {
	id := ""
	if entry != nil {
		id = entry.BookID
	}
	return s.do("PutMetadataCache", book(id), func() error { return s.Store.PutMetadataCache(entry) })
}

func (s *Store) DeleteMetadataCache(bookID string) error {
	return s.do("DeleteMetadataCache", book(bookID), func() error { return s.Store.DeleteMetadataCache(bookID) })
}

func (s *Store) UpsertMetadataFieldState(st *database.MetadataFieldState) error {
	id := ""
	if st != nil {
		id = st.BookID
	}
	return s.do("UpsertMetadataFieldState", book(id), func() error { return s.Store.UpsertMetadataFieldState(st) })
}

func (s *Store) DeleteMetadataFieldState(bookID, field string) error {
	return s.do("DeleteMetadataFieldState", book(bookID), func() error { return s.Store.DeleteMetadataFieldState(bookID, field) })
}

func (s *Store) RecordMetadataChange(rec *database.MetadataChangeRecord) error {
	id := ""
	if rec != nil {
		id = rec.BookID
	}
	return s.do("RecordMetadataChange", book(id), func() error { return s.Store.RecordMetadataChange(rec) })
}

// --- the undo journal ----------------------------------------------------

func (s *Store) CreateOperationChange(c *database.OperationChange) error {
	id := ""
	if c != nil {
		id = c.ID
	}
	return s.do("CreateOperationChange", of(ops.ResOperations, id), func() error { return s.Store.CreateOperationChange(c) })
}

func (s *Store) DeleteOperationState(id string) error {
	return s.do("DeleteOperationState", of(ops.ResOperations, id), func() error { return s.Store.DeleteOperationState(id) })
}

// --- preferences and the raw key-value store -----------------------------

func (s *Store) SetUserPreference(key, value string) error {
	return s.do("SetUserPreference", of(ops.ResUserState, key), func() error { return s.Store.SetUserPreference(key, value) })
}

func (s *Store) DeleteUserPreference(key string) error {
	return s.do("DeleteUserPreference", of(ops.ResUserState, key), func() error { return s.Store.DeleteUserPreference(key) })
}

func (s *Store) SetUserPreferenceForUser(userID, key, value string) error {
	return s.do("SetUserPreferenceForUser", of(ops.ResUserState, key), func() error { return s.Store.SetUserPreferenceForUser(userID, key, value) })
}

// SetRaw is stamped by the key's family (writer.RawFamilies); a key in no
// family is refused without calling the store.
func (s *Store) SetRaw(key string, value []byte) error {
	m, res, err := s.rawMethod("SetRaw", key)
	if err != nil {
		return err
	}
	return s.do(m, of(res, key), func() error { return s.Store.SetRaw(key, value) })
}

// DeleteRaw is stamped by the key's family, as SetRaw.
func (s *Store) DeleteRaw(key string) error {
	m, res, err := s.rawMethod("DeleteRaw", key)
	if err != nil {
		return err
	}
	return s.do(m, of(res, key), func() error { return s.Store.DeleteRaw(key) })
}

// DeleteRawBatch is one store call, so its keys must be one family: a batch
// that spans families is refused, not split.
func (s *Store) DeleteRawBatch(keys []string) error {
	if len(keys) == 0 {
		return s.Store.DeleteRawBatch(keys)
	}
	m, res, err := s.rawMethod("DeleteRawBatch", keys[0])
	if err != nil {
		return err
	}
	for _, k := range keys[1:] {
		if m2, _, err := s.rawMethod("DeleteRawBatch", k); err != nil || m2 != m {
			return fmt.Errorf("%w: %w: DeleteRawBatch spans key families (%q)", tasks.ErrOutsideEffects, writer.ErrNoRow, k)
		}
	}
	return s.do(m, of(res, keys[0]), func() error { return s.Store.DeleteRawBatch(keys) })
}
