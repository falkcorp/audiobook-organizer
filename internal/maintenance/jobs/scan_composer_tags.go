// file: internal/maintenance/jobs/scan_composer_tags.go
// version: 1.2.0
// guid: d9e5f3c4-6a7b-8c9d-0e1f-2a3b4c5d6e7f
// last-edited: 2026-07-07

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/maintenance"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
)

func init() { maintenance.Register(&scanComposerTagsJob{}) }

type scanComposerTagsJob struct{}

type sct_params struct {
	DryRun  bool   `json:"dry_run"`
	FixMode string `json:"fix_mode"` // "set_narrator" or "clear"
}

func (j *scanComposerTagsJob) ID() string       { return "scan-composer-tags" }
func (j *scanComposerTagsJob) Name() string     { return "Scan Composer Tags" }
func (j *scanComposerTagsJob) Category() string { return "Scanning" }
func (j *scanComposerTagsJob) Description() string {
	return "Bulk-scans COMPOSER tags on all audio files and optionally fixes them to match the narrator field"
}
func (j *scanComposerTagsJob) DefaultParams() any {
	return &sct_params{DryRun: true, FixMode: "set_narrator"}
}
func (j *scanComposerTagsJob) CanResume() bool { return true }

func (j *scanComposerTagsJob) Run(ctx context.Context, store database.Store, reporter maintenance.ProgressReporter, dryRun bool) error {
	opID := maintenance.OperationIDFromCtx(ctx)

	// Load fix_mode from persisted params when resuming.
	fixMode := "set_narrator"
	if opID != "" {
		if raw, err := store.GetOperationParams(opID); err == nil && len(raw) > 0 {
			var p sct_params
			if jerr := json.Unmarshal(raw, &p); jerr == nil && p.FixMode != "" {
				fixMode = p.FixMode
				dryRun = p.DryRun
			}
		}
	}

	allBooks, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}
	allAuthors, err := store.GetAllAuthors()
	if err != nil {
		return fmt.Errorf("GetAllAuthors: %w", err)
	}
	authorByID := make(map[int]string, len(allAuthors))
	for _, a := range allAuthors {
		authorByID[a.ID] = a.Name
	}
	allFiles, err := store.GetAllBookFilesCore()
	if err != nil {
		return fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	filesByBook := make(map[string][]database.BookFileCore, len(allFiles))
	for i := range allFiles {
		f := &allFiles[i]
		filesByBook[f.BookID] = append(filesByBook[f.BookID], *f)
	}

	// Skip already-processed files from a prior interrupted run.
	var existingResults []database.OperationResult
	if opID != "" {
		existingResults, _ = store.GetOperationResults(opID)
	}
	done := make(map[string]bool, len(existingResults))
	for _, r := range existingResults {
		done[r.BookID] = true // BookID stores file path for this op
	}

	audioExts := map[string]bool{".m4b": true, ".m4a": true, ".mp3": true, ".flac": true, ".ogg": true}
	var workItems []sct_work
	for i := range allBooks {
		b := &allBooks[i]
		author := ""
		if b.AuthorID != nil {
			author = authorByID[*b.AuthorID]
		}
		narrator := ""
		if b.Narrator != nil {
			narrator = *b.Narrator
		}
		for _, f := range filesByBook[b.ID] {
			if f.FilePath == "" || f.Missing {
				continue
			}
			if !audioExts[strings.ToLower(filepath.Ext(f.FilePath))] {
				continue
			}
			if done[f.FilePath] {
				continue
			}
			workItems = append(workItems, sct_work{
				bookID:    b.ID,
				bookTitle: b.Title,
				filePath:  f.FilePath,
				author:    author,
				narrator:  narrator,
			})
		}
	}

	totalFiles := len(existingResults) + len(workItems)
	alreadyDone := len(existingResults)
	slog.Info("scan-composer-tags files total, already done, to process", "opID", opID, "totalFiles", totalFiles, "alreadyDone", alreadyDone, "workItems_count", len(workItems))

	reporter.SetTotal(totalFiles)
	for i := 0; i < alreadyDone; i++ {
		reporter.Increment()
	}

	if len(workItems) == 0 {
		slog.Info("all files already processed")
		return nil
	}

	const workers = 8
	workCh := make(chan sct_work, len(workItems))
	for _, w := range workItems {
		workCh <- w
	}
	close(workCh)

	var completed int64 = int64(alreadyDone)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range workCh {
				if ctx.Err() != nil {
					return
				}
				if _, statErr := os.Stat(w.filePath); statErr != nil {
					if opID != "" {
						_ = store.CreateOperationResult(&database.OperationResult{
							OperationID: opID,
							BookID:      w.filePath,
							ResultJSON:  `{"category":"missing"}`,
							Status:      "missing",
						})
					}
					atomic.AddInt64(&completed, 1)
					mu.Lock()
					reporter.Increment()
					mu.Unlock()
					continue
				}

				tags, readErr := metadata.ReadRawTags(w.filePath)
				var r sct_result
				if readErr != nil {
					r = sct_result{
						BookID: w.bookID, BookTitle: w.bookTitle, FilePath: w.filePath,
						Category: "read_error", Error: readErr.Error(),
					}
				} else {
					composer := ""
					if vs, ok := tags["COMPOSER"]; ok && len(vs) > 0 {
						composer = strings.TrimSpace(vs[0])
					}
					category, willWrite := sct_categorize(composer, w.author, w.narrator, fixMode)
					r = sct_result{
						BookID: w.bookID, BookTitle: w.bookTitle, FilePath: w.filePath,
						Category: category, Composer: composer,
						Author: w.author, Narrator: w.narrator, WillWrite: willWrite,
					}
					if !dryRun && category != "ok" && willWrite != composer {
						if writeErr := metadata.WriteSingleTag(w.filePath, "COMPOSER", willWrite); writeErr != nil {
							r.Error = writeErr.Error()
							slog.Warn("scan-composer-tags write failed", "opID", opID, "w", w.filePath, "writeErr", writeErr)
						} else {
							r.Applied = true
							slog.Info("scan-composer-tags COMPOSER", "opID", opID, "composer", composer, "newValue", willWrite, "filePath", w.filePath)
						}
					}
				}

				if opID != "" {
					resultJSON, _ := json.Marshal(r)
					_ = store.CreateOperationResult(&database.OperationResult{
						OperationID: opID,
						BookID:      w.filePath,
						ResultJSON:  string(resultJSON),
						Status:      r.Category,
					})
				}

				atomic.AddInt64(&completed, 1)
				mu.Lock()
				reporter.Increment()
				mu.Unlock()
			}
		}()
	}

	wg.Wait()

	finalCount := atomic.LoadInt64(&completed)
	slog.Info("scan-composer-tags finished / files", "opID", opID, "finalCount", finalCount, "totalFiles", totalFiles)
	slog.Info("scan complete processed / files", "finalCount", finalCount, "totalFiles", totalFiles)
	return nil
}

// sct_result describes the COMPOSER field state for one audio file.
type sct_result struct {
	BookID    string `json:"book_id"`
	BookTitle string `json:"book_title"`
	FilePath  string `json:"file_path"`
	Category  string `json:"category"`
	Composer  string `json:"composer_on_disk"`
	Author    string `json:"author,omitempty"`
	Narrator  string `json:"narrator,omitempty"`
	WillWrite string `json:"will_write,omitempty"`
	Applied   bool   `json:"applied,omitempty"`
	Error     string `json:"error,omitempty"`
}

// sct_work is one unit of work dispatched to the parallel reader pool.
type sct_work struct {
	bookID    string
	bookTitle string
	filePath  string
	author    string
	narrator  string
}

// sct_categorize returns the problem category and the value that should
// be written in the given fix_mode ("set_narrator" or "clear").
func sct_categorize(composer, author, narrator, fixMode string) (category, willWrite string) {
	composerLower := strings.ToLower(strings.TrimSpace(composer))
	authorLower := strings.ToLower(strings.TrimSpace(author))
	narratorLower := strings.ToLower(strings.TrimSpace(narrator))

	if fixMode == "set_narrator" {
		willWrite = strings.TrimSpace(narrator)
	} else {
		willWrite = ""
	}

	if strings.TrimSpace(composer) == "" {
		if fixMode == "set_narrator" && strings.TrimSpace(narrator) != "" {
			return "missing_narrator", strings.TrimSpace(narrator)
		}
		return "ok", ""
	}

	if author != "" && composerLower == authorLower {
		return "composer_equals_author", willWrite
	}
	if narrator != "" && composerLower == narratorLower {
		if fixMode == "set_narrator" {
			return "ok", strings.TrimSpace(narrator)
		}
		return "composer_equals_narrator", ""
	}
	return "composer_mismatch", willWrite
}
