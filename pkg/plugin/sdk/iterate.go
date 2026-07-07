// file: pkg/plugin/sdk/iterate.go
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef0123456789
// last-edited: 2026-07-07

package sdk

import (
	"context"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// BookStore is the narrow interface PageBooks needs — a paginated book reader.
//
// GetAllBooksFullFrom (not GetAllBooksCore): PageBooks's callback hands full
// database.Book values to arbitrary external plugins (the public SDK
// contract), so the internal fetch must always return full-fidelity rows —
// under prod's memdb-backed default, GetAllBooksCore/GetAllBooks would return
// heavy-field-nil'd (Description/BookSig*/Author/Series) projections here,
// silently breaking any plugin that reads those fields. GetAllBooksFullFrom
// bypasses memdb and reads the authoritative Pebble row directly (see
// server_search.go's backfill loop for the same cursor pattern).
type BookStore interface {
	GetAllBooksFullFrom(afterID string, limit int) ([]database.Book, error)
}

// PageBooks calls fn for every book in the library, paging at pageSize.
//
// During each blocking GetAllBooksFullFrom call, a keepalive goroutine fires
// reporter.UpdateProgress every 60s so the stuck-op watchdog does not cancel
// the operation while the DB read is in progress (Scenario B: memdb rebuild or
// raidz2 full scan taking longer than ProgressTimeout).
//
// With the in-memory atomic clock fix in place, those UpdateProgress stamps
// are lock-free and instant even under PebbleDB compaction.
//
// pageSize of 0 defaults to 500. fn returning a non-nil error stops iteration
// and that error is returned; context cancellation is also checked between pages.
func PageBooks(
	ctx context.Context,
	store BookStore,
	reporter Reporter,
	pageSize int,
	fn func(database.Book) error,
) error {
	if pageSize <= 0 {
		pageSize = 500
	}
	afterID := ""
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Start keepalive goroutine only for the duration of the
		// GetAllBooksFullFrom call. close(stop) immediately after the call so
		// we never over-stamp progress while fn is doing real work.
		stop := make(chan struct{})
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-stop:
					return
				case <-ctx.Done():
					return
				case <-ticker.C:
					_ = reporter.UpdateProgress(0, 0, "")
				}
			}
		}()

		books, err := store.GetAllBooksFullFrom(afterID, pageSize)
		close(stop) // keepalive exits immediately after DB read completes

		if err != nil {
			return err
		}
		if len(books) == 0 {
			return nil
		}

		for _, b := range books {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err := fn(b); err != nil {
				return err
			}
		}

		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			return nil // last page
		}
	}
}
