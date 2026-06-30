// file: internal/plugins/maintenance/intro_transcribe.go
// version: 3.5.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-06-30

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
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
	// ReparseOnly re-runs ParseAudiobookIntro over the ALREADY-STORED
	// IntroTranscription text and rewrites TranscribedTitle/Author/Narrator —
	// no ffmpeg, no Whisper. Use after a parser fix to correct existing books
	// (e.g. the "[Publisher] presents ..." / read-by extraction fix) without the
	// cost of re-transcribing. Cheap: one GetBookByID + UpdateBook per book.
	ReparseOnly *bool `json:"reparse_only,omitempty"`
}

func (p *Plugin) introTranscribeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.transcribe-book-intros",
		Plugin:          "maintenance",
		DisplayName:     "Transcribe book intros",
		Description:     "Extracts the first 90 seconds of each book's first audio file and transcribes it with Whisper. Stores the result in TranscribedTitle/Author/Narrator (separate from curated metadata) for disambiguation and dedup cross-checks. Uses batch mode: one Python process per page of 200 books loads the model once. 90s captures past Audible jingles/music intros that caused 30s clips to return only 'This is Audible.' Param reparse_only=true re-runs the parser over already-stored transcripts and rewrites the parsed fields with no ffmpeg/Whisper — use it to apply a parser fix to existing books cheaply.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.transcribe-book-intros",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         10 * time.Hour,
		// A page of 200 books sent to the (remote GPU or local) Whisper backend
		// can take several minutes to return. The default 5-minute no-progress
		// watchdog would cancel the op mid-batch — which it did, repeatedly,
		// cutting off in-flight requests to the remote GPU. Per-book progress
		// (see TranscribeBatch onProgress) normally ticks every ~1-2s on the
		// remote path; this generous timeout is the backstop for the local
		// single-subprocess fallback, which is silent until it completes.
		ProgressTimeout: 30 * time.Minute,
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
	reparseOnly := params.ReparseOnly != nil && *params.ReparseOnly

	log := reporter.Logger()

	// Reparse-only mode short-circuits the whole Whisper pipeline: it re-runs the
	// parser over stored transcripts and rewrites the parsed fields. Used to apply
	// a parser fix to existing books cheaply.
	if reparseOnly {
		ids, err := store.ListBookIDs()
		if err != nil {
			return fmt.Errorf("list book ids: %w", err)
		}
		return p.reparseStoredIntros(ctx, store, reporter, ids)
	}

	// Load the FULL, ordered list of book IDs up front. This is the proven
	// uncapped primitive (memdb ID-index walk). The previous implementation
	// paginated via GetAllBooksFrom, whose memdb path silently stopped after
	// 2*pageSize books — so only the first ~400 books of the library were ever
	// transcribed. See fix/transcribe-full-library.
	allIDs, err := store.ListBookIDs()
	if err != nil {
		return fmt.Errorf("list book ids: %w", err)
	}
	total := len(allIDs)

	// Resume support: skip past the last book ID checkpointed by a prior run.
	startIdx := 0
	if params.LastBookID != "" {
		for i, id := range allIDs {
			if id == params.LastBookID {
				startIdx = i + 1
				break
			}
		}
	}

	log.Info("transcribe-book-intros: starting batch run",
		"only_missing", onlyMissing, "total_books", total,
		"start_index", startIdx, "page_size", introTranscribePageSize)

	if startIdx >= total {
		_ = reporter.UpdateProgress(1, 1, "Done — nothing to transcribe")
		return nil
	}

	// Chunk the remaining IDs into pages. Each page → one Whisper batch process
	// (the whole point of batch mode: load the model once per 200 books).
	pages := chunkIDs(allIDs[startIdx:], introTranscribePageSize)

	var processed int // cumulative books transcribed across all pages
	var lastID string // last book ID seen — captured for the checkpoint closure

	// RunItems drives the page loop, handling ctx cancellation, per-page
	// progress, and checkpointing. The item is a PAGE of IDs (not a single
	// book) so we keep one Whisper process per 200 books. Sequential by design:
	// ffmpeg parallelism (16 workers) already lives inside processTranscribePage.
	err = registry.RunItems(ctx, reporter, pages, func(ctx context.Context, ids []string) error {
		books := make([]database.Book, 0, len(ids))
		for _, id := range ids {
			b, gerr := store.GetBookByID(id)
			if gerr != nil || b == nil {
				continue
			}
			lastID = id
			if onlyMissing && b.IntroTranscription != nil && *b.IntroTranscription != "" {
				continue // already transcribed — skip (cache still warm if re-run)
			}
			books = append(books, *b)
		}
		if len(books) == 0 {
			return nil
		}
		// Per-book heartbeat: each completed book bumps the operation's progress
		// clock so the watchdog sees liveness during a multi-minute batch (the
		// old code only reported once per page and got cancelled mid-batch).
		// base is the cumulative count before this page; d is books done within it.
		base := processed
		onBook := func(d, _ int) {
			_ = reporter.UpdateProgress(base+d, total,
				fmt.Sprintf("Transcribing — %d/%d books", base+d, total))
		}
		done := p.processTranscribePage(ctx, store, log, books, p.deps.RootDir(), onBook)
		processed += done
		log.Info("transcribe-book-intros: page complete",
			"page_books", done, "cumulative_processed", processed, "total_books", total)
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   1,
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d — %d books transcribed", i+1, t, processed)
		},
		CheckpointFn: func(ctx context.Context) error {
			om := onlyMissing
			return reporter.Checkpoint(introTranscribeParams{LastBookID: lastID, OnlyMissing: &om})
		},
	})
	if err != nil {
		return err
	}

	log.Info("transcribe-book-intros: complete", "processed", processed, "total_books", total)
	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf("Done — transcribed %d books", processed))
	return nil
}

// reparseStoredIntros re-runs ParseAudiobookIntro over every book's stored
// IntroTranscription and rewrites TranscribedTitle/Author/Narrator to the new
// parse. No ffmpeg, no Whisper — used to apply a parser fix to existing data.
// UpdateBook does full-column replacement, and GetBookByID returns the full
// row, so only the three parsed fields change. IntroTranscribedAt is left
// untouched (reparse is not a new transcription).
func (p *Plugin) reparseStoredIntros(ctx context.Context, store interface {
	ListBookIDs() ([]string, error)
	GetBookByID(string) (*database.Book, error)
	UpdateBook(string, *database.Book) (*database.Book, error)
}, reporter sdk.Reporter, ids []string) error {
	log := reporter.Logger()
	total := len(ids)
	var scanned, changed int
	for i, id := range ids {
		if i%500 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			_ = reporter.UpdateProgress(i, total,
				fmt.Sprintf("Reparsing intros — %d/%d (%d updated)", i, total, changed))
		}
		b, err := store.GetBookByID(id)
		if err != nil || b == nil || b.IntroTranscription == nil || *b.IntroTranscription == "" {
			continue
		}
		scanned++
		f := transcribe.ParseAudiobookIntro(*b.IntroTranscription)
		nt, na, nn := strPtrOrNil(f.Title), strPtrOrNil(f.Author), strPtrOrNil(f.Narrator)
		if eqStrPtr(b.TranscribedTitle, nt) && eqStrPtr(b.TranscribedAuthor, na) && eqStrPtr(b.TranscribedNarrator, nn) {
			continue // unchanged — skip the write
		}
		b.TranscribedTitle, b.TranscribedAuthor, b.TranscribedNarrator = nt, na, nn
		if _, err := store.UpdateBook(b.ID, b); err != nil {
			log.Warn("reparse-intros: update failed", "book_id", b.ID, "err", err)
			continue
		}
		changed++
	}
	log.Info("reparse-intros: complete", "scanned", scanned, "changed", changed, "total_books", total)
	_ = reporter.UpdateProgress(total, total,
		fmt.Sprintf("Reparse complete — %d updated of %d transcribed (%d books)", changed, scanned, total))
	return nil
}

// strPtrOrNil returns nil for empty/whitespace strings, else a pointer to the
// trimmed value. Keeps cleared parse results out of the DB as NULL.
func strPtrOrNil(s string) *string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return &s
}

// eqStrPtr reports whether two *string values are equal (both nil, or both set
// to the same string).
func eqStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// chunkIDs splits ids into consecutive slices of at most size elements.
func chunkIDs(ids []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	chunks := make([][]string, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := start + size
		if end > len(ids) {
			end = len(ids)
		}
		chunks = append(chunks, ids[start:end])
	}
	return chunks
}

// processTranscribePage handles one page of books:
// 1. Find the first audio file for each book.
// 2. Extract 90-second WAVs in parallel with ffmpeg (cached on disk).
// 3. Call TranscribeBatch once (single Python/Whisper process).
// 4. Parse results and update all books.
// 5. Clean up temp WAVs (cached clips persist).
// Returns the number of books successfully transcribed in this page. Errors are
// logged and treated as non-fatal so one bad page never aborts the whole run.
func (p *Plugin) processTranscribePage(
	ctx context.Context,
	store database.Store,
	log interface{ Info(string, ...any); Warn(string, ...any) },
	books []database.Book,
	rootDir string,
	onBook transcribe.ProgressFunc,
) (processed int) {
	cacheDir := wavCacheDir(rootDir)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		log.Warn("transcribe: cannot create clip cache dir, running without cache", "dir", cacheDir, "err", err)
		cacheDir = ""
	}

	// tmpDir holds WAVs for books whose source file has no stored hash (no caching).
	tmpDir, err := os.MkdirTemp("", "ao-transcribe-*")
	if err != nil {
		log.Warn("transcribe: create temp dir failed", "err", err)
		return 0
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
		src, hash, _, _ := firstAudioFile(store, b)
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
		return 0
	}

	// Step 3: one Python/Whisper process for the whole page.
	log.Info("transcribe-book-intros: calling whisper batch", "jobs", len(batchJobs))
	batchResults, err := transcribe.TranscribeBatch(ctx, batchJobs, onBook)
	if err != nil {
		log.Warn("transcribe: whisper batch failed", "jobs", len(batchJobs), "err", err)
		return 0
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

	return processed
}

// audioExtSet is the set of extensions treated as playable audio by firstAudioFile.
var audioExtSet = map[string]bool{
	".m4b": true, ".mp3": true, ".m4a": true, ".mp4": true,
	".flac": true, ".aac": true, ".ogg": true, ".wma": true,
}

// firstAudioFile returns the path, a stable cache key, and the BookFile ID for
// the first audio file of book. The BookFile ID is empty when falling back to
// the book-level FilePath (single-file iTunes imports with no BookFile rows).
// Cache key priority: FileHash (content SHA-256) > fp:AcoustIDFingerprint >
// path:SHA-256(FilePath) — path-keyed entries break when organize renames files.
func firstAudioFile(store database.Store, book database.Book) (path, cacheKey, bookFileID string, err error) {
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return "", "", "", err
	}

	var audio []database.BookFile
	for _, f := range files {
		if audioExtSet[strings.ToLower(filepath.Ext(f.FilePath))] && f.FilePath != "" {
			audio = append(audio, f)
		}
	}

	if len(audio) == 0 {
		// No BookFile rows — fall back to Book.FilePath for single-file imports
		// (iTunes ingestion sets book.FilePath to the track path but skips
		// creating BookFile records when there is only one track per album).
		fp := book.FilePath
		if fp != "" && audioExtSet[strings.ToLower(filepath.Ext(fp))] {
			h := sha256.Sum256([]byte(fp))
			return fp, "path:" + hex.EncodeToString(h[:]), "", nil
		}
		return "", "", "", nil
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
	key := f.FileHash
	if key == "" && len(f.AcoustIDFingerprint) > 0 {
		key = "fp:" + hex.EncodeToString(f.AcoustIDFingerprint)
	}
	if key == "" {
		h := sha256.Sum256([]byte(f.FilePath))
		key = "path:" + hex.EncodeToString(h[:])
	}
	return f.FilePath, key, f.ID, nil
}

// wavCacheDir returns the directory used to cache extracted 90s WAV clips.
// Env WHISPER_CLIP_CACHE_DIR overrides. Default: {rootDir}/.wav-cache.
// rootDir must not be empty; callers should pass p.deps.RootDir().
func wavCacheDir(rootDir string) string {
	if d := os.Getenv("WHISPER_CLIP_CACHE_DIR"); d != "" {
		return d
	}
	return filepath.Join(rootDir, ".wav-cache")
}

// cachedClipPath returns the cache file path for a given source file hash.
// Returns "" if hash is empty (cache disabled for that file).
func cachedClipPath(cacheDir, fileHash string) string {
	if fileHash == "" {
		return ""
	}
	return filepath.Join(cacheDir, fileHash+".wav")
}
