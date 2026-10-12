// file: internal/server/catalog_coverage_op.go
// version: 1.1.0
// guid: 5b8e2c61-0d7f-4a93-b1c4-e9a3f6d82b07
// last-edited: 2026-10-12
//
// Registers catalog.coverage-report, the read-only measurement that precedes
// the local-catalog matching stage (02-filter-identification-pipeline.md
// section 4, PR 7a; owner decision D31): of the primary books nobody has
// identified, how many have a credited author with a harvested catalog block?
// It lives beside catalog.harvest-authors in internal/server because the
// catalog ops are registered here; a separate plugin package would also need
// a blank import in internal/plugins/plugins.go or the op would silently not
// exist in the binary.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"slices"
	"sync"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

const catalogCoverageOpID = "catalog.coverage-report"

// catalogCoverageTopN is how many unharvested credits the report names.
const catalogCoverageTopN = 50

// catalogCoverageUnreadableLogCap bounds how many unreadable books are logged
// by id; the full count is always in the result.
const catalogCoverageUnreadableLogCap = 20

var catalogCoverageLog = logger.New("catalog-coverage-op")

func init() {
	addOpRegistrar(func(s *Server, reg *opsregistry.Registry) error {
		return s.RegisterCatalogCoverageOp(reg)
	})
}

// RegisterCatalogCoverageOp registers catalog.coverage-report. It writes
// nothing (no Writes resource), so it is not behind catalog.enabled: an empty
// catalog is reported as such rather than refused.
func (s *Server) RegisterCatalogCoverageOp(reg *opsregistry.Registry) error {
	return reg.RegisterOp(opsregistry.OperationDef{
		ID:          catalogCoverageOpID,
		Liveness:    opsregistry.LivenessRunItems,
		Plugin:      "catalog",
		DisplayName: "Author Catalog Coverage Report",
		Description: "Read-only. Of the primary books that are unidentified (ident:unasked or ident:asked_empty), counts how many have at least one credited author with a harvested catalog block, and names the 50 unharvested credits that would cover the most books. Writes nothing; no network. The covered count is an upper bound: cat_author:name: keys also exist for co-authors of harvested titles, so a credit can have a block without ever having been harvested itself. Books whose rows cannot be read are counted as considered with unknown scope (books_unreadable); a catalog read error fails the run.",
		Cancellable: true,
		// A reader of the whole library: low priority so it yields to the ops
		// that change state.
		DefaultPriority: opsregistry.PriorityLow,
		Timeout:         6 * time.Hour,
		// Nothing to resume from: the report is cheap and idempotent, so a
		// restart simply runs it again.
		ResumePolicy:   opsregistry.ResumeRestart,
		ConcurrencyKey: catalogCoverageOpID,
		// Do not start while the harvest is writing author blocks: the count
		// would move under the report. DependsOn is one-directional (this op
		// waits for the harvest); the registry's write-set gate does not
		// consult Reads, so the harvest itself is not held back by a running
		// report.
		DependsOn:    []string{catalogHarvestOpID},
		Reads:        []opsregistry.Resource{opsregistry.ResBooks, opsregistry.ResAuthors, opsregistry.ResCatalog},
		Permissions:  []auth.Permission{auth.PermSettingsManage},
		Capabilities: []opsregistry.Capability{opsregistry.CapLibraryRead},
		Run:          s.runCatalogCoverageOp,
	})
}

func (s *Server) runCatalogCoverageOp(ctx context.Context, _ json.RawMessage, reporter opsregistry.Reporter) error {
	store := s.storeForWiring()
	if store == nil {
		return fmt.Errorf("%s: store unavailable", catalogCoverageOpID)
	}
	cat := database.NewCatalogStoreFromStore(store)
	if cat == nil {
		return fmt.Errorf("%s: catalog store unavailable (store is not Pebble-backed)", catalogCoverageOpID)
	}
	idents, ok := database.AsCapability[database.IdentIndexReader](store)
	if !ok {
		return fmt.Errorf("%s: the identification index is not available on this store", catalogCoverageOpID)
	}
	return runCatalogCoverage(ctx, reporter, store, idents, cat)
}

// catalogCoverageBooks is what the report reads of the library: one book row
// and its credit list. A narrow interface, so a test can make one book's
// author read fail.
type catalogCoverageBooks interface {
	database.BookAuthorReader
	GetBookByID(id string) (*database.Book, error)
}

// catalogCoverageCatalog is what the report reads of the catalog.
type catalogCoverageCatalog interface {
	HasAuthorKey(fold string) (bool, error)
	CountAuthorNameBlocks() (int, error)
}

// catalogCoverageOptsObserver, when set (tests only), is told the options
// RunItems is called with, so a test can prove the pool is parallel without
// timing anything.
var catalogCoverageOptsObserver func(opsregistry.RunItemsOptions)

// catalogCoverageWorkers sizes the pool. Each item is a book read, a few
// author reads and a keys-only seek: CPU and Pebble bound, so one worker per
// core. The floor of 2 keeps the concurrent path (and its locking) exercised
// on a single-core host instead of silently degrading to a sequential loop.
func catalogCoverageWorkers() int { return max(runtime.NumCPU(), 2) }

// catalogCoverageRunOptions builds the RunItems options. A named function so
// a test runs the real options rather than a hand-written copy: dropping
// Concurrency is exactly the regression that made other whole-library ops
// sequential. Label reads only its arguments; it runs inside the workers.
func catalogCoverageRunOptions(total int) opsregistry.RunItemsOptions {
	return opsregistry.RunItemsOptions{
		Concurrency:   catalogCoverageWorkers(),
		ProgressTotal: total,
		Label: func(i, t int) string {
			return fmt.Sprintf("Books %d/%d", i+1, t)
		},
	}
}

// catalogCoverageCredit is one unharvested credit and the uncovered books it
// is credited on.
type catalogCoverageCredit struct {
	Credit string `json:"credit"`
	Books  int    `json:"books"`
}

// catalogCoverageResult is the single JSON line the op logs.
type catalogCoverageResult struct {
	BooksConsidered int `json:"books_considered"`
	BooksCovered    int `json:"books_covered"`
	BooksUncovered  int `json:"books_uncovered"`
	BooksUnreadable int `json:"books_unreadable"`
	// UnreadableNote is set when books_unreadable is non-zero: those books
	// are counted in books_considered but sit in neither covered nor
	// uncovered, because their scope (and so whether they belong in the
	// report at all) is unknown.
	UnreadableNote        string                  `json:"unreadable_note,omitempty"`
	CatalogAuthorBlocks   int                     `json:"catalog_author_blocks"`
	TopUnharvestedCredits []catalogCoverageCredit `json:"top_unharvested_credits"`
}

// coverageTally holds the run's aggregates behind one mutex.
type coverageTally struct {
	mu         sync.Mutex
	considered int
	covered    int
	uncovered  int
	unreadable int
	// unreadableLogged is how many unreadable books have had their id and
	// error logged; capped at catalogCoverageUnreadableLogCap.
	unreadableLogged int
	// unharvested maps FoldKey(credit) to the credit's display name (the
	// lexicographically smallest spelling seen, so the output does not depend
	// on which worker got there first) and the uncovered books it appears on.
	unharvested map[string]*catalogCoverageCredit
}

func (t *coverageTally) addCovered() {
	t.mu.Lock()
	t.considered++
	t.covered++
	t.mu.Unlock()
}

// addUnreadable counts a book whose row or credits could not be read. It
// counts as considered with unknown scope (the row that would say whether the
// book is in the Library's default view is the one that failed), and its id
// and error are logged for the first catalogCoverageUnreadableLogCap books so
// a failing store is diagnosable without flooding the log. IDs only: a book
// id is not library content, a title or author name is.
func (t *coverageTally) addUnreadable(bookID string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.considered++
	t.unreadable++
	if t.unreadableLogged < catalogCoverageUnreadableLogCap {
		t.unreadableLogged++
		catalogCoverageLog.Warn("catalog coverage: book %s unreadable, counted as considered with unknown scope: %v", bookID, err)
	}
}

// addUncovered records an uncovered book and each of its credits (deduplicated
// by fold, so a credit listed twice counts the book once).
func (t *coverageTally) addUncovered(credits map[string]string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.considered++
	t.uncovered++
	for fold, name := range credits {
		c := t.unharvested[fold]
		if c == nil {
			t.unharvested[fold] = &catalogCoverageCredit{Credit: name, Books: 1}
			continue
		}
		c.Books++
		if name < c.Credit {
			c.Credit = name
		}
	}
}

// top returns the n credits on the most uncovered books, ties by name.
func (t *coverageTally) top(n int) []catalogCoverageCredit {
	t.mu.Lock()
	defer t.mu.Unlock()
	all := make([]catalogCoverageCredit, 0, len(t.unharvested))
	for _, c := range t.unharvested {
		all = append(all, *c)
	}
	slices.SortFunc(all, func(a, b catalogCoverageCredit) int {
		if a.Books != b.Books {
			return b.Books - a.Books
		}
		switch {
		case a.Credit < b.Credit:
			return -1
		case a.Credit > b.Credit:
			return 1
		}
		return 0
	})
	if len(all) > n {
		all = all[:n]
	}
	return all
}

// runCatalogCoverage is the op body with its dependencies injected. It writes
// nothing and makes no network call.
//
// Selection is the ident index's unasked and asked_empty members, kept only
// if the book is in the Library's default view: a primary version (a nil flag
// counts as primary), not in the trash, not quarantined. A book is covered if
// ANY of its live credits has a catalog block. Each book is independent and
// the aggregates are merged under one mutex, so the loop is a bounded worker
// pool; no book is written, so there is no exclusivity to protect.
func runCatalogCoverage(ctx context.Context, reporter opsregistry.Reporter, books catalogCoverageBooks,
	idents database.IdentIndexReader, cat catalogCoverageCatalog) error {
	blocks, err := cat.CountAuthorNameBlocks()
	if err != nil {
		return fmt.Errorf("%s: count catalog author blocks: %w", catalogCoverageOpID, err)
	}
	if blocks == 0 {
		msg := "the author catalog holds no harvested author blocks (no cat_author:name: keys); run catalog.harvest-authors first. Nothing was measured."
		_ = reporter.Log(slog.LevelWarn, msg)
		catalogCoverageLog.Warn("%s", msg)
		_ = reporter.UpdateProgress(1, 1, "catalog has no harvested authors; nothing measured")
		return nil
	}

	idSet, err := idents.IdentBookIDs([]database.IdentState{database.IdentUnasked, database.IdentAskedEmpty})
	if err != nil {
		if errors.Is(err, database.ErrIdentIndexBuilding) {
			return fmt.Errorf("%s: the identification index is still being built; retry shortly: %w", catalogCoverageOpID, err)
		}
		return fmt.Errorf("%s: read identification index: %w", catalogCoverageOpID, err)
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	tally := &coverageTally{unharvested: map[string]*catalogCoverageCredit{}}
	opts := catalogCoverageRunOptions(len(ids))
	if catalogCoverageOptsObserver != nil {
		catalogCoverageOptsObserver(opts)
	}
	err = opsregistry.RunItems(ctx, reporter, ids, func(_ context.Context, id string) error {
		return coverBook(books, cat, tally, id)
	}, opts)
	if err != nil {
		return err
	}

	res := catalogCoverageResult{
		BooksConsidered:       tally.considered,
		BooksCovered:          tally.covered,
		BooksUncovered:        tally.uncovered,
		BooksUnreadable:       tally.unreadable,
		CatalogAuthorBlocks:   blocks,
		TopUnharvestedCredits: tally.top(catalogCoverageTopN),
	}
	if res.BooksUnreadable > 0 {
		res.UnreadableNote = "unreadable books are counted in books_considered with unknown scope; they are in neither books_covered nor books_uncovered"
		msg := fmt.Sprintf("%d books could not be read (first %d logged by id at warn): %s",
			res.BooksUnreadable, min(res.BooksUnreadable, catalogCoverageUnreadableLogCap), res.UnreadableNote)
		_ = reporter.Log(slog.LevelWarn, msg)
	}
	line, err := json.Marshal(res)
	if err != nil {
		return fmt.Errorf("%s: encode result: %w", catalogCoverageOpID, err)
	}
	// The credit names are library data: they go to the op log only, never to
	// a file in the repo.
	_ = reporter.Log(slog.LevelInfo, string(line))
	pct := 0.0
	if res.BooksConsidered > 0 {
		pct = 100 * float64(res.BooksCovered) / float64(res.BooksConsidered)
	}
	_ = reporter.UpdateProgress(len(ids), len(ids), fmt.Sprintf("covered %d of %d (%.1f%%)", res.BooksCovered, res.BooksConsidered, pct))
	catalogCoverageLog.Info("catalog coverage: considered=%d covered=%d uncovered=%d unreadable=%d blocks=%d",
		res.BooksConsidered, res.BooksCovered, res.BooksUncovered, res.BooksUnreadable, res.CatalogAuthorBlocks)
	return nil
}

// coverBook classifies one book into the tally. A book that is gone, or no
// longer in the Library's default view, is not considered. A book whose row or
// credit list cannot be read is a book-level failure: it counts as
// unreadable (considered, unknown scope; never covered or uncovered) and the
// run continues. A catalog read error is not book-level -- the catalog is
// what the report measures against, so a failing read would silently skew
// every count -- and is returned so RunItems fails the op.
func coverBook(books catalogCoverageBooks, cat catalogCoverageCatalog, tally *coverageTally, id string) error {
	book, err := books.GetBookByID(id)
	if err != nil {
		tally.addUnreadable(id, err)
		return nil
	}
	if !database.InLibraryDefaultView(book) {
		return nil
	}
	credits, err := database.LiveBookAuthorNames(books, book)
	if err != nil {
		tally.addUnreadable(id, err)
		return nil
	}
	unharvested := make(map[string]string, len(credits))
	for _, credit := range credits {
		// FoldKey, not catalog.AuthorNameKey: that returns "name:<fold>" and
		// HasAuthorKey prepends "name:" itself, so every book would read
		// uncovered.
		fold := authorjunk.FoldKey(credit)
		if fold == "" {
			continue
		}
		has, err := cat.HasAuthorKey(fold)
		if err != nil {
			return fmt.Errorf("%s: catalog author lookup for book %s: %w", catalogCoverageOpID, id, err)
		}
		if has {
			tally.addCovered()
			return nil
		}
		if prev, dup := unharvested[fold]; !dup || credit < prev {
			unharvested[fold] = credit
		}
	}
	tally.addUncovered(unharvested)
	return nil
}
