// file: internal/plugins/deluge/books_tasklists.go
// version: 0.1.0
// guid: 5b7e2d94-16c8-4f03-a9e5-d41b80c6372f
// last-edited: 2026-11-22

package deluge

import (
	"context"
	"fmt"
	"sort"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own KindDeps for the Deluge kinds (F5-18 A6), dark:
// nothing outside tests calls BooksTaskLists until the flip's registration
// commit. The writer is the gated one: each method is ONE writer.Gate call
// stamped with the shared table's row for it, so a kind that declares less than
// the row is refused at the write.

// KindSource is the store the builder reads through (the writes go through the
// gate only).
type KindSource interface {
	kindStore
	kindSourceWrites
}

// ProtectedCache is the protected-path cache protected-paths-sync refreshes and
// centralize consults.
type ProtectedCache = protectedCache

// kindSourceWrites is the two store writes the cores reach.
type kindSourceWrites interface {
	ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error)
	UpdateBookFile(id string, file *database.BookFile) error
}

// BooksTaskInputs is everything BooksTaskLists builds the Deluge lists from.
type BooksTaskInputs struct {
	// Source is the library store.
	Source KindSource
	// Client is the Deluge client; nil when Deluge is not configured (centralize
	// then imports without moving torrent storage, as at HEAD).
	Client *delugeclient.Client
	// Cache is the protected-path cache.
	Cache ProtectedCache
	// Config is the live configuration.
	Config *config.Config
	// Schedules is books' schedule hook.
	Schedules func(tasks.Kind) *tasks.Schedule
}

// BooksTaskLists returns every Deluge kind with its schedule, over the books
// module's own dependency values.
func BooksTaskLists(in BooksTaskInputs) ([]tasks.Definition, error) {
	var missing []string
	for name, set := range map[string]bool{
		"Source": in.Source != nil, "Cache": in.Cache != nil,
		"Config": in.Config != nil, "Schedules": in.Schedules != nil,
	} {
		if !set {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing) // map order is random; name them the same way every time
	if len(missing) > 0 {
		return nil, fmt.Errorf("deluge: BooksTaskLists: missing input(s): %v", missing)
	}
	deps, err := newBooksKindDeps(in)
	if err != nil {
		return nil, err
	}
	return (&Plugin{}).Kinds(deps, in.Schedules), nil
}

// booksKindRows is the shared table's rows for the two writes.
func booksKindRows() (writer.Rows, error) {
	shared := writer.SharedRows()
	rows := writer.Rows{}
	for _, m := range []string{"ModifyBookFile", "UpdateBookFile"} {
		row, ok := shared[m]
		if !ok {
			return nil, fmt.Errorf("deluge: no resource row for %s", m)
		}
		rows[m] = row
	}
	return rows, nil
}

func newBooksKindDeps(in BooksTaskInputs) (KindDeps, error) {
	rows, err := booksKindRows()
	if err != nil {
		return KindDeps{}, err
	}
	src := in.Source
	d := KindDeps{
		Store: src,
		NewWriter: func(w tasks.Writer) kindWriter {
			return &booksKindWriter{src: src, gate: writer.ForRun(w, rows)}
		},
		Cache:  in.Cache,
		Config: in.Config,
	}
	// A nil *Client stays an untyped-nil interface: the cores read "is Deluge
	// configured" as Client == nil, so a typed nil would claim it is.
	if in.Client != nil {
		d.Client = headDelugeClient{c: in.Client}
	}
	return d, nil
}

// booksKindWriter is one run's gated writes. A ReadOnly run has no writer (w is
// nil): every method then refuses instead of dereferencing nil.
type booksKindWriter struct {
	src  kindSourceWrites
	gate *writer.Gate
}

func (w *booksKindWriter) ModifyBookFile(bookID, fileID string, fn func(*database.BookFile) error) (*database.BookFile, error) {
	return writer.Value(context.Background(), w.gate, "ModifyBookFile", writer.Ref(string(ops.ResBookFiles), fileID), func() (*database.BookFile, error) {
		return w.src.ModifyBookFile(bookID, fileID, fn)
	})
}

func (w *booksKindWriter) UpdateBookFile(id string, file *database.BookFile) error {
	return w.gate.Err(context.Background(), "UpdateBookFile", writer.Ref(string(ops.ResBookFiles), id), func() error {
		return w.src.UpdateBookFile(id, file)
	})
}
