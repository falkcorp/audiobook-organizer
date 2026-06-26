// file: internal/plugins/maintenance/intro_transcribe.go
// version: 2.4.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-26

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

const (
	introTranscribePageSize = 200
	introTranscribeFFWorkers = 16 // parallel ffmpeg extractions per page (I/O bound on read-optimized ZFS)
)

// introTranscribeParams is the checkpoint state for a transcription run.
type introTranscribeParams struct {
	LastBookID string `json:"last_book_id,omitempty"`
	// OnlyMissing skips books that already have an IntroTranscription.
	// Defaults to true so re-runs are incremental.
	OnlyMissing *bool `json:"only_missing,omitempty"`
}

func (p *Plugin) introTranscribeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.transcribe-book-intros",
		Plugin:          "maintenance",
		DisplayName:     "Transcribe book intros",
		Description:     "Extracts the first 90 seconds of each book's first audio file and transcribes it with Whisper. Stores the result in TranscribedTitle/Author/Narrator (separate from curated metadata) for disambiguation and dedup cross-checks. Uses batch mode: one Python process per page of 200 books loads the model once. 90s captures past Audible jingles/music intros that caused 30s clips to return only 'This is Audible.'",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.transcribe-book-intros",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapFilesRead},
		Run:             p.runIntroTranscribe,
	}
}

func (p *Plugin) runIntroTranscribe(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params introTranscribeParams
	if len(rawParams) > 0 {
		_ = json.Unmarshal(rawParams, &params)
	}
	onlyMissing := params.OnlyMissing == nil || *params.OnlyMissing

	log := reporter.Logger()
	log.Info("transcribe-book-intros: starting batch run",
		"only_missing", onlyMissing, "page_size", introTranscribePageSize)

	// Count total so we can report meaningful progress.
	// We re-count mid-run only when needed; approximate is fine.
	total := 0
	processed := 0
	cursor := params.LastBookID

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		// Load one page of books from the cursor position.
		page, err := store.GetAllBooksFrom(cursor, introTranscribePageSize)
		if err != nil {
			return fmt.Errorf("load books page: %w", err)
		}
		if len(page) == 0 {
			break
		}

		// Filter to only books that need transcription.
		var toProcess []database.Book
		for _, b := range page {
			if onlyMissing && b.IntroTranscription != nil && *b.IntroTranscription != "" {
				continue
			}
			toProcess = append(toProcess, b)
		}
		total += len(page)

		if len(toProcess) > 0 {
			done, err := p.processTranscribePage(ctx, store, log, reporter, toProcess, processed, total)
			processed += done
			if err != nil {
				// Non-fatal: log and continue to next page.
				log.Warn("transcribe-book-intros: page error", "err", err)
			}
		}

		if len(page) < introTranscribePageSize {
			break // last page
		}
		cursor = page[len(page)-1].ID
		// Persist cursor after each completed page so a server restart resumes
		// from here rather than scanning from book 0.
		_ = reporter.Checkpoint(introTranscribeParams{
			LastBookID:  cursor,
			OnlyMissing: &onlyMissing,
		})
	}

	log.Info("transcribe-book-intros: complete", "processed", processed)
	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf("Done — transcribed %d books", processed))
	return nil
}

// processTranscribePage handles one page of books:
// 1. Find the first audio file for each book.
// 2. Extract 90-second WAVs in parallel with ffmpeg.
// 3. Call TranscribeBatch once (single Python/Whisper process).
// 4. Parse results and update all books.
// 5. Clean up temp WAVs.
func (p *Plugin) processTranscribePage(
	ctx context.Context,
	store database.Store,
	log interface{ Info(string, ...any); Warn(string, ...any) },
	reporter sdk.Reporter,
	books []database.Book,
	progressOffset, progressTotal int,
) (processed int, err error) {
	cacheDir := whisperClipCacheDir()
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Warn("transcribe: cannot create clip cache dir, running without cache", "dir", cacheDir, "err", err)
		cacheDir = ""
	}

	// tmpDir holds WAVs for books whose source file has no stored hash (no caching).
	tmpDir, err := os.MkdirTemp("", "ao-transcribe-*")
	if err != nil {
		return 0, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Step 1: find first audio file + file hash for each book (fast DB reads, serial).
	type bookJob struct {
		book     database.Book
		audioSrc string
		fileHash string // empty → no cache key available
	}
	jobs := make([]bookJob, 0, len(books))
	for _, b := range books {
		src, hash, _ := firstAudioFile(store, b.ID)
		jobs = append(jobs, bookJob{book: b, audioSrc: src, fileHash: hash})
	}

	// Step 2: extract WAVs in parallel with bounded ffmpeg workers.
	// Cache hit: serve from cacheDir/{hash}.wav, skip ffmpeg entirely.
	// Cache miss: write ffmpeg output directly to cacheDir/{hash}.wav (or tmpDir if no hash).
	type wavResult struct {
		bookID   string
		wavPath  string // empty on failure
		fromCache bool
	}
	wavResults := make([]wavResult, len(jobs))
	sem := make(chan struct{}, introTranscribeFFWorkers)
	var wg sync.WaitGroup
	for i, j := range jobs {
		if j.audioSrc == "" {
			wavResults[i] = wavResult{bookID: j.book.ID}
			continue
		}
		wg.Add(1)
		go func(idx int, bookID, src, fileHash string) {
			defer wg.Done()

			// Cache hit — no semaphore needed, just a stat.
			if cacheDir != "" && fileHash != "" {
				cp := cachedClipPath(cacheDir, fileHash)
				if _, statErr := os.Stat(cp); statErr == nil {
					wavResults[idx] = wavResult{bookID: bookID, wavPath: cp, fromCache: true}
					return
				}
			}

			// Cache miss — run ffmpeg.
			sem <- struct{}{}
			defer func() { <-sem }()

			var wavPath string
			if cacheDir != "" && fileHash != "" {
				wavPath = cachedClipPath(cacheDir, fileHash)
			} else {
				wavPath = filepath.Join(tmpDir, bookID+".wav")
			}

			ffCmd := exec.CommandContext(ctx, "ffmpeg",
				"-y", "-i", src,
				"-t", "90",
				"-vn", "-ar", "16000", "-ac", "1", "-f", "wav",
				wavPath,
			)
			if out, err := ffCmd.CombinedOutput(); err != nil {
				log.Warn("transcribe: ffmpeg failed",
					"book_id", bookID, "file", src,
					"err", err, "output", strings.TrimSpace(string(out)))
				wavResults[idx] = wavResult{bookID: bookID}
				return
			}
			wavResults[idx] = wavResult{bookID: bookID, wavPath: wavPath}
		}(i, j.book.ID, j.audioSrc, j.fileHash)
	}
	wg.Wait()

	cacheHits := 0
	for _, r := range wavResults {
		if r.fromCache {
			cacheHits++
		}
	}
	if cacheHits > 0 {
		log.Info("transcribe: clip cache hits", "hits", cacheHits, "total", len(wavResults))
	}

	// Build the jobs map for TranscribeBatch (only books with valid WAVs).
	batchJobs := make(map[string]string)
	for _, r := range wavResults {
		if r.wavPath != "" {
			batchJobs[r.bookID] = r.wavPath
		}
	}
	if len(batchJobs) == 0 {
		return 0, nil
	}

	// Step 3: one Python/Whisper process for the whole page.
	log.Info("transcribe-book-intros: calling whisper batch", "jobs", len(batchJobs))
	batchResults, err := transcribe.TranscribeBatch(ctx, batchJobs)
	if err != nil {
		return 0, fmt.Errorf("whisper batch: %w", err)
	}

	// Step 4: parse results and update books.
	// Build a lookup so we can find the Book struct by ID.
	bookByID := make(map[string]database.Book, len(books))
	for _, b := range books {
		bookByID[b.ID] = b
	}

	now := time.Now()
	for bookID, result := range batchResults {
		if result.Error != "" {
			log.Warn("transcribe: whisper error", "book_id", bookID, "err", result.Error)
			continue
		}
		text := result.Text
		if text == "" {
			continue
		}

		book, ok := bookByID[bookID]
		if !ok {
			continue
		}

		fields := transcribe.ParseAudiobookIntro(text)
		book.IntroTranscription = &text
		book.IntroTranscribedAt = &now
		if fields.Title != "" {
			book.TranscribedTitle = &fields.Title
		}
		if fields.Author != "" {
			book.TranscribedAuthor = &fields.Author
		}
		if fields.Narrator != "" {
			book.TranscribedNarrator = &fields.Narrator
		}
		if _, err := store.UpdateBook(book.ID, &book); err != nil {
			log.Warn("transcribe: update failed", "book_id", bookID, "err", err)
			continue
		}
		processed++
	}

	_ = reporter.UpdateProgress(
		progressOffset+processed,
		progressTotal,
		fmt.Sprintf("Transcribed %d/%d books", progressOffset+processed, progressTotal),
	)
	return processed, nil
}

// firstAudioFile returns the path and stored file hash of the first audio file
// for the given book (lowest TrackNumber, falling back to alphabetical by FilePath).
func firstAudioFile(store database.Store, bookID string) (path, fileHash string, err error) {
	files, err := store.GetBookFiles(bookID)
	if err != nil || len(files) == 0 {
		return "", "", err
	}

	extSet := map[string]bool{
		".m4b": true, ".mp3": true, ".m4a": true,
		".flac": true, ".aac": true, ".ogg": true, ".wma": true,
	}
	var audio []database.BookFile
	for _, f := range files {
		if extSet[strings.ToLower(filepath.Ext(f.FilePath))] && f.FilePath != "" {
			audio = append(audio, f)
		}
	}
	if len(audio) == 0 {
		return "", "", nil
	}

	sort.Slice(audio, func(i, j int) bool {
		ti := audio[i].TrackNumber
		tj := audio[j].TrackNumber
		if ti != tj {
			return ti < tj
		}
		return audio[i].FilePath < audio[j].FilePath
	})
	f := audio[0]
	return f.FilePath, f.FileHash, nil
}

// whisperClipCacheDir returns the directory used to cache extracted 90s WAV clips.
// Set WHISPER_CLIP_CACHE_DIR to override; defaults to /var/lib/audiobook-organizer/whisper-clips.
func whisperClipCacheDir() string {
	if d := os.Getenv("WHISPER_CLIP_CACHE_DIR"); d != "" {
		return d
	}
	return "/var/lib/audiobook-organizer/whisper-clips"
}

// cachedClipPath returns the cache file path for a given source file hash.
// Returns "" if hash is empty (cache disabled for that file).
func cachedClipPath(cacheDir, fileHash string) string {
	if fileHash == "" {
		return ""
	}
	return filepath.Join(cacheDir, fileHash+".wav")
}
