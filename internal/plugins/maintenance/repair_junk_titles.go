// file: internal/plugins/maintenance/repair_junk_titles.go
// version: 1.6.0
// guid: 9c4e7a12-3b58-4d06-8f21-7ae5c0d94b63
// last-edited: 2026-09-26

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
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
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Recover book titles that were replaced by junk",
		Description: "Repairs books whose stored title is demonstrably not a title — \"read by narrator\" " +
			"(the importer kept the filename's trailing credit instead of the leading title) or a track tag " +
			"promoted to the book title (\"Intro\", \"Opening Credits\", \"Big Finish Ident\"). Recovers the real " +
			"title from the folder for multi-file books and from the filename convention for single-file books, " +
			"and refuses rather than guesses when there is no trustworthy evidence. Honours user overrides, " +
			"fetched values and provider-applied metadata. Never touches Doctor Who / Big Finish / Torchwood " +
			"books (owner-manual) or books under the iTunes tree, and refuses a recovered title that is the " +
			"name of the book's author or narrator. Dry-run by default; pass {\"apply\": true} to write.",
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
	store := p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	log := reporter.Logger()
	log.Info("repair-junk-titles: starting", "apply", params.Apply, "limit", params.Limit)

	// PASS 1 — page the cheap Core projection and keep ONLY the junk-titled books.
	// Filtering here rather than inside the worker matters: the per-book work
	// (files, field states, author) is several reads, and this turns a 44k-book
	// sweep into a ~2k-book one.
	// One limit-0 call = one consistent snapshot; offset pages over the async
	// memdb can skip or repeat rows on snapshot swap (reconcile #2443).
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return fmt.Errorf("GetAllBooksCore: %w", err)
	}
	// Owner-manual series (Doctor Who / Big Finish / Torchwood) are applied by
	// hand, by explicit book id, and never by a bulk op. The 2026-09-25 dry run
	// would have retitled a "Big Finish Ident" book to an actor's name. The
	// series list is read once rather than once per book; a failed read stops
	// the run, because without it the exclusion cannot be applied.
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return fmt.Errorf("GetAllSeries: %w", err)
	}
	manualSeries := map[int]bool{}
	for i := range allSeries {
		if junkTitleOwnerManual("", "", allSeries[i].Name) {
			manualSeries[allSeries[i].ID] = true
		}
	}

	var candidates []database.BookCore
	scanned := len(all)
	var skipOwnerManual, skipITunes, skipNamesPerson atomic.Int64
	for i := range all {
		if all[i].IsSoftDeleted() {
			continue
		}
		if !IsJunkTitle(all[i].Title) {
			continue
		}
		// The cheap exclusions run here, on the Core projection; the worker
		// repeats them against the book_file paths, because book.file_path can
		// be stale.
		if junkTitleOwnerManual(all[i].FilePath, all[i].Title, "") ||
			(all[i].SeriesID != nil && manualSeries[*all[i].SeriesID]) {
			skipOwnerManual.Add(1)
			continue
		}
		if pathutil.UnderFrozenITunesTree(all[i].FilePath) {
			skipITunes.Add(1)
			continue
		}
		candidates = append(candidates, all[i])
	}
	if params.Limit > 0 && len(candidates) > params.Limit {
		candidates = candidates[:params.Limit]
	}

	log.Info("repair-junk-titles: scan complete", "scanned", scanned, "junk_titled", len(candidates))
	if len(candidates) == 0 {
		summary := fmt.Sprintf("repair-junk-titles: %d books scanned, 0 repairable junk titles — nothing to do "+
			"(skipped %d owner-manual, %d iTunes tree)", scanned, skipOwnerManual.Load(), skipITunes.Load())
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
			if states[i].HasUserOverride() || states[i].HasProviderValue() {
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
		// Same exclusions against the file rows: book.file_path is stale on
		// this library and the files are where the book really lives.
		for _, fp := range paths {
			if junkTitleOwnerManual(fp, "", "") {
				skipOwnerManual.Add(1)
				return nil
			}
			if pathutil.UnderFrozenITunesTree(fp) {
				skipITunes.Add(1)
				return nil
			}
		}

		primaryAuthor := authorName(b.AuthorID)
		newTitle, method, ok := DeriveJunkTitleReplacement(b.Title, primaryAuthor, paths)
		if !ok {
			skipNoEvidence.Add(1)
			return nil
		}

		// A person's name is not a title. The 2026-09-25 dry run turned
		// "read by narrator" into "C. T. Phipps": the filename convention put
		// the author where the title was expected. Refuse any recovered title
		// that equals the book's author, its narrator, or any linked author.
		people := []string{primaryAuthor}
		if b.Narrator != nil {
			people = append(people, splitCreditNames(*b.Narrator)...)
		}
		links, lerr := store.GetBookAuthors(b.ID)
		if lerr != nil {
			// Fail closed: without the linked names the check is incomplete.
			failed.Add(1)
			log.Warn("repair-junk-titles: GetBookAuthors failed", "book_id", b.ID, "err", lerr)
			return nil
		}
		for _, l := range links {
			id := l.AuthorID
			people = append(people, authorName(&id))
		}
		if titleNamesAPerson(newTitle, people) {
			skipNamesPerson.Add(1)
			mu.Lock()
			if len(examples) < 12 {
				examples = append(examples, fmt.Sprintf("%q → %q refused: names a person", b.Title, newTitle))
			}
			mu.Unlock()
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

		// retitleBook writes only Title under the book's write lock, and only
		// while the stored title is still the junk one the replacement was
		// derived from (audit A1#15).
		if uerr := retitleBook(store, b.ID, b.Title, newTitle); uerr != nil {
			if errors.Is(uerr, errRetitleChangedUnderneath) {
				log.Warn("repair-junk-titles: title changed underneath, left as it is", "book_id", b.ID)
				return nil
			}
			failed.Add(1)
			log.Warn("repair-junk-titles: retitle failed", "book_id", b.ID, "err", uerr)
			return nil
		}
		repaired.Add(1)
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(i, total int) string { return fmt.Sprintf("book %d/%d", i+1, total) },
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
			"skipped %d (protected provenance), %d (no trustworthy evidence), %d (owner-manual series), "+
			"%d (iTunes tree), %d (recovered title names the author or narrator), failed %d | e.g. %s",
		scanned, len(candidates), verb, strings.Join(methods, " "),
		skipProvenance.Load(), skipNoEvidence.Load(), skipOwnerManual.Load(), skipITunes.Load(),
		skipNamesPerson.Load(), failed.Load(), ex)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(candidates), len(candidates), summary)
	return nil
}

// junkTitleOwnerManual reports whether any of path, title or series marks the
// book as owner-manual (Doctor Who / Big Finish / Torchwood). The title is
// checked because "Big Finish Ident" is itself one of the junk titles this op
// targets, and a book carrying it is Big Finish content.
func junkTitleOwnerManual(path, title, series string) bool {
	return applygate.IsOwnerManualOnly(path, series) || applygate.IsOwnerManualOnly(title, "")
}

// splitCreditNames splits a denormalized credit string ("A, B & C") into the
// names it lists, keeping the whole string too.
func splitCreditNames(s string) []string {
	out := []string{s}
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '&' || r == '/' })
	for _, p := range parts {
		for _, q := range strings.Split(p, " and ") {
			if q = strings.TrimSpace(q); q != "" {
				out = append(out, q)
			}
		}
	}
	return out
}

// titleNamesAPerson reports whether title equals, case-insensitively and
// ignoring surrounding whitespace, any of the given names.
func titleNamesAPerson(title string, names []string) bool {
	t := strings.TrimSpace(title)
	if t == "" {
		return false
	}
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" && strings.EqualFold(t, n) {
			return true
		}
	}
	return false
}
