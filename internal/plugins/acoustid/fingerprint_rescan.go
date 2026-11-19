// file: internal/plugins/acoustid/fingerprint_rescan.go
// version: 1.10.0
// guid: a7b8c9d0-e1f2-3456-def0-123456789abc
// last-edited: 2026-11-19

package acoustid

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// fingerprintRescanKind is rule R3's native kind for acoustid.fingerprint-rescan.
const fingerprintRescanKind = tasks.Kind("books.acoustid.fingerprint-rescan")

// fingerprintRescanHelp is the definition's description, HEAD's and the native
// kind's Help (rule R5: verbatim).
const fingerprintRescanHelp = "Generates per-file fingerprints on demand with scope and force options."

// fingerprintRescanEnv is what the rescan core takes from its shell (rules R2
// and R10): reads through Store, every fingerprint and signature write through
// Writer, and the process facts (server-decode switch, fpcalc probe, worker
// count, report file) through Host.
type fingerprintRescanEnv struct {
	kindEnv
	Store  kindStore
	Writer kindWriter
	Host   hostHooks
}

// fingerprintRescanResult is the native run's result: the tallies in the closing
// message. HEAD stored no result, so its shell drops it.
type fingerprintRescanResult struct {
	Books         int    `json:"books"`
	Files         int    `json:"files"`
	Fingerprinted int    `json:"fingerprinted"`
	Skipped       int    `json:"skipped"`
	Ineligible    int    `json:"ineligible"`
	Failed        int    `json:"failed"`
	ReportPath    string `json:"report_path,omitempty"`
}

// fingerprintRescanParallelism is the per-book fpcalc pool (rule R4): HEAD's
// hand-rolled semaphore of fpRescanWorkers() books at a time (the
// FP_PARALLEL_WORKERS knob, default 4), files within a book sequential. It stays
// HEAD's exact pool in HEAD's shell (env.Pool).
func fingerprintRescanParallelism(workers int) tasks.Parallelism {
	return tasks.Fixed(workers, "one fpcalc subprocess per in-flight book; sized from the FP_PARALLEL_WORKERS knob (fpRescanWorkers), default 4")
}

// FingerprintRescanParams controls the scope and options of a fingerprint rescan operation.
type FingerprintRescanParams struct {
	Scope   string   `json:"scope,omitempty"`    // "missing" (default), "all", or "books"
	BookIDs []string `json:"book_ids,omitempty"` // required when scope=="books"
	Force   bool     `json:"force,omitempty"`    // ignore existing fingerprints and recompute
}

const (
	scopeMissing = "missing"
	scopeAll     = "all"
	scopeBooks   = "books"

	ineligibleReportDir = "/var/lib/audiobook-organizer/reports"
)

// ineligibleEntry is one line in the JSONL report written at end of rescan.
type ineligibleEntry struct {
	BookID    string `json:"book_id"`
	BookTitle string `json:"book_title"`
	FilePath  string `json:"file_path"`
	Reason    string `json:"reason"`
}

func (p *Plugin) fingerprintRescanDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:              "acoustid.fingerprint-rescan",
		Liveness:        sdk.LivenessManual,
		Plugin:          "acoustid",
		DisplayName:     "Fingerprint rescan",
		Description:     fingerprintRescanHelp,
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "acoustid.fingerprint",
		Isolate:         false, // DISABLED 2026-05-29: PR #1172 child-mode wire-up cannot work because Pebble is single-writer; child re-open fails. See MAYDEPLOY-A revisit.
		Timeout:         12 * time.Hour,
		Capabilities: []sdk.Capability{
			sdk.CapLibraryRead,
			sdk.CapLibraryWrite,
			sdk.CapFilesRead,
			sdk.CapFilesExecute,
			sdk.CapSubprocessSpawn,
		},
		Run: p.runFingerprintRescan,
	}
}

func (p *Plugin) runFingerprintRescan(ctx context.Context, params json.RawMessage, reporter sdk.Reporter) error {
	_, err := fingerprintRescanCore(ctx, params, p.headFingerprintRescanEnv(reporter))
	return err
}

// headFingerprintRescanEnv is HEAD's env: the plugin's store for reads and
// writes and HEAD's own process hooks and hand-rolled pool. A plugin with no
// store hands the core a nil Store, which it refuses with HEAD's message (after
// the server-decode refusal, as HEAD ordered them).
func (p *Plugin) headFingerprintRescanEnv(reporter sdk.Reporter) fingerprintRescanEnv {
	env := fingerprintRescanEnv{kindEnv: headEnv(reporter, true), Host: headHostHooks()}
	if p.store != nil {
		env.Store = p.store
		env.Writer = headKindWriter{store: p.store}
	}
	return env
}

// fingerprintRescanCore is the body of runFingerprintRescan, shared by both
// shells. params is the raw start params, decoded here leniently (a bad body is
// logged and means the defaults), as HEAD did.
func fingerprintRescanCore(ctx context.Context, params json.RawMessage, env fingerprintRescanEnv) (fingerprintRescanResult, error) {
	if err := env.Host.ServerDecodeCheck("acoustid.fingerprint-rescan"); err != nil {
		return fingerprintRescanResult{}, err
	}
	if env.Store == nil {
		return fingerprintRescanResult{}, fmt.Errorf("database store not available")
	}

	if !env.Host.FingerprintAvailable() {
		return fingerprintRescanResult{}, fmt.Errorf("no fingerprint backend (fpcalc / ffmpeg) found")
	}
	store := kindView{env.Store, env.Writer}
	log := env.Logger

	var req FingerprintRescanParams
	if len(params) > 0 {
		if err := json.Unmarshal(params, &req); err != nil {
			log.Error("failed to unmarshal params", "error", err)
			req = FingerprintRescanParams{}
		}
	}

	scope := req.Scope
	if scope == "" {
		scope = scopeMissing
	}
	switch scope {
	case scopeMissing, scopeAll:
		// ok
	case scopeBooks:
		if len(req.BookIDs) == 0 {
			return fingerprintRescanResult{}, fmt.Errorf("book_ids is required when scope is \"books\"")
		}
	default:
		return fingerprintRescanResult{}, fmt.Errorf("scope must be one of: missing, all, books")
	}

	env.Progress(0, 1, "Loading books for fingerprint rescan...")

	books, lerr := loadBooksForRescan(store, scope, req.BookIDs)
	if lerr != nil {
		log.Error("load books", "error", lerr)
		return fingerprintRescanResult{}, fmt.Errorf("load books: %w", lerr)
	}

	total := len(books)
	if total == 0 {
		env.Progress(1, 1, "No books matched the requested scope")
		return fingerprintRescanResult{}, nil
	}

	workers := env.Host.Workers()

	var (
		fingerprinted  atomic.Int64
		skipped        atomic.Int64
		ineligible     atomic.Int64
		failed         atomic.Int64
		completedBooks atomic.Int64
		totalFiles     atomic.Int64
		filesDone      atomic.Int64
	)
	startedAt := time.Now()

	// ineligibleMu guards ineligibleEntries, written by concurrent book goroutines.
	var ineligibleMu sync.Mutex
	var ineligibleEntries []ineligibleEntry

	progressMsg := func() string {
		return fmt.Sprintf("Books %d/%d files ~%d/%d (fp=%d skip=%d ineligible=%d fail=%d)",
			completedBooks.Load(), int64(total), filesDone.Load(), totalFiles.Load(),
			fingerprinted.Load(), skipped.Load(), ineligible.Load(), failed.Load())
	}

	// Heartbeat goroutine keeps the registry watchdog satisfied independent of
	// how long individual books take.
	hbCtx, cancelHB := context.WithCancel(ctx)
	defer cancelHB()
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				env.ItemProgress(int(completedBooks.Load()), total, progressMsg())
			case <-hbCtx.Done():
				return
			}
		}
	}()

	// The pool limits concurrent book processing to `workers`. Files within each
	// book are processed sequentially — with N books running simultaneously
	// there are already N concurrent fpcalc calls, which is enough to saturate
	// the CPU without nested goroutine overhead. HEAD's shell runs its own
	// semaphore pool here; the native shell the executor's.
	if err := env.Pool(ctx, fingerprintRescanParallelism(workers), total, kindItemsOptions{}, func(ctx context.Context, bi int) error {
		book := books[bi]

		if ctx.Err() != nil {
			completedBooks.Add(1)
			return nil
		}

		files, ferr := env.Store.GetBookFiles(book.ID)
		if ferr != nil {
			completedBooks.Add(1)
			return nil
		}
		totalFiles.Add(int64(len(files)))

		for _, f := range files {
			if ctx.Err() != nil {
				break
			}

			outcome, reason, stop := fingerprintEligibility(f, req.Force)
			if stop {
				switch outcome {
				case fingerprintOutcomeSkipped:
					skipped.Add(1)
				case fingerprintOutcomeIneligible:
					ineligible.Add(1)
					ineligibleMu.Lock()
					ineligibleEntries = append(ineligibleEntries, ineligibleEntry{
						BookID:    book.ID,
						BookTitle: book.Title,
						FilePath:  f.FilePath,
						Reason:    reason,
					})
					ineligibleMu.Unlock()
				}
			} else {
				switch doFingerprintFile(store, f, req.Force) {
				case fingerprintOutcomeFingerprinted:
					fingerprinted.Add(1)
				case fingerprintOutcomeFailed:
					failed.Add(1)
				}
			}
			filesDone.Add(1)
		}

		if err := synthesizeBookSignatureForBook(store, book.ID); err != nil {
			log.Warn("synthesize book signature", "book_id", book.ID, "error", err)
		}
		completedBooks.Add(1)
		return nil
	}); err != nil {
		return fingerprintRescanResult{}, err
	}

	cancelHB()

	reportPath, reportErr := env.Host.WriteIneligibleReport(time.Now().Format("20060102-150405"), ineligibleEntries)
	if reportErr != nil {
		log.Warn("failed to write ineligible report", "err", reportErr)
	} else if len(ineligibleEntries) > 0 {
		log.Info("ineligible file report written", "path", reportPath, "count", len(ineligibleEntries))
	}

	// Log reason breakdown so it's visible in journalctl without reading the file.
	if len(ineligibleEntries) > 0 {
		counts := make(map[string]int)
		for _, e := range ineligibleEntries {
			// Normalise non_audio_ext:.<ext> → non_audio_ext for the summary.
			key := e.Reason
			if len(key) > 14 && key[:14] == "non_audio_ext:" {
				key = "non_audio_ext"
			}
			counts[key]++
		}
		log.Info("ineligible reason breakdown", "reasons", counts)
	}

	env.Progress(total, total,
		fmt.Sprintf("Fingerprint rescan complete in %s — fp=%d skip=%d ineligible=%d fail=%d (of %d books, %d files)",
			time.Since(startedAt).Round(time.Second),
			fingerprinted.Load(), skipped.Load(), ineligible.Load(), failed.Load(), total, filesDone.Load()))
	return fingerprintRescanResult{
		Books: total, Files: int(filesDone.Load()), Fingerprinted: int(fingerprinted.Load()),
		Skipped: int(skipped.Load()), Ineligible: int(ineligible.Load()), Failed: int(failed.Load()),
		ReportPath: reportPath,
	}, nil
}

// writeIneligibleReport writes ineligible entries as JSONL sorted by reason
// then book title. Returns the path written.
func writeIneligibleReport(opID string, entries []ineligibleEntry) (string, error) {
	if len(entries) == 0 {
		return "", nil
	}
	if err := os.MkdirAll(ineligibleReportDir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir reports: %w", err)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Reason != entries[j].Reason {
			return entries[i].Reason < entries[j].Reason
		}
		return entries[i].BookTitle < entries[j].BookTitle
	})

	name := fmt.Sprintf("fp-ineligible-%s.jsonl", opID)
	path := filepath.Join(ineligibleReportDir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("create report file: %w", err)
	}
	defer f.Close()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, e := range entries {
		if err := enc.Encode(e); err != nil {
			return "", fmt.Errorf("encode entry: %w", err)
		}
	}
	return path, w.Flush()
}

// fpRescanWorkers returns the number of parallel fpcalc workers for rescan.
// Tunable via config.AppConfig.FPParallelWorkers (FP_PARALLEL_WORKERS env
// var); defaults to 4.
func fpRescanWorkers() int {
	if n := config.AppConfig.FPParallelWorkers; n >= 1 && n <= 32 {
		return n
	}
	return 4
}

// loadBooksForRescan fetches the set of books targeted by the requested scope.
func loadBooksForRescan(store pluginStore, scope string, bookIDs []string) ([]database.Book, error) {
	switch scope {
	case scopeAll, scopeMissing:
		// GetAllBooksFullFrom returns FULL (GetBookByID-sourced) books, matching
		// the scopeBooks branch below. loadBooksForRescan's contract is full
		// books — the rescan reads/writes heavy fingerprint fields — so the
		// memdb-slim GetAllBooks projection would have silently stripped them.
		return store.GetAllBooksFullFrom("", 0)
	case scopeBooks:
		out := make([]database.Book, 0, len(bookIDs))
		for _, id := range bookIDs {
			b, err := store.GetBookByID(id)
			if err != nil || b == nil {
				continue
			}
			out = append(out, *b)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown scope %q", scope)
	}
}
