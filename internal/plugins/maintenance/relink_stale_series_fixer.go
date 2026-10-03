// file: internal/plugins/maintenance/relink_stale_series_fixer.go
// version: 1.2.0
// guid: 1d26959f-7774-48db-b0ea-fa7813f655ef
// last-edited: 2026-10-03

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// relinkSeriesFixerID is the Repairs-lane id of the stale-series relink.
const relinkSeriesFixerID = "maintenance.relink-stale-series"

// Row classes of the relink fixer. Every class but relinkClassRelink is
// held; each is its own count in PlanResult.ByClass, so the plan summary is
// the census.
const (
	// relinkClassRelink: the series row the embedded object names (by id)
	// exists, its name and author agree with the object and the book, and no
	// series history clears or contradicts it. The only applicable class.
	relinkClassRelink = "relink"
	// relinkClassCleared: the newest series history row (or a series_name
	// lock) cleared the series. Almost every series clear ever made leaves
	// this exact state: the store kept the old object when SeriesID went nil
	// (pebble_store.go preserve-on-nil). The clearing source is in the skip
	// kind (skipped_cleared_by_<source>), so SkippedByKind groups the rows by
	// who cleared them, and in Evidence.
	relinkClassCleared = "held-cleared-by-history"
	// relinkClassMismatch: something disagrees with the embedded id: the
	// series row's name or author, or a newer history row naming another
	// series.
	relinkClassMismatch = "held-name-mismatch"
	// relinkClassNameMatch: the row by id is gone, but exactly one series row
	// has the same normalized name and a compatible author.
	relinkClassNameMatch = "name-match"
	// relinkClassOrphan: no series row by id and no unique name match. The
	// embedded object is the only record of the series.
	relinkClassOrphan = "orphan"
)

// Skip kinds of the relink fixer besides the framework's and junkSkip*.
const (
	// relinkSkipClearedPrefix + the clearing row's source (or "field_lock"):
	// the series was cleared or overridden; relinking would undo that edit.
	relinkSkipClearedPrefix = "skipped_cleared_by_"
	// relinkSkipMismatch: the evidence disagrees with the embedded id.
	relinkSkipMismatch = "skipped_series_mismatch"
	// relinkSkipHistoryUnreadable: the series history could not be read in
	// full, so a clear cannot be ruled out.
	relinkSkipHistoryUnreadable = "skipped_history_unreadable"
	// relinkSkipNotStale: what a re-plan reports for a book that no longer
	// has SeriesID == nil with an embedded Series object. A plan never lists
	// it.
	relinkSkipNotStale = "skipped_not_stale"
)

// relinkSeriesFixer finds books whose SeriesID is nil but whose stored row
// still carries an embedded Series object (left by older series-clear and
// projection bugs).
//
// WHY NOW: PR #3698 makes the Pebble store drop Book.Series whenever
// Book.SeriesID is nil, on the book's next write. For these books the
// embedded object may be the only record of the real series, so it has to be
// relinked (or read by the owner) before that change deploys.
//
// Apply writes SeriesID only. The Series object and SeriesSequence are left
// alone, no file is touched, and the one history row (series_id) is what
// "undo last apply" reverts.
type relinkSeriesFixer struct {
	p *Plugin

	// seriesMu guards a short-lived series index Replan uses ONLY for the
	// name-match lookup of a book whose series row is gone (those rows are
	// held, never written). The target row of a relink is point-read fresh
	// in Replan (GetSeriesByID) and again in Apply, which re-checks its name
	// and author, so a rename or author change inside the cache window can
	// never be written through.
	seriesMu      sync.Mutex
	seriesIdx     *relinkSeriesIndex
	seriesFetched time.Time
}

func newRelinkSeriesFixer(p *Plugin) *relinkSeriesFixer { return &relinkSeriesFixer{p: p} }

var _ repairs.Fixer = (*relinkSeriesFixer)(nil)

func (f *relinkSeriesFixer) ID() string    { return relinkSeriesFixerID }
func (f *relinkSeriesFixer) Title() string { return "Stale series objects (series id lost)" }
func (f *relinkSeriesFixer) Description() string {
	return "Books whose series id is empty but whose stored row still carries the series object. " +
		"\"relink\" rows point the series id back at the series the object names (it still exists); " +
		"\"name-match\" and \"orphan\" rows are held for review. Writes the series id only; the series " +
		"object and position are left as they are. Must run before the store starts dropping the object (#3698)."
}

// relinkSeriesIndex is every series row, by id and by normalized name.
type relinkSeriesIndex struct {
	byID   map[int]database.Series
	byName map[string][]database.Series
}

func newRelinkSeriesIndex(all []database.Series) *relinkSeriesIndex {
	idx := &relinkSeriesIndex{byID: make(map[int]database.Series, len(all)), byName: map[string][]database.Series{}}
	for _, s := range all {
		idx.byID[s.ID] = s
		if n := normSeriesName(s.Name); n != "" {
			idx.byName[n] = append(idx.byName[n], s)
		}
	}
	return idx
}

// normSeriesName folds case and runs of white space.
func normSeriesName(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// relinkDecision is what Apply needs from Replan.
type relinkDecision struct {
	bookID   string
	seriesID int
	// embedded is the Series object as planned; Apply refuses the row when
	// the stored object differs.
	embedded database.Series
}

// Plan lists every live book with SeriesID == nil and an embedded Series.
//
// BookCore carries no Series and the memdb projection strips it, so every
// series-less book is point-read in full from Pebble (GetBookByID) on a
// bounded worker pool.
func (f *relinkSeriesFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	// The Complete variant: this plan is the census taken before #3698, so a
	// memdb known to be short falls through to the authoritative scan.
	cores, err := store.GetAllBooksCoreComplete(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCoreComplete: %w", err)
	}
	var cands []string
	for i := range cores {
		if cores[i].IsSoftDeleted() || cores[i].SeriesID != nil {
			continue
		}
		cands = append(cands, cores[i].ID)
	}
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	idx := newRelinkSeriesIndex(allSeries)

	found := make([]*repairs.Row, len(cands))
	var done atomic.Int64
	// Each worker writes only found[i] for its own i.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		b, err := store.GetBookByID(cands[i])
		if err != nil {
			r := relinkErrorRow(cands[i], "", fmt.Errorf("read book %s: %w", cands[i], err))
			found[i] = &r
			return nil
		}
		if !relinkStale(b) {
			return nil
		}
		r, err := f.evaluate(store, idx, idx.lookup, b)
		if err != nil {
			r = relinkErrorRow(b.ID, b.Title, err)
		}
		r.Detail = nil // a stored plan never needs it
		found[i] = &r
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Stale series objects %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	rows := make([]repairs.Row, 0)
	for _, r := range found {
		if r != nil {
			rows = append(rows, *r)
		}
	}
	return rows, nil
}

// relinkStale reports whether b is a live book with SeriesID == nil and an
// embedded Series object.
func relinkStale(b *database.Book) bool {
	return b != nil && !b.IsSoftDeleted() && b.SeriesID == nil && b.Series != nil
}

// relinkClassError is the class of a row whose book could not be read or
// classified, so the census (PlanResult.ByClass) still counts it.
const relinkClassError = "error"

func relinkErrorRow(bookID, title string, err error) repairs.Row {
	r := repairs.Row{RowID: bookID, BookIDs: []string{bookID}, Title: title, Class: relinkClassError,
		Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
	r.Fingerprint = relinkFingerprint(r, "error")
	return r
}

// Replan re-reads the book; a book that is gone or no longer stale comes back
// skipped with a different fingerprint (changed_since_plan), never an error.
func (f *relinkSeriesFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	b, err := store.GetBookByID(planned.RowID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", planned.RowID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Skipped: junkSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Reason = r.SkipReason
		r.Fingerprint = relinkFingerprint(r, "gone")
		return r, nil
	}
	if !relinkStale(b) {
		r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Skipped: relinkSkipNotStale,
			SkipReason: "the book has a series id again, or no longer carries a series object", Risk: repairs.RiskLow}
		r.Reason = r.SkipReason
		r.Fingerprint = relinkFingerprint(r, "not-stale")
		return r, nil
	}
	idx, err := f.index(store)
	if err != nil {
		return repairs.Row{}, err
	}
	return f.evaluate(store, idx, freshSeriesByID(store), b)
}

// relinkSeriesByID finds one series row by id.
type relinkSeriesByID func(id int) (database.Series, bool, error)

func (idx *relinkSeriesIndex) lookup(id int) (database.Series, bool, error) {
	s, ok := idx.byID[id]
	return s, ok, nil
}

// freshSeriesByID point-reads the series row, never a cache.
func freshSeriesByID(store OpsStore) relinkSeriesByID {
	return func(id int) (database.Series, bool, error) {
		if id <= 0 {
			return database.Series{}, false, nil
		}
		s, err := store.GetSeriesByID(id)
		if err != nil {
			return database.Series{}, false, fmt.Errorf("read series %d: %w", id, err)
		}
		if s == nil {
			return database.Series{}, false, nil
		}
		return *s, true, nil
	}
}

func (f *relinkSeriesFixer) index(store OpsStore) (*relinkSeriesIndex, error) {
	f.seriesMu.Lock()
	defer f.seriesMu.Unlock()
	if f.seriesIdx != nil && time.Since(f.seriesFetched) < time.Minute {
		return f.seriesIdx, nil
	}
	all, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("list series: %w", err)
	}
	f.seriesIdx, f.seriesFetched = newRelinkSeriesIndex(all), time.Now()
	return f.seriesIdx, nil
}

// evaluate classifies one stale book. b must satisfy relinkStale.
//
// byID finds the series row by id: Plan answers from its index (read at plan
// start), Replan with a fresh point read.
func (f *relinkSeriesFixer) evaluate(store OpsStore, idx *relinkSeriesIndex, byID relinkSeriesByID, b *database.Book) (repairs.Row, error) {
	emb := *b.Series
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Risk: repairs.RiskReview,
		Current: map[string]string{
			"series_id":            "",
			"embedded_series_id":   strconv.Itoa(emb.ID),
			"embedded_series_name": emb.Name,
		}}
	if b.AuthorID != nil {
		a, err := store.GetAuthorByID(*b.AuthorID)
		if err != nil {
			return repairs.Row{}, fmt.Errorf("read author %d of %s: %w", *b.AuthorID, b.ID, err)
		}
		if a != nil {
			r.Author = a.Name
		}
	}
	r.Evidence = append(r.Evidence, fmt.Sprintf("series_id is empty; the stored series object names id %d %q", emb.ID, emb.Name))
	if b.ITunesPersistentID != nil && *b.ITunesPersistentID != "" {
		r.Evidence = append(r.Evidence, fmt.Sprintf("book carries iTunes persistent id %s (database write only; no file is touched)", *b.ITunesPersistentID))
	}
	authors, err := relinkBookAuthorIDs(store, b)
	if err != nil {
		return repairs.Row{}, err
	}

	// The series row by id, and the unique same-name candidate when it is gone.
	var target, candidate *database.Series
	s, ok, err := byID(emb.ID)
	if err != nil {
		return repairs.Row{}, err
	}
	if ok && emb.ID > 0 {
		t := s
		target = &t
		r.Evidence = append(r.Evidence, fmt.Sprintf("series row %d exists: %q", s.ID, s.Name))
	} else {
		r.Evidence = append(r.Evidence, fmt.Sprintf("no series row has id %d", emb.ID))
		var matches []database.Series
		for _, s := range idx.byName[normSeriesName(emb.Name)] {
			if relinkAuthorCompatible(s, authors) {
				matches = append(matches, s)
			}
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		switch len(matches) {
		case 0:
			r.Evidence = append(r.Evidence, "no series row has that name with a compatible author")
		case 1:
			candidate = &matches[0]
			r.Evidence = append(r.Evidence, fmt.Sprintf("candidate: series %d %q is the only same-name series with a compatible author", candidate.ID, candidate.Name))
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = strconv.Itoa(m.ID)
			}
			r.Evidence = append(r.Evidence, fmt.Sprintf("%d same-name series with a compatible author (%s); none chosen", len(matches), strings.Join(ids, ", ")))
		}
	}

	// History first, for every class: a series someone cleared is a
	// deliberate unlink whether or not its row still exists.
	hv, err := relinkSeriesHistory(store, b.ID, emb, target)
	if err != nil {
		return repairs.Row{}, err
	}
	if hv.evidence != "" {
		r.Evidence = append(r.Evidence, hv.evidence)
	} else {
		r.Evidence = append(r.Evidence, "no series history for this book")
	}
	lockWhy := ""
	if target != nil {
		if lockWhy, err = relinkSeriesLockWhy(store, b.ID, emb, *target); err != nil {
			return repairs.Row{}, err
		}
	} else if lockWhy, err = relinkSeriesLockWhy(store, b.ID, emb, emb); err != nil {
		return repairs.Row{}, err
	}

	switch {
	case hv.unreadable != "":
		r.Class, r.Skipped, r.SkipReason = relinkClassError, relinkSkipHistoryUnreadable, hv.unreadable
	case hv.cleared:
		r.Class, r.Skipped = relinkClassCleared, relinkSkipClearedPrefix+relinkClearBucket(hv.source, hv.changeType)
		r.SkipReason = "the series was cleared: " + hv.evidence
	case lockWhy != "":
		r.Class, r.Skipped, r.SkipReason = relinkClassCleared, relinkSkipClearedPrefix+relinkClearFieldLock, lockWhy
		r.Evidence = append(r.Evidence, "field lock: "+lockWhy)
	case hv.mismatch:
		r.Class, r.Skipped = relinkClassMismatch, relinkSkipMismatch
		r.SkipReason = "series history disagrees with the stored object: " + hv.evidence
	case target != nil && normSeriesName(target.Name) != normSeriesName(emb.Name):
		r.Class, r.Skipped = relinkClassMismatch, relinkSkipMismatch
		r.SkipReason = fmt.Sprintf("series row %d is named %q, the stored object %q", target.ID, target.Name, emb.Name)
	case target != nil && !relinkAuthorCompatible(*target, authors):
		r.Class, r.Skipped = relinkClassMismatch, relinkSkipMismatch
		r.SkipReason = fmt.Sprintf("series row %d belongs to author %s, which is not one of the book's", target.ID, intPtrStr(target.AuthorID))
	case target != nil:
		r.Class = relinkClassRelink
	case candidate != nil:
		r.Class, r.Skipped = relinkClassNameMatch, junkSkipNeedsManual
		r.SkipReason = "the series row the object named is gone; one same-name series exists, but which series this book belongs to is the owner's call"
		r.Proposed = map[string]string{"candidate_series_id": strconv.Itoa(candidate.ID)}
	default:
		r.Class, r.Skipped = relinkClassOrphan, junkSkipNeedsManual
		r.SkipReason = "no series row matches the stored object; it is the only record of this series"
	}
	extra := relinkFingerprintExtra(b, emb, r.Class, target, candidate) + "|h=" + hv.key + "|lock=" + lockWhy

	// Doctor Who / Big Finish / Torchwood by series name. The framework
	// guard reads the series name through SeriesID, which is nil on every
	// row here, so it never sees these names. It overrides the skip kind but
	// keeps the class, so the census still counts the row.
	for _, name := range []string{emb.Name, nameOf(target), nameOf(candidate)} {
		if name != "" && applygate.IsOwnerManualOnly("", name) {
			r.Skipped = repairs.SkipOwnerManual
			r.SkipReason = fmt.Sprintf("series %q is Doctor Who / Big Finish / Torchwood; owner applies these by hand", name)
			r.Proposed = nil
			break
		}
	}
	if r.Skipped != "" {
		r.Reason = r.SkipReason
		r.Fingerprint = relinkFingerprint(r, extra)
		return r, nil
	}
	r.Proposed = map[string]string{"series_id": strconv.Itoa(target.ID), "series_name": target.Name}
	r.Reason = "the series id was lost, the stored series object names an existing series with the same name and a " +
		"compatible author, and the newest series history agrees with it (or there is none); review: an unlink that " +
		"wrote no history leaves this same state (a top-level series_name \"\" edit of a book whose series id was " +
		"already empty, an operation revert made before reverts recorded history, cleanup_series), so the history " +
		"cannot prove the unlink was not deliberate"
	r.Detail = &relinkDecision{bookID: b.ID, seriesID: target.ID, embedded: emb}
	r.Fingerprint = relinkFingerprint(r, extra)
	return r, nil
}

// Clear buckets: the fixed set of sources a cleared row's skip kind names
// (skipped_cleared_by_<bucket>), so the plan summary's SkippedByKind counts
// the cleared rows by kind of writer. The full source stays in Evidence.
const (
	relinkClearManual        = "manual"
	relinkClearBatch         = "batch"
	relinkClearUndo          = "undo"
	relinkClearOpRevert      = "operation_revert"
	relinkClearFixer         = "fixer"
	relinkClearMetadataApply = "metadata_apply"
	relinkClearOther         = "other"
	// relinkClearFieldLock is the bucket of a series_name lock (no history
	// row decided).
	relinkClearFieldLock = "field_lock"
)

// relinkOpRevertSource is audiobooks.RevertHistorySource, spelled out so the
// maintenance plugin does not import the audiobooks service.
const relinkOpRevertSource = "operation_revert"

// relinkClearBucket maps a clearing history row's source and change type to
// its bucket.
func relinkClearBucket(source, changeType string) string {
	src := strings.ToLower(strings.TrimSpace(source))
	switch {
	case src == relinkOpRevertSource:
		return relinkClearOpRevert
	case src == "undo-last-apply" || changeType == "undo":
		return relinkClearUndo
	case src == "manual" || src == "user_edit" || changeType == database.ChangeTypeManual:
		return relinkClearManual
	case strings.Contains(src, "batch") || changeType == "batch":
		return relinkClearBatch
	case strings.HasPrefix(src, "maintenance.") || strings.HasPrefix(src, "repairs") || changeType == "bulk_update":
		return relinkClearFixer
	case changeType == "fetched" || changeType == "apply":
		return relinkClearMetadataApply
	default:
		return relinkClearOther
	}
}

func nameOf(s *database.Series) string {
	if s == nil {
		return ""
	}
	return s.Name
}

// relinkBookAuthorIDs is every author id of b: its primary author and its
// book_authors rows.
func relinkBookAuthorIDs(store OpsStore, b *database.Book) (map[int]bool, error) {
	ids := map[int]bool{}
	if b.AuthorID != nil {
		ids[*b.AuthorID] = true
	}
	links, err := store.GetBookAuthors(b.ID)
	if err != nil {
		return nil, fmt.Errorf("read authors of %s: %w", b.ID, err)
	}
	for _, l := range links {
		ids[l.AuthorID] = true
	}
	return ids, nil
}

// relinkAuthorCompatible: the series has no author, the book has none, or
// the series author is one of the book's.
func relinkAuthorCompatible(s database.Series, bookAuthors map[int]bool) bool {
	return s.AuthorID == nil || len(bookAuthors) == 0 || bookAuthors[*s.AuthorID]
}

// seriesHistoryReader reads one field's full change history. The store keys
// history by book, FIELD and time, so the whole-book read
// (GetBookChangeHistory) is ordered field by field, not newest first across
// fields, and its limit is applied after that: neither can find the newest
// series row. Asserted on the ops store rather than widening OpsStore.
type seriesHistoryReader interface {
	GetMetadataChangeHistory(bookID string, field string, limit int) ([]database.MetadataChangeRecord, error)
}

// The prod ops store (Server.OpsStore) is a database.Store; this proves it
// answers the per-field history read, so the assertion never misses in prod.
var _ seriesHistoryReader = database.Store(nil)

// relinkSeriesHistoryFields are the history fields a series change is
// recorded under: "series" (edits and metadata applies, display names with
// refs), "series_id" (the repairs Writer) and "series_name" (overrides).
var relinkSeriesHistoryFields = []string{database.HistoryFieldSeries, "series_id", database.FieldKeySeriesName}

// relinkHistoryAll reads a field's whole history: a series clear may be
// older than any fixed window.
const relinkHistoryAll = 1 << 30

// relinkHistoryVerdict is what the series history says about a stale book.
type relinkHistoryVerdict struct {
	// cleared: the newest series row set the series to empty (or null).
	cleared bool
	// mismatch: the newest series row names a different series.
	mismatch bool
	// source / changeType of the deciding row; evidence describes it; key
	// goes into the fingerprint.
	source, changeType, evidence, key string
	// unreadable says why the history could not be read in full.
	unreadable string
}

// relinkSeriesHistory finds the newest series-related history row by
// ChangedAt across series, series_id and series_name (all rows of each
// field), and judges it. A row this fixer wrote counts like any other: a user
// clear after the fixer's relink must hold the row. Rows sharing the newest
// time are judged together, a clear among them winning.
func relinkSeriesHistory(store OpsStore, bookID string, emb database.Series, target *database.Series) (relinkHistoryVerdict, error) {
	var v relinkHistoryVerdict
	hist, ok := store.(seriesHistoryReader)
	if !ok {
		v.unreadable = "the store cannot read per-field change history, so a series clear cannot be ruled out"
		v.key = "unreadable"
		return v, nil
	}
	var newest []database.MetadataChangeRecord
	for _, field := range relinkSeriesHistoryFields {
		rows, err := hist.GetMetadataChangeHistory(bookID, field, relinkHistoryAll)
		if err != nil {
			return v, fmt.Errorf("read %s history of %s: %w", field, bookID, err)
		}
		for _, h := range rows {
			// An override row with no value means "override removed", not
			// "series cleared": it says nothing about the series itself.
			if h.ChangeType == "override" && (h.NewValue == nil || strings.TrimSpace(*h.NewValue) == "null") {
				continue
			}
			switch {
			case len(newest) == 0 || h.ChangedAt.After(newest[0].ChangedAt):
				newest = []database.MetadataChangeRecord{h}
			case h.ChangedAt.Equal(newest[0].ChangedAt):
				newest = append(newest, h)
			}
		}
	}
	if len(newest) == 0 {
		v.key = "none"
		return v, nil
	}
	sort.Slice(newest, func(i, j int) bool { return newest[i].Field < newest[j].Field })
	describe := func(h database.MetadataChangeRecord, what string) string {
		return fmt.Sprintf("history: %s %s by source %q at %s (change type %q)", h.Field, what, h.Source,
			h.ChangedAt.UTC().Format(time.RFC3339Nano), h.ChangeType)
	}
	var agree *database.MetadataChangeRecord
	for i := range newest {
		h := newest[i]
		switch relinkHistoryNames(h, emb, target) {
		case relinkHistCleared:
			v.cleared, v.source, v.changeType = true, h.Source, h.ChangeType
			v.evidence = describe(h, "set to empty")
			v.key = fmt.Sprintf("cleared/%s/%s/%d", h.Field, h.Source, h.ChangedAt.UnixNano())
			return v, nil
		case relinkHistOther:
			if !v.mismatch {
				v.mismatch, v.source = true, h.Source
				v.evidence = describe(h, "set to another series ("+relinkHistValue(h)+")")
				v.key = fmt.Sprintf("other/%s/%s/%d", h.Field, h.Source, h.ChangedAt.UnixNano())
			}
		default:
			if agree == nil {
				agree = &newest[i]
			}
		}
	}
	if v.mismatch {
		return v, nil
	}
	v.source = agree.Source
	v.evidence = describe(*agree, "last set to this series ("+relinkHistValue(*agree)+")")
	v.key = fmt.Sprintf("agree/%s/%s/%d", agree.Field, agree.Source, agree.ChangedAt.UnixNano())
	return v, nil
}

// What one history row says about the series.
const (
	relinkHistAgrees = iota
	relinkHistCleared
	relinkHistOther
)

// relinkHistoryNames judges one series history row against the stored
// object (and the target row, by name, when it exists). The ref's series id
// decides when the row has one; a Writer series_id row carries the id as its
// value; any other row is compared by name. An absent, null or empty value
// is a clear.
func relinkHistoryNames(h database.MetadataChangeRecord, emb database.Series, target *database.Series) int {
	if h.NewRef != nil {
		if h.NewRef.SeriesID == nil {
			return relinkHistCleared
		}
		if *h.NewRef.SeriesID == emb.ID {
			return relinkHistAgrees
		}
		return relinkHistOther
	}
	if h.NewValue == nil || strings.TrimSpace(*h.NewValue) == "null" {
		return relinkHistCleared
	}
	val := decodeJSONString(*h.NewValue)
	if val == "" {
		return relinkHistCleared
	}
	if h.Field == "series_id" {
		if id, err := strconv.Atoi(val); err == nil {
			if id == emb.ID {
				return relinkHistAgrees
			}
			return relinkHistOther
		}
	}
	n := normSeriesName(val)
	if n == normSeriesName(emb.Name) || (target != nil && n == normSeriesName(target.Name)) {
		return relinkHistAgrees
	}
	return relinkHistOther
}

func relinkHistValue(h database.MetadataChangeRecord) string {
	if h.NewRef != nil && h.NewRef.SeriesID != nil {
		return "series id " + strconv.Itoa(*h.NewRef.SeriesID)
	}
	if h.NewValue == nil {
		return "no value"
	}
	return *h.NewValue
}

// relinkSeriesLockWhy reports why a series_name lock forbids the relink (""
// when none does). The lock set comes from database.LoadFieldLocks, the one
// reader of both lock stores (field states and the legacy preference blob).
// A lock is honoured as agreeing only when its override value names the
// stored object's series or the target's; a lock with no readable value
// (the legacy blob carries none) holds the row.
func relinkSeriesLockWhy(store OpsStore, bookID string, emb, target database.Series) (string, error) {
	locks, err := database.LoadFieldLocks(store, bookID)
	if err != nil {
		return "", fmt.Errorf("read field locks of %s: %w", bookID, err)
	}
	if !locks.Locked(database.FieldKeySeriesName) {
		return "", nil
	}
	states, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return "", fmt.Errorf("read field states of %s: %w", bookID, err)
	}
	for _, st := range states {
		if st.Field != database.FieldKeySeriesName || st.OverrideValue == nil {
			continue
		}
		v := decodeJSONString(*st.OverrideValue)
		n := normSeriesName(v)
		switch {
		case n == "":
			return "a user override set the series to empty", nil
		case n == normSeriesName(emb.Name) || n == normSeriesName(target.Name):
			return "", nil // the lock names this series: relinking agrees with it
		default:
			return fmt.Sprintf("a user override names series %q, not %q", v, target.Name), nil
		}
	}
	return "the series_name field is user-locked", nil
}

// decodeJSONString decodes a JSON-encoded string value; anything that is not
// a JSON string is returned trimmed as is.
func decodeJSONString(raw string) string {
	var s string
	if err := json.Unmarshal([]byte(raw), &s); err == nil {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(raw)
}

func relinkFingerprintExtra(b *database.Book, emb database.Series, class string, target, candidate *database.Series) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "sid=%v|emb=%d/%s/%s|class=%s", b.SeriesID == nil, emb.ID, emb.Name, intPtrStr(emb.AuthorID), class)
	for _, s := range []*database.Series{target, candidate} {
		if s != nil {
			fmt.Fprintf(&sb, "|s=%d/%s/%s", s.ID, s.Name, intPtrStr(s.AuthorID))
		} else {
			sb.WriteString("|s=-")
		}
	}
	return sb.String()
}

func intPtrStr(p *int) string {
	if p == nil {
		return ""
	}
	return strconv.Itoa(*p)
}

func relinkFingerprint(r repairs.Row, extra string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n%s\n%s\n%s\n", r.RowID, r.Class, r.Skipped, r.Proposed["series_id"], extra)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])[:32]
}

func sameSeries(a, b database.Series) bool {
	return a.ID == b.ID && a.Name == b.Name && intPtrStr(a.AuthorID) == intPtrStr(b.AuthorID)
}

// Apply sets SeriesID, compare-and-set: the target series row must still
// exist, and inside the book write the row must still have SeriesID == nil
// and the planned Series object.
func (f *relinkSeriesFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*relinkDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", relinkSeriesFixerID, fresh.RowID)
	}
	store := f.p.deps.OpsStore()
	if store == nil {
		return fmt.Errorf("database not initialized")
	}
	s, err := store.GetSeriesByID(d.seriesID)
	if err != nil {
		return fmt.Errorf("read series %d: %w", d.seriesID, err)
	}
	if s == nil {
		return fmt.Errorf("%w: series %d no longer exists", repairs.ErrChangedSincePlan, d.seriesID)
	}
	_, err = w.Modify(d.bookID, func(row *database.Book) error {
		if row.SeriesID != nil {
			return fmt.Errorf("%w: book %s has series id %d now", repairs.ErrChangedSincePlan, d.bookID, *row.SeriesID)
		}
		if row.Series == nil || !sameSeries(*row.Series, d.embedded) {
			return fmt.Errorf("%w: book %s's series object changed", repairs.ErrChangedSincePlan, d.bookID)
		}
		// The fresh series row must still agree with the object, by name and
		// author, as Plan required.
		if normSeriesName(s.Name) != normSeriesName(d.embedded.Name) {
			return fmt.Errorf("%w: series %d is named %q now, the stored object %q", repairs.ErrChangedSincePlan, s.ID, s.Name, d.embedded.Name)
		}
		authors, aerr := relinkBookAuthorIDs(store, row)
		if aerr != nil {
			return aerr
		}
		if !relinkAuthorCompatible(*s, authors) {
			return fmt.Errorf("%w: series %d belongs to author %s now, not one of the book's", repairs.ErrChangedSincePlan, s.ID, intPtrStr(s.AuthorID))
		}
		why, lerr := relinkSeriesLockWhy(store, d.bookID, d.embedded, *s)
		if lerr != nil {
			return lerr
		}
		if why != "" {
			return fmt.Errorf("%w: %s", repairs.ErrChangedSincePlan, why)
		}
		hv, herr := relinkSeriesHistory(store, d.bookID, d.embedded, s)
		if herr != nil {
			return herr
		}
		if hv.unreadable != "" || hv.cleared || hv.mismatch {
			return fmt.Errorf("%w: %s%s", repairs.ErrChangedSincePlan, hv.unreadable, hv.evidence)
		}
		id := d.seriesID
		row.SeriesID = &id
		return nil
	})
	return err
}
