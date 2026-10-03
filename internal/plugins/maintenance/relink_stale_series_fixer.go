// file: internal/plugins/maintenance/relink_stale_series_fixer.go
// version: 1.0.0
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

// Row classes of the relink fixer.
const (
	// relinkClassRelink: the series row the embedded object names (by id)
	// still exists; apply points SeriesID back at it.
	relinkClassRelink = "relink"
	// relinkClassNameMatch: that row is gone, but exactly one series row has
	// the same normalized name and a compatible author. Held for the owner.
	relinkClassNameMatch = "name-match"
	// relinkClassOrphan: no series row by id and no unique name match. The
	// embedded object is the only record of the series. Held.
	relinkClassOrphan = "orphan"
)

// Skip kinds of the relink fixer besides the framework's and junkSkip*.
const (
	// relinkSkipUserCleared: the book's series was cleared or overridden by a
	// user (a series_name field lock, or a history row that set a series
	// field to empty). Relinking would undo that edit.
	relinkSkipUserCleared = "skipped_user_cleared_series"
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

	// seriesMu guards a short-lived series index for Replan, which runs once
	// per applied row. Apply re-reads the target series row itself, so a
	// stale cache can never relink to a deleted series.
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
		r, err := f.evaluate(store, idx, b)
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

func relinkErrorRow(bookID, title string, err error) repairs.Row {
	r := repairs.Row{RowID: bookID, BookIDs: []string{bookID}, Title: title,
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
	return f.evaluate(store, idx, b)
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
func (f *relinkSeriesFixer) evaluate(store OpsStore, idx *relinkSeriesIndex, b *database.Book) (repairs.Row, error) {
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

	// Classify.
	var target *database.Series
	var candidate *database.Series
	if s, ok := idx.byID[emb.ID]; ok && emb.ID > 0 {
		r.Class = relinkClassRelink
		target = &s
		r.Evidence = append(r.Evidence, fmt.Sprintf("series row %d exists: %q", s.ID, s.Name))
		if normSeriesName(s.Name) != normSeriesName(emb.Name) {
			r.Evidence = append(r.Evidence, fmt.Sprintf("the series row's name %q differs from the stored object's %q (renamed since?)", s.Name, emb.Name))
		}
	} else {
		r.Evidence = append(r.Evidence, fmt.Sprintf("no series row has id %d", emb.ID))
		authors, err := relinkBookAuthorIDs(store, b)
		if err != nil {
			return repairs.Row{}, err
		}
		var matches []database.Series
		for _, s := range idx.byName[normSeriesName(emb.Name)] {
			if relinkAuthorCompatible(s, authors) {
				matches = append(matches, s)
			}
		}
		sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
		switch len(matches) {
		case 1:
			r.Class = relinkClassNameMatch
			candidate = &matches[0]
			r.Proposed = map[string]string{"candidate_series_id": strconv.Itoa(candidate.ID)}
			r.Evidence = append(r.Evidence, fmt.Sprintf("candidate: series %d %q is the only same-name series with a compatible author", candidate.ID, candidate.Name))
		default:
			r.Class = relinkClassOrphan
			if len(matches) > 1 {
				ids := make([]string, len(matches))
				for i, m := range matches {
					ids[i] = strconv.Itoa(m.ID)
				}
				r.Evidence = append(r.Evidence, fmt.Sprintf("%d same-name series with a compatible author (%s); none chosen", len(matches), strings.Join(ids, ", ")))
			} else {
				r.Evidence = append(r.Evidence, "no series row has that name with a compatible author")
			}
		}
	}

	extra := relinkFingerprintExtra(b, emb, r.Class, target, candidate)

	// Doctor Who / Big Finish / Torchwood by series name. The framework
	// guard reads the series name through SeriesID, which is nil on every
	// row here, so it never sees these names.
	for _, name := range []string{emb.Name, nameOf(target), nameOf(candidate)} {
		if name != "" && applygate.IsOwnerManualOnly("", name) {
			r.Skipped = repairs.SkipOwnerManual
			r.SkipReason = fmt.Sprintf("series %q is Doctor Who / Big Finish / Torchwood; owner applies these by hand", name)
			r.Reason = r.SkipReason
			r.Fingerprint = relinkFingerprint(r, extra)
			return r, nil
		}
	}

	switch r.Class {
	case relinkClassNameMatch:
		r.Skipped, r.SkipReason = junkSkipNeedsManual,
			"the series row the object named is gone; one same-name series exists, but which series this book belongs to is the owner's call"
		r.Reason = r.SkipReason
		r.Fingerprint = relinkFingerprint(r, extra)
		return r, nil
	case relinkClassOrphan:
		r.Skipped, r.SkipReason = junkSkipNeedsManual,
			"no series row matches the stored object; it is the only record of this series"
		r.Reason = r.SkipReason
		r.Fingerprint = relinkFingerprint(r, extra)
		return r, nil
	}

	// relink: refuse a book whose series a user cleared or overrode.
	why, err := relinkUserCleared(store, b.ID, emb, *target)
	if err != nil {
		return repairs.Row{}, err
	}
	if why != "" {
		r.Skipped, r.SkipReason = relinkSkipUserCleared, why
		r.Reason = why
		r.Fingerprint = relinkFingerprint(r, extra+"|cleared:"+why)
		return r, nil
	}
	r.Proposed = map[string]string{"series_id": strconv.Itoa(target.ID), "series_name": target.Name}
	r.Reason = "the series id was lost but the stored series object still names an existing series; " +
		"review: a user's series clear made before field locks were written leaves this same state"
	r.Detail = &relinkDecision{bookID: b.ID, seriesID: target.ID, embedded: emb}
	r.Fingerprint = relinkFingerprint(r, extra)
	return r, nil
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

// relinkSeriesFields are the history fields a series edit is recorded under.
var relinkSeriesFields = map[string]bool{"series_id": true, "series": true, database.FieldKeySeriesName: true}

// relinkHistoryWindow bounds the history read per book.
const relinkHistoryWindow = 200

// relinkUserCleared reports why the book's series must not be relinked
// because a user cleared or replaced it ("" when nothing says so):
//   - a series_name field lock or override whose value is empty or names
//     neither the stored object nor the target series;
//   - the newest history row for a series field set it to empty, written by
//     anyone but this fixer.
//
// A clear made through the old top-level edit path wrote neither, so it is
// not detectable here; that is why every relink row is RiskReview.
func relinkUserCleared(store OpsStore, bookID string, emb, target database.Series) (string, error) {
	states, err := store.GetMetadataFieldStates(bookID)
	if err != nil {
		return "", fmt.Errorf("read field states of %s: %w", bookID, err)
	}
	for _, st := range states {
		if st.Field != database.FieldKeySeriesName || !st.HasUserOverride() {
			continue
		}
		if st.OverrideValue == nil {
			return "the series_name field is user-locked", nil
		}
		v := decodeJSONString(*st.OverrideValue)
		n := normSeriesName(v)
		if n == "" {
			return "a user override set the series to empty", nil
		}
		if n != normSeriesName(emb.Name) && n != normSeriesName(target.Name) {
			return fmt.Sprintf("a user override names series %q, not %q", v, target.Name), nil
		}
	}
	hist, ok := store.(bookHistoryReader)
	if !ok {
		return "", nil
	}
	rows, err := hist.GetBookChangeHistory(bookID, relinkHistoryWindow)
	if err != nil {
		return "", fmt.Errorf("read change history of %s: %w", bookID, err)
	}
	for _, h := range rows {
		if !relinkSeriesFields[h.Field] {
			continue
		}
		// The newest series row decides.
		if h.Source != relinkSeriesFixerID && h.NewValue != nil && decodeJSONString(*h.NewValue) == "" &&
			strings.TrimSpace(*h.NewValue) != "null" {
			return fmt.Sprintf("history: %s set %s to empty (%s)", h.Source, h.Field, h.ChangedAt.UTC().Format(time.RFC3339)), nil
		}
		break
	}
	return "", nil
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
		id := d.seriesID
		row.SeriesID = &id
		return nil
	})
	return err
}
