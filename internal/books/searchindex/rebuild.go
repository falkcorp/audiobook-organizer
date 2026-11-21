// file: internal/books/searchindex/rebuild.go
// version: 0.1.0
// guid: 0c7e1604-8236-4812-b668-60aedc41cda0
// last-edited: 2026-11-20

package searchindex

import (
	"context"
	"errors"
	"fmt"
	"runtime"

	"github.com/falkcorp/mediacore/storage/index"
	"golang.org/x/sync/errgroup"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/search"
)

const (
	// DefaultPageSize is how many books one keyset page reads.
	DefaultPageSize = 500
	// DefaultChunkSize is how many books one worker resolves and indexes at a
	// time: three batch reads of relations per chunk, never one per book.
	DefaultChunkSize = 100
)

// Options tune a Spec; the zero value is production's.
type Options struct {
	// Workers bounds the rebuild's worker pool; 0 means runtime.NumCPU().
	Workers int
	// PageSize and ChunkSize: 0 means the defaults. ChunkSize is clamped to
	// PageSize.
	PageSize, ChunkSize int
}

func (o Options) resolved() (workers, page, chunk int) {
	workers, page, chunk = o.Workers, o.PageSize, o.ChunkSize
	if workers < 1 {
		workers = runtime.NumCPU()
	}
	if page < 1 {
		page = DefaultPageSize
	}
	if chunk < 1 || chunk > page {
		chunk = min(DefaultChunkSize, page)
	}
	return workers, page, chunk
}

// NewSpec is the books full-text index declaration over src.
func NewSpec(src Source, opts Options) index.Spec {
	return index.Spec{
		Name:           Name,
		MappingVersion: MappingVersion,
		Fields:         Fields(),
		Rebuild: func(ctx context.Context, w index.RebuildWriter) error {
			return rebuild(ctx, src, w, opts)
		},
		Document: func(ctx context.Context, id string) (index.Doc, bool, error) {
			return document(ctx, src, id)
		},
	}
}

// rebuild walks the whole library and writes every live book's document.
//
// Shape (repo CLAUDE.md, "Concurrency"): one producer pages the keyset cursor
// serially and hands each page's chunks to an errgroup bounded at workers; g.Go
// blocks while every worker is busy, so paging never runs ahead of indexing and
// at most a few pages are resident. A worker does three batch relation reads for
// its chunk, builds the documents with no store call, and Puts them
// (RebuildWriter.Put is concurrency-safe). Chunks are disjoint slices of one
// page and a book ID is in exactly one chunk, so two workers never write the
// same document.
//
// A failed relation read fails the rebuild rather than indexing books without
// their author, series or tags: a rebuild is retryable, and a document built
// from partial relations is wrong with no signal. (HEAD's startup backfill logs
// and indexes the chunk; it has the dirty-set reconciler behind it, which the
// rebuild does not.)
func rebuild(ctx context.Context, src Source, w index.RebuildWriter, opts Options) error {
	workers, pageSize, chunkSize := opts.resolved()
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(workers)

	var pageErr error
	afterID := ""
producer:
	for {
		if err := gctx.Err(); err != nil {
			pageErr = err
			break
		}
		books, err := src.GetAllBooksFullFrom(afterID, pageSize)
		if err != nil {
			pageErr = fmt.Errorf("searchindex: read books after %q: %w", afterID, err)
			break
		}
		if len(books) == 0 {
			break
		}
		for lo := 0; lo < len(books); lo += chunkSize {
			hi := min(lo+chunkSize, len(books))
			chunk := books[lo:hi:hi]
			g.Go(func() error { return indexChunk(gctx, src, w, chunk) })
			if gctx.Err() != nil {
				break producer
			}
		}
		afterID = books[len(books)-1].ID
		if len(books) < pageSize {
			break
		}
	}
	err := g.Wait()
	if err != nil {
		// A worker's error is the cause; the producer's context error is its
		// echo.
		return err
	}
	return pageErr
}

func indexChunk(ctx context.Context, src Source, w index.RebuildWriter, books []database.Book) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	live := books[:0:0]
	for i := range books {
		if !books[i].IsSoftDeleted() {
			live = append(live, books[i])
		}
	}
	if len(live) == 0 {
		return nil
	}
	rel, err := search.LoadBookRelations(src, live)
	if err != nil {
		return fmt.Errorf("searchindex: relations of %d books from %q: %w", len(live), live[0].ID, err)
	}
	for i := range live {
		if err := w.Put(live[i].ID, Document(&live[i], rel)); err != nil {
			return fmt.Errorf("searchindex: put %q: %w", live[i].ID, err)
		}
	}
	return nil
}

// document is Spec.Document: one book's document, or false when the book is
// gone or soft-deleted, which makes the reconciler delete it from the index.
func document(ctx context.Context, src Source, id string) (index.Doc, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	b, err := src.GetBookByID(id)
	if err != nil {
		return nil, false, fmt.Errorf("searchindex: read book %q: %w", id, err)
	}
	if b == nil || b.IsSoftDeleted() {
		return nil, false, nil
	}
	rel, err := search.LoadBookRelations(src, []database.Book{*b})
	if err != nil {
		return nil, false, errors.Join(fmt.Errorf("searchindex: relations of book %q", id), err)
	}
	return Document(b, rel), true, nil
}
