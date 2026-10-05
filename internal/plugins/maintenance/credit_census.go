// file: internal/plugins/maintenance/credit_census.go
// version: 1.0.0
// guid: 1df256d6-add9-41de-bf6d-622ed04e2944
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorcredit"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/util"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// --- credit-census ---
//
// WHY THIS EXISTS. The owner decided on 2026-10-04 that authors and narrators
// are ordered credit lists of individual records (book_authors and
// book_narrators, position 0..n), and that the flat fields (Book.AuthorID, the
// persisted Book.Author snapshot, the Narrator column and NarratorsJSON) become
// derived values. Before any writer or backfill moves (PR 1-6 of the plan in
// docs/plans/2026-10-04-author-narrator-credit-lists-audit.md), this op counts
// every way the flat fields and the lists disagree today, so the owner reads
// exact numbers rather than the audit's estimates. PR 3 (the backfill) is gated
// on this op re-running with zero in every class whose Kind is "disagreement".
//
// READ ONLY. No apply mode, no write capability, and creditCensusStore lists
// only reads, so a write cannot compile into this file without a reviewer
// widening that interface on purpose. The only thing it writes is its own
// result payload on its own operation row (registry.ReporterSetResult).
//
// WHERE THE BOOKS ARE. Every class carries its full sorted book id list in the
// result payload, so each count clicks through to its books (owner rule:
// every count must click through). Read it with GET /operations/:id/result.
// The op deliberately writes NO OperationChange rows: those are the change
// ledger, they feed each book's own change history and the revert path, and a
// census that changed nothing must not appear in 77k books' histories as a
// change. GET /operations/:id/changes is therefore empty for this op.
//
// Payload size: one id is ~27 bytes, so a class holding every book of a 77k
// library is ~2 MB. maintenance.author-path-link already stores one entry per
// in-scope book in its result, so a library-scale result is not new.

// creditCensusStore is this op's ENTIRE store surface. Every method is a read.
type creditCensusStore interface {
	ListBookIDs() ([]string, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetAllAuthors() ([]database.Author, error)
	// GetAuthorByID follows author tombstones; the census uses that to tell a
	// merged-away id (tombstoned) from one that resolves to nothing (dangling).
	GetAuthorByID(id int) (*database.Author, error)
	ListNarrators() ([]database.Narrator, error)
	GetNarratorByID(id int) (*database.Narrator, error)
}

// Class kinds.
const (
	// creditKindDisagreement is a flat field that disagrees with a credit list,
	// or a list entry that points at no live record. PR 3's backfill must take
	// every one of these to zero.
	creditKindDisagreement = "disagreement"
	// creditKindQuality is a record-level content problem the backfill does not
	// own (a combined-name record, a narrator credit that is not a list of
	// people). Reported so the owner sees it; not part of the PR 3 gate.
	creditKindQuality = "quality"
	// creditKindInformational is context: a subset of another class, or a state
	// that is not a disagreement by itself.
	creditKindInformational = "informational"
	// creditKindError is a book the census could not read. A run with any is
	// marked incomplete and fails.
	creditKindError = "error"
)

// Class keys. Stable strings: the UI and PR 3 select books by them.
const (
	ccNoAuthor                 = "no_author"
	ccAuthorIDJoinEmpty        = "author_id_join_empty"
	ccAuthorIDNotPrimary       = "author_id_not_primary"
	ccAuthorIDNotInJoin        = "author_id_not_in_join"
	ccAuthorPositionsNotNorm   = "author_positions_not_normalized"
	ccAuthorPositionsAllZero   = "author_positions_all_zero"
	ccAuthorJoinCombined       = "author_join_combined"
	ccAuthorSnapshotStale      = "author_snapshot_stale"
	ccAuthorIDDangling         = "author_id_dangling"
	ccAuthorIDTombstoned       = "author_id_tombstoned"
	ccNarratorColumnNoJunction = "narrator_column_no_junction"
	ccNarratorColumnDiffers    = "narrator_column_differs_junction"
	ccNarratorJunctionNoColumn = "narrator_junction_no_column"
	ccNarratorColumnNotPeople  = "narrator_column_not_people"
	ccNarratorsJSONNoJunction  = "narrators_json_junction_empty"
	ccNarratorsJSONDisagrees   = "narrators_json_disagrees_both"
	ccNarratorPositionsNotNorm = "narrator_positions_not_normalized"
	ccNarratorJunctionCombined = "narrator_junction_combined"
	ccNarratorIDDangling       = "narrator_id_dangling"
	ccReadError                = "read_error"
)

// creditClassDef is one class as the payload describes it.
type creditClassDef struct {
	Key        string
	Kind       string
	Definition string
}

// creditCensusClasses is every class in report order. The definitions are the
// contract PR 3 builds on, so they are written out in the payload too.
var creditCensusClasses = []creditClassDef{
	{ccNoAuthor, creditKindInformational,
		"Book.AuthorID is unset (nil or <= 0) and book_authors is empty: no author credit at all."},
	{ccAuthorIDJoinEmpty, creditKindDisagreement,
		"Book.AuthorID is set but book_authors is empty."},
	{ccAuthorIDNotPrimary, creditKindDisagreement,
		"Book.AuthorID is in book_authors, but no row at the lowest Position credits it. " +
			"Disjoint from author_id_not_in_join. A tie at the lowest Position that includes " +
			"AuthorID is NOT counted here; it is counted under author_positions_not_normalized."},
	{ccAuthorIDNotInJoin, creditKindDisagreement,
		"Book.AuthorID is set, book_authors is not empty, and no row credits AuthorID."},
	{ccAuthorPositionsNotNorm, creditKindDisagreement,
		"book_authors positions are not exactly 0..n-1 (duplicates such as 'A @0, B @0', gaps, or negatives)."},
	{ccAuthorPositionsAllZero, creditKindInformational,
		"book_authors has more than one row and every row is at Position 0. Subset of author_positions_not_normalized."},
	{ccAuthorJoinCombined, creditKindQuality,
		"book_authors credits a live author record whose name reads as several people (authorcredit.LooksCombined " +
			"against the author name index)."},
	{ccAuthorSnapshotStale, creditKindDisagreement,
		"The persisted Book.Author snapshot is set and differs from Book.AuthorID: AuthorID is unset, the snapshot's " +
			"id is not AuthorID, or its name is not the current name of AuthorID's record."},
	{ccAuthorIDDangling, creditKindDisagreement,
		"Book.AuthorID or a book_authors row names an author id that resolves to no record, even through tombstones."},
	{ccAuthorIDTombstoned, creditKindDisagreement,
		"Book.AuthorID or a book_authors row names a merged-away author id that resolves only through a tombstone."},
	{ccNarratorColumnNoJunction, creditKindDisagreement,
		"The Narrator column is a list of people (util.CleanNarratorCredit verdict 'people') and book_narrators is empty."},
	{ccNarratorColumnDiffers, creditKindDisagreement,
		"The Narrator column is a list of people and book_narrators is not empty, but the cleaned column " +
			"(util.CleanNarratorCredit against the book's live authors) and the junction's names in Position order " +
			"differ (compared with util.NormalizeAuthor, the narrator_name: index normalizer)."},
	{ccNarratorJunctionNoColumn, creditKindDisagreement,
		"book_narrators is not empty and the Narrator column is empty."},
	{ccNarratorColumnNotPeople, creditKindQuality,
		"The Narrator column is set but util.CleanNarratorCredit does not read it as a list of people (junk such as a " +
			"URL, or only the book's own authors). The junction sync leaves such books alone by design."},
	{ccNarratorsJSONNoJunction, creditKindDisagreement,
		"NarratorsJSON names at least one narrator and book_narrators is empty (the case ABS serves from its second tier)."},
	{ccNarratorsJSONDisagrees, creditKindDisagreement,
		"NarratorsJSON names at least one narrator and its set of names matches neither book_narrators nor the " +
			"Narrator column (raw split or cleaned)."},
	{ccNarratorPositionsNotNorm, creditKindDisagreement,
		"book_narrators positions are not exactly 0..n-1."},
	{ccNarratorJunctionCombined, creditKindQuality,
		"book_narrators credits a narrator record whose name util.SplitCreditNames splits into several people."},
	{ccNarratorIDDangling, creditKindDisagreement,
		"A book_narrators row names a narrator id that resolves to no record."},
	{ccReadError, creditKindError,
		"The census could not read this book or its credits. Its other classes are unknown; the run is incomplete."},
}

// creditCensusClass is one class in the result payload.
type creditCensusClass struct {
	Key        string   `json:"key"`
	Kind       string   `json:"kind"`
	Definition string   `json:"definition"`
	Count      int      `json:"count"`
	BookIDs    []string `json:"book_ids"`
}

// creditCensusResult is the op's result payload (GET /operations/:id/result).
type creditCensusResult struct {
	// Complete is false when the run was canceled or any book was unreadable.
	// An incomplete census must never be read as a clean one.
	Complete bool `json:"complete"`
	// BooksListed is what ListBookIDs returned; BooksScanned the books read and
	// classified; BooksGone the listed ids whose row was gone by the time the
	// worker read it (deleted mid-run).
	BooksListed      int `json:"books_listed"`
	BooksScanned     int `json:"books_scanned"`
	BooksGone        int `json:"books_gone"`
	AuthorsIndexed   int `json:"authors_indexed"`
	NarratorsIndexed int `json:"narrators_indexed"`
	// DisagreementBooks is the number of distinct books in at least one class
	// of kind "disagreement": the size of PR 3's work list.
	DisagreementBooks int `json:"disagreement_books"`
	// Counts is the per-class count, for a glance without walking Classes.
	Counts     map[string]int      `json:"counts"`
	Classes    []creditCensusClass `json:"classes"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt time.Time           `json:"finished_at"`
	Error      string              `json:"error,omitempty"`
}

// creditCensusIndex is the frozen author/narrator snapshot taken once before
// the scan. Workers share it read-only. An id missing from it is looked up in
// the store, which is how a tombstoned or newly created record is told apart
// from a dangling one.
type creditCensusIndex struct {
	store          creditCensusStore
	authors        map[int]database.Author
	authorNameNorm map[string]bool
	narrators      map[int]database.Narrator
}

func buildCreditCensusIndex(store creditCensusStore) (*creditCensusIndex, error) {
	authors, err := store.GetAllAuthors()
	if err != nil {
		return nil, fmt.Errorf("list authors: %w", err)
	}
	narrators, err := store.ListNarrators()
	if err != nil {
		return nil, fmt.Errorf("list narrators: %w", err)
	}
	idx := &creditCensusIndex{
		store:          store,
		authors:        make(map[int]database.Author, len(authors)),
		authorNameNorm: make(map[string]bool, len(authors)),
		narrators:      make(map[int]database.Narrator, len(narrators)),
	}
	for _, a := range authors {
		idx.authors[a.ID] = a
		if n := util.NormalizeAuthor(strings.TrimSpace(a.Name)); n != "" {
			idx.authorNameNorm[n] = true
		}
	}
	for _, n := range narrators {
		idx.narrators[n.ID] = n
	}
	return idx, nil
}

// authorState is what an author id resolves to.
type authorState int

const (
	authorLive authorState = iota
	authorTombstoned
	authorDangling
)

// resolveAuthor returns the record id resolves to and how. A tombstoned id
// returns the canonical record.
func (idx *creditCensusIndex) resolveAuthor(id int) (database.Author, authorState, error) {
	if a, ok := idx.authors[id]; ok {
		return a, authorLive, nil
	}
	a, err := idx.store.GetAuthorByID(id)
	if err != nil {
		return database.Author{}, authorDangling, fmt.Errorf("read author %d: %w", id, err)
	}
	if a == nil {
		return database.Author{}, authorDangling, nil
	}
	if a.ID != id {
		return *a, authorTombstoned, nil
	}
	// Created after the snapshot: live.
	return *a, authorLive, nil
}

func (idx *creditCensusIndex) resolveNarrator(id int) (database.Narrator, bool, error) {
	if n, ok := idx.narrators[id]; ok {
		return n, true, nil
	}
	n, err := idx.store.GetNarratorByID(id)
	if err != nil {
		return database.Narrator{}, false, fmt.Errorf("read narrator %d: %w", id, err)
	}
	if n == nil {
		return database.Narrator{}, false, nil
	}
	return *n, true, nil
}

func (idx *creditCensusIndex) looksCombined(name string) bool {
	return authorcredit.LooksCombined(name, func(piece string) bool {
		return idx.authorNameNorm[util.NormalizeAuthor(strings.TrimSpace(piece))]
	})
}

// positionsNormalized reports whether positions are exactly 0..n-1 in some
// order.
func positionsNormalized(positions []int) bool {
	seen := make([]bool, len(positions))
	for _, p := range positions {
		if p < 0 || p >= len(positions) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

func normalizedNames(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if v := util.NormalizeAuthor(strings.TrimSpace(n)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func sameOrderedNames(a, b []string) bool {
	a, b = normalizedNames(a), normalizedNames(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameNameSet(a, b []string) bool {
	set := func(xs []string) map[string]bool {
		m := make(map[string]bool, len(xs))
		for _, x := range normalizedNames(xs) {
			m[x] = true
		}
		return m
	}
	sa, sb := set(a), set(b)
	if len(sa) != len(sb) {
		return false
	}
	for k := range sa {
		if !sb[k] {
			return false
		}
	}
	return true
}

// parseCreditCensusNarratorsJSON reads NarratorsJSON the way the ABS mapper's
// second tier does (parseNarratorsJSON in internal/server/handlers/abs/mapper.go):
// a JSON array of strings, else a bare credit string split by
// util.SplitCreditNames. Kept in step by hand because that function is
// unexported in a handler package; both go away when PR 6 drops the column.
func parseCreditCensusNarratorsJSON(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var arr []string
	if err := json.Unmarshal([]byte(raw), &arr); err == nil {
		out := make([]string, 0, len(arr))
		for _, n := range arr {
			if n = strings.TrimSpace(n); n != "" {
				out = append(out, n)
			}
		}
		return out
	}
	return util.SplitCreditNames(raw)
}

// classifyCreditBook returns every class book falls in. join and junction are
// the book's book_authors and book_narrators rows as stored. It reads the store
// only to resolve ids missing from the frozen index.
func classifyCreditBook(idx *creditCensusIndex, book *database.Book, join []database.BookAuthor, junction []database.BookNarrator) ([]string, error) {
	var classes []string
	add := func(k string) { classes = append(classes, k) }

	// --- authors ---
	authorID := 0
	if book.AuthorID != nil && *book.AuthorID > 0 {
		authorID = *book.AuthorID
	}

	sortedJoin := append([]database.BookAuthor(nil), join...)
	sort.SliceStable(sortedJoin, func(i, j int) bool { return sortedJoin[i].Position < sortedJoin[j].Position })

	switch {
	case authorID == 0 && len(join) == 0:
		add(ccNoAuthor)
	case authorID != 0 && len(join) == 0:
		add(ccAuthorIDJoinEmpty)
	case authorID != 0:
		inJoin, atMin := false, false
		minPos := sortedJoin[0].Position
		for _, r := range sortedJoin {
			if r.AuthorID == authorID {
				inJoin = true
				if r.Position == minPos {
					atMin = true
				}
			}
		}
		switch {
		case !inJoin:
			add(ccAuthorIDNotInJoin)
		case !atMin:
			add(ccAuthorIDNotPrimary)
		}
	}

	if len(join) > 0 {
		positions := make([]int, len(join))
		allZero := len(join) > 1
		for i, r := range join {
			positions[i] = r.Position
			if r.Position != 0 {
				allZero = false
			}
		}
		if !positionsNormalized(positions) {
			add(ccAuthorPositionsNotNorm)
		}
		if allZero {
			add(ccAuthorPositionsAllZero)
		}
	}

	// Resolve AuthorID first, then the join in Position order: the live author
	// names in credit order (the rule database.LiveBookAuthorNames uses), which
	// CleanNarratorCredit needs to drop the book's own authors from a narrator
	// credit. A tombstoned id contributes its canonical record's name; dropping
	// it would make every self-read book look like a narrator mismatch.
	var (
		authorNames         []string
		seenResolved        = map[int]bool{}
		dangling, tombstone bool
		combined            bool
		primaryRecord       *database.Author
	)
	ids := make([]int, 0, len(sortedJoin)+1)
	if authorID != 0 {
		ids = append(ids, authorID)
	}
	for _, r := range sortedJoin {
		ids = append(ids, r.AuthorID)
	}
	for i, id := range ids {
		a, state, err := idx.resolveAuthor(id)
		if err != nil {
			return nil, err
		}
		switch state {
		case authorDangling:
			dangling = true
			continue
		case authorTombstoned:
			tombstone = true
		}
		if i == 0 && authorID != 0 {
			rec := a
			primaryRecord = &rec
		}
		// The combined check covers join rows only (i > 0 when AuthorID led).
		if (authorID == 0 || i > 0) && idx.looksCombined(a.Name) {
			combined = true
		}
		if seenResolved[a.ID] {
			continue
		}
		seenResolved[a.ID] = true
		if name := strings.TrimSpace(a.Name); name != "" {
			authorNames = append(authorNames, name)
		}
	}
	if combined {
		add(ccAuthorJoinCombined)
	}
	if dangling {
		add(ccAuthorIDDangling)
	}
	if tombstone {
		add(ccAuthorIDTombstoned)
	}

	if snap := book.Author; snap != nil {
		stale := false
		switch {
		case authorID == 0:
			stale = true
		case snap.ID != 0 && snap.ID != authorID:
			stale = true
		case primaryRecord != nil && strings.TrimSpace(snap.Name) != strings.TrimSpace(primaryRecord.Name):
			stale = true
		case primaryRecord == nil:
			// AuthorID dangles: the snapshot names a record that no longer exists.
			stale = true
		}
		if stale {
			add(ccAuthorSnapshotStale)
		}
	}

	// --- narrators ---
	sortedJunction := append([]database.BookNarrator(nil), junction...)
	sort.SliceStable(sortedJunction, func(i, j int) bool { return sortedJunction[i].Position < sortedJunction[j].Position })

	var junctionNames []string
	narratorDangling, narratorCombined := false, false
	for _, r := range sortedJunction {
		n, ok, err := idx.resolveNarrator(r.NarratorID)
		if err != nil {
			return nil, err
		}
		if !ok {
			narratorDangling = true
			continue
		}
		name := strings.TrimSpace(n.Name)
		if name == "" {
			continue
		}
		junctionNames = append(junctionNames, name)
		if len(util.SplitCreditNames(name)) >= 2 {
			narratorCombined = true
		}
	}
	if len(junction) > 0 {
		positions := make([]int, len(junction))
		for i, r := range junction {
			positions[i] = r.Position
		}
		if !positionsNormalized(positions) {
			add(ccNarratorPositionsNotNorm)
		}
	}
	if narratorDangling {
		add(ccNarratorIDDangling)
	}
	if narratorCombined {
		add(ccNarratorJunctionCombined)
	}

	column := ""
	if book.Narrator != nil {
		column = strings.TrimSpace(*book.Narrator)
	}
	var cleaned []string
	if column != "" {
		people, verdict := util.CleanNarratorCredit(column, authorNames)
		switch {
		case verdict != util.NarratorCreditPeople:
			add(ccNarratorColumnNotPeople)
		case len(junction) == 0:
			add(ccNarratorColumnNoJunction)
		case !sameOrderedNames(people, junctionNames):
			add(ccNarratorColumnDiffers)
		}
		cleaned = people
	} else if len(junction) > 0 {
		add(ccNarratorJunctionNoColumn)
	}

	if book.NarratorsJSON != nil {
		if jsonNames := parseCreditCensusNarratorsJSON(*book.NarratorsJSON); len(jsonNames) > 0 {
			if len(junction) == 0 {
				add(ccNarratorsJSONNoJunction)
			}
			matchesJunction := len(junction) > 0 && sameNameSet(jsonNames, junctionNames)
			matchesColumn := column != "" &&
				(sameNameSet(jsonNames, util.SplitCreditNames(column)) || (len(cleaned) > 0 && sameNameSet(jsonNames, cleaned)))
			if !matchesJunction && !matchesColumn {
				add(ccNarratorsJSONDisagrees)
			}
		}
	}

	return classes, nil
}

// creditCensusAccumulator collects book ids per class across workers.
type creditCensusAccumulator struct {
	mu      sync.Mutex
	byClass map[string][]string
}

func (a *creditCensusAccumulator) add(bookID string, classes []string) {
	if len(classes) == 0 {
		return
	}
	a.mu.Lock()
	for _, c := range classes {
		a.byClass[c] = append(a.byClass[c], bookID)
	}
	a.mu.Unlock()
}

func (p *Plugin) creditCensusDef() sdk.OperationDef {
	return sdk.OperationDef{
		ID:          "maintenance.credit-census",
		Liveness:    sdk.LivenessRunItems,
		Plugin:      "maintenance",
		DisplayName: "Author/narrator credit census",
		Description: "READ ONLY — changes nothing. Counts every way the flat author/narrator fields " +
			"(Book.AuthorID, the Book.Author snapshot, the Narrator column, NarratorsJSON) disagree with the " +
			"book_authors / book_narrators credit lists: AuthorID with an empty join, AuthorID not primary or " +
			"not in the join, unnormalized positions, combined-name records, a stale snapshot, narrator column " +
			"vs junction drift, NarratorsJSON drift, dangling or tombstoned ids, and books with no author. " +
			"Every class lists all its book ids in GET /operations/:id/result. Gate for the credits backfill.",
		// ResumeDrop: a read-only census is cheap to re-trigger, and its memory
		// is bounded to id lists, so a fresh run is the simplest correct resume.
		ResumePolicy:    sdk.ResumeDrop,
		DefaultPriority: sdk.PriorityLow,
		ConcurrencyKey:  "maintenance.credit-census",
		Reads:           []sdk.Resource{sdk.ResBooks, sdk.ResAuthors},
		Cancellable:     true,
		Isolate:         false,
		Timeout:         2 * time.Hour,
		Capabilities:    []sdk.Capability{sdk.CapLibraryRead},
		Run:             p.runCreditCensus,
	}
}

func (p *Plugin) runCreditCensus(ctx context.Context, _ json.RawMessage, reporter sdk.Reporter) error {
	ops := p.deps.OpsStore()
	if ops == nil {
		return fmt.Errorf("database not initialized")
	}
	var store creditCensusStore = ops
	res, err := runCreditCensusScan(ctx, store, reporter, runtime.NumCPU())
	if res != nil {
		if serr := registry.ReporterSetResult(reporter, res); serr != nil {
			if err == nil {
				return fmt.Errorf("store credit census result: %w", serr)
			}
			_ = reporter.Log(slog.LevelError, "credit-census: result not stored", slog.String("error", serr.Error()))
		}
	}
	return err
}

// runCreditCensusScan does the work. It returns a result whenever it got as far
// as listing books, including on cancel or read failure (Complete=false), so
// the caller can persist what was counted.
func runCreditCensusScan(ctx context.Context, store creditCensusStore, reporter sdk.Reporter, concurrency int) (*creditCensusResult, error) {
	started := time.Now()
	_ = reporter.Log(slog.LevelInfo, "credit-census: start (read only)")
	_ = reporter.UpdateProgress(0, 1, "Indexing authors and narrators…")

	idx, err := buildCreditCensusIndex(store)
	if err != nil {
		return nil, err
	}
	ids, err := store.ListBookIDs()
	if err != nil {
		return nil, fmt.Errorf("list book ids: %w", err)
	}
	sort.Strings(ids)

	acc := &creditCensusAccumulator{byClass: map[string][]string{}}
	var scanned, gone, failed atomic.Int64
	var firstErrMu sync.Mutex
	var firstErr error

	runErr := registry.RunItems(ctx, reporter, ids, func(_ context.Context, id string) error {
		classes, gotBook, cerr := creditCensusOne(idx, store, id)
		switch {
		case cerr != nil:
			failed.Add(1)
			acc.add(id, []string{ccReadError})
			firstErrMu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("book %s: %w", id, cerr)
			}
			firstErrMu.Unlock()
		case !gotBook:
			gone.Add(1)
		default:
			scanned.Add(1)
			acc.add(id, classes)
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: concurrency,
		// Label runs inside each worker goroutine: the counters are atomics.
		Label: func(i, total int) string {
			return fmt.Sprintf("Credit census %d/%d (classified %d, unreadable %d)",
				i+1, total, scanned.Load(), failed.Load())
		},
	})

	res := &creditCensusResult{
		BooksListed:      len(ids),
		BooksScanned:     int(scanned.Load()),
		BooksGone:        int(gone.Load()),
		AuthorsIndexed:   len(idx.authors),
		NarratorsIndexed: len(idx.narrators),
		Counts:           make(map[string]int, len(creditCensusClasses)),
		StartedAt:        started,
		FinishedAt:       time.Now(),
	}
	disagreement := map[string]bool{}
	for _, def := range creditCensusClasses {
		bookIDs := acc.byClass[def.Key]
		sort.Strings(bookIDs)
		if bookIDs == nil {
			bookIDs = []string{}
		}
		res.Classes = append(res.Classes, creditCensusClass{
			Key: def.Key, Kind: def.Kind, Definition: def.Definition,
			Count: len(bookIDs), BookIDs: bookIDs,
		})
		res.Counts[def.Key] = len(bookIDs)
		if def.Kind == creditKindDisagreement {
			for _, id := range bookIDs {
				disagreement[id] = true
			}
		}
	}
	res.DisagreementBooks = len(disagreement)

	var outErr error
	switch {
	case runErr != nil:
		outErr = fmt.Errorf("credit census stopped: %w", runErr)
	case failed.Load() > 0:
		outErr = fmt.Errorf("credit census incomplete: %d book(s) unreadable, first: %w", failed.Load(), firstErr)
	}
	res.Complete = outErr == nil
	if outErr != nil {
		res.Error = outErr.Error()
	}

	summary := fmt.Sprintf("credit-census (read only, complete=%t): listed=%d scanned=%d gone=%d disagreement_books=%d counts=%v",
		res.Complete, res.BooksListed, res.BooksScanned, res.BooksGone, res.DisagreementBooks, res.Counts)
	_ = reporter.Log(slog.LevelInfo, summary)
	_ = reporter.UpdateProgress(len(ids), len(ids), summary)
	return res, outErr
}

// creditCensusOne reads one book and its credits and classifies it. gotBook is
// false when the row is gone (deleted since ListBookIDs).
func creditCensusOne(idx *creditCensusIndex, store creditCensusStore, id string) (classes []string, gotBook bool, err error) {
	book, err := store.GetBookByID(id)
	if err != nil {
		return nil, false, fmt.Errorf("read book: %w", err)
	}
	if book == nil {
		return nil, false, nil
	}
	join, err := store.GetBookAuthors(id)
	if err != nil {
		return nil, true, fmt.Errorf("read book_authors: %w", err)
	}
	junction, err := store.GetBookNarrators(id)
	if err != nil {
		return nil, true, fmt.Errorf("read book_narrators: %w", err)
	}
	classes, err = classifyCreditBook(idx, book, join, junction)
	if err != nil {
		return nil, true, err
	}
	return classes, true, nil
}
