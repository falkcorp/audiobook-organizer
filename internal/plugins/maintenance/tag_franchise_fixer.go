// file: internal/plugins/maintenance/tag_franchise_fixer.go
// version: 1.0.0
// guid: 8c8a3b12-5d98-45d2-9751-c25bf96df34e
// last-edited: 2026-10-04

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
)

// tagFranchiseFixerID is the Repairs-lane id of the franchise tagger.
const tagFranchiseFixerID = "maintenance.tag-franchise"

// Row classes and skip kinds of the franchise tagger.
const (
	// tfClassStrong: at least one signal on the book itself names the
	// franchise (its path, title, series, a credit, a transcribed field, a
	// file path, a Big Finish download name).
	tfClassStrong = "strong"
	// tfClassWeak: only weak evidence -- the book's folder holds files that
	// name the franchise, a file of the book also sits in a franchise
	// folder, a title only the broad rule names, or a "Missy" credit. A
	// person reads these rows; they are RiskReview, never RiskLow.
	tfClassWeak = "weak-only"

	tfSkipTagged      = "already_tagged"
	tfSkipConflicting = "conflicting_franchise_tag"
	tfSkipNoSignal    = "no_signal"
	tfSkipGone        = "gone"
)

// tfFolderMaxFiles bounds the folder-contents signal: a folder holding more
// rows than this is a dump (a whole library root, "Unknown Author"), whose
// other files say nothing about one book. The census used the same bound.
const tfFolderMaxFiles = 150

// tfTwinMinSize: a same-name file counts as a twin only from this size up,
// so a shared intro jingle or a cover image is not evidence.
const tfTwinMinSize = 1 << 20

// tagFranchiseFixer proposes franchise tags (franchise:doctor-who,
// franchise:big-finish, franchise:torchwood, plus range:<range>) for every
// book the shared matcher (internal/franchise) finds, so the owner-manual
// guards still recognise the book after its title or path changes, and the
// Doctor Who cleanup can select its books by tag.
//
// It writes book_tag rows only (repairs.BookTagsOnly): no book field, no
// book_file row, no file and nothing the iTunes library reads, so iTunes
// books are tagged too and the framework guard does not skip its rows.
// Every tag is journaled (Writer.AddBookTag) and the apply operation's
// revert removes it again.
type tagFranchiseFixer struct{ p *Plugin }

func newTagFranchiseFixer(p *Plugin) *tagFranchiseFixer { return &tagFranchiseFixer{p: p} }

var (
	_ repairs.Fixer        = (*tagFranchiseFixer)(nil)
	_ repairs.BookTagsOnly = (*tagFranchiseFixer)(nil)
)

func (f *tagFranchiseFixer) ID() string { return tagFranchiseFixerID }
func (f *tagFranchiseFixer) Title() string {
	return "Franchise tags (Doctor Who / Big Finish / Torchwood)"
}
func (f *tagFranchiseFixer) Description() string {
	return "Tags every Doctor Who, Big Finish and Torchwood book the shared franchise matcher finds with " +
		"franchise:<name> and range:<range> (source franchise-matcher), showing the signals that matched. " +
		"Once tagged, the owner-manual guards hold the book even if its title or path changes. Rows found " +
		"only by weak evidence (the book's folder holds franchise files, a same-name file sits in a franchise " +
		"folder, a title only the broad rule names, a \"Missy\" credit) are review rows. Writes tags only -- " +
		"never a file, a book field or the iTunes library -- so iTunes books are tagged too. A book that already " +
		"carries a different franchise tag is skipped. Undo with the apply operation's revert."
}

// BookTagsOnly implements repairs.BookTagsOnly: the apply adds book_tag rows
// and nothing else.
func (f *tagFranchiseFixer) BookTagsOnly() bool { return true }

// tfState is what Replan needs from plan time: the weak, library-wide
// signals it cannot rebuild for one book (the folder and twin indexes span
// the whole library).
type tfState struct {
	Weak []franchise.Signal `json:"weak,omitempty"`
}

// tfDecision is carried from Replan to Apply.
type tfDecision struct {
	bookID string
	tags   []string
}

// tfReader is what the tagger reads.
type tfReader interface {
	GetAllBooksCore(limit, offset int) ([]database.BookCore, error)
	GetAllBookFilesCore() ([]database.BookFileCore, error)
	GetAllSeries() ([]database.Series, error)
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	database.BookAuthorReader
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetNarratorByID(id int) (*database.Narrator, error)
}

// tfIndex is the library-wide evidence: which folders hold files whose NAME
// names the franchise, and which (name, size) pairs are franchise files.
type tfIndex struct {
	folder map[string]franchise.Signal
	twin   map[string]franchise.Signal
	// folderFiles counts the file rows per folder (tfFolderMaxFiles).
	folderFiles map[string]int
}

func tfTwinKey(path string, size int64) string {
	return strings.ToLower(filepath.Base(path)) + "\x00" + fmt.Sprint(size)
}

// buildIndex scans every book_file row once, in parallel shards (each worker
// builds its own maps; they are merged after), for the weak signals.
func (f *tagFranchiseFixer) buildIndex(ctx context.Context, files []database.BookFileCore) tfIndex {
	ix := tfIndex{folder: map[string]franchise.Signal{}, twin: map[string]franchise.Signal{}, folderFiles: map[string]int{}}
	for i := range files {
		ix.folderFiles[filepath.Dir(files[i].FilePath)]++
	}
	workers := max(runtime.NumCPU(), 1)
	type part struct{ folder, twin map[string]franchise.Signal }
	parts := make([]part, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			pt := part{folder: map[string]franchise.Signal{}, twin: map[string]franchise.Signal{}}
			for i := w; i < len(files); i += workers {
				if i%4096 == 0 && ctx.Err() != nil {
					return
				}
				p := files[i].FilePath
				if p == "" {
					continue
				}
				if h, ok := franchise.Match(p); ok {
					if files[i].FileSize >= tfTwinMinSize {
						pt.twin[tfTwinKey(p, files[i].FileSize)] = franchise.Signal{Field: franchise.FieldTwinFile, Value: p, Hit: h, Weak: true}
					}
					// A file whose own name (not its folder) names the
					// franchise marks its folder.
					if hb, ok := franchise.Match(filepath.Base(p)); ok {
						pt.folder[filepath.Dir(p)] = franchise.Signal{Field: franchise.FieldFolderContents, Value: p, Hit: hb, Weak: true}
					}
				}
			}
			parts[w] = pt
		}(w)
	}
	wg.Wait()
	for _, pt := range parts {
		for k, v := range pt.folder {
			if cur, ok := ix.folder[k]; !ok || v.Value < cur.Value {
				ix.folder[k] = v
			}
		}
		for k, v := range pt.twin {
			if cur, ok := ix.twin[k]; !ok || v.Value < cur.Value {
				ix.twin[k] = v
			}
		}
	}
	return ix
}

// weakFor returns the library-wide weak signals of one book: its folder
// holds franchise-named files, or one of its files is a twin of a franchise
// file elsewhere.
func (ix tfIndex) weakFor(b database.BookCore, files []database.BookFileCore) []franchise.Signal {
	var out []franchise.Signal
	seen := map[string]bool{}
	dirs := map[string]bool{}
	if b.FilePath != "" {
		dirs[b.FilePath] = true
		dirs[filepath.Dir(b.FilePath)] = true
	}
	for i := range files {
		dirs[filepath.Dir(files[i].FilePath)] = true
	}
	ds := make([]string, 0, len(dirs))
	for d := range dirs {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	for _, d := range ds {
		if s, ok := ix.folder[d]; ok && ix.folderFiles[d] <= tfFolderMaxFiles && !seen["f"+s.Value] {
			seen["f"+s.Value] = true
			out = append(out, s)
		}
	}
	for i := range files {
		if files[i].FileSize < tfTwinMinSize {
			continue
		}
		s, ok := ix.twin[tfTwinKey(files[i].FilePath, files[i].FileSize)]
		if !ok || s.Value == files[i].FilePath || seen["t"+s.Value] {
			continue
		}
		seen["t"+s.Value] = true
		s.Value = files[i].FilePath + " = " + s.Value
		out = append(out, s)
	}
	return out
}

func (f *tagFranchiseFixer) reader() (tfReader, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, fmt.Errorf("database not initialized")
	}
	return store, nil
}

// Plan evaluates every live book. A row is emitted for each book with any
// signal; books with none produce no row.
func (f *tagFranchiseFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	store, err := f.reader()
	if err != nil {
		return nil, err
	}
	tags := f.p.deps.BookTagReader()
	if tags == nil {
		return nil, fmt.Errorf("%s: no tag store", tagFranchiseFixerID)
	}
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	series, err := store.GetAllSeries()
	if err != nil {
		return nil, fmt.Errorf("GetAllSeries: %w", err)
	}
	seriesName := repairs.SeriesNamesFrom(series)
	ix := f.buildIndex(ctx, files)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	byBook := map[string][]database.BookFileCore{}
	for i := range files {
		byBook[files[i].BookID] = append(byBook[files[i].BookID], files[i])
	}
	var live []database.BookCore
	for i := range all {
		if !all[i].IsSoftDeleted() {
			live = append(live, all[i])
		}
	}
	rows := make([]*repairs.Row, len(live))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(live)), func(_ context.Context, i int) error {
		defer done.Add(1)
		b := live[i]
		weak := ix.weakFor(b, byBook[b.ID])
		r, ok, err := f.evaluate(store, tags, seriesName, b.ID, weak)
		if err != nil {
			r = repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = tfFingerprint(r, nil)
			ok = true
		}
		if ok {
			rows[i] = &r
		}
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		// Label runs inside the workers: it reads only the atomic.
		Label: func(_, total int) string { return fmt.Sprintf("Franchise tags %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	out := make([]repairs.Row, 0, 2048)
	for _, r := range rows {
		if r != nil {
			out = append(out, *r)
		}
	}
	return out, nil
}

// Replan re-reads the book and decides it again, with the weak signals the
// plan stored (Row.State): they come from a library-wide index a single row
// cannot rebuild. A book that is gone, or that lost every signal, comes back
// skipped with a different fingerprint (changed_since_plan).
func (f *tagFranchiseFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
	store, err := f.reader()
	if err != nil {
		return repairs.Row{}, err
	}
	tags := f.p.deps.BookTagReader()
	if tags == nil {
		return repairs.Row{}, fmt.Errorf("%s: no tag store", tagFranchiseFixerID)
	}
	var st tfState
	if len(planned.State) > 0 {
		if err := json.Unmarshal(planned.State, &st); err != nil {
			return repairs.Row{}, fmt.Errorf("row %s: unreadable plan state: %w", planned.RowID, err)
		}
	}
	series, err := store.GetAllSeries()
	if err != nil {
		return repairs.Row{}, fmt.Errorf("GetAllSeries: %w", err)
	}
	r, ok, err := f.evaluate(store, tags, repairs.SeriesNamesFrom(series), planned.RowID, st.Weak)
	if err != nil {
		return repairs.Row{}, err
	}
	if !ok {
		r = repairs.Row{RowID: planned.RowID, BookIDs: []string{planned.RowID}, Skipped: tfSkipNoSignal,
			SkipReason: "the book no longer carries any franchise signal", Risk: repairs.RiskLow}
		r.Fingerprint = tfFingerprint(r, nil)
	}
	return r, nil
}

// evaluate reads one book fresh and decides its row. ok is false when the
// book has no signal at all (no row).
func (f *tagFranchiseFixer) evaluate(store tfReader, tagReader BookTagReader, seriesName repairs.SeriesNamer, id string, weak []franchise.Signal) (repairs.Row, bool, error) {
	b, err := store.GetBookByID(id)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read book %s: %w", id, err)
	}
	if b == nil || b.IsSoftDeleted() {
		r := repairs.Row{RowID: id, BookIDs: []string{id}, Skipped: tfSkipGone,
			SkipReason: "the book no longer exists", Risk: repairs.RiskLow}
		r.Fingerprint = tfFingerprint(r, nil)
		return r, true, nil
	}
	files, err := store.GetBookFiles(id)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read files of %s: %w", id, err)
	}
	authors, err := database.LiveBookAuthorNames(store, b)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read authors of %s: %w", id, err)
	}
	narrators, err := tfNarratorNames(store, b)
	if err != nil {
		return repairs.Row{}, false, err
	}
	cur, err := tagReader.GetBookTagsDetailed(id)
	if err != nil {
		return repairs.Row{}, false, fmt.Errorf("read tags of %s: %w", id, err)
	}
	e := franchise.Evidence{Path: b.FilePath, Title: b.Title, Publisher: strOrEmpty(b.Publisher),
		Narrators: narrators, Authors: authors,
		TranscribedTitle: strOrEmpty(b.TranscribedTitle), TranscribedAuthor: strOrEmpty(b.TranscribedAuthor)}
	if b.SeriesID != nil && seriesName != nil {
		e.Series = seriesName(*b.SeriesID)
	}
	for i := range files {
		e.FilePaths = append(e.FilePaths, files[i].FilePath)
		if t := strOrEmpty(files[i].TranscribedTitle); t != "" {
			e.FileTranscribedTitles = append(e.FileTranscribedTitles, t)
		}
		if a := strOrEmpty(files[i].TranscribedAuthor); a != "" {
			e.FileTranscribedAuthors = append(e.FileTranscribedAuthors, a)
		}
	}
	res := franchise.Detect(e)
	res.Signals = append(res.Signals, weak...)
	if !res.Held() {
		return repairs.Row{}, false, nil
	}
	fr, rng := res.Classify()
	r := repairs.Row{RowID: id, BookIDs: []string{id}, Title: b.Title, Class: tfClassStrong, Risk: repairs.RiskLow}
	if len(authors) > 0 {
		r.Author = authors[0]
	}
	if !res.Strong() {
		r.Class, r.Risk = tfClassWeak, repairs.RiskReview
	}
	for _, s := range res.Signals {
		r.Evidence = append(r.Evidence, tfDescribe(s))
	}
	if len(weak) > 0 {
		raw, err := json.Marshal(tfState{Weak: weak})
		if err != nil {
			return repairs.Row{}, false, fmt.Errorf("row %s: encode state: %w", id, err)
		}
		r.State = raw
	}
	var have []string
	var otherFranchise string
	for _, t := range cur {
		tag := strings.ToLower(strings.TrimSpace(t.Tag))
		if strings.HasPrefix(tag, franchise.FranchiseTagPrefix) || strings.HasPrefix(tag, franchise.RangeTagPrefix) {
			have = append(have, tag)
		}
		if strings.HasPrefix(tag, franchise.FranchiseTagPrefix) && tag != franchise.FranchiseTagPrefix+fr && otherFranchise == "" {
			otherFranchise = tag
		}
	}
	sort.Strings(have)
	r.Current = map[string]string{"tags": strings.Join(have, ", ")}
	want := franchise.Tags(fr, rng)
	var missing []string
	for _, t := range want {
		if !tfContains(have, t) {
			missing = append(missing, t)
		}
	}
	r.Proposed = map[string]string{"tags": strings.Join(want, ", ")}
	switch {
	case otherFranchise != "":
		r.Skipped = tfSkipConflicting
		r.SkipReason = fmt.Sprintf("the book already carries %s; the matcher says %s -- read it by hand", otherFranchise, franchise.FranchiseTagPrefix+fr)
		r.Reason = r.SkipReason
	case len(missing) == 0:
		r.Skipped, r.SkipReason = tfSkipTagged, "the book already carries "+strings.Join(want, ", ")
		r.Reason = r.SkipReason
	default:
		r.Reason = fmt.Sprintf("%s: %d signal(s), first %s", strings.Join(missing, ", "), len(res.Signals), r.Evidence[0])
		r.Detail = &tfDecision{bookID: id, tags: missing}
	}
	r.Fingerprint = tfFingerprint(r, missing)
	return r, true, nil
}

// tfNarratorNames reads the narrator field and the book_narrators rows.
func tfNarratorNames(store tfReader, b *database.Book) ([]string, error) {
	var names []string
	if n := strOrEmpty(b.Narrator); n != "" {
		names = append(names, n)
	}
	links, err := store.GetBookNarrators(b.ID)
	if err != nil {
		return nil, fmt.Errorf("read narrators of %s: %w", b.ID, err)
	}
	for _, l := range links {
		n, err := store.GetNarratorByID(l.NarratorID)
		if err != nil {
			return nil, fmt.Errorf("read narrator %d of %s: %w", l.NarratorID, b.ID, err)
		}
		if n != nil && n.Name != "" {
			names = append(names, n.Name)
		}
	}
	return names, nil
}

func tfDescribe(s franchise.Signal) string {
	what := s.Hit.Franchise
	if s.Hit.Range != "" {
		what += "/" + s.Hit.Range
	}
	out := fmt.Sprintf("%s %q matches %q (%s, %s)", s.Field, s.Value, s.Hit.Text, s.Hit.Term, what)
	if s.Weak {
		out += " [weak]"
	}
	return out
}

func tfContains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// tfFingerprint hashes every input of the row's decision: the current and
// proposed tags, the skip, the class and every signal.
func tfFingerprint(r repairs.Row, missing []string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n%s\n%s\n%s\n%s\n", r.RowID, r.Current["tags"], r.Proposed["tags"], r.Skipped, r.Class, strings.Join(missing, ","))
	for _, e := range r.Evidence {
		sb.WriteString(e)
		sb.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])[:32]
}

// Apply adds the missing tags, each journaled; a tag the book gained since
// the re-plan is left alone (Writer.AddBookTag).
func (f *tagFranchiseFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*tfDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", tagFranchiseFixerID, fresh.RowID)
	}
	for i, t := range d.tags {
		if _, err := w.AddBookTag(d.bookID, t, franchise.TagSource); err != nil {
			if i > 0 {
				return fmt.Errorf("tag %s %q after %v: %w: %w", d.bookID, t, d.tags[:i], repairs.ErrPartiallyApplied, err)
			}
			return fmt.Errorf("tag %s %q: %w", d.bookID, t, err)
		}
	}
	return nil
}
