// file: internal/maintenance/jobs/backfill_book_files.go
// version: 1.8.0
// guid: a1000005-0000-0000-0000-000000000005
// last-edited: 2026-09-25

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"log/slog"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/bookfileaudio"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metafetch"
	ulid "github.com/oklog/ulid/v2"
)

func init() { maintenance.Register(&backfillBookFilesJob{}) }

type backfillBookFilesJob struct{}

type backfillBookFilesResult struct {
	DryRun         bool `json:"dry_run"`
	BooksScanned   int  `json:"books_scanned"`
	CandidateFiles int  `json:"candidate_files"`
	Created        int  `json:"created"`
	Errors         int  `json:"errors"`
}

func (j *backfillBookFilesJob) ID() string       { return "backfill-book-files" }
func (j *backfillBookFilesJob) Name() string     { return "Backfill Book Files" }
func (j *backfillBookFilesJob) Category() string { return "files" }
func (j *backfillBookFilesJob) DefaultParams() any {
	return struct {
		DryRun bool `json:"dry_run"`
	}{DryRun: true}
}
func (j *backfillBookFilesJob) Description() string {
	return "Create book_files rows for books that have none"
}
func (j *backfillBookFilesJob) CanResume() bool { return false }

// backfillBookFilesConcurrency sizes the worker pool: one per CPU, capped at 8
// because the per-row work is a header read of the library disk (I/O bound
// past a few readers), not CPU.
func backfillBookFilesConcurrency() int {
	return max(1, min(runtime.NumCPU(), 8))
}
func (j *backfillBookFilesJob) Run(ctx context.Context, store maintenance.JobStore, reporter maintenance.ProgressReporter, dryRun bool) error {
	books, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return err
	}
	reporter.SetTotal(len(books))
	result := backfillBookFilesResult{DryRun: dryRun, BooksScanned: len(books)}

	// Worker pool over books. Each new row now gets a duration header read (a
	// subprocess) before it is written, which on a sequential whole-library
	// loop is the single-core shape CLAUDE.md forbids. Workers are partitioned
	// by book: a worker only creates rows for the one book it holds, so no two
	// workers write the same book. mu guards result and reporter, which were
	// single-goroutine before.
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(backfillBookFilesConcurrency())
	for i := range books {
		if gctx.Err() != nil {
			break
		}
		book := &books[i]
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			mu.Lock()
			reporter.Increment()
			mu.Unlock()
			files, err := store.GetBookFiles(book.ID)
			if err != nil {
				return nil
			}
			if len(files) > 0 {
				return nil
			}
			audioFiles := backfillBookFilePaths(book.FilePath)
			newFiles := make([]*database.BookFile, 0, len(audioFiles))
			for _, fp := range audioFiles {
				newFiles = append(newFiles, &database.BookFile{
					ID:       ulid.Make().String(),
					BookID:   book.ID,
					FilePath: fp,
					Format:   filepath.Ext(fp),
				})
			}
			mu.Lock()
			result.CandidateFiles += len(newFiles)
			mu.Unlock()
			if dryRun || len(newFiles) == 0 {
				return nil
			}
			// Never write a readable file with Duration 0 (ABS sums row
			// durations). A single-file book may use its own duration; every
			// other row gets a bounded header read.
			known := bookfileaudio.Known{SingleFileBook: len(newFiles) == 1}
			if book.Duration != nil {
				known.BookDurationSec = *book.Duration
			}
			for _, bf := range newFiles {
				if st, serr := os.Stat(bf.FilePath); serr == nil {
					bf.FileSize = st.Size()
				}
				bookfileaudio.EnsureDuration(bf, known, nil)
			}
			if cerr := store.BatchCreateBookFiles(newFiles); cerr != nil {
				msg := cerr.Error()
				slog.Error("failed to create book files", "details", msg)
				mu.Lock()
				reporter.Log("error", "backfill-book-files: failed to create book_files", &msg)
				result.Errors += len(newFiles)
				mu.Unlock()
				return nil
			}
			mu.Lock()
			result.Created += len(newFiles)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	return saveBackfillBookFilesResult(ctx, store, result)
}

func backfillBookFilePaths(path string) []string {
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		return metafetch.AudioFilesInDir(path)
	}
	if !isBackfillableAudioFile(path) {
		return nil
	}
	return []string{path}
}

// isBackfillableAudioFile asks only "is this a library audio file?" — it reads
// no bytes and decodes nothing, so it resolves against the configured
// supported_extensions rather than a private list. The private list it used to
// hold knew 8 extensions and so skipped .aax/.aaxc/.aiff/.aif/.mka/.oga/.wav
// books entirely: they got no book_file rows backfilled, silently.
func isBackfillableAudioFile(path string) bool {
	return config.SupportedExtensionSet().MatchPath(path)
}

func saveBackfillBookFilesResult(ctx context.Context, store maintenance.JobStore, result backfillBookFilesResult) error {
	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("backfill-book-files: marshal result: %w", err)
	}
	encoded := string(payload)
	now := time.Now()
	status := "completed"
	var runErr error
	if result.Errors > 0 {
		status = "failed"
		runErr = fmt.Errorf("backfill-book-files: %d book_file row creation error(s)", result.Errors)
	}
	opLog := &database.OperationSummaryLog{
		ID:          maintenance.OperationIDFromCtx(ctx),
		Type:        "backfill-book-files",
		Status:      status,
		Progress:    1.0,
		Result:      &encoded,
		CreatedAt:   now,
		UpdatedAt:   now,
		CompletedAt: &now,
	}
	if err := store.SaveOperationSummaryLog(opLog); err != nil {
		return fmt.Errorf("backfill-book-files: save summary: %w", err)
	}
	slog.Info("backfill-book-files complete", "dry_run", result.DryRun, "books_scanned", result.BooksScanned,
		"candidate_files", result.CandidateFiles, "created", result.Created, "errors", result.Errors)
	return runErr
}

// Policy declares the bridge's existing behaviour verbatim: see DefaultPolicy.
func (j *backfillBookFilesJob) Policy() maintenance.ExecutionPolicy {
	return maintenance.DefaultPolicy()
}
