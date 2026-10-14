// file: pkg/ops/opstest/defs_test.go
// version: 1.0.0
// guid: 7c1e5a38-9b26-4f04-8d7a-2e6f3b9c5d17
// last-edited: 2026-10-14

package opstest

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The test defs. Every one runs over a World's books (synthetic data only:
// book-001.., file-001..) and is built by a function of the World's Books
// handle, which stays valid across World.Reset.

// common is the Common block every test def shares.
func common(title string, eff ops.Effects) ops.Common {
	return ops.Common{Title: title, Help: "opstest fixture.", Effects: eff, Permission: auth.PermLibraryEditMetadata}
}

// upperTitle is the correct, idempotent write every title def makes: the
// title upper-cased (a second run changes nothing).
func upperTitle(b *database.Book) error {
	b.Title = strings.ToUpper(b.Title)
	return nil
}

// bookSource is a Snapshot source over every book.
func bookSource(books *FakeBooks) func(*ops.RC, struct{}) (ops.Source[database.Book], error) {
	return func(*ops.RC, struct{}) (ops.Source[database.Book], error) {
		all, err := books.AllBooks()
		return ops.Source[database.Book]{Items: all, Key: func(b database.Book) string { return b.ID },
			Order: ops.Snapshot}, err
	}
}

type titleResult struct {
	Done    int64 `json:"done"`
	Changed int64 `json:"changed"`
}

func titleFinish(_ *ops.RC, _ struct{}, s ops.Summary) (titleResult, error) {
	return titleResult{Done: s.Done}, nil
}

// titleDef is the correct twin most mutation tests compare against: a
// Snapshot Batch that upper-cases every title through the Writer.
func titleDef(books *FakeBooks) ops.Definition {
	return ops.Batch("opstest.upper-title", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Upper-case titles", ops.Writes(ops.ResBooks)),
		Source: bookSource(books),
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			ch, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			if err == nil && ch.Any() {
				rc.Count("changed", 1)
			}
			return err
		},
		Finish: titleFinish,
	})
}

// partitionOf groups books two by two: book-001 and book-002 are p-00.
func partitionOf(b database.Book) string {
	var n int
	if _, err := fmt.Sscanf(b.ID, "book-%d", &n); err != nil {
		return b.ID
	}
	return fmt.Sprintf("p-%02d", (n-1)/2)
}

// partitionDef is titleDef with PartitionBy.
func partitionDef(books *FakeBooks) ops.Definition {
	return ops.Batch("opstest.upper-title-partitioned", ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common:      common("Upper-case titles by pair", ops.Writes(ops.ResBooks)),
		Source:      bookSource(books),
		PartitionBy: partitionOf,
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: titleFinish,
	})
}

// streamDef is titleDef over a paged source claiming order.
func streamDef(books *FakeBooks, order ops.SourceOrder) ops.Definition {
	return ops.Batch("opstest.upper-title-"+order.String(), ops.BatchSpec[struct{}, database.Book, titleResult]{
		Common: common("Upper-case titles, paged", ops.Writes(ops.ResBooks)),
		Source: func(*ops.RC, struct{}) (ops.Source[database.Book], error) {
			return ops.Source[database.Book]{
				Pages: func(_ context.Context, after string, limit int) ([]database.Book, string, error) {
					return books.Page(after, limit)
				},
				Key: func(b database.Book) string { return b.ID }, Order: order, Total: ops.Unknown(),
			}, nil
		},
		Item: func(rc *ops.ItemRC, _ struct{}, b database.Book) error {
			_, err := rc.Writer().ModifyBook(rc.Context(), b.ID, upperTitle)
			return err
		},
		Finish: titleFinish,
	})
}

// fenced reports a write the Writer refused because the run is fenced.
func fenced(err error) bool { return errors.Is(err, ops.ErrFenced) }
