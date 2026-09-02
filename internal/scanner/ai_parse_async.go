// file: internal/scanner/ai_parse_async.go
// version: 1.6.0
// guid: 5c5dc851-ad6d-4624-b836-a85e38ae5d02
// last-edited: 2026-09-02

package scanner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
)

// EnqueueAIParseFn hands a batch of books that need AI filename parsing to the
// operations queue instead of parsing them inline.
//
// Why this exists: until 2026-08-24 the AI phase ran synchronously at the end of
// every folder in ProcessBooksParallel. Each batch of 20 filenames is a round
// trip to an LLM with a 2s inter-batch delay, so on a library-sized scan the AI
// phase, not the file I/O, is what the scan spends its wall clock on -- and the
// scan holds the "library.scan" ConcurrencyKey the entire time. Moving the work
// to its own queued operation lets the scan finish at disk speed while the LLM
// work drains behind it.
//
// nil by default: internal/scanner has no dependency on the operations registry
// (that would be an import cycle), so internal/server sets this at startup. When
// it is nil -- unit tests, the CLI scanner, any embedder that never wires it --
// the scan keeps the old inline behaviour and nothing changes.
//
// Guarded by enqueueAIParseMu rather than being a bare global: it is written at
// server construction and read from every scan worker goroutine. The package
// already has this exact pattern for the store (pkgStoreMu / SetStore) for the
// same reason.
var (
	enqueueAIParseMu sync.RWMutex
	enqueueAIParseFn func(ctx context.Context, books []AIParseCandidate) error
)

// SetEnqueueAIParse wires (or with nil, unwires) the queue hook. It returns the
// previous value so a caller that installs a hook can restore it -- the test
// binary constructs many servers, and without a restore the last one to run
// leaves a torn-down registry wired for everything after it.
func SetEnqueueAIParse(fn func(ctx context.Context, books []AIParseCandidate) error) func(ctx context.Context, books []AIParseCandidate) error {
	enqueueAIParseMu.Lock()
	defer enqueueAIParseMu.Unlock()
	prev := enqueueAIParseFn
	enqueueAIParseFn = fn
	return prev
}

// EnqueueAIParseWired reports whether a queue is wired. Exported so the server
// package can assert its own registration actually took effect: an unwired hook
// is not an error anywhere, it just silently reverts scans to blocking on the
// LLM inline.
func EnqueueAIParseWired() bool {
	return getEnqueueAIParse() != nil
}

func getEnqueueAIParse() func(ctx context.Context, books []AIParseCandidate) error {
	enqueueAIParseMu.RLock()
	defer enqueueAIParseMu.RUnlock()
	return enqueueAIParseFn
}

// AIParseCandidate is one book as it rides in the operation's params.
//
// It carries the database row ID, and that is the whole point. The first cut of
// this keyed on FilePath, which does not survive the gap between enqueue and
// run: OrganizeOneBook sends every book under RootDir -- i.e. every book in a
// normal library scan -- through ReOrganizeInPlace, which is a true
// safeRename. By the time the queued batch ran, the path in its params named
// nothing, GetBookByFilePath returned no row, and the parse was discarded with
// no error and no log line. An ID survives both the rename and the
// copy-and-demote that CreateOrganizedVersion does.
//
// The field set is measured, not assumed: ai_batch_phase.go reads FilePath,
// Title, Author, Series, Position, Narrator and Publisher, and the saver writes
// the same seven. A full Book would drag SegmentFiles and SegmentHashes into
// the params row -- megabytes of paths and hashes per batch that nothing on
// this path reads.
type AIParseCandidate struct {
	ID        string `json:"id"`
	FilePath  string `json:"file_path"`
	Title     string `json:"title,omitzero"`
	Author    string `json:"author,omitzero"`
	Series    string `json:"series,omitzero"`
	Position  int    `json:"position,omitzero"`
	Narrator  string `json:"narrator,omitzero"`
	Publisher string `json:"publisher,omitzero"`
}

// ErrAIParseEnqueueUnavailable is returned by enqueueAIParse when no queue has
// been wired. It is a signal to run inline, not a failure.
var ErrAIParseEnqueueUnavailable = errors.New("ai parse enqueue not configured")

// aiParseEnqueueChunk is how many books ride in one queued operation.
//
// This is deliberately larger than ai_batch_phase.go's batchSize of 20 (which is
// how many filenames go in one LLM prompt). One operation carries several
// prompts' worth of work so the queue does not fill with thousands of
// nearly-empty operations, which is the failure mode that made the v1 op tables
// unreadable. 200 books is roughly 10 LLM batches, i.e. a few minutes of work
// per operation -- coarse enough to be cheap, fine enough to stay resumable and
// to show real progress in the UI.
const aiParseEnqueueChunk = 200

// enqueueAIParse hands the AI candidates to the queue in chunks.
//
// It resolves each candidate to its database row ID here, while the scan's paths
// are still live -- this runs inside ProcessBooksParallel, and AutoOrganizeFn
// (which renames files) does not run until after it returns. A candidate with no
// row is DROPPED rather than queued with an empty ID: saveBookToDatabase returns
// early without creating a row for a file that duplicates an already
// version-linked book, and there is nothing for the batch to write to.
//
// It returns the number of LEADING candidates it successfully queued. A
// candidate list longer than one chunk becomes several operations, and an
// enqueue failure part-way through leaves the earlier chunks queued -- they are
// already accepted work and cannot be recalled. Returning the boundary lets the
// caller fall back to inline parsing for the remainder ONLY, instead of
// re-parsing books that are already sitting in the queue and paying for every
// one of them twice at the LLM.
func enqueueAIParse(ctx context.Context, books []Book, candidates []int, scanLog logger.Logger) (int, error) {
	enqueue := getEnqueueAIParse()
	if enqueue == nil {
		return 0, ErrAIParseEnqueueUnavailable
	}
	store := getStore()
	if store == nil {
		return 0, ErrAIParseEnqueueUnavailable
	}

	queued := 0
	noRow := 0
	batch := make([]AIParseCandidate, 0, aiParseEnqueueChunk)
	// pending counts candidate-list positions consumed into the current batch,
	// including any dropped below, so `queued` stays an index into `candidates`
	// rather than a count of books.
	pending := 0
	flush := func() error {
		if len(batch) > 0 {
			if err := enqueue(ctx, batch); err != nil {
				return err
			}
			scanLog.Info("queued %d book(s) for background AI filename parsing", len(batch))
			batch = make([]AIParseCandidate, 0, aiParseEnqueueChunk)
		}
		queued += pending
		pending = 0
		return nil
	}

	for _, idx := range candidates {
		pending++
		if idx < 0 || idx >= len(books) {
			continue
		}
		row, err := store.GetBookByFilePath(books[idx].FilePath)
		if err != nil || row == nil {
			noRow++
			continue
		}
		batch = append(batch, aiParseCandidate(row.ID, books[idx]))
		if len(batch) >= aiParseEnqueueChunk {
			if err := flush(); err != nil {
				return queued, err
			}
		}
	}
	if err := flush(); err != nil {
		return queued, err
	}
	if noRow > 0 {
		scanLog.Warn("AI parse: %d of %d candidate(s) had no database row and were not queued", noRow, len(candidates))
	}
	return queued, nil
}

// aiParseCandidate strips a Book down to what the AI phase actually reads and
// writes, which is measured rather than assumed: ai_batch_phase.go touches
// FilePath, Title, Author, Series, Position, Narrator and Publisher, and
// saveAIFieldsToPrimary touches the same seven.
//
// This bounds the operation's params row. A Book carries SegmentFiles and
// SegmentHashes, so a batch of segment-heavy multi-file books would serialize
// multiple megabytes of paths and hashes into a single op row -- none of which
// any code on this path reads. Carrying them would also make the params blob a
// second, stale copy of data the scan already wrote.
func aiParseCandidate(id string, b Book) AIParseCandidate {
	return AIParseCandidate{
		ID:        id,
		FilePath:  b.FilePath,
		Title:     b.Title,
		Author:    b.Author,
		Series:    b.Series,
		Position:  b.Position,
		Narrator:  b.Narrator,
		Publisher: b.Publisher,
	}
}

// book renders the candidate as the Book the AI phase mutates in place.
func (c AIParseCandidate) book() Book {
	return Book{
		FilePath:  c.FilePath,
		Title:     c.Title,
		Author:    c.Author,
		Series:    c.Series,
		Position:  c.Position,
		Narrator:  c.Narrator,
		Publisher: c.Publisher,
	}
}

// RunAIParseForBooks parses a queued batch of books and saves what it filled in.
//
// This is the operation handler's entry point. It builds its own parser from the
// live config rather than inheriting one from the scan, so a batch that sits in
// the queue across a config change uses the backend that is configured when it
// actually runs.
//
// Returns nil when AI parsing is disabled: a queued batch that arrives after the
// operator turned AI off is not an error, it is work that no longer needs doing.
func RunAIParseForBooks(ctx context.Context, cands []AIParseCandidate, scanLog logger.Logger) (AIPhaseSummary, error) {
	if len(cands) == 0 {
		return AIPhaseSummary{}, nil
	}
	parser, enabled := newAIParser(scanLog)
	if !enabled {
		scanLog.Info("AI parse batch of %d book(s) skipped: AI parsing is not enabled", len(cands))
		return AIPhaseSummary{BooksNominated: len(cands), Disabled: true}, nil
	}

	// The AI phase mutates Book values in place, so build the slice once and
	// key the row IDs off the element ADDRESSES. Keying off FilePath would
	// reintroduce, one layer down, exactly the path-identity assumption that
	// carrying an ID exists to remove.
	books := make([]Book, len(cands))
	idByBook := make(map[*Book]string, len(cands))
	candidates := make([]int, len(cands))
	for i := range cands {
		books[i] = cands[i].book()
		idByBook[&books[i]] = cands[i].ID
		candidates[i] = i
	}

	save := func(ctx context.Context, b *Book) (string, error) {
		return saveAIFieldsToPrimary(ctx, idByBook[b], b)
	}
	return runAIBatchPhase(ctx, parser, books, candidates, scanLog, save), nil
}

// saveAIFieldsToPrimary writes what the AI filled in, and nothing else.
//
// The inline AI phase saves through saveBookToDatabase, and that is correct
// there: it runs inside the scan, on a freshly-walked book, before anything has
// organized it. Neither assumption survives the move to a queued operation, and
// both fail silently:
//
//   - WRONG ROW. saveBookToDatabase keys on FilePath. The scan's AutoOrganizeFn
//     (scanner/service.go:526) runs strictly after ProcessBooksParallel returns,
//     and organize COPIES: CreateOrganizedVersion makes a new row primary and
//     demotes the source to IsPrimaryVersion=false / organized_source, leaving
//     the source row sitting at exactly the path this batch carries. So a
//     path-keyed write lands on the demoted row while the primary -- the record
//     the UI shows -- keeps the empty field. CreateOrganizedVersion snapshots
//     Title/AuthorID/SeriesID/Narrator at creation, so it cannot pick the value
//     up afterwards either.
//
//   - WRONG SIDE EFFECTS. saveBookToDatabase is the full scan write path: dedup,
//     version grouping, path normalization, the RootDir-gated primary branch.
//     Re-running all of it minutes later, on a stale path, for the sake of a
//     title string is not an update, it is a second import.
//
// The blast radius is narrower than it looks, and worth stating so nobody
// "simplifies" this back: organize DEFERS books with no resolvable author
// (organizer/service.go:708), so the candidates nominated for an empty Title or
// Author are still unorganized when the batch runs and would have been fine. The
// ones that bite are candidates that already had a good author and were
// nominated for an empty Series -- those organize immediately.
//
// So this resolves the row fresh, follows the version group to its primary, and
// UpdateBooks only the fields that are still empty there.
func saveAIFieldsToPrimary(_ context.Context, id string, book *Book) (string, error) {
	store := getStore()
	if store == nil {
		return "", errors.New("no store configured")
	}

	var row *database.Book
	var err error
	if id != "" {
		row, err = store.GetBookByID(id)
		if err != nil {
			return "", fmt.Errorf("look up book %s: %w", id, err)
		}
	}
	if row == nil {
		// No ID (an inline caller) or the row was deleted under it. Fall back to
		// the path, which still works for the inline phase and for anything the
		// organizer has not touched.
		row, err = store.GetBookByFilePath(book.FilePath)
		if err != nil {
			return "", fmt.Errorf("look up %s: %w", book.FilePath, err)
		}
	}
	if row == nil {
		// The scan normalizes a multi-file book's row to its parent directory,
		// so a batch queued with a segment path finds nothing here. Same
		// recovery the scan itself uses.
		if recovered := recoverNormalizedBookPath(book.FilePath); recovered != "" {
			row, err = store.GetBookByFilePath(recovered)
			if err != nil {
				return "", fmt.Errorf("look up recovered path %s: %w", recovered, err)
			}
		}
	}
	if row == nil {
		// The row can legitimately be gone by the time the batch runs (deleted,
		// merged by dedup), so this is not an error -- but it is not nothing
		// either. Logged because the previous version returned a bare nil here,
		// which made a systematic resolution failure across every book in every
		// batch look exactly like a library where nothing needed writing.
		aiParseLog.Warn("AI parse: no row for %s (id %q); parse discarded", book.FilePath, id)
		return "", nil
	}

	target, verr := primaryVersionOf(store, row)
	if verr != nil {
		// Deliberately not fatal: writing to the row we have is better than
		// dropping the parse. Logged so a systematic failure is visible as
		// something other than metadata quietly landing on demoted rows.
		aiParseLog.Warn("AI parse: could not resolve the primary version for %s (%v); writing to the row as found, which may be a demoted organized_source", book.FilePath, verr)
	}
	if target != nil {
		row = target
	}

	// A user lock beats "still empty". A blank the user locked is a deliberate
	// blank, and a field the user set that the AI also filled is theirs. Same
	// fail-closed adapter the rescan overlay uses: if the locks cannot be read,
	// every lockable column is treated as locked and the parse is left unapplied
	// (the row's path is still stamped -- it was attempted).
	lockedKeys, locksOK := lockedFieldsForBook(store, row.ID)
	if !locksOK {
		aiParseLog.Warn("AI parse: could not read field locks for %s; leaving every lockable field alone", row.ID)
	}
	locks := database.NewFieldLocks(row.ID, lockedKeys)

	changed := false
	if row.Title == "" && book.Title != "" && !locks.Locked(database.FieldKeyTitle) {
		row.Title = book.Title
		changed = true
	}
	if (row.AuthorID == nil || *row.AuthorID == 0) && book.Author != "" && !locks.Locked(database.FieldKeyAuthorName) {
		authorID, aerr := resolveAuthorID(book.Author)
		if aerr != nil {
			return "", fmt.Errorf("resolve author %q: %w", book.Author, aerr)
		}
		if authorID != nil {
			row.AuthorID = authorID
			changed = true
		}
	}
	if row.SeriesID == nil && book.Series != "" && !locks.Locked(database.FieldKeySeriesName) {
		seriesID, seriesPos, serr := resolveSeriesID(book.Series, row.AuthorID)
		if serr != nil {
			return "", fmt.Errorf("resolve series %q: %w", book.Series, serr)
		}
		if seriesID != nil {
			row.SeriesID = seriesID
			changed = true
			// The LLM's own position wins; the number stripped out of the series
			// name fills the gap when it had none. Either way it is only written
			// into an EMPTY sequence -- a sequence the row already carries came
			// from somewhere with more context than a regex over a name.
			//
			// 🔑 `book.Position > 0` was part of this condition before the merge
			// with #3054 and is deliberately GONE, not lost: the position no
			// longer comes only from the model. seriesPos carries the number
			// stripped out of the series name, so requiring book.Position > 0
			// here would silently discard it in exactly the case this change
			// exists to fix. `pos > 0` below still gates the write.
			if row.SeriesSequence == nil && !locks.Locked(database.FieldKeySeriesPosition) {
				pos := book.Position
				if pos <= 0 {
					pos = seriesPos
				}
				if pos > 0 {
					row.SeriesSequence = &pos
				}
			}
		}
	}
	if isBlankPtr(row.Narrator) && book.Narrator != "" && !locks.Locked(database.FieldKeyNarrator) {
		n := book.Narrator
		row.Narrator = &n
		changed = true
	}
	if isBlankPtr(row.Publisher) && book.Publisher != "" && !locks.Locked(database.FieldKeyPublisher) {
		pub := book.Publisher
		row.Publisher = &pub
		changed = true
	}

	// The row's CURRENT path is what gets stamped into the scan cache, not the
	// one the params carried -- organize may have renamed the file since. An
	// unchanged row still returns its path: a book the LLM had nothing to say
	// about has still been ATTEMPTED, and must stop being re-read.
	if !changed {
		return row.FilePath, nil
	}
	if _, uerr := store.UpdateBook(row.ID, row); uerr != nil {
		return "", uerr
	}
	return row.FilePath, nil
}

// aiParseLog is the logger for the queued path's own diagnostics -- the ones
// that must be visible even when no scan is running.
var aiParseLog = logger.New("ai_parse")

// isBlankPtr treats a nil pointer and a pointer to "" as equally empty. Both
// occur: the extractor writes a pointer to the empty string when a tag is
// present but blank.
func isBlankPtr(s *string) bool {
	return s == nil || strings.TrimSpace(*s) == ""
}

// primaryVersionOf returns the primary member of row's version group, or nil
// when row is already primary or is in no group.
//
// A group it cannot read is returned as an ERROR, not as a silent nil. Failing
// open is still the right call -- skipping the write loses the update too -- but
// it must not be silent: `row` in that case is the demoted organized_source row,
// so a failure here writes the AI fields somewhere the UI never shows them, and
// that is precisely the bug this function exists to prevent. A transient store
// read error would otherwise reinstate it with a successful UpdateBook and
// nothing anywhere saying so.
func primaryVersionOf(store scanBookLookup, row *database.Book) (*database.Book, error) {
	if row.VersionGroupID == nil || *row.VersionGroupID == "" {
		return nil, nil
	}
	if row.IsPrimaryVersion != nil && *row.IsPrimaryVersion {
		return nil, nil
	}
	members, err := store.GetBooksByVersionGroup(*row.VersionGroupID)
	if err != nil {
		return nil, fmt.Errorf("read version group %s: %w", *row.VersionGroupID, err)
	}
	for i := range members {
		if members[i].IsPrimaryVersion != nil && *members[i].IsPrimaryVersion {
			return &members[i], nil
		}
	}
	// A group with no primary member. CreateOrganizedVersion always sets the
	// flag explicitly, so this is a group formed by some other path -- and a nil
	// IsPrimaryVersion serializes as ABSENT, not false, so "no member is
	// primary" and "no member says" are the same shape here. Fall through to
	// the caller's own row rather than guessing which member wins.
	return nil, nil
}

// newAIParser builds the AI fallback parser from the configured LLM backend.
//
// Extracted from ProcessBooksParallel on 2026-08-24 so the inline scan path and
// the queued library.ai-parse operation construct the parser the same way. The
// hazard this guards against is the one described below: a second, divergent
// copy of the backend-routing logic.
//
// Until 2026-08-16 the inline version of this block ignored AIBackend entirely:
// it constructed the cloud OpenAI parser whenever EnableAIParsing was set,
// passing enabled=true unconditionally. The effect was that a deployment with
// ai_backend.llm_mode="disabled" and a local Ollama endpoint configured still
// sent every scan batch to api.openai.com -- which is exactly how the 2026-08-16
// rescan burned its batches against an exhausted-credit key. Routing through
// EffectiveLLMMode makes the operator's setting the thing that decides, and
// keeps this in step with the "llmparser" service in internal/ai/register.go.
// newAIParser builds the parser the scan and the queued operation both use.
//
// It returns the aiBatchParser INTERFACE rather than *ai.OpenAIParser because
// openai-fallback-local resolves to a parserChain, not to a single client. Both
// callers only ever hand the result to runAIBatchPhase, which already takes the
// interface, so nothing loses a capability by this.
func newAIParser(scanLog logger.Logger) (aiBatchParser, bool) {
	if !config.AppConfig.EnableAIParsing {
		return nil, false
	}
	cfg := &config.AppConfig
	switch cfg.EffectiveLLMMode() {
	case config.AIBackendModeDisabled:
		scanLog.Info("AI parsing skipped: llm_mode is disabled")
	case config.AIBackendModeLocal:
		baseURL := localLLMBaseURL(cfg)
		if baseURL == "" {
			scanLog.Warn("AI parsing enabled with llm_mode=local but no local base URL is configured")
			return nil, false
		}
		parser := ai.NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, cfg.AIBackend.LocalLLMModel, true)
		if parser != nil && parser.IsEnabled() {
			scanLog.Info("local LLM parser initialized for filename metadata fallback (base_url=%s model=%s)",
				baseURL, cfg.AIBackend.LocalLLMModel)
			return parser, true
		}
		scanLog.Warn("failed to initialize local LLM parser, AI fallback disabled")
	case config.AIBackendModeOpenAIFallbackLocal:
		// The mode existed as a constant for months with nothing acting on it:
		// it fell into the `default` arm below and behaved as plain openai, so
		// selecting it bought exactly nothing. This is the trigger its own doc
		// comment claimed was "wired by the error-classification layer".
		var rungs []parserRung

		if cfg.OpenAIAPIKey != "" {
			if p := ai.NewOpenAIParser(cfg, cfg.OpenAIAPIKey, true); p != nil && p.IsEnabled() {
				rungs = append(rungs, parserRung{name: "openai", parser: p})
			}
		}

		// The local rung is built LAZILY, inside ensure. Constructing it here
		// would pay for a backend that a healthy scan never asks for, and once
		// this rung learns to start a daemon (the next stage) that cost stops
		// being theoretical.
		//
		// It is skipped rather than attempted when no local backend is
		// configured. A rung with no model behind it cannot answer, and
		// pulling one mid-scan is a decision rather than a fallback -- the
		// books go to the queue instead, which is what the directive asks for
		// when a local backend cannot be started.
		if baseURL := localLLMBaseURL(cfg); baseURL != "" {
			model := cfg.AIBackend.LocalLLMModel
			rungs = append(rungs, parserRung{
				name: "local",
				ensure: func(context.Context) (aiBatchParser, bool) {
					if model == "" {
						scanLog.Warn("local LLM fallback skipped: a base URL is configured (%s) but no model is set", baseURL)
						return nil, false
					}
					p := ai.NewOpenAIParserWithBaseURL(cfg, "ollama", baseURL, model, true)
					if p == nil || !p.IsEnabled() {
						return nil, false
					}
					return p, true
				},
			})
		}

		switch len(rungs) {
		case 0:
			scanLog.Warn("AI parsing enabled with llm_mode=openai-fallback-local but neither an OpenAI key nor a local base URL is configured")
			return nil, false
		case 1:
			scanLog.Info("AI parsing using the %q backend only; llm_mode=openai-fallback-local has nothing to fall back to", rungs[0].name)
		default:
			scanLog.Info("AI parsing chain initialized: %s", rungNames(rungs))
		}
		return newParserChain(scanLog, rungs...), true

	default: // openai
		if cfg.OpenAIAPIKey == "" {
			scanLog.Warn("AI parsing enabled but OpenAI API key is not configured")
			return nil, false
		}
		parser := ai.NewOpenAIParser(cfg, cfg.OpenAIAPIKey, true)
		if parser != nil && parser.IsEnabled() {
			scanLog.Debug("OpenAI parser initialized for filename metadata fallback")
			return parser, true
		}
		scanLog.Warn("failed to initialize OpenAI parser, AI fallback disabled")
	}
	return nil, false
}

// saveBookAndReportPath adapts the inline scan's saveBook to the phase's saver
// signature. The inline path runs before AutoOrganizeFn, so the book's own
// FilePath is still the row's path and is the right thing to stamp.
func saveBookAndReportPath(ctx context.Context, book *Book) (string, error) {
	if err := saveBook(ctx, book); err != nil {
		return "", err
	}
	return book.FilePath, nil
}

// localLLMBaseURL resolves the local backend's endpoint. The embedding base URL
// is the fallback because a deployment that runs a local Ollama for embeddings
// already has one running, and requiring it to be configured twice is how the
// two drift apart.
func localLLMBaseURL(cfg *config.Config) string {
	if cfg.AIBackend.LocalBaseURL != "" {
		return cfg.AIBackend.LocalBaseURL
	}
	return cfg.Embedding.BaseURL
}

// rungNames renders a chain's rungs in the order they will be tried.
func rungNames(rungs []parserRung) string {
	names := make([]string, len(rungs))
	for i, r := range rungs {
		names[i] = r.name
	}
	return strings.Join(names, " -> ")
}
