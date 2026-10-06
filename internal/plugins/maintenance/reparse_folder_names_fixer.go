// file: internal/plugins/maintenance/reparse_folder_names_fixer.go
// version: 1.0.0
// guid: ac1c25d1-3594-43f6-9254-90bf2ab0b284
// last-edited: 2026-10-05

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/authorjunk"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// reparseFolderNamesFixerID is the Repairs-lane id of the folder re-parse
// fixer.
const reparseFolderNamesFixerID = "maintenance.reparse-folder-names"

// Row classes: which field a row would rewrite.
const (
	reparseClassTitle  = "title"
	reparseClassAuthor = "author"
	reparseClassSeries = "series"
)

// reparseSkipAuthor marks an author row: listed for review, never written
// here (an author rewrite moves the credit junction; the author fixers own
// that write class).
const reparseSkipAuthor = "skipped_author_review_only"

// reparseFolderNamesFixer lists EXISTING books whose title, author or series
// the folder parse wrote and the shared title parser (metadata.ParseBookName,
// 2026-10-05) now reads differently ("2018 - Blueshift" was titled "2018";
// "Discworld 24 - The Fifth Elephant - 01" was "Discworld 24"). The scanner
// no longer rewrites existing rows with the new parse (owner, 2026-10-05:
// "search + new imports only"), so this is how they change: one row per
// field, applied only by the ids the owner approves.
//
// A field is a row only when the stored value IS what the old folder parse
// produced (metadata.LegacyFolderParse, a frozen copy) and the new parse
// (metadata.ExtractMetadataFromFolder) produces a different, non-empty one:
// a value from a tag, an apply or an edit is never listed. A field with a
// user override or a repair lock is held. iTunes-owned and Doctor Who / Big
// Finish / Torchwood rows are skipped by the framework's guards.
//
// Apply writes titles (writeTitleOnly: journaled, and locked so a rescan
// cannot restore the old value) and series links to an EXISTING series row
// of that name (journaled; position with it). Author rows are review-only.
// Undo is the apply operation's revert.
type reparseFolderNamesFixer struct{ p *Plugin }

func newReparseFolderNamesFixer(p *Plugin) *reparseFolderNamesFixer {
	return &reparseFolderNamesFixer{p: p}
}

var _ repairs.Fixer = (*reparseFolderNamesFixer)(nil)

func (f *reparseFolderNamesFixer) ID() string { return reparseFolderNamesFixerID }
func (f *reparseFolderNamesFixer) Title() string {
	return "Folder names re-read by the new title parser"
}
func (f *reparseFolderNamesFixer) Description() string {
	return "Existing books whose title, author or series came from the old folder parse and that the new title " +
		"parser reads differently (a release year, an author or a track number no longer kept in the title; a " +
		"series slot read). One row per field, before and after. Never a value from a tag or an edit, never a " +
		"locked field. Applies titles and links to existing series rows only; author changes are listed for " +
		"review and not written. Undo with the apply operation's revert."
}

// reparseStored is what a book holds now, by name.
type reparseStored struct {
	Title, Author, Series string
	Position              int
}

// reparseChange is one field the new parse would rewrite.
type reparseChange struct {
	Class    string
	From, To string
	// ToPosition is the series position the new parse reads (series rows).
	ToPosition int
}

// reparseFolderDir is the folder the scanner parses for a book: the book's
// path when it is a directory (a multi-file book), else the file's folder.
func reparseFolderDir(path string) string {
	if ext := filepath.Ext(path); ext != "" && len(ext) <= 5 && !strings.Contains(ext, " ") {
		return filepath.Dir(path)
	}
	return path
}

// reparseChanges compares the stored values with the old and new folder
// parses of dir and returns each field the old parse wrote and the new one
// reads differently.
func reparseChanges(stored reparseStored, legacy, fresh *metadata.FolderMetadata) []reparseChange {
	if legacy == nil || fresh == nil {
		return nil
	}
	same := func(a, b string) bool { return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b)) }
	first := func(xs []string) string {
		if len(xs) == 0 {
			return ""
		}
		return xs[0]
	}
	var out []reparseChange
	// A proposed title that names the book's own author is no title: a
	// folder "1966 - Roger Zelazny - ..." with no author folder above keeps
	// the author as its first field.
	if legacy.Title != "" && same(stored.Title, legacy.Title) && fresh.Title != "" && !same(fresh.Title, stored.Title) &&
		(stored.Author == "" || !authorjunk.SamePersonName(fresh.Title, stored.Author)) {
		out = append(out, reparseChange{Class: reparseClassTitle, From: stored.Title, To: fresh.Title})
	}
	if la, na := first(legacy.Authors), first(fresh.Authors); la != "" && same(stored.Author, la) && na != "" && !same(na, stored.Author) {
		out = append(out, reparseChange{Class: reparseClassAuthor, From: stored.Author, To: na})
	}
	if legacy.SeriesName != "" && same(stored.Series, legacy.SeriesName) && fresh.SeriesName != "" &&
		(!same(fresh.SeriesName, stored.Series) || (fresh.SeriesPosition > 0 && fresh.SeriesPosition != stored.Position)) {
		out = append(out, reparseChange{Class: reparseClassSeries, From: stored.Series, To: fresh.SeriesName, ToPosition: fresh.SeriesPosition})
	}
	return out
}

// reparseDecision is what Apply writes for one row.
type reparseDecision struct {
	bookID   string
	change   reparseChange
	seriesID int
	fromSID  *int
	fromSeq  *int
}

// Plan lists every field row over every live book.
func (f *reparseFolderNamesFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	var books []database.BookCore
	for i := range all {
		if !all[i].IsSoftDeleted() && strings.TrimSpace(all[i].FilePath) != "" {
			books = append(books, all[i])
		}
	}
	perBook := make([][]repairs.Row, len(books))
	names := newReparseNames(store)
	var done atomic.Int64
	// Each worker writes only perBook[i] for its own i; names is locked.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(books)), func(_ context.Context, i int) error {
		defer done.Add(1)
		rows, err := f.evaluate(books[i], names)
		if err != nil {
			r := repairs.Row{RowID: books[i].ID, BookIDs: []string{books[i].ID}, Title: books[i].Title,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = reparseFingerprint(r)
			rows = []repairs.Row{r}
		}
		perBook[i] = rows
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Folder re-parse %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var rows []repairs.Row
	for _, rs := range perBook {
		rows = append(rows, rs...)
	}
	return rows, nil
}

// Replan re-reads the book and returns the row with the same id as a plan
// made now would, or a skipped row when the field no longer qualifies.
func (f *reparseFolderNamesFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	bookID, class, _ := strings.Cut(planned.RowID, "|")
	b, err := store.GetBookByID(bookID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read book %s: %w", bookID, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: planned.RowID, BookIDs: []string{bookID}, Skipped: junkSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Fingerprint = reparseFingerprint(r)
		return r, nil
	}
	rows, err := f.evaluate(b.Core(), newReparseNames(store))
	if err != nil {
		return repairs.Row{}, err
	}
	for _, r := range rows {
		if r.RowID == planned.RowID {
			return r, nil
		}
	}
	r := repairs.Row{RowID: planned.RowID, BookIDs: []string{bookID}, Title: b.Title, Class: class,
		Skipped: junkSkipNotJunk, SkipReason: "the new folder parse no longer changes this field", Risk: repairs.RiskLow}
	r.Reason = r.SkipReason
	r.Fingerprint = reparseFingerprint(r)
	return r, nil
}

// evaluate returns one row per field the new parse would rewrite on b.
func (f *reparseFolderNamesFixer) evaluate(b database.BookCore, names *reparseNames) ([]repairs.Row, error) {
	store := f.p.deps.OpsStore()
	stored := reparseStored{Title: b.Title, Author: names.author(b.AuthorID), Series: names.series(b.SeriesID)}
	if b.SeriesSequence != nil {
		stored.Position = *b.SeriesSequence
	}
	dir := reparseFolderDir(b.FilePath)
	fresh, err := metadata.ExtractMetadataFromFolder(dir)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dir, err)
	}
	changes := reparseChanges(stored, metadata.LegacyFolderParse(dir), fresh)
	if len(changes) == 0 {
		return nil, nil
	}
	locked, err := database.LockedUserFields(store, b.ID)
	if err != nil {
		return nil, fmt.Errorf("read field locks of %s: %w", b.ID, err)
	}
	rows := make([]repairs.Row, 0, len(changes))
	for _, c := range changes {
		r := repairs.Row{RowID: b.ID + "|" + c.Class, BookIDs: []string{b.ID}, Title: b.Title, Author: stored.Author,
			Class: c.Class, Risk: repairs.RiskLow,
			Current:  map[string]string{c.Class: c.From},
			Proposed: map[string]string{c.Class: c.To},
			Evidence: []string{"folder " + dir, "the stored value is what the old folder parse wrote"},
			Reason:   fmt.Sprintf("the folder parse now reads %s %q instead of %q", c.Class, c.To, c.From)}
		if c.Class == reparseClassSeries && c.ToPosition > 0 {
			r.Current["series_position"] = strconv.Itoa(stored.Position)
			r.Proposed["series_position"] = strconv.Itoa(c.ToPosition)
		}
		key := map[string]string{reparseClassTitle: database.FieldKeyTitle, reparseClassAuthor: database.FieldKeyAuthorName,
			reparseClassSeries: database.FieldKeySeriesName}[c.Class]
		switch {
		case locked[key] || (c.Class == reparseClassSeries && locked[database.FieldKeySeriesPosition]):
			r.Skipped, r.SkipReason = junkSkipUserLocked, "the "+c.Class+" is locked; it is never rewritten"
		case c.Class == reparseClassAuthor:
			r.Skipped = reparseSkipAuthor
			r.SkipReason = "author changes are listed for review only: an author rewrite moves the book's credits, which the author fixers do"
			r.Risk = repairs.RiskReview
		case c.Class == reparseClassSeries:
			s, serr := store.GetSeriesByName(c.To, b.AuthorID)
			if serr != nil {
				return nil, fmt.Errorf("read series %q: %w", c.To, serr)
			}
			if s == nil {
				r.Skipped, r.SkipReason = junkSkipNeedsManual, fmt.Sprintf("no series named %q exists to link; none is created here", c.To)
			} else {
				r.Detail = &reparseDecision{bookID: b.ID, change: c, seriesID: s.ID, fromSID: b.SeriesID, fromSeq: b.SeriesSequence}
				r.Evidence = append(r.Evidence, fmt.Sprintf("series row %d", s.ID))
			}
		default:
			r.Detail = &reparseDecision{bookID: b.ID, change: c}
		}
		if r.Skipped != "" {
			r.Reason = r.SkipReason
		}
		r.Fingerprint = reparseFingerprint(r)
		rows = append(rows, r)
	}
	return rows, nil
}

// Apply writes one approved row.
func (f *reparseFolderNamesFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*reparseDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", reparseFolderNamesFixerID, fresh.RowID)
	}
	store := f.p.deps.OpsStore()
	switch d.change.Class {
	case reparseClassTitle:
		return writeTitleOnly(w, store, d.bookID, d.change.From, d.change.To)
	case reparseClassSeries:
		if err := w.RecordChange(d.bookID, repairs.UndoEntry{ChangeType: "metadata_update", Field: "series_id",
			Old: intPtrString(d.fromSID), New: strconv.Itoa(d.seriesID)}); err != nil {
			return err
		}
		_, err := w.Modify(d.bookID, func(b *database.Book) error {
			if !sameIntPtr(b.SeriesID, d.fromSID) || !sameIntPtr(b.SeriesSequence, d.fromSeq) {
				return fmt.Errorf("%w: book %s series changed", repairs.ErrChangedSincePlan, d.bookID)
			}
			locked, lerr := database.LockedUserFields(store, d.bookID)
			if lerr != nil {
				return fmt.Errorf("read field locks of %s: %w", d.bookID, lerr)
			}
			if locked[database.FieldKeySeriesName] || locked[database.FieldKeySeriesPosition] {
				return fmt.Errorf("%w: the series is now locked", repairs.ErrChangedSincePlan)
			}
			id := d.seriesID
			b.SeriesID = &id
			if d.change.ToPosition > 0 {
				pos := d.change.ToPosition
				b.SeriesSequence = &pos
			}
			return nil
		})
		return err
	}
	return fmt.Errorf("%s: row %s: %s rows are not written", reparseFolderNamesFixerID, fresh.RowID, d.change.Class)
}

// reparseFingerprint hashes a row's decision inputs and outputs.
func reparseFingerprint(r repairs.Row) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n%s\n%s\n", r.RowID, r.Skipped, r.Class, strings.Join(r.Evidence, "|"))
	for _, m := range []map[string]string{r.Current, r.Proposed} {
		for _, k := range []string{reparseClassTitle, reparseClassAuthor, reparseClassSeries, "series_position"} {
			fmt.Fprintf(&sb, "%s=%s\n", k, m[k])
		}
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])[:32]
}

// reparseNames caches author and series names by id for one plan.
type reparseNames struct {
	store   OpsStore
	mu      sync.Mutex
	authors map[int]string
	seriesN map[int]string
}

func newReparseNames(store OpsStore) *reparseNames {
	return &reparseNames{store: store, authors: map[int]string{}, seriesN: map[int]string{}}
}

func (n *reparseNames) author(id *int) string {
	if id == nil {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if v, ok := n.authors[*id]; ok {
		return v
	}
	name := ""
	if a, err := n.store.GetAuthorByID(*id); err == nil && a != nil {
		name = a.Name
	}
	n.authors[*id] = name
	return name
}

func (n *reparseNames) series(id *int) string {
	if id == nil {
		return ""
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if v, ok := n.seriesN[*id]; ok {
		return v
	}
	name := ""
	if s, err := n.store.GetSeriesByID(*id); err == nil && s != nil {
		name = s.Name
	}
	n.seriesN[*id] = name
	return name
}
