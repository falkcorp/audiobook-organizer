// file: internal/plugins/maintenance/repair_junk_titles.go
// version: 1.0.0
// guid: 9c4e7a12-3b58-4d06-8f21-7ae5c0d94b63
// last-edited: 2026-08-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// RepairJunkTitlesParams are the JSON parameters for the junk-title repair.
type RepairJunkTitlesParams struct {
	// Apply, when true, writes the recovered titles. Default false — dry run.
	Apply bool `json:"apply"`
	// Limit caps how many BOOKS are repaired (0 = all). Useful for a canary.
	Limit int `json:"limit,omitempty"`
}

func (p *Plugin) repairJunkTitlesDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.repair-junk-titles",
		Plugin:      "maintenance",
		DisplayName: "Recover book titles that were replaced by junk",
		Description: "Repairs books whose stored title is demonstrably not a title — \"read by narrator\" " +
			"(the importer kept the filename's trailing credit instead of the leading title) or a track tag " +
			"promoted to the book title (\"Intro\", \"Opening Credits\", \"Big Finish Ident\"). Recovers the real " +
			"title from the folder for multi-file books and from the filename convention for single-file books, " +
			"and refuses rather than guesses when there is no trustworthy evidence. Honours user overrides, " +
			"fetched values and provider-applied metadata. Dry-run by default; pass {\"apply\": true} to write.",
		ResumePolicy:    sdk.ResumeRestart,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.repair-junk-titles",
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		ProgressTimeout: 30 * time.Minute,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead, sdk.CapLibraryWrite},
		Run:             p.runRepairJunkTitles,
	}
}

func (p *Plugin) runRepairJunkTitles(ctx context.Context, raw json.RawMessage, reporter sdk.Reporter) error {
	var params RepairJunkTitlesParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &params); err != nil {
			return fmt.Errorf("invalid params: %w", err)
		}
	}
	store := p.deps.Store()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	log.Info("repair-junk-titles: starting", "apply", params.Apply, "limit", params.Limit)

	// PASS 1 — page the cheap Core projection and keep ONLY the junk-titled books.
	// Filtering here rather than inside the worker matters: the per-book work
	// (files, field states, author) is several reads, and this turns a 44k-book
	// sweep into a ~2k-book one.
	const pageSize = 1000
	var candidates []database.BookCore
	scanned := 0
	for offset := 0; ; offset += pageSize {
		page, err := store.GetAllBooksCore(pageSize, offset)
		if err != nil {
			return fmt.Errorf("GetAllBooksCore offset=%d: %w", offset, err)
		}
		scanned += len(page)
		for i := range page {
			if page[i].MarkedForDeletion != nil && *page[i].MarkedForDeletion {
				continue
			}
			if IsJunkTitle(page[i].Title) {
				candidates = append(candidates, page[i])
			}
		}
		if len(page) < pageSize {
			break
		}
	}
	if params.Limit > 0 && len(candidates) > params.Limit {
		candidates = candidates[:params.Limit]
	}

	log.Info("repair-junk-titles: scan complete", "scanned", scanned, "junk_titled", len(candidates))
	if len(candidates) == 0 {
		summary := fmt.Sprintf("repair-junk-titles: %d books scanned, 0 junk titles found — nothing to do", scanned)
		_ = reporter.Log(slog.LevelInfo, summary)
		_ = reporter.UpdateProgress(1, 1, summary)
		return nil
	}

	var repaired, wouldRepair, skipProvenance, skipNoEvidence, failed atomic.Int64
	byMethod := map[string]int{}
	var mu sync.Mutex
	var examples []string

	// authorNames caches id→name; a whole series shares one author, so the same
	// handful of ids repeat across hundreds of books.
	authorNames := map[int]string{}
	var authorMu sync.Mutex
	authorName := func(id *int) string {
		if id == nil {
			return ""
		}
		authorMu.Lock()
		defer authorMu.Unlock()
		if n, ok := authorNames[*id]; ok {
			return n
		}
		n := ""
		if a, err := store.GetAuthorByID(*id); err == nil && a != nil {
			n = a.Name
		}
		authorNames[*id] = n
		return n
	}

	runErr := registry.RunItems(ctx, reporter, candidates, func(ctx context.Context, b database.BookCore) error {
		// Provenance guard — same rules as maintenance.title-repair. A title a
		// human set, or one a metadata provider supplied, is not ours to rewrite
		// however wrong it looks.
		states, serr := store.GetMetadataFieldStates(b.ID)
		if serr != nil {
			failed.Add(1)
			log.Warn("repair-junk-titles: GetMetadataFieldStates failed", "book_id", b.ID, "err", serr)
			return nil
		}
		for i := range states {
			if states[i].Field != "title" {
				continue
			}
			if states[i].OverrideLocked || states[i].OverrideValue != nil || states[i].FetchedValue != nil {
				skipProvenance.Add(1)
				return nil
			}
		}

		files, ferr := store.GetBookFiles(b.ID)
		if ferr != nil {
			failed.Add(1)
			log.Warn("repair-junk-titles: GetBookFiles failed", "book_id", b.ID, "err", ferr)
			return nil
		}
		paths := make([]string, 0, len(files))
		for i := range files {
			if strings.TrimSpace(files[i].FilePath) != "" {
				paths = append(paths, files[i].FilePath)
			}
		}

		newTitle, method, ok := DeriveJunkTitleReplacement(b.Title, authorName(b.AuthorID), paths)
		if !ok {
			skipNoEvidence.Add(1)
			return nil
		}

		mu.Lock()
		byMethod[method]++
		if len(examples) < 12 {
			examples = append(examples, fmt.Sprintf("%q → %q [%s]", b.Title, newTitle, method))
		}
		mu.Unlock()

		if !params.Apply {
			wouldRepair.Add(1)
			return nil
		}

		full, gerr := store.GetBookByID(b.ID)
		if gerr != nil || full == nil {
			failed.Add(1)
			log.Warn("repair-junk-titles: GetBookByID failed", "book_id", b.ID, "err", gerr)
			return nil
		}
		full.Title = newTitle
		if _, uerr := store.UpdateBook(b.ID, full); uerr != nil {
			failed.Add(1)
			log.Warn("repair-junk-titles: UpdateBook failed", "book_id", b.ID, "err", uerr)
			return nil
		}
		repaired.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label: func(i, total int) string { return fmt.Sprintf("book %d/%d", i+1, total) },
	})
	if runErr != nil && ctx.Err() != nil {
		log.Warn("repair-junk-titles: cancelled", "repaired", repaired.Load())
		return ctx.Err()
	}
	if runErr != nil {
		log.Warn("repair-junk-titles: some books failed", "err", runErr)
	}

	verb := fmt.Sprintf("would repair %d", wouldRepair.Load())
	if params.Apply {
		verb = fmt.Sprintf("repaired %d", repaired.Load())
	}
	mu.Lock()
	methods := make([]string, 0, len(byMethod))
	for m, n := range byMethod {
		methods = append(methods, fmt.Sprintf("%s=%d", m, n))
	}
	ex := strings.Join(examples, "; ")
	mu.Unlock()

	summary := fmt.Sprintf(
		"repair-junk-titles: %d books scanned, %d junk-titled, %s (by evidence: %s), "+
			"skipped %d (protected provenance), %d (no trustworthy evidence), failed %d | e.g. %s",
		scanned, len(candidates), verb, strings.Join(methods, " "),
		skipProvenance.Load(), skipNoEvidence.Load(), failed.Load(), ex)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(candidates), len(candidates), summary)
	return nil
}
