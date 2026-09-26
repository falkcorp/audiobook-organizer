// file: internal/plugins/maintenance/cover_ops.go
// version: 1.0.0
// guid: e5a3c8f2-1d7b-4e96-8c40-6b2f9d1a7e35
// last-edited: 2026-09-26

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
	"github.com/falkcorp/audiobook-organizer/internal/covers"
	"github.com/falkcorp/audiobook-organizer/internal/covertext"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/foldercover"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/opmode"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
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

// coverOpBooks lists the books a cover op examines: the requested ids, or
// every book not marked for deletion, capped at Limit.
func coverOpBooks(store OpsStore, p coverOpParams) ([]database.Book, error) {
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

func (p *Plugin) runFolderCoverBackfill(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	const opID = "maintenance.folder-cover-backfill"
	params, dryRun, err := parseCoverOpParams(opID, raw)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return errors.New("database not initialized")
	}
	rootDir := p.deps.RootDir()
	if rootDir == "" {
		return errors.New("root_dir is not configured; covers are stored under it")
	}
	if dryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no cover is stored and no book is written")
	} else if err := refuseWhileLibraryScanActive(p.deps.OperationQueueStore(), opID); err != nil {
		return err
	}
	books, err := coverOpBooks(store, params)
	if err != nil {
		return err
	}
	var (
		mu       sync.Mutex
		counts   = map[foldercover.Outcome]int{}
		examples []string
		failures []string
	)
	// Partitioned by book: RunItems hands each book to exactly one worker and
	// each worker writes only that book's row (ModifyBook), so no two workers
	// touch the same row. Two books sharing one image store the same
	// content-addressed file, which StoreCoverImage makes safe.
	runErr := registry.RunItems(ctx, reporter, books, func(ctx context.Context, b database.Book) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var plan foldercover.Plan
		if dryRun {
			plan = foldercover.Evaluate(store, &b, rootDir)
		} else {
			plan = foldercover.Apply(store, &b, rootDir)
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
	}, registry.RunItemsOptions{
		// Directory reads, image header decodes and one tag read per book:
		// IO bound, so twice the core count by default.
		Concurrency: clampWorkers(params.Workers, runtime.NumCPU()*2),
		ErrMode:     registry.ErrModeCollect,
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
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf("%s — folder covers over %d books: %s", mode, len(books), strings.Join(parts, " ")))
	for _, e := range examples {
		_ = reporter.Log(slog.LevelInfo, "  example: "+e)
	}
	for _, f := range failures {
		_ = reporter.Log(slog.LevelWarn, "  failed: "+f)
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
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runCoverTextRead,
	}
}

// coverImage is one of a book's cover images in the form sent to the model.
type coverImage struct {
	ref covertext.ImageRef
	img metadata.CoverImage
}

// gatherCoverImages collects a book's distinct cover images. Only reads.
func gatherCoverImages(store OpsStore, book *database.Book, rootDir string) ([]coverImage, []error) {
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

func (p *Plugin) runCoverTextRead(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	const opID = "maintenance.cover-text-read"
	params, dryRun, err := parseCoverOpParams(opID, raw)
	if err != nil {
		return err
	}
	store := p.deps.OpsStore()
	if store == nil {
		return errors.New("database not initialized")
	}
	rootDir := p.deps.RootDir()
	reader := newCoverTextReader()
	workers := clampWorkers(params.Workers, runtime.NumCPU()*2)
	if dryRun {
		_ = reporter.Log(slog.LevelInfo, "DRY RUN — no image is sent to a model and nothing is stored")
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
	}
	books, err := coverOpBooks(store, params)
	if err != nil {
		return err
	}

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
	runErr := registry.RunItems(ctx, reporter, books, func(ctx context.Context, b database.Book) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		imgs, errs := gatherCoverImages(store, &b, rootDir)
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
			if err := covertext.PutBook(store, idx); err != nil {
				note(func() { indexWriteErr++ })
				return fmt.Errorf("store cover index for %s: %w", b.ID, err)
			}
		}
		for _, ci := range imgs {
			if !claim(ci.ref.Hash) {
				continue
			}
			prev, err := covertext.Get(store, ci.ref.Hash)
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
			registry.TouchLiveness(reporter)
			rec := &covertext.Record{Hash: ci.ref.Hash, PromptVersion: covertext.PromptVersion, Attempts: 1}
			if prev != nil {
				rec.Attempts = prev.Attempts + 1
			}
			res, rerr := reader.ReadCoverText(ctx, ci.img.Data, ci.img.MIMEType)
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
			if err := covertext.Put(store, rec); err != nil {
				return fmt.Errorf("store cover text %s: %w", ci.ref.Hash, err)
			}
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: workers,
		ErrMode:     registry.ErrModeCollect,
		// Above the per-attempt vision timeout, so a read that fails over
		// once still fits.
		PerItemTimeout: 3*ai.CoverTextAttemptTimeout + time.Minute,
	})

	mu.Lock()
	defer mu.Unlock()
	mode := "READ"
	if dryRun {
		mode = "DRY RUN"
	}
	_ = reporter.Log(slog.LevelInfo, fmt.Sprintf(
		"%s — cover text over %d books: books_with_covers=%d distinct_images=%d already_current=%d to_read=%d read_ok=%d read_failed=%d gather_errors=%d index_write_errors=%d workers=%d",
		mode, len(books), booksWithImages, images, current, toRead, readOK, readFailed, gatherErrs, indexWriteErr, workers))
	sort.Strings(failures)
	for _, f := range failures {
		_ = reporter.Log(slog.LevelWarn, "  failed: "+f)
	}
	return runErr
}
