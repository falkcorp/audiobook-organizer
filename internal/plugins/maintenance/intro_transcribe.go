// file: internal/plugins/maintenance/intro_transcribe.go
// version: 3.16.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-08-07

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// transcribeBatchFn is a test seam over transcribe.TranscribeBatch so tests can
// substitute a stub (e.g. one returning *transcribe.TransportError) without a
// live Whisper endpoint. Production code never reassigns it.
var transcribeBatchFn = transcribe.TranscribeBatch

const (
	introTranscribePageSize  = 200
	introTranscribeFFWorkers = 8 // parallel ffmpeg per page; 6 pages run concurrently → 48 total
	introTranscribePageConc  = 6 // pages in parallel: 48 concurrent ffmpeg (= server core count)
	//                              and 6 concurrent Whisper batches to keep the GPU saturated.
	//                              Tuned for the 48-core prod server (<server>); audio decode
	//                              is I/O-light so one ffmpeg per core is the practical ceiling.

	// silenceSentinel is stored as IntroTranscription when all retry attempts
	// (longer clip + second audio file) return 0 chars from Whisper. It lets
	// only_missing=true skip these books on future runs without re-trying them
	// every time. Use retry_silence=true to force a fresh attempt.
	//
	// Aliased from internal/transcribe rather than redeclared: the classifier
	// must map this exact literal to IntroKindUnknown/"silence-sentinel", and two
	// independent declarations could drift apart silently — turning "known
	// silent" into "unparsed prose" across the library.
	silenceSentinel = transcribe.SilenceSentinel
)

// introTranscribeParams is the checkpoint state for a transcription run.
type introTranscribeParams struct {
	LastBookID string `json:"last_book_id,omitempty"`
	// OnlyMissing skips books that already have an IntroTranscription (including
	// the [SILENCE] sentinel). Defaults to true so re-runs are incremental.
	OnlyMissing *bool `json:"only_missing,omitempty"`
	// RetrySilence includes books marked [SILENCE] for another attempt. Useful
	// after adding more audio files or tuning the VAD threshold.
	RetrySilence *bool `json:"retry_silence,omitempty"`
	// ReparseOnly re-runs the classifier over the ALREADY-STORED
	// IntroTranscription text and UPGRADES TranscribedTitle/Author/Narrator —
	// no ffmpeg, no Whisper. Use after a parser fix to correct existing books
	// (e.g. the leaked "written by" verb) without the cost of re-transcribing.
	// Cheap: one GetBookByID + UpdateBook per changed book.
	//
	// It only ever upgrades. A non-credits verdict leaves the stored fields
	// alone, because ~1.4% of books hold a parse their current transcript can no
	// longer regenerate. See the guard in reparseStoredIntros.
	ReparseOnly *bool `json:"reparse_only,omitempty"`
	// ExtractOnly rebuilds the WAV clip cache WITHOUT calling Whisper. ffmpeg runs
	// at full concurrency (48 on the prod server) and is no longer gated by the
	// single GPU, so the whole library's clips re-extract in ~30-60 min instead of
	// the ~2 days a full GPU re-transcribe takes. Use after moving/clearing the
	// cache: extract-only first (fast, CPU-bound), then a normal run transcribes
	// off the warm cache (cache hits → no ffmpeg, only the GPU step remains).
	ExtractOnly *bool `json:"extract_only,omitempty"`
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
		// GetBookByID→mutate→UpdateBook whole-row write-back on Book rows.
		Writes:      []sdk.Resource{sdk.ResBooks},
		Cancellable: true,
		Isolate:     false,
		Timeout:     10 * time.Hour,
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
	retrySilence := params.RetrySilence != nil && *params.RetrySilence
	reparseOnly := params.ReparseOnly != nil && *params.ReparseOnly
	extractOnly := params.ExtractOnly != nil && *params.ExtractOnly

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
	// paginated via GetAllBooksFullFrom, whose memdb path silently stopped after
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

	// Live aggregate: the op records per-outcome counts into accum and flushes
	// them to the stats:transcribe PebbleDB key after each page so an external
	// monitor reads one key instead of scanning books or scraping op logs. The
	// sink is best-effort — when the store doesn't implement TranscribeStatsStore
	// (e.g. a test stub) counts are tracked in memory only.
	// The live store is often a wrapper (memdb/query layer) around *PebbleStore,
	// so a direct assertion misses the capability — unwrap one level like the
	// HTTP handlers do. Without this the sink stays nil and stats:transcribe is
	// never written (the aggregate endpoint returns null even while the op runs).
	var statsSink database.TranscribeStatsStore
	if s, ok := store.(database.TranscribeStatsStore); ok {
		statsSink = s
	} else if uw, ok := store.(interface{ Unwrap() database.Store }); ok {
		if s, ok2 := uw.Unwrap().(database.TranscribeStatsStore); ok2 {
			statsSink = s
		}
	}
	startedAt := time.Now()
	accum := newTranscribeStatsAccum(statsSink, startedAt.Format(time.RFC3339), total, startedAt)
	accum.flush(false) // initial write: monitor sees the run has started

	// processed and lastID are written by concurrent page goroutines — must be
	// thread-safe. processed uses atomic arithmetic; lastID uses a mutex because
	// string assignment is not atomic (pointer + length, two words).
	var processed atomic.Int64
	var lastIDMu sync.Mutex
	var lastID string

	// RunItems drives the page loop with introTranscribePageConc pages in flight
	// at once. Each page runs its own ffmpeg workers (introTranscribeFFWorkers),
	// so total concurrent ffmpeg extractions = pageConc × ffWorkers (32 at default
	// settings). Pages send independent batches to the Whisper server; the server's
	// BatchedInferencePipeline queues them and the GPU processes overlapping small
	// batches far more efficiently than one large serial one.
	err = registry.RunItems(ctx, reporter, pages, func(ctx context.Context, ids []string) error {
		books := make([]database.Book, 0, len(ids))
		skipped := 0
		for _, id := range ids {
			b, gerr := store.GetBookByID(id)
			if gerr != nil || b == nil {
				continue
			}
			lastIDMu.Lock()
			lastID = id
			lastIDMu.Unlock()
			// extract-only ignores onlyMissing: the whole point is to (re)build the
			// WAV cache for EVERY book, regardless of transcription status.
			if !extractOnly && onlyMissing && b.IntroTranscription != nil && *b.IntroTranscription != "" {
				// [SILENCE] books are skipped normally; retry_silence=true includes them.
				if *b.IntroTranscription != silenceSentinel || !retrySilence {
					skipped++
					continue
				}
			}
			books = append(books, *b)
		}
		accum.recordSkipped(skipped)
		if len(books) == 0 {
			accum.flush(false)
			return nil
		}
		// Per-book heartbeat: each completed book bumps the operation's progress
		// clock so the watchdog sees liveness during a multi-minute batch.
		// base is a snapshot of the cumulative count before this page starts; it
		// is read once (atomic load) so the closure captures the right baseline
		// even when other pages increment processed concurrently.
		base := int(processed.Load())
		verb := "Transcribing"
		if extractOnly {
			verb = "Extracting"
		}
		onBook := func(d, _ int) {
			cur := base + d
			_ = reporter.UpdateProgress(cur, total,
				fmt.Sprintf("%s — %d/%d books", verb, cur, total))
		}
		done := p.processTranscribePage(ctx, store, log, books, p.deps.RootDir(), onBook, accum, extractOnly)
		cum := int(processed.Add(int64(done)))
		accum.flush(false) // persist cumulative counts so the monitor sees live progress
		log.Info("transcribe-book-intros: page complete",
			"page_books", done, "cumulative_processed", cum, "total_books", total)
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   introTranscribePageConc,
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return fmt.Sprintf("Page %d/%d — %d books transcribed", i+1, t, int(processed.Load()))
		},
		CheckpointFn: func(ctx context.Context) error {
			lastIDMu.Lock()
			lid := lastID
			lastIDMu.Unlock()
			om := onlyMissing
			return reporter.Checkpoint(introTranscribeParams{LastBookID: lid, OnlyMissing: &om})
		},
	})
	if err != nil {
		// Mark the run done even on error so the monitor stops treating it as
		// in-flight; the counts reflect whatever was processed before the error.
		accum.flush(true)
		return err
	}

	accum.flush(true)
	st := accum.snapshot()
	total64 := int(processed.Load())
	log.Info("transcribe-book-intros: complete",
		"processed", total64, "total_books", total,
		"ok", st.OK, "source_missing", st.SourceMissing, "no_audio", st.NoAudio,
		"ffmpeg_error", st.FFmpegError, "whisper_error", st.WhisperError,
		"empty", st.Empty, "skipped_existing", st.SkippedExisting, "cache_hits", st.CacheHits)
	_ = reporter.UpdateProgress(1, 1, fmt.Sprintf(
		"Done — %d ok, %d source-missing, %d ffmpeg-err, %d whisper-err, %d empty (of %d total)",
		st.OK, st.SourceMissing, st.FFmpegError, st.WhisperError, st.Empty, total))
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
		c := transcribe.ClassifyIntro(*b.IntroTranscription, transcribe.UnknownPosition)

		// 🔴 REPARSE ONLY EVER UPGRADES. It never clears a stored parse.
		//
		// The stored parsed fields are NOT always reproducible from the stored
		// transcript. applyOutcome overwrites IntroTranscription unconditionally
		// but writes parsed fields only when a title was extracted, so a later,
		// WORSE transcription ("This is Audible.") replaces good text while the
		// good parse survives beside it. Measured on prod 2026-08-07 over 987
		// sampled books: 1.4% (~644 library-wide) hold a parse their current
		// transcript cannot regenerate — e.g. a book whose transcript is now
		// "This is Audible." but which still carries "Wind and Truth" /
		// "Brandon Sanderson".
		//
		// So a non-credits verdict means "this text is not an announcement",
		// NOT "the stored value is wrong". Bad values are neutralised by
		// consumers gating on the classification, never by erasing data here.
		if c.Kind != transcribe.IntroKindCredits {
			continue
		}
		f := c.Fields
		nt, na, nn := strPtrOrNil(f.Title), strPtrOrNil(f.Author), strPtrOrNil(f.Narrator)
		// A credits verdict guarantees title and author; narrator is optional, so
		// keep any existing narrator rather than dropping it.
		if nn == nil {
			nn = b.TranscribedNarrator
		}
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
// 4. Parse results and write per-book outcome (status + parsed fields).
// 5. Clean up temp WAVs (cached clips persist).
//
// EVERY book in the page gets a TranscribeStatus written (change-guarded), so
// the per-book field and the stats:transcribe aggregate together explain exactly
// why transcription did or didn't produce data — the single most useful signal
// being how many books fail with source_file_missing (stale FilePath after an
// organize move).
//
// Returns the number of books successfully transcribed (status ok) in this page.
// Errors are logged and treated as non-fatal so one bad page never aborts the run.
func (p *Plugin) processTranscribePage(
	ctx context.Context,
	store database.Store,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
	},
	books []database.Book,
	rootDir string,
	onBook transcribe.ProgressFunc,
	accum *transcribeStatsAccum,
	extractOnly bool,
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

	now := time.Now()
	bookByID := make(map[string]database.Book, len(books))
	for _, b := range books {
		bookByID[b.ID] = b
	}

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

	// Step 2: extract WAVs in parallel with bounded ffmpeg workers. Each
	// goroutine owns its wavResults[idx] slot, so writes there are race-free.
	// status carries the terminal extraction outcome ("" = WAV produced, defer
	// to Whisper); detail carries the ffmpeg stderr tail for diagnosis.
	type wavResult struct {
		bookID    string
		wavPath   string // empty on failure
		fromCache bool
		status    string // "" | statusNoAudio | statusSourceMissing | statusFFmpegError
		detail    string
	}
	wavResults := make([]wavResult, len(jobs))
	sem := make(chan struct{}, introTranscribeFFWorkers)
	var wg sync.WaitGroup
	for i, j := range jobs {
		if j.audioSrc == "" {
			wavResults[i] = wavResult{bookID: j.book.ID, status: statusNoAudio}
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

			// Distinguish a stale FilePath (the dominant failure after organize
			// moves files) from a real ffmpeg/codec error: stat the source first.
			if _, statErr := os.Stat(src); statErr != nil {
				wavResults[idx] = wavResult{bookID: bookID, status: statusSourceMissing, detail: "source file not found: " + src}
				return
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
				tail := ffmpegErrorTail(string(out))
				log.Warn("transcribe: ffmpeg failed",
					"book_id", bookID, "file", src, "err", err, "output", tail)
				wavResults[idx] = wavResult{bookID: bookID, status: statusFFmpegError, detail: tail}
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
		accum.recordCacheHits(cacheHits)
	}

	// Record terminal extraction failures now (no Whisper needed for them).
	wrByID := make(map[string]wavResult, len(wavResults))
	for _, r := range wavResults {
		wrByID[r.bookID] = r
		if r.status != "" {
			b := bookByID[r.bookID]
			p.applyOutcome(store, log, &b, r.status, r.detail, "", transcribe.IntroFields{}, now, accum)
		}
	}

	// extract-only: the WAV cache is rebuilt; stop here, never call the GPU.
	// Count every book that now has a clip (fresh ffmpeg or cache hit) as
	// "extracted" and bump progress once for the page. This is what decouples
	// the cache rebuild from the single-GPU transcription bottleneck.
	if extractOnly {
		extracted := 0
		for _, r := range wavResults {
			if r.wavPath != "" {
				accum.recordOutcome(statusExtracted, now)
				extracted++
			}
		}
		if onBook != nil && extracted > 0 {
			onBook(extracted, extracted)
		}
		return extracted
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
	batchResults, err := transcribeBatchFn(ctx, batchJobs, onBook)
	var te *transcribe.TransportError
	switch {
	case errors.As(err, &te):
		// Endpoint problem, not a file problem. Write NOTHING; the page is
		// deferred and will be retried next run. On 2026-07-01 this exact
		// branch's absence recorded a day-long endpoint outage as ~34,000
		// false per-book whisper_error verdicts.
		log.Warn("transcribe: endpoints unreachable, page deferred (no status written)",
			"jobs", len(batchJobs), "endpoints", te.Endpoints, "recognized", te.Recognized, "err", te.Err)
		return 0
	case err != nil:
		// Whole-batch failure: every book that had a WAV is a whisper_error.
		// Note classifyTransport wraps EVERY batch-level error from the remote
		// path as a TransportError, so in practice this arm is unreachable from
		// the remote path; kept as defence for the local path.
		log.Warn("transcribe: whisper batch failed", "jobs", len(batchJobs), "err", err)
		detail := truncateDetail(err.Error())
		for bookID := range batchJobs {
			b := bookByID[bookID]
			p.applyOutcome(store, log, &b, statusWhisperError, detail, "", transcribe.IntroFields{}, now, accum)
		}
		return 0
	}

	// Step 3b: retry silence — books that Whisper returned 0 chars for get two
	// more attempts before we give up and store the [SILENCE] sentinel.
	// Retry 1: same file, 300-second clip (catches books with a long music intro).
	// Retry 2: first 90 seconds of the second audio file (catches disc-opener
	//          music tracks where dialogue starts on track 2).
	// After both retries, surviving 0-char books are marked [SILENCE] so
	// only_missing=true skips them; retry_silence=true clears the sentinel.
	type silentBook struct {
		book database.Book
		src  string
	}
	var silentQueue []silentBook
	for bookID, result := range batchResults {
		if result.Error == "" && result.Text == "" {
			if b, ok := bookByID[bookID]; ok {
				src, _, _, _ := firstAudioFile(store, b)
				if src != "" {
					silentQueue = append(silentQueue, silentBook{book: b, src: src})
				}
			}
		}
	}

	// silencedBookIDs tracks books written with the sentinel so Step 4 skips them.
	silencedBookIDs := map[string]bool{}

	if len(silentQueue) > 0 {
		log.Info("transcribe: retrying with 300s clip", "count", len(silentQueue))
		retry1Jobs := make(map[string]string, len(silentQueue))
		for _, s := range silentQueue {
			wav := filepath.Join(tmpDir, "r1_"+s.book.ID+".wav")
			cmd := exec.CommandContext(ctx, "ffmpeg",
				"-y", "-i", s.src, "-t", "300",
				"-vn", "-ar", "16000", "-ac", "1", "-f", "wav", wav)
			if out, ferr := cmd.CombinedOutput(); ferr != nil {
				log.Warn("transcribe: retry1 ffmpeg failed", "book_id", s.book.ID, "err", ferr, "out", strings.TrimSpace(string(out)))
			} else {
				retry1Jobs[s.book.ID] = wav
			}
		}
		if len(retry1Jobs) > 0 {
			if r1, rerr := transcribeBatchFn(ctx, retry1Jobs, onBook); rerr == nil {
				for id, res := range r1 {
					if res.Text != "" {
						batchResults[id] = res
					}
				}
			}
		}

		// Collect books still silent after retry1.
		var stillSilent []silentBook
		for _, s := range silentQueue {
			if res := batchResults[s.book.ID]; res.Text == "" && res.Error == "" {
				stillSilent = append(stillSilent, s)
			}
		}

		if len(stillSilent) > 0 {
			log.Info("transcribe: retrying with second audio file", "count", len(stillSilent))
			retry2Jobs := make(map[string]string, len(stillSilent))
			for _, s := range stillSilent {
				src2, _, _, _ := nthAudioFile(store, s.book, 1)
				if src2 == "" {
					continue
				}
				wav := filepath.Join(tmpDir, "r2_"+s.book.ID+".wav")
				cmd := exec.CommandContext(ctx, "ffmpeg",
					"-y", "-i", src2, "-t", "90",
					"-vn", "-ar", "16000", "-ac", "1", "-f", "wav", wav)
				if out, ferr := cmd.CombinedOutput(); ferr != nil {
					log.Warn("transcribe: retry2 ffmpeg failed", "book_id", s.book.ID, "err", ferr, "out", strings.TrimSpace(string(out)))
				} else {
					retry2Jobs[s.book.ID] = wav
				}
			}
			if len(retry2Jobs) > 0 {
				if r2, rerr := transcribeBatchFn(ctx, retry2Jobs, onBook); rerr == nil {
					for id, res := range r2 {
						if res.Text != "" {
							batchResults[id] = res
						}
					}
				}
			}

			// Mark books that exhausted all retries with the silence sentinel.
			sentinel := silenceSentinel
			markedSilent := 0
			for _, s := range stillSilent {
				if batchResults[s.book.ID].Text != "" {
					continue
				}
				b := s.book
				b.IntroTranscription = &sentinel
				if _, uerr := store.UpdateBook(b.ID, &b); uerr != nil {
					log.Warn("transcribe: silence sentinel write failed", "book_id", b.ID, "err", uerr)
				} else {
					markedSilent++
					silencedBookIDs[b.ID] = true
				}
			}
			if markedSilent > 0 {
				log.Info("transcribe: marked as silence", "count", markedSilent)
			}
		}
	}

	// Step 4: write per-book outcome for every book that had a WAV submitted.
	// Books that received the silence sentinel in Step 3b are skipped here so
	// the sentinel is not overwritten with statusEmpty.
	for bookID := range batchJobs {
		book := bookByID[bookID]
		result, ok := batchResults[bookID]
		switch {
		case !ok || result.Error != "":
			detail := "no whisper result"
			if ok {
				detail = truncateDetail(result.Error)
			}
			log.Warn("transcribe: whisper error", "book_id", bookID, "err", detail)
			p.applyOutcome(store, log, &book, statusWhisperError, detail, "", transcribe.IntroFields{}, now, accum)
		case result.Text == "":
			if silencedBookIDs[bookID] {
				continue // handled by Step 3b; sentinel already written
			}
			p.applyOutcome(store, log, &book, statusEmpty, "whisper returned empty text", "", transcribe.IntroFields{}, now, accum)
		default:
			fields := transcribe.ParseAudiobookIntro(result.Text)
			// statusOK requires a parsed title. When Whisper returns text but
			// the parser can't extract a title, store the raw transcript but
			// mark it statusUnparsed so it's excluded by OnlyParsedTranscription
			// and can be retried later via reparse_only after a parser fix.
			outcome := statusOK
			if fields.Title == "" {
				outcome = statusUnparsed
			}
			if p.applyOutcome(store, log, &book, outcome, "", result.Text, fields, now, accum) {
				processed++
			}
		}
	}

	return processed
}

// applyOutcome writes a book's transcription outcome and records it in the
// aggregate. On statusOK/statusUnparsed it also stores the raw transcript (and,
// for OK, the parsed fields). Writes are change-guarded: a repeat of the SAME
// failure (same status + detail) skips the UpdateBook so a re-run over ~45K
// stale-path books doesn't churn the DB. A transcript outcome (OK or Unparsed)
// always writes — fresh transcript text is new data. Returns true when the book
// produced a transcript this run (OK or Unparsed), which the caller counts as
// processed.
func (p *Plugin) applyOutcome(
	store database.Store,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
	},
	book *database.Book,
	status, detail, text string,
	fields transcribe.IntroFields,
	now time.Time,
	accum *transcribeStatsAccum,
) (ok bool) {
	accum.recordOutcome(status, now)

	newDetail := strPtrOrNil(detail)

	// A transcript outcome (OK or Unparsed) always carries fresh text and must
	// write. Only pure failures are change-guarded: identical status+detail
	// already stored → no write (avoids churning ~45K stale-path books on re-run).
	hasTranscript := status == statusOK || status == statusUnparsed
	if !hasTranscript &&
		book.TranscribeStatus != nil && *book.TranscribeStatus == status &&
		eqStrPtr(book.TranscribeError, newDetail) {
		return false
	}

	book.TranscribeAttemptedAt = &now
	st := status
	book.TranscribeStatus = &st
	book.TranscribeError = newDetail

	if hasTranscript {
		book.IntroTranscription = &text
		book.IntroTranscribedAt = &now
		// Parsed fields are set only when present. For statusUnparsed Title is
		// empty by definition, leaving TranscribedTitle nil — which is exactly
		// what OnlyParsedTranscription filters on.
		if fields.Title != "" {
			book.TranscribedTitle = &fields.Title
		}
		if fields.Author != "" {
			book.TranscribedAuthor = &fields.Author
		}
		if fields.Narrator != "" {
			book.TranscribedNarrator = &fields.Narrator
		}
		if fields.Translator != "" {
			book.TranscribedTranslator = &fields.Translator
		}
		if fields.CoverArtist != "" {
			book.TranscribedCoverArtist = &fields.CoverArtist
		}
	}

	if _, err := store.UpdateBook(book.ID, book); err != nil {
		log.Warn("transcribe: update failed", "book_id", book.ID, "status", status, "err", err)
		return false
	}
	return hasTranscript
}

// ffmpegErrorTail returns the most diagnostic tail of ffmpeg stderr. ffmpeg
// prints a long build banner before the actual error; the real reason is on the
// last non-empty line(s). Returns at most ~200 chars.
func ffmpegErrorTail(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Walk back to the last non-empty line — that's almost always the error.
	last := ""
	for i := len(lines) - 1; i >= 0; i-- {
		if s := strings.TrimSpace(lines[i]); s != "" {
			last = s
			break
		}
	}
	return truncateDetail(last)
}

// truncateDetail caps a detail string so per-book error fields and the stats
// blob stay small.
func truncateDetail(s string) string {
	s = strings.TrimSpace(s)
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// audioExtSet is the set of extensions treated as playable audio by firstAudioFile.
var audioExtSet = map[string]bool{
	".m4b": true, ".mp3": true, ".m4a": true, ".mp4": true,
	".flac": true, ".aac": true, ".ogg": true, ".wma": true,
}

// firstAudioFile returns the path, cache key, and BookFile ID for the first
// (lowest track number) audio file of book. Delegates to nthAudioFile(0).
func firstAudioFile(store database.Store, book database.Book) (path, cacheKey, bookFileID string, err error) {
	return nthAudioFile(store, book, 0)
}

// nthAudioFile returns the path, cache key, and BookFile ID for the nth audio
// file of book, sorted by (disc, track, path) — the same canonical ordering
// GetBookFiles uses. n=0 is the first file.
// The book-level FilePath fallback (for single-track iTunes imports with no
// BookFile rows) is only used when n==0 and no BookFile records exist.
// Cache key priority: FileHash > fp:sha256(AcoustIDFingerprint) > path:sha256(FilePath).
func nthAudioFile(store database.Store, book database.Book, n int) (path, cacheKey, bookFileID string, err error) {
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
		if n != 0 {
			return "", "", "", nil
		}
		// No BookFile rows — fall back to Book.FilePath for single-file imports.
		fp := book.FilePath
		if fp != "" && audioExtSet[strings.ToLower(filepath.Ext(fp))] {
			h := sha256.Sum256([]byte(fp))
			return fp, "path:" + hex.EncodeToString(h[:]), "", nil
		}
		return "", "", "", nil
	}

	// Sort: disc ASC, track ASC, file_path ASC — IDENTICAL to the canonical
	// ordering in PebbleStore.GetBookFiles (pebble_store_bookfiles.go).
	//
	// This comparator used to omit DiscNumber, comparing only (track, path). That
	// is not a cosmetic difference: GetBookFiles hands the slice back already in
	// (disc, track, path) order, and re-sorting without the disc key DESTROYS it.
	// For a book whose DiscNumber came from tags on scan, disc-2-track-1 ties with
	// disc-1-track-1 and FilePath breaks the tie arbitrarily — so "the first file"
	// could be the opening of disc 2.
	//
	// Why that matters more per-file than it did per-book: the per-file intro
	// signal rests on "track 1 carries the spoken opening, tracks 2..N do not", and
	// POSITION is the discriminator that separates a genuine book start from a
	// continuation. If this sort disagrees about which row IS track 1, the
	// discriminator reads the wrong row and the whole signal silently misfires. At
	// book level the same bug was merely a wrong sample.
	//
	// Books written by the iTunes regroup path are unaffected either way:
	// assignDiscTrack (internal/itunes/service/fs_regroup_shape.go) stamps
	// DiscNumber=0 and TrackNumber=1..N contiguously over play order, per the
	// owner decision that discs are flattened. With disc always 0 the two
	// comparators agree. It is the legacy/tag-scanned multi-disc rows, which
	// predate that convention and still carry real disc numbers, that were broken.
	//
	// Kept as an explicit re-sort rather than relying on GetBookFiles' ordering:
	// `store` is the database.Store INTERFACE, and no implementation is
	// contractually bound to return files in any particular order.
	sort.Slice(audio, func(i, j int) bool {
		if audio[i].DiscNumber != audio[j].DiscNumber {
			return audio[i].DiscNumber < audio[j].DiscNumber
		}
		if audio[i].TrackNumber != audio[j].TrackNumber {
			return audio[i].TrackNumber < audio[j].TrackNumber
		}
		return audio[i].FilePath < audio[j].FilePath
	})

	if n >= len(audio) {
		return "", "", "", nil
	}

	f := audio[n]
	key := f.FileHash
	if key == "" && len(f.AcoustIDFingerprint) > 0 {
		// Hash the fingerprint — raw fingerprints are 400–2000 bytes;
		// hex-encoding them directly exceeds the 255-byte Linux filename limit.
		fpHash := sha256.Sum256(f.AcoustIDFingerprint)
		key = "fp:" + hex.EncodeToString(fpHash[:])
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
