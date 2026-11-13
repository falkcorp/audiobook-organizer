// file: internal/plugins/maintenance/cover_ops.go
// version: 1.2.0
// guid: e5a3c8f2-1d7b-4e96-8c40-6b2f9d1a7e35
// last-edited: 2026-11-13

// Package maintenance — ops maintenance.folder-cover-backfill and
// maintenance.cover-text-read.
//
// folder-cover-backfill gives every book with no cover and no embedded art the
// cover image from its own folder (internal/foldercover holds the rule). The
// scanner does the same for new books; this op covers the library imported
// before that. Preview by default.
//
// cover-text-read has a vision model read the text on every cover image a book
// has (its stored local cover, its first audio file's embedded picture, its
// folder image) and STORES it per image hash (internal/covertext). It never
// writes book metadata. Preview by default: dry_run=false is what spends model
// time. Resumable by construction: an image whose current-prompt read is
// stored is skipped, so a rerun after a stop continues where it left off, and
// failed reads are stored with their error and retried.
package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/ai"
	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/covers"
	"github.com/falkcorp/audiobook-organizer/internal/covertext"
	"github.com/falkcorp/audiobook-organizer/internal/foldercover"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
	"github.com/falkcorp/mediacore/tasks"
)

// coverOpParams is shared by both ops.
type coverOpParams struct {
	// DryRun defaults to TRUE when omitted; dry_run is the accepted alias.
	DryRun      *bool `json:"dryRun,omitempty"`
	DryRunSnake *bool `json:"dry_run,omitempty"`
	// BookIDs limits the run to these books.
	BookIDs      []string `json:"bookIds,omitempty"`
	BookIDsSnake []string `json:"book_ids,omitempty"`
	// Limit caps the number of books examined (0 = no cap).
	Limit int `json:"limit"`
	// Workers overrides the worker count (clamped 1-32). Default: NumCPU for
	// the folder-cover op; the pool's llm.cover_art_vision capacity for
	// cover-text-read.
	Workers int `json:"workers"`
	// Force (cover-text-read only) re-reads images that already have a
	// current read.
	Force bool `json:"force"`
}

func parseCoverOpParams(opID string, raw json.RawMessage) (coverOpParams, bool, error) {
	var p coverOpParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return p, true, fmt.Errorf("invalid params: %w", err)
		}
	}
	dry, err := opmode.ResolveDryRun(opID, p.DryRunSnake, p.DryRun)
	if err != nil {
		return p, true, err
	}
	p.BookIDs = append(p.BookIDs, p.BookIDsSnake...)
	return p, dry, nil
}

// t6g3CoverBookReader is the two reads coverOpBooks makes.
type t6g3CoverBookReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetAllBooksCore(limit int, offset int) ([]database.BookCore, error)
}

// coverFolderStore is the foldercover.Store a cover core hands foldercover: its
// reads come from the run's reader and its one write (ModifyBook) from the
// run's writer, so foldercover never holds a path around either (rule R10). A
// nil w is a preview's: foldercover.Evaluate never writes.
type coverFolderStore struct {
	r t6g3CoverReader
	w t6g3CoverWriter
}

func (s coverFolderStore) GetBookFiles(bookID string) ([]database.BookFile, error) {
	return s.r.GetBookFiles(bookID)
}

func (s coverFolderStore) GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error) {
	return s.r.GetMetadataFieldStates(bookID)
}

func (s coverFolderStore) GetUserPreference(key string) (*database.UserPreference, error) {
	return s.r.GetUserPreference(key)
}

func (s coverFolderStore) ModifyBook(id string, fn func(*database.Book) error) (*database.Book, error) {
	if s.w == nil {
		return nil, errors.New("cover: no writer in a preview run")
	}
	return s.w.ModifyBook(id, fn)
}

// coverKV is the covertext.KV a cover core hands covertext: reads from the
// run's store, writes through the run's writer.
type coverKV struct {
	r t6g3RawReader
	w t6g3CoverWriter
}

func (k coverKV) GetRaw(key string) ([]byte, error) { return k.r.GetRaw(key) }

func (k coverKV) SetRaw(key string, value []byte) error {
	if k.w == nil {
		return errors.New("cover: no writer in a preview run")
	}
	return k.w.SetRaw(key, value)
}

// t6g3RefuseWhileScan is refuseWhileLibraryScanActive over a store that may
// have no queue (errT6g3NoCapability: HEAD's nil queue, "cannot verify").
func t6g3RefuseWhileScan(r OpQueueReader, opID string) error {
	active, err := r.ListActiveOperationsV2()
	if errors.Is(err, errT6g3NoCapability) {
		return refuseWhileLibraryScanActive(nil, opID)
	}
	return refuseWhileLibraryScanActive(listedQueue{active, err}, opID)
}

// listedQueue replays one ListActiveOperationsV2 answer.
type listedQueue struct {
	rows []database.OperationV2Row
	err  error
}

func (q listedQueue) ListActiveOperationsV2() ([]database.OperationV2Row, error) {
	return q.rows, q.err
}

// coverTextSlots caps the concurrent reads of a native cover-text run at the
// pool's capacity (HEAD sized its worker count to it).
func coverTextSlots(n int) chan struct{} { return make(chan struct{}, max(n, 1)) }

// coverOpWorkers is HEAD's default worker count of both cover ops (IO bound:
// twice the cores, clamped 1-32), which is the native kinds' fixed parallelism.
func coverOpWorkers() int { return clampWorkers(0, runtime.NumCPU()*2) }

// coverOpBooks lists the books a cover op examines: the requested ids, or
// every book not marked for deletion, capped at Limit.
func coverOpBooks(store t6g3CoverBookReader, p coverOpParams) ([]database.Book, error) {
	var out []database.Book
	if len(p.BookIDs) > 0 {
		for _, id := range p.BookIDs {
			b, err := store.GetBookByID(id)
			if err != nil {
				return nil, fmt.Errorf("GetBookByID %s: %w", id, err)
			}
			if b != nil {
				out = append(out, *b)
			}
		}
	} else {
		cores, err := store.GetAllBooksCore(0, 0)
		if err != nil {
			return nil, fmt.Errorf("GetAllBooksCore: %w", err)
		}
		for i := range cores {
			if cores[i].MarkedForDeletion != nil && *cores[i].MarkedForDeletion {
				continue
			}
			out = append(out, cores[i].ToBook())
		}
	}
	if p.Limit > 0 && p.Limit < len(out) {
		out = out[:p.Limit]
	}
	return out, nil
}

func clampWorkers(n, def int) int {
	if n <= 0 {
		n = def
	}
	return min(max(n, 1), 32)
}

// ---------------------------------------------------------------------------
// maintenance.folder-cover-backfill
// ---------------------------------------------------------------------------

func (p *Plugin) folderCoverBackfillDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.folder-cover-backfill",
		Preview:     sdk.PreviewSpec{Params: coverOpParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Use folder cover images",
		Description: "For books with no cover and no embedded cover art, uses the best cover image in the book's own folder " +
			"(.jpg/.jpeg/.png/.bmp/.webp/.gif, any case; named cover/folder/front/album/artwork first, then the largest; " +
			"images under 200 px or named back/spine/disc/etc. are skipped; BMP is stored as JPEG). Never replaces a cover " +
			"or touches a locked cover, and in a folder shared by several books only an image named after the book's own file is used. " +
			"Default dry-run previews; set dry_run=false to apply (refused while a library scan runs).",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.folder-cover-backfill",
		Writes:          []sdk.Resource{sdk.ResBooks},
		Cancellable:     true,
		Timeout:         120 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runFolderCoverBackfill,
	}
}

// folderCoverOutcomeOrder fixes the summary's order.
var folderCoverOutcomeOrder = []foldercover.Outcome{
	foldercover.OutcomeApplied, foldercover.OutcomeWouldApply, foldercover.OutcomeHasCover,
	foldercover.OutcomeHasEmbedded, foldercover.OutcomeLocked, foldercover.OutcomeLocksUnavailable,
	foldercover.OutcomeNoFolder, foldercover.OutcomeNoCandidate, foldercover.OutcomeRaced, foldercover.OutcomeError,
}

// folderCoverBackfillKind is the native kind of maintenance.folder-cover-backfill (F5-08).
const folderCoverBackfillKind tasks.Kind = "books.maintenance.folder-cover-backfill"

// folderCoverBackfillEnv is what folderCoverBackfillCore needs from its engine.
type folderCoverBackfillEnv struct {
	t6g3Base
	// Items is the per-book loop, on the def's worker pool.
	Items t6g3ItemsFunc
}

// runFolderCoverBackfill is the v2 shell of maintenance.folder-cover-backfill.
func (p *Plugin) runFolderCoverBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, dryRun, err := parseCoverOpParams(folderCoverBackfillOpID, raw)
	if err != nil {
		return err
	}
	if p.deps.OpsStore() == nil {
		return errT6g3NoStore
	}
	return folderCoverBackfillCore(ctx, params, folderCoverBackfillEnv{
		t6g3Base: p.t6g3HeadBase(ctx, reporter, !dryRun),
		// Directory reads, image header decodes and one tag read per book: IO
		// bound, so twice the core count by default.
		Items: t6g3HeadItems(ctx, reporter, clampWorkers(params.Workers, runtime.NumCPU()*2)),
	})
}

// folderCoverBackfillOpID is HEAD's def ID, which the core's messages carry.
const folderCoverBackfillOpID = "maintenance.folder-cover-backfill"

// folderCoverBackfillKind builds the native kind. HEAD declares a Preview, so
// it runs in either mode (HEAD's dry_run default of true becomes the start
// request's mode). Writes found: ModifyBook on the book's cover fields only.
// HEAD's `workers` param (clamped 1-32) is not carried: a Task's pool is fixed
// at HEAD's default of twice the cores (see the F5-08 seams note).
func (p *Plugin) folderCoverBackfillKind(deps t6g3Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	eff := tasks.Writes(tasks.Res("books", "books"))
	return tasks.Task(folderCoverBackfillKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: t6g3Common(p.folderCoverBackfillDef(), folderCoverBackfillKind, eff, sched),
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			var params coverOpParams
			if err := t6g3DecodeParams(folderCoverBackfillKind, raw, &params, "dry_run", "dryRun"); err != nil {
				return struct{}{}, err
			}
			params.BookIDs = append(params.BookIDs, params.BookIDsSnake...)
			base, err := t6g3NativeBase(folderCoverBackfillKind, rc, deps, true)
			if err != nil {
				return struct{}{}, err
			}
			if err := t6g3NeedStore(folderCoverBackfillKind, base); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, folderCoverBackfillCore(rc.Context(), params, folderCoverBackfillEnv{
				t6g3Base: base,
				Items:    t6g3NativeItems(rc, folderCoverBackfillParallelism()),
			})
		},
	})
}

// folderCoverBackfillParallelism is rule R4: HEAD's default pool, an IO-bound
// twice the core count.
func folderCoverBackfillParallelism() tasks.Parallelism {
	return tasks.Fixed(coverOpWorkers(), "directory reads, image header decodes and one tag read per book: IO bound, twice the core count")
}

// folderCoverBackfillCore is the shared body of both shells.
func folderCoverBackfillCore(ctx context.Context, params coverOpParams, env folderCoverBackfillEnv) error {
	const opID = folderCoverBackfillOpID
	dryRun := !env.Live
	rootDir := env.Store.RootDir()
	if rootDir == "" {
		return errors.New("root_dir is not configured; covers are stored under it")
	}
	if dryRun {
		env.Log(slog.LevelInfo, "DRY RUN — no cover is stored and no book is written")
	} else if err := t6g3RefuseWhileScan(env.Store, opID); err != nil {
		return err
	}
	books, err := coverOpBooks(env.Store, params)
	if err != nil {
		return err
	}
	var fcStore foldercover.Store = coverFolderStore{r: env.Store, w: env.Writer}
	var (
		mu       sync.Mutex
		counts   = map[foldercover.Outcome]int{}
		examples []string
		failures []string
	)
	// Partitioned by book: the loop hands each book to exactly one worker and
	// each worker writes only that book's row (ModifyBook), so no two workers
	// touch the same row. Two books sharing one image store the same
	// content-addressed file, which StoreCoverImage makes safe.
	runErr := env.Items(len(books), t6g3ItemsOpts{Collect: true}, func(ctx context.Context, i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b := books[i]
		var plan foldercover.Plan
		if dryRun {
			plan = foldercover.Evaluate(fcStore, &b, rootDir)
		} else {
			plan = foldercover.Apply(fcStore, &b, rootDir)
		}
		mu.Lock()
		defer mu.Unlock()
		counts[plan.Outcome]++
		if (plan.Outcome == foldercover.OutcomeApplied || plan.Outcome == foldercover.OutcomeWouldApply) && len(examples) < 10 {
			examples = append(examples, fmt.Sprintf("%s ← %s (%dx%d %s)", b.ID, plan.Candidate.Path, plan.Candidate.Width, plan.Candidate.Height, plan.Candidate.Format))
		}
		if plan.Err != nil && len(failures) < 10 {
			failures = append(failures, fmt.Sprintf("%s: %s: %v", b.ID, plan.Outcome, plan.Err))
		}
		return nil
	})

	mu.Lock()
	defer mu.Unlock()
	parts := make([]string, 0, len(folderCoverOutcomeOrder))
	for _, o := range folderCoverOutcomeOrder {
		parts = append(parts, fmt.Sprintf("%s=%d", o, counts[o]))
	}
	mode := "APPLIED"
	if dryRun {
		mode = "DRY RUN"
	}
	env.Log(slog.LevelInfo, fmt.Sprintf("%s — folder covers over %d books: %s", mode, len(books), strings.Join(parts, " ")))
	for _, e := range examples {
		env.Log(slog.LevelInfo, "  example: "+e)
	}
	for _, f := range failures {
		env.Log(slog.LevelWarn, "  failed: "+f)
	}
	return runErr
}

// ---------------------------------------------------------------------------
// maintenance.cover-text-read
// ---------------------------------------------------------------------------

// coverTextReader is the vision reader the op uses. ai.RoutedCoverTextReader
// in production.
type coverTextReader interface {
	Capacity() int
	ReadCoverText(ctx context.Context, image []byte, mimeType string) (*ai.CoverTextResult, error)
}

// newCoverTextReader is a seam for tests.
var newCoverTextReader = func() coverTextReader { return ai.NewRoutedCoverTextReader(ai.ConfigPool()) }

// coverTextNow is a seam for tests.
var coverTextNow = time.Now

func (p *Plugin) coverTextReadDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.cover-text-read",
		Preview:     sdk.PreviewSpec{Params: coverOpParams{}},
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Read cover text (vision)",
		Description: "A vision model on the AI pool reads the text on every cover image each book has (its local cover, " +
			"its embedded cover, its folder cover): title, subtitle, authors, narrators, series and number, publisher and " +
			"any other printed text. The text is STORED per image and shown on the book page; no book metadata is changed. " +
			"Needs ai_endpoints_routing on and a pool row ticked for llm.cover_art_vision with the vision feature. " +
			"Resumable: images already read with the current prompt are skipped (force=true re-reads). " +
			"Default dry-run counts the images; set dry_run=false to read them.",
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.cover-text-read",
		Cancellable:     true,
		Timeout:         24 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runCoverTextRead,
	}
}

// coverImage is one of a book's cover images in the form sent to the model.
type coverImage struct {
	ref covertext.ImageRef
	img metadata.CoverImage
}

// gatherCoverImages collects a book's distinct cover images. Only reads.
func gatherCoverImages(store foldercover.Store, book *database.Book, rootDir string) ([]coverImage, []error) {
	var (
		out  []coverImage
		errs []error
		seen = map[string]bool{}
	)
	add := func(src covertext.Source, path string, data []byte) {
		ci, err := metadata.NormalizeCoverBytes(data)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s cover %s: %w", src, path, err))
			return
		}
		h := ci.SHA256()
		if seen[h] {
			return
		}
		seen[h] = true
		out = append(out, coverImage{ref: covertext.ImageRef{Hash: h, Source: src, Path: path}, img: ci})
	}

	// Local: the id-named cover, else the file a local cover_url names.
	local := metadata.CoverPathForBook(rootDir, book.ID)
	if local == "" && book.CoverURL != nil {
		if name, ok := strings.CutPrefix(strings.TrimSpace(*book.CoverURL), metadata.LocalCoverURLPrefix); ok &&
			name != "" && !strings.ContainsAny(name, "/\\?#") && !strings.Contains(name, "..") {
			if p, err := covers.FindCoverFile(name, rootDir); err == nil {
				local = p
			}
		}
	}
	if local != "" {
		if data, err := metadata.ReadCoverFile(local); err != nil {
			errs = append(errs, fmt.Errorf("local cover %s: %w", local, err))
		} else {
			add(covertext.SourceLocal, local, data)
		}
	}

	// Embedded: the first present audio file's picture.
	if first, err := foldercover.FirstAudio(store, book); err != nil {
		errs = append(errs, err)
	} else if first != "" {
		if data, _, err := metadata.ExtractCoverArtBytes(first); err == nil && len(data) > 0 {
			add(covertext.SourceEmbedded, first, data)
		}
	}

	// Folder: the image the folder-cover rule selects.
	if fc, err := foldercover.BestFolderImage(store, book); err != nil {
		errs = append(errs, err)
	} else if fc != nil {
		if ci, err := metadata.LoadFolderCover(*fc); err != nil {
			errs = append(errs, fmt.Errorf("folder cover %s: %w", fc.Path, err))
		} else if h := ci.SHA256(); !seen[h] {
			seen[h] = true
			out = append(out, coverImage{ref: covertext.ImageRef{Hash: h, Source: covertext.SourceFolder, Path: fc.Path}, img: ci})
		}
	}
	return out, errs
}

// coverTextReadKind is the native kind of maintenance.cover-text-read (F5-08).
const coverTextReadKind tasks.Kind = "books.maintenance.cover-text-read"

// coverTextReadOpID is HEAD's def ID, which the core's messages carry.
const coverTextReadOpID = "maintenance.cover-text-read"

// coverTextReadEnv is what coverTextReadCore needs from its engine.
type coverTextReadEnv struct {
	t6g3Base
	// Reader is the vision reader (newCoverTextReader's, a package seam).
	Reader coverTextReader
	// ItemsFor returns the per-book loop on a pool of the given size. The HEAD
	// shell sizes it (the pool's capacity when live); the native shell's pool is
	// the def's fixed Parallelism and ignores the size.
	ItemsFor func(workers int) t6g3ItemsFunc
	// CapReads caps the concurrent reads at the reader's capacity, for an engine
	// whose pool is not sized to it.
	CapReads bool
	// Touch stamps the run's liveness clock before a model read (HEAD:
	// registry.TouchLiveness). Nil is none: the native engine has no such clock.
	Touch func()
}

// runCoverTextRead is the v2 shell of maintenance.cover-text-read.
func (p *Plugin) runCoverTextRead(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	params, dryRun, err := parseCoverOpParams(coverTextReadOpID, raw)
	if err != nil {
		return err
	}
	if p.deps.OpsStore() == nil {
		return errT6g3NoStore
	}
	return coverTextReadCore(ctx, params, coverTextReadEnv{
		t6g3Base: p.t6g3HeadBase(ctx, reporter, !dryRun),
		Reader:   newCoverTextReader(),
		ItemsFor: func(workers int) t6g3ItemsFunc { return t6g3HeadItems(ctx, reporter, workers) },
		Touch:    func() { registry.TouchLiveness(reporter) },
	})
}

// coverTextReadKind builds the native kind. HEAD declares a Preview, so it runs
// in either mode. Writes found: the per-image text records and the per-book
// image index, both raw-key records of the cover-text store (resource
// cover_text); the op never writes a book. HEAD's `workers` param and its
// capacity-sized live pool are not carried: the pool is fixed, and a live run
// caps its concurrent reads at the reader's capacity instead.
func (p *Plugin) coverTextReadKind(deps t6g3Deps, sched func(tasks.Kind) *tasks.Schedule) tasks.Definition {
	eff := tasks.Writes(tasks.Res("books", "cover_text"))
	return tasks.Task(coverTextReadKind, tasks.TaskSpec[json.RawMessage, struct{}]{
		Common: t6g3Common(p.coverTextReadDef(), coverTextReadKind, eff, sched),
		Run: func(rc *tasks.RC, raw json.RawMessage) (struct{}, error) {
			var params coverOpParams
			if err := t6g3DecodeParams(coverTextReadKind, raw, &params, "dry_run", "dryRun"); err != nil {
				return struct{}{}, err
			}
			params.BookIDs = append(params.BookIDs, params.BookIDsSnake...)
			base, err := t6g3NativeBase(coverTextReadKind, rc, deps, true)
			if err != nil {
				return struct{}{}, err
			}
			if err := t6g3NeedStore(coverTextReadKind, base); err != nil {
				return struct{}{}, err
			}
			return struct{}{}, coverTextReadCore(rc.Context(), params, coverTextReadEnv{
				t6g3Base: base,
				Reader:   newCoverTextReader(),
				ItemsFor: func(int) t6g3ItemsFunc { return t6g3NativeItems(rc, coverTextReadParallelism()) },
				CapReads: true,
			})
		},
	})
}

// coverTextReadParallelism is rule R4: HEAD's default pool for a preview and
// the shape of its live pool (a vision model's capacity, bounded by the same
// 1-32 clamp).
func coverTextReadParallelism() tasks.Parallelism {
	return tasks.Fixed(coverOpWorkers(), "vision reads queue on the AI pool: HEAD sized the pool to its capacity, capped by twice the core count by default")
}

// coverTextReadCore is the shared body of both shells.
func coverTextReadCore(ctx context.Context, params coverOpParams, env coverTextReadEnv) error {
	const opID = coverTextReadOpID
	dryRun := !env.Live
	rootDir := env.Store.RootDir()
	reader := env.Reader
	workers := clampWorkers(params.Workers, runtime.NumCPU()*2)
	var slots chan struct{}
	if dryRun {
		env.Log(slog.LevelInfo, "DRY RUN — no image is sent to a model and nothing is stored")
	} else {
		capacity := reader.Capacity()
		if capacity == 0 {
			return fmt.Errorf("%s: no AI endpoint can read cover text: turn ai_endpoints_routing on and tick llm.cover_art_vision "+
				"(with the vision feature and a capability_models entry) on the pool rows", opID)
		}
		// Size to what the pool can actually run. More workers than slots
		// only queue; a vision model sharing a node with the text model swaps
		// on every request, so over-subscribing is worse than useless.
		workers = clampWorkers(params.Workers, capacity)
		if env.CapReads {
			slots = coverTextSlots(workers)
		}
	}
	books, err := coverOpBooks(env.Store, params)
	if err != nil {
		return err
	}
	var fcStore foldercover.Store = coverFolderStore{r: env.Store, w: nil}
	var kv covertext.KV = coverKV{r: env.Store, w: env.Writer}

	var (
		mu                                            sync.Mutex
		claimed                                       = map[string]bool{}
		booksWithImages, images, current, toRead      int
		readOK, readFailed, gatherErrs, indexWriteErr int
		failures                                      []string
	)
	claim := func(h string) bool {
		mu.Lock()
		defer mu.Unlock()
		if claimed[h] {
			return false
		}
		claimed[h] = true
		return true
	}
	note := func(f func()) {
		mu.Lock()
		defer mu.Unlock()
		f()
	}

	// Partitioned by book, and each distinct image by hash: claim() hands a
	// hash to exactly one worker, so a cover shared by many books is read once
	// and no two workers write the same record. Each record is written as its
	// read lands, so a stop loses at most the reads in flight.
	runErr := env.ItemsFor(workers)(len(books), t6g3ItemsOpts{
		Collect: true,
		// Above the per-attempt vision timeout, so a read that fails over
		// once still fits.
		ItemTimeout: 3*ai.CoverTextAttemptTimeout + time.Minute,
	}, func(ctx context.Context, i int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		b := books[i]
		imgs, errs := gatherCoverImages(fcStore, &b, rootDir)
		note(func() {
			gatherErrs += len(errs)
			if len(imgs) > 0 {
				booksWithImages++
			}
		})
		if len(imgs) == 0 {
			return nil
		}
		if !dryRun {
			idx := &covertext.BookIndex{BookID: b.ID, UpdatedAt: coverTextNow().UTC()}
			for _, ci := range imgs {
				idx.Images = append(idx.Images, ci.ref)
			}
			if err := covertext.PutBook(kv, idx); err != nil {
				note(func() { indexWriteErr++ })
				return fmt.Errorf("store cover index for %s: %w", b.ID, err)
			}
		}
		for _, ci := range imgs {
			if !claim(ci.ref.Hash) {
				continue
			}
			prev, err := covertext.Get(kv, ci.ref.Hash)
			if err != nil {
				return fmt.Errorf("read cover text %s: %w", ci.ref.Hash, err)
			}
			note(func() { images++ })
			if prev.Current() && !params.Force {
				note(func() { current++ })
				continue
			}
			note(func() { toRead++ })
			if dryRun {
				continue
			}
			if env.Touch != nil {
				env.Touch()
			}
			rec := &covertext.Record{Hash: ci.ref.Hash, PromptVersion: covertext.PromptVersion, Attempts: 1}
			if prev != nil {
				rec.Attempts = prev.Attempts + 1
			}
			res, rerr := readCoverTextSlot(ctx, reader, slots, ci)
			if rerr != nil && ctx.Err() != nil {
				return ctx.Err() // cancelled mid-read: not a failed read
			}
			rec.ReadAt = coverTextNow().UTC()
			if rerr != nil {
				rec.Status, rec.Error = covertext.StatusError, rerr.Error()
				note(func() {
					readFailed++
					if len(failures) < 10 {
						failures = append(failures, fmt.Sprintf("%s (%s %s): %v", b.ID, ci.ref.Source, ci.ref.Hash[:12], rerr))
					}
				})
			} else {
				rec.Status, rec.Text, rec.Raw, rec.Model, rec.EndpointID = covertext.StatusOK, res.Text, res.Raw, res.Model, res.EndpointID
				note(func() { readOK++ })
			}
			if err := covertext.Put(kv, rec); err != nil {
				return fmt.Errorf("store cover text %s: %w", ci.ref.Hash, err)
			}
		}
		return nil
	})

	mu.Lock()
	defer mu.Unlock()
	mode := "READ"
	if dryRun {
		mode = "DRY RUN"
	}
	env.Log(slog.LevelInfo, fmt.Sprintf(
		"%s — cover text over %d books: books_with_covers=%d distinct_images=%d already_current=%d to_read=%d read_ok=%d read_failed=%d gather_errors=%d index_write_errors=%d workers=%d",
		mode, len(books), booksWithImages, images, current, toRead, readOK, readFailed, gatherErrs, indexWriteErr, workers))
	sort.Strings(failures)
	for _, f := range failures {
		env.Log(slog.LevelWarn, "  failed: "+f)
	}
	return runErr
}

// readCoverTextSlot reads one image, holding one of slots when the engine caps
// concurrent reads (a nil slots is no cap).
func readCoverTextSlot(ctx context.Context, reader coverTextReader, slots chan struct{}, ci coverImage) (*ai.CoverTextResult, error) {
	if slots != nil {
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return reader.ReadCoverText(ctx, ci.img.Data, ci.img.MIMEType)
}
