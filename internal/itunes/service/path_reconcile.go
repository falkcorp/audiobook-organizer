// file: internal/itunes/service/path_reconcile.go
// version: 2.3.0
// guid: 9e3b7a1d-4c2f-4a60-b8d5-2f1e8c0d9a47
// last-edited: 2026-07-05
//
// One-time (repeatable) backfill that walks every book with an
// iTunes persistent ID, recomputes book_files.ITunesPath from the
// current FilePath, and enqueues the
// writeback batcher. Fixes libraries where organize ran before the
// path-update bug was patched and iTunes now shows "missing files"
// for books that were moved under the hood.

package itunesservice

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	"github.com/falkcorp/audiobook-organizer/internal/operations"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// pathReconcilerStore is the narrow slice of the service's Store that
// PathReconciler needs.
type pathReconcilerStore interface {
	database.BookStore
	database.BookFileStore
	database.OperationStore
}

// PathReconciler walks iTunes-tracked books, recomputes their
// ITunesPath fields from the current FilePath, and enqueues the
// write-back batcher so the ITL learns the new locations.
type PathReconciler struct {
	store    pathReconcilerStore
	enqueuer Enqueuer
}

// newPathReconciler wires a PathReconciler with the given store and enqueuer.
// nil enqueuer just skips the write-back enqueue step (useful for tests).
func newPathReconciler(store pathReconcilerStore, enqueuer Enqueuer) *PathReconciler {
	return &PathReconciler{store: store, enqueuer: enqueuer}
}

// iTunesPathReconcileResult is the per-run tally returned in
// progress logs. Exported so the future handler-level test can
// assert on it.
type iTunesPathReconcileResult struct {
	Scanned          int `json:"scanned"`
	ITunesTracked    int `json:"itunes_tracked"`
	PathsUpdated     int `json:"paths_updated"`
	FilePathsUpdated int `json:"file_paths_updated"`
	EnqueuedForWrite int `json:"enqueued_for_write"`
	Errors           int `json:"errors"`
}

// Reconcile is the operation body. Read-only over iTunes PIDs (PIDs
// are not changed), read-write over ITunesPath fields. Idempotent —
// safe to re-run. Skips books without an iTunes PID.
func (r *PathReconciler) Reconcile(ctx context.Context, opID string, progress operations.ProgressReporter) error {
	if r.store == nil {
		return fmt.Errorf("database not initialized")
	}

	_ = progress.Log("info", "Starting iTunes path reconcile", nil)

	// Load all books — 100k is the same cap other maintenance ops use.
	books, err := r.store.GetAllBooks(100000, 0)
	if err != nil {
		return fmt.Errorf("load books: %w", err)
	}

	result := iTunesPathReconcileResult{Scanned: len(books)}
	_ = progress.Log("info", fmt.Sprintf("Reconciling iTunes paths for %d books", len(books)), nil)
	_ = progress.UpdateProgress(0, len(books), "Scanning books for iTunes PID coverage")

	// Wrap ProgressReporter to implement registry.Reporter interface for RunItems.
	reporterAdapter := &progressReporterAdapter{underlying: progress}

	// Guard shared state with mutex
	var resultMu sync.Mutex

	// Use RunItems to parallelize per-book processing.
	// Concurrency defaults to runtime.NumCPU() for DB-read-bound work.
	err = registry.RunItems(ctx, reporterAdapter, books, func(ctx context.Context, b database.Book) error {
		hasITunesBook := b.ITunesPersistentID != nil && *b.ITunesPersistentID != ""

		bookFiles, _ := r.store.GetBookFiles(b.ID)
		hasITunesFile := false
		for _, bf := range bookFiles {
			if bf.ITunesPersistentID != "" {
				hasITunesFile = true
				break
			}
		}

		if !hasITunesBook && !hasITunesFile {
			return nil
		}

		// Accumulate counters under lock to prevent data races.
		resultMu.Lock()
		result.ITunesTracked++
		resultMu.Unlock()

		for _, bf := range bookFiles {
			if bf.ITunesPersistentID == "" || bf.FilePath == "" {
				continue
			}
			want := metafetch.ComputeITunesPath(bf.FilePath)
			if want == "" || want == bf.ITunesPath {
				continue
			}
			bf.ITunesPath = want
			if err := r.store.UpdateBookFile(bf.ID, &bf); err != nil {
				resultMu.Lock()
				result.Errors++
				resultMu.Unlock()
				_ = progress.Log("warn", fmt.Sprintf("update book_file %s: %v", bf.ID, err), nil)
				continue
			}
			resultMu.Lock()
			result.FilePathsUpdated++
			resultMu.Unlock()
		}

		if r.enqueuer != nil {
			r.enqueuer.Enqueue(b.ID)
			resultMu.Lock()
			result.EnqueuedForWrite++
			resultMu.Unlock()
		}

		return nil
	}, registry.RunItemsOptions{
		Concurrency: runtime.NumCPU(),
	})
	if err != nil {
		return err
	}

	if r.enqueuer != nil && result.EnqueuedForWrite > 0 {
		time.Sleep(200 * time.Millisecond)
	}

	_ = operations.ClearState(r.store, opID)
	summary := fmt.Sprintf(
		"iTunes path reconcile complete: scanned=%d iTunes-tracked=%d book-paths-updated=%d file-paths-updated=%d enqueued=%d errors=%d",
		result.Scanned, result.ITunesTracked, result.PathsUpdated, result.FilePathsUpdated, result.EnqueuedForWrite, result.Errors,
	)
	_ = progress.Log("info", summary, nil)
	_ = progress.UpdateProgress(len(books), len(books), summary)
	return nil
}

// progressReporterAdapter implements registry.Reporter by wrapping
// operations.ProgressReporter and providing stub implementations for
// additional registry.Reporter methods.
type progressReporterAdapter struct {
	underlying operations.ProgressReporter
}

func (a *progressReporterAdapter) UpdateProgress(current, total int, message string) error {
	return a.underlying.UpdateProgress(current, total, message)
}

func (a *progressReporterAdapter) Log(level slog.Level, message string, attrs ...slog.Attr) error {
	// Convert registry log call to operations.ProgressReporter format.
	// The registry uses slog.Level, but operations.ProgressReporter uses string.
	levelStr := level.String()
	return a.underlying.Log(levelStr, message, nil)
}

func (a *progressReporterAdapter) Logger() *slog.Logger {
	// Return a no-op logger for registry.Reporter compatibility.
	// The returned logger is safe to use but discards all output.
	return slog.Default()
}

func (a *progressReporterAdapter) Checkpoint(state interface{}) error {
	// No-op checkpoint for this adapter.
	return nil
}

func (a *progressReporterAdapter) IsCanceled() bool {
	return a.underlying.IsCanceled()
}

func (a *progressReporterAdapter) RunPhase(ctx context.Context, name string, fn func(context.Context, registry.Reporter) error) error {
	// For this simple adapter, just run the function directly.
	return fn(ctx, a)
}

func (a *progressReporterAdapter) Trigger(ctx context.Context, eventName string, payload any) error {
	// No-op trigger for this adapter.
	return nil
}

func (a *progressReporterAdapter) SetCurrentItem(label string) {
	// No-op SetCurrentItem for this adapter.
}
