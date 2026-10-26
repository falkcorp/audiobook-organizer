// file: internal/plugins/maintenance/intro_transcribe.go
// version: 3.42.0
// guid: c3d4e5f6-a7b8-9012-cdef-123456789012
// last-edited: 2026-10-26

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/audiobook-organizer/internal/ai/resultjournal"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
	"github.com/falkcorp/audiobook-organizer/internal/transcribe"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// introTranscribeOpID is the per-book op's registry id (error prefixes, the
// write gate).
const introTranscribeOpID = "maintenance.transcribe-book-intros"

// bookIntroHeartbeat is the liveness tick the per-book op sends while a whisper
// request is outstanding. It stamps the watchdog's clock (TouchLiveness) and
// nothing else: the page goroutines each own their own progress numerator
// (base + done, see onBook), so a tick that wrote a number would step the
// op's progress backwards between two books. The message goes to the current
// item label, which is an event and writes nothing to the store.
//
// A timer-driven TouchLiveness is acceptable here, against that method's own
// advice, because the ticker exists only while a request is in flight and the
// transport supervises that request (it polls the worker's /health and ends
// the request when the worker is wedged or gone), so the tick cannot outlive
// the work it vouches for.
func bookIntroHeartbeat(reporter sdk.Reporter) func(msg string) {
	return func(msg string) {
		registry.TouchLiveness(reporter)
		reporter.SetCurrentItem(msg)
	}
}

// transcribeBatchFn is a test seam over transcribe.TranscribeBatchOpts so tests
// can substitute a stub (e.g. one returning *transcribe.TransportError) without
// a live Whisper endpoint. Production code never reassigns it.
var transcribeBatchFn = transcribe.TranscribeBatchOpts

// transcribePathBatchFn and pathPipelineAvailableFn are the same kind of test
// seam for the remote_extract path: the batch call that sends SERVER PATHS to a
// worker, and the probe that decides whether any worker can take them.
var (
	transcribePathBatchFn   = transcribe.TranscribeByPathBatch
	pathPipelineAvailableFn = transcribe.PathPipelineAvailable
)

// extractMode says where a page's audio clips are cut.
type extractMode int

const (
	// extractLocal runs ffmpeg in this process (after serverdecode.Check at run
	// level) and uploads the WAVs; the clip cache applies.
	extractLocal extractMode = iota
	// extractRemote sends the book's source path to a worker, which cuts the
	// clip on its own machine. This process decodes nothing and writes no clip
	// cache, which is the whole point of the mode.
	extractRemote
)

// remoteClipSeconds is the clip length remote jobs ask for; it matches the
// local first-pass clip.
const (
	remoteClipSeconds      = 90
	remoteRetryLongSeconds = 300
)

// whisperJournalKind is the resultjournal kind for whisper transcripts.
const whisperJournalKind = resultjournal.KindWhisperIntro

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
	// RetryFailed includes books whose last attempt ended in a DURABLE failure
	// (source_file_missing / no_audio / ffmpeg_error) that selection otherwise
	// skips. Selection already self-heals those the moment the source file is
	// back on disk (see transcribeInputChanged), so this flag is only needed to
	// force a re-attempt while the file is still absent/unchanged — e.g. to
	// re-probe after fixing something outside the row the stat can't see. Mirrors
	// RetrySilence. Defaults false.
	RetryFailed *bool `json:"retry_failed,omitempty"`
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
	// DryRun previews what a run WOULD change without writing anything. It
	// gates every mode: reparse_only, extract_only and the full transcribe.
	// Omitted means PREVIEW (opmode.ResolveDryRun); pass dry_run=false to run.
	//
	// A preview of the transcribe modes runs the work selection and reports what
	// it would do (how many books, how many skipped, a few ids) and then
	// returns: no ffmpeg, no Whisper, no clip-cache write, no stats:transcribe
	// write, no book write, and no serverdecode check (nothing is decoded). A
	// live run writes books, the clip cache and the stats key and calls Whisper.
	//
	// ⚠️ Behaviour change (2026-08-07): for reparse_only runs, dry_run DEFAULTS
	// TO TRUE. The 2026-08-07 reparse of 12,990 books was dispatched with no
	// preview at all; that was acceptable only because reparse is upgrade-only.
	// ⚠️ Behaviour change (2026-10-18): the same default now covers the full
	// transcribe and extract_only paths, which used to ignore dry_run and write.
	// A caller that wants them to run must send dry_run=false (the
	// transcribe_monitor.py relaunch does).
	//
	// The checkpoint (CheckpointFn below) deliberately leaves this field nil:
	// ResumeRestart merges the checkpoint over the run's ORIGINAL params, so a
	// live run's dry_run=false stays in the resumed params and a resumed live run
	// stays live. Writing dry_run into the checkpoint would let it override that.
	DryRun *bool `json:"dry_run,omitempty"`
	// DryRunCamel is the camelCase spelling (dryRun) that the rest of the op
	// family accepts (opmode.DryRunParams). Both are resolved together by
	// opmode.ResolveDryRun: one sent wins, both must agree. Without it a
	// {"dryRun":false} body was ignored and the run previewed. Like DryRun, the
	// checkpoint leaves it nil so a resumed run keeps its original mode.
	DryRunCamel *bool `json:"dryRun,omitempty"`
	// RemoteExtract chooses where the first-90s clip is cut. Omitted means
	// AUTO: remote when this host may not decode audio (serverdecode.Allowed()
	// is false -- production) and some Whisper endpoint advertises
	// path_pipeline, local otherwise. true forces remote and errors if no
	// endpoint can serve it; false forces local, which then needs
	// ALLOW_SERVER_DECODE like it always did. extract_only always runs local:
	// its output is this server's clip cache. Remote mode skips the clip cache.
	// The checkpoint leaves it nil so a resumed run re-resolves it.
	RemoteExtract *bool `json:"remote_extract,omitempty"`
}

func (p *Plugin) introTranscribeDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "maintenance.transcribe-book-intros",
		Preview:         sdk.PreviewSpec{Params: introTranscribeParams{}},
		Liveness:        sdk.LivenessRunItems,
		Plugin:          "maintenance",
		DisplayName:     "Transcribe book intros",
		Description:     "Transcribes the first 90 seconds of each book's first audio file with Whisper. Where the clip is cut depends on remote_extract: by default, when this server may not decode audio (production) and a Whisper worker advertises path_pipeline, the server sends the worker the file's path and the worker cuts the clip from its own NFS mount (no server decode, no clip cache; a worker with a lost mount defers the page instead of failing books); otherwise ffmpeg runs on this host, which needs ALLOW_SERVER_DECODE. Stores the result in TranscribedTitle/Author/Narrator (separate from curated metadata) for disambiguation and dedup cross-checks. Works in pages of 200 books sent as batches. 90s captures past Audible jingles/music intros that caused 30s clips to return only 'This is Audible.' By default (only_missing) it skips books whose last attempt failed for a durable reason (source file gone, no audio, ffmpeg could not decode; a path_unmapped book, whose worker could not read the share, is never durable and is simply retried) and auto-retries them once their source file is back on disk, so it stops re-running the same broken books every pass; retry_failed=true forces those back in. Param reparse_only=true re-runs the parser over already-stored transcripts and rewrites the parsed fields with no ffmpeg/Whisper — use it to apply a parser fix to existing books cheaply. dry_run defaults to true for every mode (full transcribe, extract_only and reparse_only): a run that states no mode only selects the work and reports how many books it would process, writing nothing and decoding nothing. Pass dry_run=false (or dryRun=false) to transcribe, extract or reparse for real.",
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
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite, sdk.CapFilesRead},
		Run:             p.runIntroTranscribe,
	}
}

func (p *Plugin) runIntroTranscribe(ctx context.Context, rawParams json.RawMessage, reporter sdk.Reporter) error {
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}

	var params introTranscribeParams
	if len(rawParams) > 0 {
		if err := json.Unmarshal(rawParams, &params); err != nil {
			return fmt.Errorf("maintenance.intro-transcribe: decode params: %w", err)
		}
	}
	onlyMissing := params.OnlyMissing == nil || *params.OnlyMissing
	retrySilence := params.RetrySilence != nil && *params.RetrySilence
	retryFailed := params.RetryFailed != nil && *params.RetryFailed
	reparseOnly := params.ReparseOnly != nil && *params.ReparseOnly
	extractOnly := params.ExtractOnly != nil && *params.ExtractOnly
	// One mode for every path below; omitted = preview.
	dryRun, err := opmode.ResolveDryRun("maintenance.transcribe-book-intros", params.DryRun, params.DryRunCamel)
	if err != nil {
		return err
	}

	log := reporter.Logger()

	// Reparse-only mode short-circuits the whole Whisper pipeline: it re-runs the
	// parser over stored transcripts and rewrites the parsed fields. Used to apply
	// a parser fix to existing books cheaply.
	if reparseOnly {
		// ⚠️ dry_run defaults to TRUE on the reparse path (behaviour change
		// 2026-08-07): previews are free, and the alternative is dispatching a
		// library-wide write blind. Pass dry_run=false to apply.
		ids, err := store.ListBookIDs()
		if err != nil {
			return fmt.Errorf("list book ids: %w", err)
		}
		return p.reparseStoredIntros(ctx, store, reporter, ids, dryRun)
	}

	// Where the clips are cut. Resolved for previews too, so the preview can name
	// it; the probe is one read-only /health per endpoint and decodes nothing.
	mode, modeNote, err := resolveExtractMode(ctx, params.RemoteExtract, extractOnly)
	if err != nil {
		return err
	}

	// ffmpeg runs on this host ONLY in local mode, so only local mode is gated.
	// reparse_only above never decodes, and neither does a preview (it only
	// selects work). A remote-mode run decodes nothing here: the worker cuts the
	// clip, which is how production transcribes without breaking the standing
	// no-decode-on-the-server rule.
	if !dryRun && mode == extractLocal {
		if err := serverdecode.Check("maintenance.transcribe-book-intros"); err != nil {
			if modeNote != "" {
				return fmt.Errorf("%w (remote_extract not used: %s)", err, modeNote)
			}
			return err
		}
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

	// Resume support: skip past the last book ID checkpointed by a prior run.
	// The checkpoint is searched in the FULL list, not the work list: the
	// checkpointed book was attempted, so under only_missing it is no longer
	// work and would not be found there. The checkpoint is the most recently
	// touched id across introTranscribePageConc concurrent pages, so resuming
	// by position can skip the unfinished remainder of a slower page; those
	// books still lack a transcript and are picked up by the next run.
	//
	// A checkpointed book that was merged or deleted since the last run
	// resumes at its successor: ListBookIDs returns IDs in byte order on both
	// store paths, so that is a forward seek to the first ID greater than the
	// checkpoint, the same seek GetAllBooksFullFrom uses. This used to restart
	// the whole library from the first book. The exact match is still looked
	// up first so a present checkpoint resumes right after itself whatever
	// order the store lists in.
	startIdx := 0
	if params.LastBookID != "" {
		if i := slices.Index(allIDs, params.LastBookID); i >= 0 {
			startIdx = i + 1
		} else {
			startIdx = sort.SearchStrings(allIDs, params.LastBookID)
			log.Info("transcribe-book-intros: checkpointed book is no longer listed, resuming at the next book ID",
				"last_book_id", logging.Sanitize(params.LastBookID))
		}
	}

	// Decide up front which books this run will attempt. The denominator of
	// every progress line and of stats:transcribe is the WORK, not the library:
	// with only_missing (the default) a library of 83k books with 2k missing
	// used to report "1,200/83,228" for a run that was 60% done — the numerator
	// counted attempts and the denominator counted everything, including the
	// 81k books that were skipped before any page ran.
	//
	// The selection reads every listed book before the first page runs, so it
	// reports progress once up front: the watchdog otherwise sees an op that
	// has never reported and cancels it as unwired rather than stuck.
	// Every progress message names the run's mode (introModeMarker), starting
	// with this one, which both modes report before they diverge.
	mark := introModeMarker(dryRun)
	_ = reporter.UpdateProgress(0, 1, mark+fmt.Sprintf("Selecting work — reading %d books", len(allIDs)-startIdx))
	sel, err := selectTranscribeWork(ctx, store, allIDs[startIdx:], transcribeSelect{
		onlyMissing:  onlyMissing,
		retrySilence: retrySilence,
		retryFailed:  retryFailed,
		extractOnly:  extractOnly,
	})
	if err != nil {
		return err
	}
	total := len(sel.work)

	if dryRun {
		return previewIntroTranscribe(reporter, sel, extractOnly, len(allIDs), startIdx, mode, modeNote)
	}

	log.Info("transcribe-book-intros: starting batch run",
		"only_missing", onlyMissing, "retry_failed", retryFailed,
		"library_books", len(allIDs),
		"total_books", total, "skipped_existing", sel.skipped,
		"skipped_known_broken", sel.failedSkipped,
		"not_found", sel.notFound, "read_errors", sel.readErrors,
		"start_index", startIdx, "page_size", introTranscribePageSize,
		"extract_mode", describeExtractMode(mode, modeNote))
	if sel.unreadable() > 0 {
		log.Warn("transcribe-book-intros: books listed but not readable, left out of this run",
			"not_found", sel.notFound, "read_errors", sel.readErrors,
			"samples", sel.samples)
	}

	// Live aggregate: the op records per-outcome counts into accum and flushes
	// them to the stats:transcribe PebbleDB key after each page so an external
	// monitor reads one key instead of scanning books or scraping op logs. The
	// sink is best-effort — when the store doesn't implement TranscribeStatsStore
	// (e.g. a test stub) counts are tracked in memory only.
	// The live store is often a wrapper (memdb/query layer) around *PebbleStore,
	// so a direct assertion misses the capability — unwrap one level like the
	// HTTP handlers do. Without this the sink stays nil and stats:transcribe is
	// never written (the aggregate endpoint returns null even while the op runs).
	statsSink, _ := database.AsCapability[database.TranscribeStatsStore](store)

	// The whisper result journal makes a restart mid-page lossless: every
	// transcript an endpoint returns is journalled (keyed by clip bytes + the
	// endpoint's model) before progress is reported, and the next run serves
	// it instead of re-sending the clip. Built per run from this run's store
	// and passed down as a parameter -- never parked on *Plugin, where two
	// concurrent runs would share it. The journal holds RESULTS only; whether
	// a book already carries its transcript is read from the book row
	// (applyOutcome), so byte-identical clips of two books both get written.
	//
	// retry_silence refreshes the journal (lookups bypassed, results still
	// journalled): that run exists to get a different answer for the same
	// audio after VAD/decoder tuning, which a served entry would defeat.
	startedAt := time.Now()
	// The run id is the registry op id, the same scheme the per-file op uses, so
	// a strike, a permanent verdict and `clear_run_id` mean the same thing in
	// both ops (a start time string matched no op id, so clear_run_id could not
	// select this op's verdicts and SameRun could never match the other op's).
	accum := newTranscribeStatsAccum(statsSink, fileIntroRunID(reporter), total, startedAt)
	pj := pageJournal{refresh: retrySilence}
	if j, jerr := resultjournal.New(store, whisperJournalKind); jerr != nil {
		log.Warn("transcribe-book-intros: result journal unavailable, running without restart protection", "err", jerr)
	} else {
		pj.journal = countingJournal{ResultJournal: j, accum: accum}
	}
	accum.recordSkipped(sel.skipped)
	accum.recordUnreadable(sel.unreadable())

	if total == 0 {
		accum.flush(true) // the monitor still sees the run, its skip count and that it is over
		_ = reporter.UpdateProgress(1, 1, mark+fmt.Sprintf(
			"Done — nothing to transcribe (%d already transcribed, %d unreadable)",
			sel.skipped, sel.unreadable()))
		return nil
	}
	accum.flush(false) // initial write: monitor sees the run has started

	// Chunk the work into pages. Each page → one Whisper batch process
	// (the whole point of batch mode: load the model once per 200 books).
	pages := chunkIDs(sel.work, introTranscribePageSize)

	// processed and lastID are written by concurrent page goroutines — must be
	// thread-safe. processed uses atomic arithmetic; lastID uses a mutex because
	// string assignment is not atomic (pointer + length, two words).
	var processed atomic.Int64
	accum.heartbeat = bookIntroHeartbeat(reporter)
	// A timeout strike is a write to a book_file row, which a library scan's
	// unlocked batch upsert can race (see waitWhileScanBusy). The book op does
	// not wait for the scan: it checks once per page, and while one is running it
	// defers the strike (the book is simply retried next run) instead of holding
	// the page, and its liveness beat, for as long as the scan lasts. A host with
	// no queue reader runs ungated, as this op always has.
	if queue := p.deps.OperationQueueStore(); queue != nil {
		accum.gate = func(context.Context) error {
			return scanBusyNow(introTranscribeOpID, queue)
		}
	}
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
		skipped, deferred := 0, 0
		for _, id := range ids {
			b, gerr := store.GetBookByID(id)
			if gerr != nil || b == nil {
				// Selection read this book moments ago; it is in the
				// denominator. It gets no outcome this run and is retried by
				// the next one — counted as deferred so the aggregate still
				// adds up, and named so the anomaly is findable.
				deferred++
				log.Warn("transcribe-book-intros: book readable at selection but not at page time, deferred",
					"book_id", id, "err", gerr)
				continue
			}
			lastIDMu.Lock()
			lastID = id
			lastIDMu.Unlock()
			// Re-checked at page time: selection ran at the start of the run, and
			// a book transcribed since (another op, a manual apply) must not be
			// redone. hasTranscriptAlready — not needsTranscribeWork — so a
			// durable-failure book that selection promoted via the self-heal
			// recheck (its source is back) is NOT re-excluded here. Normally a
			// no-op: the work list already excludes completed books.
			if hasTranscriptAlready(b, transcribeSelect{
				onlyMissing: onlyMissing, retrySilence: retrySilence, extractOnly: extractOnly,
			}) {
				skipped++
				continue
			}
			books = append(books, *b)
		}
		accum.recordSkipped(skipped)
		accum.recordDeferred(deferred)
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
				mark+fmt.Sprintf("%s — %d/%d books", verb, cur, total))
		}
		done := p.processTranscribePage(ctx, store, log, books, p.deps.RootDir(), onBook, accum, extractOnly, retryFailed, pj, mode)
		cum := int(processed.Add(int64(done)))
		accum.flush(false) // persist cumulative counts so the monitor sees live progress
		if n := accum.stuck.n.Load(); n >= whisperStuckMaxBatches {
			// Every one of these pages was deferred with nothing written. A pool
			// that stays wedged would otherwise defer the whole library one page
			// at a time and report a clean run.
			return fmt.Errorf("%s: %w: %d pages in a row; stopping the run, nothing was written for them and the books stay undone (restart the stuck workers)",
				"maintenance.transcribe-book-intros", errWhisperStuck, n)
		}
		if n := accum.unavailable.n.Load(); n >= whisperUnavailableMaxBatches {
			// Every one of these pages already waited for the pool (the
			// dispatcher paces a benched pool) and got nothing: a dead pool
			// would otherwise defer the whole library one page at a time.
			return fmt.Errorf("%s: %w: %d pages in a row got no answer; stopping the run, nothing was written for them and the books stay undone",
				"maintenance.transcribe-book-intros", errWhisperUnavailable, n)
		}
		log.Info("transcribe-book-intros: page complete",
			"page_books", done, "cumulative_processed", cum, "total_books", total)
		return nil
	}, registry.RunItemsOptions{
		Concurrency:   introTranscribePageConc,
		ProgressTotal: len(pages),
		Label: func(i, t int) string {
			return mark + fmt.Sprintf("Page %d/%d — %d books transcribed", i+1, t, int(processed.Load()))
		},
		// NOTE: registry.runItemsPar never calls CheckpointFn -- only the
		// sequential runItemsSeq does -- so at introTranscribePageConc > 1 this
		// LastBookID checkpoint is never written and a resumed run starts from
		// the top. Resume is still lossless: only_missing re-selects every book
		// without a transcript, and the whisper result journal serves the
		// clips a killed run already transcribed.
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
		"processed", total64, "total_books", total, "attempted", st.Attempted,
		"ok", st.OK, "source_missing", st.SourceMissing, "no_audio", st.NoAudio,
		"ffmpeg_error", st.FFmpegError, "whisper_error", st.WhisperError,
		"path_unmapped", st.PathUnmapped, "empty", st.Empty, "deferred", st.Deferred, "skipped_existing", st.SkippedExisting,
		"skipped_known_broken", sel.failedSkipped,
		"unreadable", st.Unreadable, "cache_hits", st.CacheHits)
	_ = reporter.UpdateProgress(1, 1, mark+fmt.Sprintf(
		"Done — %d ok, %d source-missing, %d ffmpeg-err, %d whisper-err, %d path-unmapped, %d empty, %d deferred "+
			"(of %d total; %d skipped, %d of them known-broken not retried, %d unreadable)",
		st.OK, st.SourceMissing, st.FFmpegError, st.WhisperError, st.PathUnmapped, st.Empty, st.Deferred, total,
		st.SkippedExisting, sel.failedSkipped, st.Unreadable)+journalFailureNote(st.JournalWriteFailures))
	return nil
}

// introPreviewMarker and introLiveMarker open every progress message of a
// run in every mode (full transcribe, extract_only and reparse_only), so a reader of the run's progress
// (scripts/transcribe_monitor.py's PREVIEW_MARKER and LIVE_MARKER) can tell
// the mode from any message, including the "Selecting work" line both modes
// report first. TestIntroTranscribe_ProgressNamesTheMode pins them; change
// the monitor with them.
const (
	introPreviewMarker = "PREVIEW (dry_run, nothing written) — "
	introLiveMarker    = "LIVE — "
)

func introModeMarker(dryRun bool) string {
	if dryRun {
		return introPreviewMarker
	}
	return introLiveMarker
}

// resolveExtractMode decides where clips are cut (see
// introTranscribeParams.RemoteExtract). note is a human reason when remote was
// considered and not chosen, so a refusal can say why remote was not used.
//
// extract_only is always local: it exists to rebuild THIS server's clip cache,
// which a worker-side extraction cannot populate.
func resolveExtractMode(ctx context.Context, remoteExtract *bool, extractOnly bool) (mode extractMode, note string, err error) {
	if extractOnly {
		if remoteExtract != nil && *remoteExtract {
			return extractLocal, "", errors.New("remote_extract=true cannot be combined with extract_only: " +
				"extract_only rebuilds this server's clip cache, which a worker cannot fill")
		}
		return extractLocal, "", nil
	}
	if remoteExtract != nil && !*remoteExtract {
		return extractLocal, "", nil
	}
	forced := remoteExtract != nil
	if !forced && serverdecode.Allowed() {
		// A development host that opted in keeps the historical local path.
		return extractLocal, "", nil
	}
	ok, reason := pathPipelineAvailableFn(ctx)
	if ok {
		return extractRemote, "", nil
	}
	if forced {
		return extractLocal, "", fmt.Errorf("remote_extract=true but no whisper endpoint can serve path jobs: %s", reason)
	}
	return extractLocal, "no whisper endpoint advertises path_pipeline: " + reason, nil
}

func describeExtractMode(mode extractMode, note string) string {
	if mode == extractRemote {
		return "on a Whisper worker (remote_extract: no decoding on this server)"
	}
	if note != "" {
		return "on this host (" + note + ")"
	}
	return "on this host"
}

// introPreviewSamples bounds the book ids a preview names.
const introPreviewSamples = 5

// previewIntroTranscribe reports what a live run would do from the work
// selection and returns. It is the whole of a transcribe/extract_only preview:
// it calls no ffmpeg and no Whisper and writes no clip, no stats key, no
// journal entry and no book, so everything it needs is already in sel.
func previewIntroTranscribe(reporter sdk.Reporter, sel transcribeSelection, extractOnly bool, libraryBooks, startIdx int, mode extractMode, modeNote string) error {
	verb := "transcribe"
	if extractOnly {
		verb = "extract clips for"
	}
	samples := sel.work[:min(introPreviewSamples, len(sel.work))]
	msg := introPreviewMarker + fmt.Sprintf(
		"would %s %d books (of %d listed, starting at %d); "+
			"%d skipped (%d of them known-broken, not retried), %d unreadable; first ids: [%s]; "+
			"clips would be cut %s. Pass dry_run=false to run.",
		verb, len(sel.work), libraryBooks, startIdx,
		sel.skipped, sel.failedSkipped, sel.unreadable(), strings.Join(samples, ", "),
		describeExtractMode(mode, modeNote))
	reporter.Logger().Info("transcribe-book-intros: preview",
		"would_process", len(sel.work), "library_books", libraryBooks, "start_index", startIdx,
		"skipped_existing", sel.skipped, "skipped_known_broken", sel.failedSkipped,
		"not_found", sel.notFound, "read_errors", sel.readErrors, "sample_ids", samples,
		"extract_mode", describeExtractMode(mode, modeNote))
	if sel.unreadable() > 0 {
		reporter.Logger().Warn("transcribe-book-intros: books listed but not readable, left out of the preview",
			"not_found", sel.notFound, "read_errors", sel.readErrors, "samples", sel.samples)
	}
	_ = reporter.UpdateProgress(1, 1, msg)
	return nil
}

// journalFailureNote names journal write failures in the op's final message.
// Those books still got their transcripts; what was lost is restart
// protection for them, which a monitor should see rather than only a log.
func journalFailureNote(n int) string {
	if n == 0 {
		return ""
	}
	return fmt.Sprintf(" — %d journal-write failures (transcripts written, not restart-protected)", n)
}

// pageJournal is the whisper result journal as processTranscribePage uses it.
// A zero value means no journal.
type pageJournal struct {
	journal transcribe.ResultJournal
	// refresh bypasses journal lookups (see transcribe.BatchOptions.RefreshJournal).
	refresh bool
}

// countingJournal counts failed journal writes into the run's stats. The
// failure itself stays non-fatal (transcribe logs it and the transcript is
// still written to the book).
type countingJournal struct {
	transcribe.ResultJournal
	accum *transcribeStatsAccum
}

func (c countingJournal) Complete(key, endpoint, model string, result any) error {
	err := c.ResultJournal.Complete(key, endpoint, model, result)
	if err != nil {
		c.accum.recordJournalWriteFailure()
	}
	return err
}

// extractClip runs ffmpeg to write the first `seconds` of src as a 16 kHz mono
// WAV at dst, returning ffmpeg's combined output.
//
// ffmpeg writes to a unique temp file in dst's directory, which is renamed
// over dst only after ffmpeg succeeds. dst is usually the SHARED clip cache,
// and another page (up to introTranscribePageConc run at once) may stat, hash
// and upload the same path at any moment; with ffmpeg writing dst in place, a
// half-written clip could be transcribed and its transcript journalled. The
// rename is atomic within a directory, so dst is always absent or complete.
func extractClip(ctx context.Context, src, dst string, seconds int) ([]byte, error) {
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*.partial")
	if err != nil {
		return nil, fmt.Errorf("create temp clip: %w", err)
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-y", "-i", src,
		"-t", strconv.Itoa(seconds),
		"-vn", "-ar", "16000", "-ac", "1", "-f", "wav",
		tmpPath,
	).CombinedOutput()
	if err != nil {
		_ = os.Remove(tmpPath)
		return out, err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		_ = os.Remove(tmpPath)
		return out, fmt.Errorf("publish clip: %w", err)
	}
	return out, nil
}

// reparseStoredIntros re-runs ParseAudiobookIntro over every book's stored
// IntroTranscription and rewrites TranscribedTitle/Author/Narrator to the new
// parse. No ffmpeg, no Whisper — used to apply a parser fix to existing data.
// UpdateBook does full-column replacement, and GetBookByID returns the full
// row, so only the three parsed fields change. IntroTranscribedAt is left
// untouched (reparse is not a new transcription).
//
// With dryRun=true (the default for reparse_only runs) the identical
// classification+comparison logic runs and every bucket is counted, but the
// UpdateBook call is skipped — nothing is written.
func (p *Plugin) reparseStoredIntros(ctx context.Context, store interface {
	ListBookIDs() ([]string, error)
	GetBookByID(string) (*database.Book, error)
	ModifyBook(string, func(*database.Book) error) (*database.Book, error)
}, reporter sdk.Reporter, ids []string, dryRun bool) error {
	log := reporter.Logger()
	total := len(ids)
	verb := "updated"
	if dryRun {
		verb = "would update"
	}
	mark := introModeMarker(dryRun)
	var scanned, changed int
	for i, id := range ids {
		if i%500 == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
			_ = reporter.UpdateProgress(i, total,
				mark+fmt.Sprintf("Reparsing intros — %d/%d (%d %s)", i, total, changed, verb))
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
		parsedNarrator := nn
		if nn == nil {
			nn = b.TranscribedNarrator
		}
		if eqStrPtr(b.TranscribedTitle, nt) && eqStrPtr(b.TranscribedAuthor, na) && eqStrPtr(b.TranscribedNarrator, nn) {
			continue // unchanged — skip the write
		}
		if !dryRun {
			// The parse above came from the transcript this loop read. Write
			// only the three parsed columns, under the book's write lock
			// (ModifyBook), so a column another writer commits meanwhile is
			// not reverted (audit A1#15). The fresh row decides: a transcript
			// replaced meanwhile no longer backs this parse and is left for
			// the next run, and a narrator filled meanwhile is kept.
			transcript := *b.IntroTranscription
			wrote := false
			written, err := store.ModifyBook(b.ID, func(cur *database.Book) error {
				if cur.IntroTranscription == nil || *cur.IntroTranscription != transcript {
					return database.ErrSkipBookWrite
				}
				narrator := parsedNarrator
				if narrator == nil {
					narrator = cur.TranscribedNarrator
				}
				if eqStrPtr(cur.TranscribedTitle, nt) && eqStrPtr(cur.TranscribedAuthor, na) && eqStrPtr(cur.TranscribedNarrator, narrator) {
					return database.ErrSkipBookWrite
				}
				cur.TranscribedTitle, cur.TranscribedAuthor, cur.TranscribedNarrator = nt, na, narrator
				wrote = true
				return nil
			})
			if err != nil {
				log.Warn("reparse-intros: update failed", "book_id", b.ID, "err", err)
				continue
			}
			if written == nil || !wrote {
				continue
			}
		}
		changed++
	}
	log.Info("reparse-intros: complete", "dry_run", dryRun,
		"scanned", scanned, "changed", changed, "total_books", total)
	if dryRun {
		_ = reporter.UpdateProgress(total, total,
			mark+fmt.Sprintf("would update %d of %d transcribed (%d books; pass dry_run=false to apply)", changed, scanned, total))
	} else {
		_ = reporter.UpdateProgress(total, total,
			mark+fmt.Sprintf("Reparse complete — updated %d of %d transcribed (%d books)", changed, scanned, total))
	}
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

// transcribeSelect is the per-run policy for which books are work.
type transcribeSelect struct {
	onlyMissing  bool // skip books that already carry a transcript
	retrySilence bool // ...except [SILENCE] ones, which are retried
	retryFailed  bool // ...and durable-failure ones, which are otherwise skipped
	extractOnly  bool // (re)build the WAV cache for EVERY book; ignores onlyMissing
}

// needsTranscribeWork is the in-memory predicate for "selection attempts this
// book". It reads only the row (no I/O) so it is unit-testable without a
// filesystem. selectTranscribeWork is the ONLY caller: the page loop uses the
// narrower hasTranscriptAlready instead, because a book selection promoted via
// the self-heal recheck (transcribeInputChanged) must not be re-excluded here.
//
// A book that failed its last attempt for a DURABLE reason
// (source_file_missing / no_audio / ffmpeg_error) is skipped by default: without
// this, every run re-selects the same broken books and re-runs ffmpeg on them —
// 9,151 of 9,922 work items on the 2026-09-05 run were last-run failures. The
// [SILENCE] sentinel already handled that one class via IntroTranscription; the
// other durable classes have no sentinel and are handled here off
// TranscribeStatus, which applyOutcome already records. retry_failed=true forces
// them back in; and selectTranscribeWork re-promotes any whose source file has
// reappeared or changed, so a repoint self-heals with no flag.
//
// whisper_error and empty stay retryable: those are usually a transient endpoint
// or clip-extraction defect (a whole-batch TransportError is deferred with no
// status written at all), and skipping them would bury a recoverable failure.
func needsTranscribeWork(b *database.Book, sel transcribeSelect) bool {
	// extract-only ignores onlyMissing: the whole point is to (re)build the
	// WAV cache for EVERY book, regardless of transcription status.
	if sel.extractOnly || !sel.onlyMissing {
		return true
	}
	// Already carries a transcript (or the [SILENCE] sentinel).
	if b.IntroTranscription != nil && *b.IntroTranscription != "" {
		// [SILENCE] books are skipped normally; retry_silence=true includes them.
		return *b.IntroTranscription == silenceSentinel && sel.retrySilence
	}
	// No transcript. Skip durable failures unless retry_failed forces a retry.
	if !sel.retryFailed && b.TranscribeStatus != nil && isDurableTranscribeFailure(*b.TranscribeStatus) {
		return false
	}
	return true
}

// isDurableTranscribeFailure reports whether a stored TranscribeStatus means the
// last attempt failed for a reason that re-running the exact same input cannot
// fix: the source file is gone, the book has no audio file, ffmpeg could not
// decode the bytes, or the Whisper decoder timed out on the audio on every one
// of its allowed attempts (transcribe_timeout). Selection skips these (needsTranscribeWork) and only
// re-attempts them when transcribeInputChanged sees the underlying file back.
func isDurableTranscribeFailure(status string) bool {
	switch status {
	case statusSourceMissing, statusNoAudio, statusFFmpegError, statusTranscribeTimeout:
		return true
	default:
		return false
	}
}

// isDurableSkip reports whether a book selection is skipping specifically
// because its last attempt was a durable failure whose input has not returned —
// i.e. known-broken, not already-transcribed. Used only to attribute the
// skipped count so those books surface in the run log instead of silently
// shrinking the denominator. Callers reach it only after needsTranscribeWork
// returned false and transcribeInputChanged returned false.
func isDurableSkip(b *database.Book, sel transcribeSelect) bool {
	if !sel.onlyMissing || sel.extractOnly || sel.retryFailed {
		return false
	}
	if b.IntroTranscription != nil && *b.IntroTranscription != "" {
		return false
	}
	return b.TranscribeStatus != nil && isDurableTranscribeFailure(*b.TranscribeStatus)
}

// hasTranscriptAlready is the page-time recheck: a book that gained a real (or
// [SILENCE]) transcript between selection and page time is skipped so it is not
// redone. It is deliberately NARROWER than needsTranscribeWork — it never
// re-applies the durable-failure skip — so a durable book that selection
// promoted via transcribeInputChanged (its source is back) still runs at page
// time. It equals the previous `!needsTranscribeWork`, preserving the old
// page-time behaviour exactly.
func hasTranscriptAlready(b *database.Book, sel transcribeSelect) bool {
	if sel.extractOnly || !sel.onlyMissing {
		return false
	}
	if b.IntroTranscription == nil || *b.IntroTranscription == "" {
		return false
	}
	// A [SILENCE] book counts as "already done" unless retry_silence re-includes it.
	return *b.IntroTranscription != silenceSentinel || !sel.retrySilence
}

// transcribeInputChanged reports whether a durable-failure book that
// needsTranscribeWork skipped should nonetheless be retried because its source
// audio has reappeared or changed since the failed attempt. This is the
// self-heal path: after the missing-file repoint restores a source file, the
// next run picks the book up with no flag. Best-effort I/O — any error (no file,
// stat failure) means "still broken, keep skipping".
//
// For source_file_missing / no_audio the file being present again IS the change.
// For ffmpeg_error and transcribe_timeout the file was present at failure time too, so require the bytes
// to have changed — mtime after the failed attempt (a re-encode or replace).
// TranscribeAttemptedAt holds the FIRST-failure time (applyOutcome's change-guard
// does not refresh it on a repeat, and selection skips the book on later runs so
// it never re-enters applyOutcome), which is exactly the reference the mtime
// compare wants.
func transcribeInputChanged(store bookFileLister, b *database.Book) bool {
	// Only the no-transcript durable-failure case self-heals. A book that already
	// carries a transcript is done regardless of a stale durable TranscribeStatus.
	if b.IntroTranscription != nil && *b.IntroTranscription != "" {
		return false
	}
	if b.TranscribeStatus == nil || !isDurableTranscribeFailure(*b.TranscribeStatus) {
		return false
	}
	ref, err := firstAudioFile(store, *b)
	if err != nil || ref.Path == "" {
		return false
	}
	fi, err := os.Stat(ref.Path)
	if err != nil {
		return false // source still absent
	}
	// The book carries the first file's permanent verdict (stamped without
	// sending it), and the first file no longer carries one: the verdict was
	// cleared (clear_run_id on the run that made it permanent, or the audio
	// decoded since). The stamp's cause is gone, so the book is due again.
	if *b.TranscribeStatus == statusTranscribeTimeout && b.TranscribeError != nil &&
		*b.TranscribeError == firstFilePermanentTimeoutDetail && ref.BookFileID != "" &&
		ref.TranscribeStatus != statusTranscribeTimeout {
		return true
	}
	if *b.TranscribeStatus == statusFFmpegError || *b.TranscribeStatus == statusTranscribeTimeout {
		if b.TranscribeAttemptedAt == nil {
			return false
		}
		return fi.ModTime().After(*b.TranscribeAttemptedAt)
	}
	return true
}

// transcribeSelection is what selectTranscribeWork decided.
type transcribeSelection struct {
	work    []string // ids this run attempts, in library (ListBookIDs) order
	skipped int      // ids not attempted this run (already done OR known-broken)
	// failedSkipped is the subset of skipped that were skipped because their last
	// attempt was a DURABLE failure (source gone / no audio / ffmpeg) whose input
	// has not come back — i.e. known-broken, not already-transcribed. Surfaced in
	// the run log so those books do not silently vanish from the denominator.
	failedSkipped int
	notFound      int      // ids listed by the index but with no row behind them
	readErrors    int      // ids whose read failed (I/O, decode) — a store fault
	samples       []string // first few "id: err" / "id: not found", for the log
}

// unreadable is every listed id this run could not read, for any reason.
func (s transcribeSelection) unreadable() int { return s.notFound + s.readErrors }

// selectSampleLimit caps how many unreadable ids are named in the log.
const selectSampleLimit = 5

// selectTranscribeWork reads every listed book once and keeps the ones
// needsTranscribeWork accepts, preserving order. Reads fan out over a bounded
// worker pool (a library-scale loop with a store read per item); each worker
// owns its own slot so no lock is needed.
//
// A book the store lists but cannot return is counted, never silently
// dropped: a run that quietly shrinks is the failure the counters exist to
// expose. The two shapes are kept apart because they mean different things.
// (nil, nil) is index/row drift — a real gap in the library, count it and go
// on. A non-nil error is the STORE failing (closed DB, I/O, decode), and when
// it fails for every id the correct answer is not "nothing to transcribe,
// done": that is the run this function refuses to return, because it would
// be recorded as a success.
func selectTranscribeWork(ctx context.Context, store transcribeSelectStore, ids []string, sel transcribeSelect) (transcribeSelection, error) {
	const (
		verdictNotFound = iota
		verdictReadError
		verdictSkip       // already transcribed / [SILENCE]
		verdictFailedSkip // durable failure whose input has not come back
		verdictWork
	)
	verdicts := make([]uint8, len(ids))
	errs := make([]error, len(ids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())
	for i, id := range ids {
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			b, err := store.GetBookByID(id)
			switch {
			case err != nil:
				verdicts[i], errs[i] = verdictReadError, err
			case b == nil:
				verdicts[i] = verdictNotFound
			case needsTranscribeWork(b, sel):
				verdicts[i] = verdictWork
			case transcribeInputChanged(store, b):
				// Durable failure whose source file is back on disk (or was
				// re-encoded): self-heal it into work with no flag. This is what
				// makes a missing-file repoint restore transcription on its own.
				verdicts[i] = verdictWork
			case isDurableSkip(b, sel):
				verdicts[i] = verdictFailedSkip
			default:
				verdicts[i] = verdictSkip
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return transcribeSelection{}, fmt.Errorf("select transcribe work: %w", err)
	}
	out := transcribeSelection{}
	var firstErr error
	for i, v := range verdicts {
		switch v {
		case verdictWork:
			out.work = append(out.work, ids[i])
		case verdictFailedSkip:
			out.skipped++
			out.failedSkipped++
		case verdictSkip:
			out.skipped++
		case verdictNotFound:
			out.notFound++
			if len(out.samples) < selectSampleLimit {
				out.samples = append(out.samples, ids[i]+": not found")
			}
		default:
			out.readErrors++
			if firstErr == nil {
				firstErr = errs[i]
			}
			if len(out.samples) < selectSampleLimit {
				out.samples = append(out.samples, ids[i]+": "+errs[i].Error())
			}
		}
	}
	if len(ids) > 0 && out.readErrors == len(ids) {
		return transcribeSelection{}, fmt.Errorf(
			"select transcribe work: every one of %d book reads failed, refusing to report an empty run as done: %w",
			len(ids), firstErr)
	}
	return out, nil
}

// chunkIDs splits ids into consecutive slices of at most size elements.
func chunkIDs(ids []string, size int) [][]string {
	if size < 1 {
		size = 1
	}
	chunks := make([][]string, 0, (len(ids)+size-1)/size)
	for start := 0; start < len(ids); start += size {
		end := min(start+size, len(ids))
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
	store transcribePageStore,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
	},
	books []database.Book,
	rootDir string,
	onBook transcribe.ProgressFunc,
	accum *transcribeStatsAccum,
	extractOnly bool,
	retryDurable bool, // retry_failed: re-attempt durable failures instead of skipping them
	pj pageJournal,
	mode extractMode,
) (processed int) {
	remote := mode == extractRemote
	// Remote mode cuts no clip on this host, so it neither creates the clip
	// cache directory nor a temp dir for WAVs: nothing here may write audio.
	cacheDir, tmpDir := "", ""
	if !remote {
		cacheDir = wavCacheDir(rootDir)
		if err := os.MkdirAll(cacheDir, 0o755); err != nil {
			log.Warn("transcribe: cannot create clip cache dir, running without cache", "dir", cacheDir, "err", err)
			cacheDir = ""
		}

		// tmpDir holds WAVs for books whose source file has no stored hash (no caching).
		var err error
		tmpDir, err = os.MkdirTemp("", "ao-transcribe-*")
		if err != nil {
			log.Warn("transcribe: create temp dir failed, page deferred (no status written)", "books", len(books), "err", err)
			accum.recordDeferred(len(books))
			return 0
		}
		defer os.RemoveAll(tmpDir)
	}

	now := time.Now()
	// gate answers once per page whether a library scan is running, and is asked
	// only when the page has a book_file write to make.
	gate := sync.OnceValue(func() error { return accum.gateWrite(ctx) })
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
	// refByID keeps each book's first-file reference: step 4 needs its row id and
	// stored hash to count a transcribe_timeout strike against the right file.
	refByID := make(map[string]audioFileRef, len(books))
	for _, b := range books {
		ref, _ := firstAudioFile(store, b)
		if !retryDurable && !extractOnly && ref.TranscribeStatus == statusTranscribeTimeout {
			// The per-file op (or an earlier run) already made this file's
			// decoder timeout permanent. Do not send it again: stamp the verdict
			// on the book so selection skips it from now on, and move on.
			log.Warn("transcribe: first file is a permanent transcribe_timeout, book not sent",
				"book_id", b.ID, "file_id", ref.BookFileID)
			// Not sent, so not an attempt: it is counted as skipped (a verdict
			// is already held), never through recordOutcome, which would add an
			// Attempted and a TranscribeTimeout for a book no worker saw (and
			// again on every forced run). A book that already carries a
			// transcript is left exactly as it is: labelling real text a
			// failure would be wrong.
			accum.recordSkipped(1)
			p.writeOutcome(store, log, &b, statusTranscribeTimeout,
				firstFilePermanentTimeoutDetail, "", transcribe.IntroFields{}, now, true)
			continue
		}
		refByID[b.ID] = ref
		src, hash := ref.Path, ref.CacheKey
		jobs = append(jobs, bookJob{book: b, audioSrc: src, fileHash: hash})
	}

	// sentRef is the file each book's LATEST request actually carried. It starts
	// as the first file and moves to the second when retry 2 sends that one, so
	// a timeout is charged to the file that timed out and not to file 1.
	sentRef := maps.Clone(refByID)

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
		if remote {
			// Nothing is cut here. A stat is not a decode, and it keeps a stale
			// FilePath's verdict identical to local mode ("source file not
			// found") without spending a worker request on it.
			if _, statErr := os.Stat(j.audioSrc); statErr != nil {
				wavResults[i] = wavResult{bookID: j.book.ID, status: statusSourceMissing, detail: "source file not found: " + j.audioSrc}
			} else {
				wavResults[i] = wavResult{bookID: j.book.ID} // accepted: the worker cuts it
			}
			continue
		}
		idx, bookID, src, fileHash := i, j.book.ID, j.audioSrc, j.fileHash
		wg.Go(func() {
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

			if out, err := extractClip(ctx, src, wavPath, 90); err != nil {
				tail := ffmpegErrorTail(string(out))
				log.Warn("transcribe: ffmpeg failed",
					"book_id", bookID, "file", src, "err", err, "output", tail)
				wavResults[idx] = wavResult{bookID: bookID, status: statusFFmpegError, detail: tail}
				return
			}
			wavResults[idx] = wavResult{bookID: bookID, wavPath: wavPath}
		})
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

	// Build the jobs for the whisper call. Local: the WAV path of every book
	// that has a clip. Remote: the SOURCE path of every book the stat accepted,
	// which the worker cuts itself. submitted is the set of books whose outcome
	// step 4 writes.
	batchJobs := make(map[string]string)
	pathJobs := make(map[string]transcribe.PathJob)
	submitted := make(map[string]bool)
	srcByID := make(map[string]string, len(jobs))
	for _, j := range jobs {
		srcByID[j.book.ID] = j.audioSrc
	}
	for _, r := range wavResults {
		switch {
		case remote && r.status == "":
			pathJobs[r.bookID] = transcribe.PathJob{Path: srcByID[r.bookID], Seconds: remoteClipSeconds}
			submitted[r.bookID] = true
		case !remote && r.wavPath != "":
			batchJobs[r.bookID] = r.wavPath
			submitted[r.bookID] = true
		}
	}
	if len(submitted) == 0 {
		return 0
	}

	// Step 3: one whisper batch for the whole page.
	log.Info("transcribe-book-intros: calling whisper batch", "jobs", len(submitted), "remote_extract", remote)
	batchOpts := transcribe.BatchOptions{OnProgress: onBook, Journal: pj.journal, RefreshJournal: pj.refresh}
	var batchResults map[string]transcribe.BatchResult
	var err error
	// A request can outlast the watchdog's progress budget (a chunk of clips each
	// allowed up to the worker's per-clip deadline), so say the run is alive
	// while it waits. The ticker is stopped by a defer, so it ends even if the
	// call panics.
	func() {
		defer tickWhileWaiting(whisperWaitTickInterval, func(elapsed time.Duration) {
			accum.beat(fmt.Sprintf("waiting on whisper: %d books in flight for %s", len(submitted), elapsed.Round(time.Second)))
		})()
		if remote {
			batchResults, err = transcribePathBatchFn(ctx, pathJobs, batchOpts)
		} else {
			batchResults, err = transcribeBatchFn(ctx, batchJobs, batchOpts)
		}
	}()
	// A cancelled run writes nothing: whatever the pool answered before the cancel
	// is discarded (the books are retried by the next run), the same as the
	// per-file op. Applying a half-finished page after the operator stopped the
	// run is the surprising choice, and the strike writes below would go on to
	// wait on the scan gate with nobody listening.
	if ctx.Err() != nil {
		log.Warn("transcribe: run cancelled during the whisper call, page discarded (no status written)", "jobs", len(submitted))
		accum.recordDeferred(len(submitted))
		return 0
	}
	// On an error the pool may still have answered some books: batchResults
	// holds what settled before it. Those are applied below; every other
	// submitted book has no answer and is deferred (or, for a whole-batch
	// failure that is not a transport error, written as the failure).
	if batchResults == nil {
		batchResults = map[string]transcribe.BatchResult{}
	}
	if err != nil {
		// A pool that failed this call produced its timeouts and crashes under
		// conditions that say nothing about the clips: they are read as no
		// answer (deferred, never struck), except what the dispatcher itself
		// condemned. See dropPoolStrikes.
		if before := len(batchResults); before > 0 {
			batchResults = dropPoolStrikes(batchResults, batchStrike)
			if dropped := before - len(batchResults); dropped > 0 {
				log.Warn("transcribe: the pool failed this call, so its timeouts/crashes are not read as verdicts; those books are deferred without a strike",
					"dropped", dropped, "answered", len(batchResults))
			}
		}
	}
	answered := len(batchResults)
	if err == nil || !transcribe.IsWorkerStuck(err) || answered > 0 {
		accum.stuck.reset() // the pool answered (or failed some other way)
	}
	if err == nil || answered > 0 {
		accum.unavailable.reset()
	}
	// batchErr is the detail written for a book with no answer when the whole
	// batch failed in a way that is not a transport error. Empty otherwise.
	batchErr := ""
	var te *transcribe.TransportError
	switch {
	case errors.As(err, &te):
		// Endpoint problem, not a file problem. Write NOTHING for a book that got
		// no answer; it is retried next run. On 2026-07-01 this exact branch's
		// absence recorded a day-long endpoint outage as ~34,000 false per-book
		// whisper_error verdicts.
		if transcribe.IsWorkerStuck(err) {
			// Reachable but wedged: not an outage, and not a verdict on any book.
			// The run loop stops the run after whisperStuckMaxBatches in a row.
			streak := accum.stuck.n.Load()
			if answered == 0 {
				streak = accum.stuck.hit()
			}
			log.Warn("transcribe: whisper worker stuck, unanswered books deferred (no status written for them)",
				"jobs", len(submitted), "answered", answered, "stuck_pages_in_a_row", streak, "of", whisperStuckMaxBatches,
				"endpoints", te.Endpoints, "err", te.Err.Error())
		} else {
			// A pool that could not be reached, or stayed unavailable through the
			// dispatcher's pacing, counts toward stopping the run; any other
			// batch-level failure (a worker fault, a capability refusal) is
			// deferred and the run goes on.
			streak := accum.unavailable.n.Load()
			if answered == 0 && (te.Recognized || transcribe.IsPoolUnavailable(err)) {
				streak = accum.unavailable.hit()
			}
			log.Warn("transcribe: endpoints unreachable, unanswered books deferred (no status written for them)",
				"jobs", len(submitted), "answered", answered, "endpoints", te.Endpoints, "recognized", te.Recognized,
				"unavailable_pages_in_a_row", streak, "of", whisperUnavailableMaxBatches, "err", te.Err.Error())
		}
		if answered == 0 {
			accum.recordDeferred(len(submitted))
			return 0
		}
	case err != nil:
		// Whole-batch failure: every book that had a WAV and no answer is a
		// whisper_error. Note classifyTransport wraps EVERY batch-level error from
		// the remote path as a TransportError, so in practice this arm is
		// unreachable from the remote path; kept as defence for the local path.
		log.Warn("transcribe: whisper batch failed", "jobs", len(submitted), "answered", answered, "err", err)
		batchErr = truncateDetail(err.Error())
	}

	// A first-pass timeout on a first file that the per-file op already heard as
	// silence says nothing new: it is the same 90 s clip, and its answer is held.
	// Treat it as the empty first pass it is and go on to the longer-clip
	// retries. Recording the timeout instead hit the held result (no strike) and
	// deferred the book on every run with nothing ever trying the 300 s clip.
	for bookID, result := range batchResults {
		if isTimeoutResult(result.Error) && submitted[bookID] && sentRef[bookID].HeldSilence {
			log.Info("transcribe: first file timed out but already holds silence from the per-file op, going on to the silence retries",
				"book_id", bookID, "file_id", sentRef[bookID].BookFileID)
			batchResults[bookID] = transcribe.BatchResult{}
		}
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
	// wasSilent is every book that came back empty on the first pass, kept after
	// silentQueue is consumed: a retry that cannot be judged (see
	// bookTimeoutHeldResult) still has to know the book was silent.
	wasSilent := map[string]bool{}
	for bookID, result := range batchResults {
		if result.Error == "" && result.Text == "" {
			if b, ok := bookByID[bookID]; ok {
				ref, _ := firstAudioFile(store, b)
				src := ref.Path
				if src != "" {
					silentQueue = append(silentQueue, silentBook{book: b, src: src})
					wasSilent[bookID] = true
				}
			}
		}
	}

	// silencedBookIDs tracks books written with the sentinel so Step 4 skips them.
	silencedBookIDs := map[string]bool{}

	// retryFailed holds the books whose silence retry told us nothing about the
	// book: a batch/transport error, no result, a misconfigured worker, or (local)
	// a clip that could not be cut. A retry that returned a real per-job verdict
	// is NOT in here; its error stays in batchResults for Step 4 to classify.
	// A book in here was never shown to be silent: writing the [SILENCE] sentinel for it would make only_missing skip
	// it forever on the strength of a mount drop or an endpoint outage. It gets
	// no outcome this run and is counted deferred, so the next run retries it.
	retryFailed := map[string]bool{}

	// longClipUnknown holds the books whose retry 1 (the 300 s clip) got no
	// verdict: a timeout or crash, or a dispatcher condemnation (which is what a
	// worker older than the per-clip deadline produces). Nothing listened past
	// 90 s for them, so they may NEVER be sealed silent this run: retry 2 still
	// runs (a transcript from file 2 is the book's answer), but an empty or
	// failed retry 2 defers the book instead of writing [SILENCE]. A book with no
	// second file is deferred outright. No strike is counted: the per-file op
	// only ever sends the 90 s clip, and a strike from a clip nobody else sends
	// would walk a file the rest of the library can read toward a permanent
	// verdict.
	longClipUnknown := map[string]bool{}

	// retryRound re-transcribes a set of source files (book id -> path) with a
	// clip of the given length and returns whatever came back, with the ids
	// whose retry failed (see retryFailed) in the second result.
	// Local mode cuts the clips with ffmpeg here; remote mode sends the paths to
	// a worker, so a remote-mode run's retries never decode on this server either.
	retryRound := func(name string, srcs map[string]string, seconds int) (map[string]transcribe.BatchResult, map[string]bool) {
		failed := map[string]bool{}
		// collect flags every submitted id whose retry says nothing about the
		// book: no result at all, or (remote) a worker that is itself
		// misconfigured. A per-job error that IS a verdict about the book
		// (ffmpeg_error on the 300 s clip, a not_found the server's own stat
		// confirms) is not a failure here: the caller keeps it in batchResults
		// and Step 4's classifyTranscribeError decides what it means.
		//
		// A worker_misconfigured answer is the worker's fault on either transport
		// (the upload worker answers it too when its decoder cannot start), so it
		// is never a verdict. When the dispatch itself failed, a strike-type
		// answer in it is not a verdict either (see dropPoolStrikes), unless the
		// dispatcher condemned the file.
		collect := func(res map[string]transcribe.BatchResult, sent map[string]string, rerr error) {
			for id := range sent {
				r, ok := res[id]
				switch {
				case !ok, transcribe.IsNoAnswer(r.Error):
					failed[id] = true
				case rerr != nil && isTimeoutResult(r.Error) && !r.Condemned:
					failed[id] = true
				}
			}
		}
		if len(srcs) == 0 {
			return nil, failed
		}
		if remote {
			jobs := make(map[string]transcribe.PathJob, len(srcs))
			for id, src := range srcs {
				jobs[id] = transcribe.PathJob{Path: src, Seconds: seconds}
			}
			res, rerr := transcribePathBatchFn(ctx, jobs, batchOpts)
			if rerr != nil {
				// Whatever the pool answered before it failed stands; collect
				// marks every id it did not answer as failed.
				log.Warn("transcribe: "+name+" path batch failed", "jobs", len(jobs), "answered", len(res), "err", rerr)
			}
			collect(res, srcs, rerr)
			return res, failed
		}
		wavs := make(map[string]string, len(srcs))
		for id, src := range srcs {
			wav := filepath.Join(tmpDir, "r"+strings.TrimPrefix(name, "retry")+"_"+id+".wav")
			cmd := exec.CommandContext(ctx, "ffmpeg",
				"-y", "-i", src, "-t", strconv.Itoa(seconds),
				"-vn", "-ar", "16000", "-ac", "1", "-f", "wav", wav)
			if out, ferr := cmd.CombinedOutput(); ferr != nil {
				log.Warn("transcribe: "+name+" ffmpeg failed", "book_id", id, "err", ferr, "out", strings.TrimSpace(string(out)))
				failed[id] = true
			} else {
				wavs[id] = wav
			}
		}
		if len(wavs) == 0 {
			return nil, failed
		}
		res, rerr := transcribeBatchFn(ctx, wavs, batchOpts)
		if rerr != nil {
			log.Warn("transcribe: "+name+" batch failed", "jobs", len(wavs), "answered", len(res), "err", rerr)
		}
		collect(res, wavs, rerr)
		return res, failed
	}

	if err != nil && len(silentQueue) > 0 {
		// The pool is failing, so a longer clip would go to the same place. A
		// book that came back empty is not known to be silent here either way:
		// it is deferred (see retryFailed), not written as silence.
		log.Warn("transcribe: pool failed this page, silence retries skipped and those books deferred", "count", len(silentQueue))
		for _, sb := range silentQueue {
			retryFailed[sb.book.ID] = true
		}
		silentQueue = nil
	}

	if len(silentQueue) > 0 {
		log.Info("transcribe: retrying with 300s clip", "count", len(silentQueue))
		retry1Srcs := make(map[string]string, len(silentQueue))
		for _, sb := range silentQueue {
			retry1Srcs[sb.book.ID] = sb.src
		}
		r1, r1failed := retryRound("retry1", retry1Srcs, remoteRetryLongSeconds)
		for id, res := range r1 {
			if r1failed[id] {
				continue // says nothing about the book; Step 4 defers it
			}
			if isTimeoutResult(res.Error) {
				// The 300 s clip ran the decoder out of time (or killed it), so
				// nothing heard past 90 s: see longClipUnknown. The book goes on
				// to retry 2 if it has a second file, and is never sealed silent.
				log.Warn("transcribe: retry 1 (300s clip) timed out, no strike counted; the book may not be marked silent this run",
					"book_id", id, "condemned", res.Condemned, "err", truncateDetail(res.Error))
				longClipUnknown[id] = true
				continue
			}
			// A transcript replaces the empty first pass; so does a per-job
			// error, which Step 4 classifies (see retryRound's collect).
			if res.Text != "" || res.Error != "" {
				batchResults[id] = res
			}
		}
		for id := range r1failed {
			retryFailed[id] = true
		}

		// Collect books still silent after retry1 (a failed retry is not
		// "still silent": it is unknown, see retryFailed).
		var stillSilent []silentBook
		for _, s := range silentQueue {
			if retryFailed[s.book.ID] {
				continue
			}
			if res := batchResults[s.book.ID]; res.Text == "" && res.Error == "" {
				stillSilent = append(stillSilent, s)
			}
		}

		if len(stillSilent) > 0 {
			log.Info("transcribe: retrying with second audio file", "count", len(stillSilent))
			retry2Srcs := make(map[string]string, len(stillSilent))
			for _, sb := range stillSilent {
				ref2, _ := nthAudioFile(store, sb.book, 1)
				if ref2.Path == "" {
					continue
				}
				if !retryDurable && ref2.TranscribeStatus == statusTranscribeTimeout {
					// The second file is already a permanent transcribe_timeout:
					// sending it again only costs a worker its decoder. The book
					// is treated as having no usable second file.
					log.Warn("transcribe: second audio file is a permanent transcribe_timeout, retry 2 not sent",
						"book_id", sb.book.ID, "file_id", ref2.BookFileID)
					continue
				}
				retry2Srcs[sb.book.ID] = ref2.Path
				// From here a timeout on this book is the SECOND file's.
				sentRef[sb.book.ID] = ref2
			}
			r2, r2failed := retryRound("retry2", retry2Srcs, remoteClipSeconds)
			for id, res := range r2 {
				if r2failed[id] {
					continue // says nothing about the book; Step 4 defers it
				}
				if res.Text != "" || res.Error != "" {
					batchResults[id] = res
				}
			}
			for id := range r2failed {
				retryFailed[id] = true
			}

			// Mark books that exhausted all retries with the silence sentinel.
			markedSilent := 0
			for _, s := range stillSilent {
				if br := batchResults[s.book.ID]; br.Text != "" || br.Error != "" || retryFailed[s.book.ID] || longClipUnknown[s.book.ID] {
					continue
				}
				// s.book was copied before ffmpeg and two whisper passes ran, so
				// the write re-reads the row (see writeSilenceSentinel).
				id := s.book.ID
				wrote, settled := writeSilenceSentinel(store, log, id)
				if wrote {
					markedSilent++
				}
				if settled {
					silencedBookIDs[id] = true
				}
			}
			if markedSilent > 0 {
				log.Info("transcribe: marked as silence", "count", markedSilent)
			}
		}
		// A book whose 300 s clip got no verdict and that retry 2 did not answer
		// with text or a verdict of its own is deferred, not silent.
		for id := range longClipUnknown {
			if br := batchResults[id]; br.Text == "" && br.Error == "" {
				retryFailed[id] = true
			}
		}
	}

	// Step 4: write per-book outcome for every book that had a WAV submitted.
	// Books that received the silence sentinel in Step 3b are skipped here so
	// the sentinel is not overwritten with statusEmpty.
	for bookID := range submitted {
		book := bookByID[bookID]
		result, ok := batchResults[bookID]
		switch {
		case !ok && batchErr == "":
			// No answer is not a verdict. The pool failed before it reached this
			// book, or a transport dropped the job: nothing is written, and the
			// next run sends it again. (Writing "no whisper result" as a
			// whisper_error here is what turned a half-answered page into
			// per-book failures.)
			log.Warn("transcribe: no whisper result for a sent book, deferred (no status written)", "book_id", bookID)
			accum.recordDeferred(1)
		case ok && transcribe.IsNoAnswer(result.Error):
			// No answer about the book: the worker is broken (ffmpeg missing from
			// its PATH, a decoder that cannot start), the request was
			// abandoned, or the dispatcher deferred a
			// job only faulted workers answered. Storing it as a per-book verdict
			// is the 2026-08-31 shape: write nothing and defer, like an
			// unreachable endpoint. Checked before every other error case: local
			// mode would otherwise classify it as a whisper_error.
			log.Warn("transcribe: no answer about the book, deferred (no status written)", "book_id", bookID, "err", result.Error)
			accum.recordDeferred(1)
		case ok && isTimeoutResult(result.Error):
			// The worker killed its decoder on this clip, or it died holding it.
			// Counted per file (the file that clip came from), and permanent only
			// at the strike limit; see recordBookTimeout.
			outcome, held := p.recordBookTimeout(ctx, store, log, &book, sentRef[bookID], result, now, accum, gate)
			if outcome != bookTimeoutHeldResult {
				continue
			}
			// The file the request carried already holds a result, so the timeout
			// says nothing about it: use that result rather than guess.
			switch {
			case held.real():
				// The file's own transcript (the per-file op covered it) outranks
				// silence: it is the book's answer, parsed like any other.
				log.Warn("transcribe: the timed-out file already holds a transcript, applied as the book's result",
					"book_id", bookID, "file_id", sentRef[bookID].BookFileID)
				if p.applyTranscript(store, log, &book, held.text, now, accum) {
					processed++
				}
			case held.silent() && wasSilent[bookID] && !longClipUnknown[bookID]:
				// The file is itself recorded as silent and the book was silent
				// on the first pass: silent as far as every clip could listen.
				// Not when the 300 s clip got no verdict (longClipUnknown): then
				// nothing listened past 90 s and the book is deferred below.
				if _, settled := writeSilenceSentinel(store, log, bookID); settled {
					log.Warn("transcribe: silent book whose retry hit a file already recorded as silent, marked as silence", "book_id", bookID)
					continue
				}
				accum.recordDeferred(1)
			default:
				// A result that is neither text nor silence cannot be judged;
				// the sentinel is never written on that.
				log.Warn("transcribe: the timed-out file holds a result that is neither a transcript nor silence, book deferred",
					"book_id", bookID, "file_id", sentRef[bookID].BookFileID, "held_status", held.status)
				accum.recordDeferred(1)
			}
		case !ok || result.Error != "":
			status, detail := statusWhisperError, batchErr
			if ok {
				status, detail = classifyTranscribeError(remote, srcByID[bookID], result.Error)
			}
			log.Warn("transcribe: "+status, "book_id", bookID, "err", detail)
			p.applyOutcome(store, log, &book, status, detail, "", transcribe.IntroFields{}, now, accum)
		case result.Text == "":
			if silencedBookIDs[bookID] {
				continue // handled by Step 3b; sentinel already written
			}
			if retryFailed[bookID] {
				// A silence retry failed, so the book is not known to be
				// silent: write nothing (no sentinel, no empty verdict) and
				// leave it for the next run.
				log.Warn("transcribe: silence retry failed, book deferred (no status written)", "book_id", bookID)
				accum.recordDeferred(1)
				continue
			}
			p.applyOutcome(store, log, &book, statusEmpty, "whisper returned empty text", "", transcribe.IntroFields{}, now, accum)
		default:
			if p.applyTranscript(store, log, &book, result.Text, now, accum) {
				processed++
			}
			// The audio decoded after all: strikes counted against these bytes
			// (by this op or the per-file one) are evidence the file no longer
			// supports.
			p.clearFileTimeouts(store, log, bookID, sentRef[bookID], gate)
		}
	}

	return processed
}

// applyTranscript parses a transcript and writes it as the book's outcome.
// statusOK requires a parsed title. When Whisper returns text but the parser
// can't extract a title, the raw transcript is stored but marked statusUnparsed
// so it's excluded by OnlyParsedTranscription and can be retried later via
// reparse_only after a parser fix. Returns whether a transcript was written.
func (p *Plugin) applyTranscript(store bookModifier, log interface {
	Info(string, ...any)
	Warn(string, ...any)
}, book *database.Book, text string, now time.Time, accum *transcribeStatsAccum) bool {
	fields := transcribe.ParseAudiobookIntro(text)
	outcome := statusOK
	if fields.Title == "" {
		outcome = statusUnparsed
	}
	return p.applyOutcome(store, log, book, outcome, "", text, fields, now, accum)
}

// writeSilenceSentinel stores the silence sentinel on a book, under the book's
// write lock (ModifyBook), so a column another writer committed meanwhile is
// not reverted (audit A1#15). A transcript that landed meanwhile outranks the
// sentinel and is kept, and a row already carrying it is not written again (no
// UpdatedAt bump). wrote reports a new write; settled reports that the book now
// carries the sentinel or a transcript, i.e. nothing more is owed for it.
func writeSilenceSentinel(store bookModifier, log interface{ Warn(string, ...any) }, bookID string) (wrote, settled bool) {
	sentinel := silenceSentinel
	written, err := store.ModifyBook(bookID, func(cur *database.Book) error {
		if cur.IntroTranscription != nil && *cur.IntroTranscription != "" {
			return database.ErrSkipBookWrite
		}
		cur.IntroTranscription = &sentinel
		wrote = true
		return nil
	})
	switch {
	case err != nil:
		log.Warn("transcribe: silence sentinel write failed", "book_id", bookID, "err", err)
		return false, false
	case written == nil:
		log.Warn("transcribe: silence sentinel write skipped, book gone", "book_id", bookID)
		return false, false
	}
	return wrote, true
}

// applyOutcome writes a book's transcription outcome and records it in the
// aggregate. On statusOK/statusUnparsed it also stores the raw transcript (and,
// for OK, the parsed fields). Writes are change-guarded: a repeat of the SAME
// failure (same status + detail) skips the UpdateBook so a re-run over ~45K
// stale-path books doesn't churn the DB. A transcript outcome (OK or Unparsed)
// writes unless the row already carries the SAME transcript text under the
// SAME status -- the per-book idempotency guard that makes a re-run served
// from the whisper result journal apply nothing twice. That guard reads the
// BOOK ROW, never the journal: the journal is keyed by clip content, and two
// books with byte-identical clips must each get their own write. Returns true
// when this call wrote a transcript (OK or Unparsed), which the caller counts
// as processed; an idempotent skip is not counted, like a repeated failure.
func (p *Plugin) applyOutcome(
	store bookModifier,
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
	return p.writeOutcome(store, log, book, status, detail, text, fields, now, false)
}

// writeOutcome is applyOutcome without the accounting: it writes the book's
// outcome and counts nothing. A caller that did not attempt the book (it holds
// a verdict already and was not sent) uses it directly so Attempted and the
// per-status counters stay a count of books a worker actually saw.
//
// keepTranscript leaves a book that already carries a transcript untouched
// (re-read under the write lock): a failure label over real text is wrong, and
// the stored verdict is no reason to relabel it.
func (p *Plugin) writeOutcome(
	store bookModifier,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
	},
	book *database.Book,
	status, detail, text string,
	fields transcribe.IntroFields,
	now time.Time,
	keepTranscript bool,
) (ok bool) {
	newDetail := strPtrOrNil(detail)
	hasTranscript := status == statusOK || status == statusUnparsed

	// `book` is the page's copy, taken before ffmpeg and whisper ran; only its
	// ID is used here. ModifyBook re-reads the row under its write lock and
	// sets only the transcribe columns, so a column another writer committed
	// while the clip was being transcribed is not reverted (audit A1#15).
	wrote := false
	written, err := store.ModifyBook(book.ID, func(cur *database.Book) error {
		if keepTranscript && cur.IntroTranscription != nil && *cur.IntroTranscription != "" {
			return database.ErrSkipBookWrite
		}
		// A transcript outcome (OK or Unparsed) always carries fresh text and must
		// write. Only pure failures are change-guarded: identical status+detail
		// already stored → no write (avoids churning ~45K stale-path books on re-run).
		if !hasTranscript &&
			cur.TranscribeStatus != nil && *cur.TranscribeStatus == status &&
			eqStrPtr(cur.TranscribeError, newDetail) {
			return database.ErrSkipBookWrite
		}
		// Same transcript, same status already stored: nothing new to write.
		// Status is compared too so an unparsed->ok upgrade (parser fix) of
		// identical text still writes.
		if hasTranscript &&
			cur.TranscribeStatus != nil && *cur.TranscribeStatus == status &&
			cur.IntroTranscription != nil && *cur.IntroTranscription == text {
			return database.ErrSkipBookWrite
		}

		cur.TranscribeAttemptedAt = &now
		st := status
		cur.TranscribeStatus = &st
		cur.TranscribeError = newDetail

		if hasTranscript {
			cur.IntroTranscription = &text
			cur.IntroTranscribedAt = &now
			// Parsed fields are set only when present. For statusUnparsed Title is
			// empty by definition, leaving TranscribedTitle nil — which is exactly
			// what OnlyParsedTranscription filters on.
			if fields.Title != "" {
				cur.TranscribedTitle = &fields.Title
			}
			if fields.Author != "" {
				cur.TranscribedAuthor = &fields.Author
			}
			if fields.Narrator != "" {
				cur.TranscribedNarrator = &fields.Narrator
			}
			if fields.Translator != "" {
				cur.TranscribedTranslator = &fields.Translator
			}
			if fields.CoverArtist != "" {
				cur.TranscribedCoverArtist = &fields.CoverArtist
			}
		}
		wrote = true
		return nil
	})
	if err != nil {
		log.Warn("transcribe: update failed", "book_id", book.ID, "status", status, "err", err)
		return false
	}
	if written == nil {
		log.Warn("transcribe: update skipped, book gone", "book_id", book.ID, "status", status)
		return false
	}
	if !wrote {
		return false
	}
	return hasTranscript
}

// classifyTranscribeError maps one book's per-file error from the whisper call
// to the status and detail to store.
//
// Local mode: every error is a whisper_error, as it always was. Remote mode:
// the worker prefixes its errors with a stable code (transcribe.PathErrorCode),
// and each code means something different for the book's FUTURE:
//
//   - ffmpeg_error: the worker's ffmpeg could not decode the file. A verdict
//     about the file, so it is the (durable) ffmpeg_error status.
//   - path_unmapped / not_found: the worker could not reach the file. Whether the
//     BOOK is at fault is answered by the one witness this process has: if the
//     server cannot stat the file either, it is gone (source_file_missing,
//     durable, self-heals when the file returns). If the server CAN, the fault
//     is the worker's mapping or mount, so path_unmapped, which is not durable
//     and is retried next run.
//   - worker_misconfigured never reaches this function: the caller defers the
//     book (no status written), because the worker, not the book, is at fault.
//   - ffmpeg_timeout, container_rejected, bad_request, whisper_error, or no code:
//     not a verdict about the file (an NFS stall, a refused container, a
//     malformed request, inference trouble):
//     whisper_error, retried next run. Burying a book as ffmpeg_error because an
//     NFS read stalled would hide it until its mtime changed.
func classifyTranscribeError(remote bool, src, errText string) (status, detail string) {
	if !remote {
		return statusWhisperError, truncateDetail(errText)
	}
	_, msg, _ := strings.Cut(errText, ": ")
	switch transcribe.PathErrorCode(errText) {
	case transcribe.PathErrFFmpeg:
		return statusFFmpegError, truncateDetail(msg)
	case transcribe.PathErrUnmapped, transcribe.PathErrNotFound:
		if _, err := os.Stat(src); err != nil {
			return statusSourceMissing, "source file not found: " + src
		}
		return statusPathUnmapped, truncateDetail(errText)
	default:
		return statusWhisperError, truncateDetail(errText)
	}
}

// firstFilePermanentTimeoutDetail is the detail stamped on a book whose first
// file already carries a permanent transcribe_timeout, so the book was not sent.
// transcribeInputChanged matches it exactly to undo the stamp once the file's
// verdict is gone.
const firstFilePermanentTimeoutDetail = "first audio file is a permanent transcribe_timeout (decoder ran past its deadline)"

// bookTimeoutOutcome is what recordBookTimeout did with one book.
type bookTimeoutOutcome int

const (
	// bookTimeoutDone: the book was accounted for (deferred, or made permanent)
	// and nothing more is owed.
	bookTimeoutDone bookTimeoutOutcome = iota
	// bookTimeoutHeldResult: the file that timed out already holds a result, so
	// the timeout is no information about the book and NOTHING was recorded or
	// counted. The caller decides what the book gets from the held result
	// (see heldIntroResult and Step 4).
	bookTimeoutHeldResult
)

// heldIntroResult is what the timed-out file already held: its stored
// transcript and TranscribeStatus ("" when unset), read under the row lock that
// refused the strike.
type heldIntroResult struct{ text, status string }

// real reports a transcript that is the file's actual answer.
func (h heldIntroResult) real() bool { return h.text != "" && h.text != silenceSentinel }

// silent reports a result that says the clip held no speech: the sentinel, or an
// empty verdict with no text.
func (h heldIntroResult) silent() bool {
	return h.text == silenceSentinel || (h.text == "" && h.status == statusEmpty)
}

// recordBookTimeout handles one book whose request the worker answered with a
// transcribe_timeout (or decoder_crashed). ref is the file that request carried
// (sentRef: the first file, or the second when retry 2 sent it), and the strike
// is counted on THAT file's row, so the per-file op shares it and a replaced file
// starts again; only when the file reaches the strike limit does the book itself
// get the permanent status. Below the limit the book is deferred: nothing about
// it is written and the next run retries. A strike counts once per run, and only
// when the previous one is at least transcribeStrikeMinGap old (one stall seen
// by both ops, or by two pages, is one occasion): otherwise the book is
// deferred without a second strike.
//
// A book with no book_file row (the Book.FilePath fallback) or no stored hash
// has nowhere to keep a strike, so it can never become permanent here: it is
// deferred and logged, and every run will send it again. The worker's own
// deadline bounds the cost of each attempt, which is why that is tolerable.
//
// gate answers whether a book_file write may be made now (no scan running); it
// is asked at most once per page by the caller.
func (p *Plugin) recordBookTimeout(
	ctx context.Context,
	store transcribePageStore,
	log interface {
		Info(string, ...any)
		Warn(string, ...any)
	},
	book *database.Book, ref audioFileRef, result transcribe.BatchResult, now time.Time, accum *transcribeStatsAccum,
	gate func() error,
) (bookTimeoutOutcome, heldIntroResult) {
	errText := result.Error
	intros, hasCap := database.AsCapability[database.BookFileIntroStore](store)
	if !hasCap || ref.BookFileID == "" || ref.StoredHash == "" {
		log.Warn("transcribe: transcribe_timeout but no book_file row/hash to count strikes on, book deferred (no status written)",
			"book_id", book.ID, "has_row", ref.BookFileID != "", "has_hash", ref.StoredHash != "", "has_capability", hasCap)
		accum.recordDeferred(1)
		return bookTimeoutDone, heldIntroResult{}
	}
	// The strike is a book_file write; a library scan can race it, so while one
	// is running the book is deferred instead of the page waiting for the scan.
	if err := gate(); err != nil {
		log.Warn("transcribe: write gate closed, transcribe_timeout strike not recorded, book deferred",
			"book_id", book.ID, "file_id", ref.BookFileID, "err", err.Error())
		accum.recordDeferred(1)
		return bookTimeoutDone, heldIntroResult{}
	}
	detail := truncateDetail(errText)
	out, err := intros.RecordIntroTimeout(book.ID, ref.BookFileID, database.IntroTimeoutStrike{
		SourceHash: ref.StoredHash, RunOpID: accum.snapshot().RunOpID, Detail: detail, At: now,
		Limit: transcribeTimeoutStrikeLimit, MinGap: transcribeStrikeMinGap, Worker: result.Endpoint,
	})
	switch {
	case err != nil:
		log.Warn("transcribe: recording a transcribe_timeout strike failed, book deferred", "book_id", book.ID, "file_id", ref.BookFileID, "err", err.Error())
		accum.recordDeferred(1)
	case out.SameRun:
		log.Info("transcribe: transcribe_timeout already counted for this file in this run, book deferred without a second strike",
			"book_id", book.ID, "file_id", ref.BookFileID, "strikes", out.Strikes)
		accum.recordDeferred(1)
	case out.TooSoon:
		log.Info("transcribe: transcribe_timeout too soon after the file's last strike, book deferred without a second strike",
			"book_id", book.ID, "file_id", ref.BookFileID, "strikes", out.Strikes, "min_gap", transcribeStrikeMinGap.String())
		accum.recordDeferred(1)
	case out.SameWorker:
		log.Info("transcribe: transcribe_timeout from the same worker as the file's last strike, book deferred without a second strike",
			"book_id", book.ID, "file_id", ref.BookFileID, "strikes", out.Strikes, "worker", result.Endpoint)
		accum.recordDeferred(1)
	case out.Stale:
		log.Warn("transcribe: transcribe_timeout strike not recorded (file changed), book deferred",
			"book_id", book.ID, "file_id", ref.BookFileID)
		accum.recordDeferred(1)
	case out.Skipped:
		log.Warn("transcribe: transcribe_timeout strike not recorded (file already has a result)",
			"book_id", book.ID, "file_id", ref.BookFileID)
		return bookTimeoutHeldResult, heldIntroResult{text: out.HeldText, status: out.HeldStatus}
	case out.Permanent:
		log.Warn("transcribe: transcribe_timeout is now permanent for this book",
			"book_id", book.ID, "file_id", ref.BookFileID, "strikes", out.Strikes, "limit", transcribeTimeoutStrikeLimit)
		p.applyOutcome(store, log, book, statusTranscribeTimeout, detail, "", transcribe.IntroFields{}, now, accum)
	default:
		log.Warn("transcribe: transcribe_timeout strike recorded, book deferred to the next run",
			"book_id", book.ID, "file_id", ref.BookFileID, "strikes", out.Strikes, "limit", transcribeTimeoutStrikeLimit)
		accum.recordDeferred(1)
	}
	return bookTimeoutDone, heldIntroResult{}
}

// clearFileTimeouts drops the timeout strikes on the file whose audio just
// produced a transcript (and a transcribe_timeout verdict they led to). It only
// acts when the ref shows strikes counted against the bytes that were sent, and
// is best-effort: a failure, or a scan running, leaves the strikes for the next
// run's success.
func (p *Plugin) clearFileTimeouts(
	store transcribePageStore,
	log interface{ Warn(string, ...any) },
	bookID string, ref audioFileRef, gate func() error,
) {
	if ref.BookFileID == "" || ref.StoredHash == "" || ref.IntroTimeouts == 0 || ref.IntroTimeoutHash != ref.StoredHash {
		return
	}
	intros, ok := database.AsCapability[database.BookFileIntroStore](store)
	if !ok {
		return
	}
	if err := gate(); err != nil {
		log.Warn("transcribe: write gate closed, timeout strikes left in place", "book_id", bookID, "file_id", ref.BookFileID, "err", err.Error())
		return
	}
	if _, err := intros.ClearIntroTimeouts(bookID, ref.BookFileID, ref.StoredHash); err != nil {
		log.Warn("transcribe: clearing timeout strikes failed", "book_id", bookID, "file_id", ref.BookFileID, "err", err.Error())
	}
}

// ffmpegErrorTail returns the most diagnostic tail of ffmpeg stderr. ffmpeg
// prints a long build banner before the actual error; the real reason is on the
// last non-empty line(s). Returns at most ~200 chars.
func ffmpegErrorTail(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	// Walk back to the last non-empty line — that's almost always the error.
	last := ""
	for _, line := range slices.Backward(lines) {
		if s := strings.TrimSpace(line); s != "" {
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

// transcribableExtSet is the set of containers the whisper worker can decode.
// A CAPABILITY list, not supported_extensions: it carries .mp4 (which the
// library does not ingest) and omits DRM-encrypted .aax/.aaxc (which the
// library does ingest but nothing here can decode).
var transcribableExtSet = map[string]bool{
	".m4b": true, ".mp3": true, ".m4a": true, ".mp4": true,
	".flac": true, ".aac": true, ".ogg": true, ".wma": true,
}

// audioFileRef identifies one audio file of a book, together with everything
// the transcription/clip cache needs to know about it.
//
// StoredHash is carried separately from CacheKey on purpose. CacheKey falls
// back to an "fp:"/"path:" form when the row has no hash, so a caller that
// wants to know whether book_files.file_hash is populated would otherwise have
// to sniff a prefix off the cache key — one string answering two questions,
// which is how a caller ends up overwriting a hash it meant to leave alone.
type audioFileRef struct {
	// Path is the on-disk path. Empty when the book has no usable audio file;
	// every caller must check this before using the rest.
	Path string
	// CacheKey is the clip-cache filename stem:
	// StoredHash > "fp:"+sha256(AcoustIDFingerprint) > "path:"+sha256(FilePath).
	CacheKey string
	// BookFileID is the book_files row id. Empty for the Book.FilePath
	// fallback, where there is no row to write back to.
	BookFileID string
	// StoredHash is book_files.file_hash exactly as stored — empty when the row
	// has none. Per internal/filehash it is the canonical chunked digest; a
	// value that disagrees with filehash.BookFileHash is a corrupted row, not a
	// signal to rewrite.
	StoredHash string
	// TranscribeStatus is the per-file transcription status stored on the row
	// ("" when none, and always "" for the Book.FilePath fallback). The book op
	// reads it to honour a permanent per-file verdict the per-file op wrote.
	TranscribeStatus string
	// IntroTimeouts and IntroTimeoutHash are the decoder-timeout strikes the row
	// carries and the FileHash they were counted against (zero/"" when none).
	IntroTimeouts    int
	IntroTimeoutHash string
	// HeldSilence is true when the row already holds the per-file op's silence
	// result for its CURRENT bytes (status empty, provenance hash equal to the
	// stored FileHash): the first 90 s of this file were heard and are silent.
	HeldSilence bool
}

// firstAudioFile returns the first (lowest disc/track) audio file of book.
// Delegates to nthAudioFile(0).
func firstAudioFile(store bookFileLister, book database.Book) (audioFileRef, error) {
	return nthAudioFile(store, book, 0)
}

// nthAudioFile returns the nth audio file of book, sorted by (disc, track,
// path) — the same canonical ordering GetBookFiles uses. n=0 is the first file.
// The book-level FilePath fallback (for single-track iTunes imports with no
// BookFile rows) is only used when n==0 and no BookFile records exist.
func nthAudioFile(store bookFileLister, book database.Book, n int) (audioFileRef, error) {
	files, err := store.GetBookFiles(book.ID)
	if err != nil {
		return audioFileRef{}, err
	}

	var audio []database.BookFile
	for _, f := range files {
		if transcribableExtSet[strings.ToLower(filepath.Ext(f.FilePath))] && f.FilePath != "" {
			audio = append(audio, f)
		}
	}

	if len(audio) == 0 {
		if n != 0 {
			return audioFileRef{}, nil
		}
		// No BookFile rows — fall back to Book.FilePath for single-file imports.
		fp := book.FilePath
		if fp != "" && transcribableExtSet[strings.ToLower(filepath.Ext(fp))] {
			h := sha256.Sum256([]byte(fp))
			return audioFileRef{Path: fp, CacheKey: "path:" + hex.EncodeToString(h[:])}, nil
		}
		return audioFileRef{}, nil
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
		return audioFileRef{}, nil
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
	ref := audioFileRef{Path: f.FilePath, CacheKey: key, BookFileID: f.ID, StoredHash: f.FileHash}
	if f.TranscribeStatus != nil {
		ref.TranscribeStatus = *f.TranscribeStatus
	}
	ref.IntroTimeouts, ref.IntroTimeoutHash = f.IntroTimeouts, f.IntroTimeoutHash
	ref.HeldSilence = f.FileHash != "" && f.IntroSourceHash == f.FileHash && ref.TranscribeStatus == statusEmpty
	return ref, nil
}

// wavCacheDir returns the directory used to cache extracted 90s WAV clips.
// Env WHISPER_CLIP_CACHE_DIR overrides. Default: {rootDir}/.wav-cache.
// rootDir must not be empty; callers should pass p.deps.RootDir().
func wavCacheDir(rootDir string) string {
	if d := config.AppConfig.WhisperClipCacheDir; d != "" {
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
