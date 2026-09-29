// file: internal/plugins/maintenance/junk_title_fixer.go
// version: 1.0.1
// guid: 7c3e9a15-2b6d-4f48-a9e1-5d0b8c4f7a26
// last-edited: 2026-09-28

package maintenance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/metadata"
	"github.com/falkcorp/audiobook-organizer/internal/operations/registry"
	"github.com/falkcorp/audiobook-organizer/internal/repairs"
	"github.com/falkcorp/audiobook-organizer/internal/util"
)

// junkTitlesFixerID is the Repairs-lane id of the junk-title fixer. It is the
// id the retired maintenance.repair-junk-titles op wrote history under, so a
// book's history reads the same before and after the op was absorbed here.
const junkTitlesFixerID = "maintenance.repair-junk-titles"

// Skip kinds of the junk-title fixer's own rows. The framework adds
// repairs.SkipITunes / repairs.SkipOwnerManual for hands-off books.
const (
	// junkSkipFragment: the book is a chapter file of a bigger book. It is
	// not retitled; the fragment-consolidation fixer folds it back in.
	junkSkipFragment = "fragment"
	// junkSkipNeedsManual: no trustworthy replacement title was found.
	junkSkipNeedsManual = "needs_manual"
	// junkSkipUserLocked: the title carries a user override; never touched.
	junkSkipUserLocked = "user_locked"
	// junkSkipNotJunk / junkSkipGone: what a re-plan reports for a book
	// whose title stopped being junk, or that no longer exists. A plan never
	// lists either.
	junkSkipNotJunk = "not_junk"
	junkSkipGone    = "gone"
)

// Sources of a proposed title, in priority order.
const (
	junkSrcTranscribed = "transcription"
	junkSrcCandidate   = "metadata_candidate"
	junkSrcStripped    = "title_prefix_stripped"
	junkSrcFolder      = "folder"
	junkSrcFilename    = "filename"
	junkSrcSeriesNum   = "series_and_number"
)

// junkCandidateMinScore is the lowest metafetch candidate score the fixer
// takes a title from (scores are 0..1). The cached search ran on the junk
// title, so a candidate must also agree on the author (junkCandidateTitle).
const junkCandidateMinScore = 0.5

// junkIndexTTL bounds how long Replan reuses the library-wide index during an
// apply. Rebuilding it lists every book, so it is not done per row.
const junkIndexTTL = 2 * time.Minute

// junkTitleFixer finds books whose stored title is not a title (a chapter
// number, a narrator credit, a track tag, a placeholder, a bare roman numeral,
// a junk prefix) and proposes the real one. It absorbed the
// maintenance.repair-junk-titles op, which matched an exact five-string set.
type junkTitleFixer struct {
	p *Plugin

	idxMu      sync.Mutex
	idx        *junkIndex
	idxBuiltAt time.Time

	// applied remembers the titles this process wrote, by author, so a later
	// row in the same apply that proposes the same title for the same author
	// is refused as a fragment even though the cached index predates the
	// first write.
	appliedMu sync.Mutex
	applied   map[string]bool
}

func newJunkTitleFixer(p *Plugin) *junkTitleFixer {
	return &junkTitleFixer{p: p, applied: map[string]bool{}}
}

var _ repairs.Fixer = (*junkTitleFixer)(nil)

func (f *junkTitleFixer) ID() string    { return junkTitlesFixerID }
func (f *junkTitleFixer) Title() string { return "Junk titles" }
func (f *junkTitleFixer) Description() string {
	return "Books titled with a chapter number (\"01\", \"Chapter 12\", \"Disc 2\"), a narrator credit " +
		"(\"read by narrator\"), a track tag (\"Opening\", \"Intro\"), a placeholder (\"Unknown Title\"), a bare " +
		"roman numeral, or a title behind a track-number or punctuation prefix. Proposes the real title from, in " +
		"order: the intro transcription, a matching metadata candidate, the title without its prefix, the folder or " +
		"filename, the series and number. Chapter files of a bigger book are listed as fragments for the " +
		"consolidation fixer, never retitled. Writes the title only, never a user-locked one."
}

// junkIndex is the library-wide state a decision reads besides the book
// itself. It depends only on paths, membership and existing titles, never on
// another junk row's proposal, so applying one row cannot change another's
// fingerprint except through the duplicate-title check it exists for.
type junkIndex struct {
	// dirBooks counts live books per work folder (junkWorkDir); dirChapters
	// counts those among them titled only by a chapter position.
	dirBooks    map[string]int
	dirChapters map[string]int
	// titles holds "<author id>|<TitleSortKey>" of every live book whose
	// title is real, so a proposal that duplicates an existing book of the
	// same author is caught.
	titles  map[string]bool
	authors map[int]string
	series  map[int]string
}

// junkWorkDir is the folder that names the work a book path belongs to: the
// book's folder (the file's folder for a file path), one level up when that
// folder is a disc/part folder ("Eldest/CD1").
func junkWorkDir(p string) string {
	if p == "" {
		return ""
	}
	d := p
	if _, audio := audioExts[strings.ToLower(filepath.Ext(p))]; audio {
		d = filepath.Dir(p)
	}
	if metadata.IsChapterOnlyTitle(filepath.Base(d)) {
		d = filepath.Dir(d)
	}
	return d
}

func junkTitleKey(authorID *int, title string) string {
	a := 0
	if authorID != nil {
		a = *authorID
	}
	return strconv.Itoa(a) + "|" + util.TitleSortKey(title)
}

// buildJunkIndex lists every book once (one consistent snapshot) and returns
// the index plus the junk-titled candidates.
func (f *junkTitleFixer) buildJunkIndex() (*junkIndex, []database.BookCore, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return nil, nil, fmt.Errorf("database not initialized")
	}
	all, err := store.GetAllBooksCore(0, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("GetAllBooksCore: %w", err)
	}
	allSeries, err := store.GetAllSeries()
	if err != nil {
		return nil, nil, fmt.Errorf("GetAllSeries: %w", err)
	}
	idx := &junkIndex{dirBooks: map[string]int{}, dirChapters: map[string]int{}, titles: map[string]bool{}, authors: map[int]string{}, series: map[int]string{}}
	for i := range allSeries {
		idx.series[allSeries[i].ID] = allSeries[i].Name
	}
	var cands []database.BookCore
	// A single pass of map inserts over the snapshot: no per-book I/O here,
	// the per-book reads run on the worker pool in Plan.
	for i := range all {
		b := &all[i]
		if b.IsSoftDeleted() {
			continue
		}
		kind := metadata.ClassifyJunkTitle(b.Title)
		if d := junkWorkDir(b.FilePath); d != "" {
			idx.dirBooks[d]++
			if kind.IsChapterKind() {
				idx.dirChapters[d]++
			}
		}
		if kind == metadata.JunkNone {
			idx.titles[junkTitleKey(b.AuthorID, b.Title)] = true
			continue
		}
		cands = append(cands, *b)
	}
	return idx, cands, nil
}

func (f *junkTitleFixer) cachedIndex() (*junkIndex, error) {
	f.idxMu.Lock()
	defer f.idxMu.Unlock()
	if f.idx != nil && time.Since(f.idxBuiltAt) < junkIndexTTL {
		return f.idx, nil
	}
	idx, _, err := f.buildJunkIndex()
	if err != nil {
		return nil, err
	}
	f.idx, f.idxBuiltAt = idx, time.Now()
	return idx, nil
}

// Plan lists every junk-titled book as a row: applicable when a replacement
// was found, skipped (fragment / needs_manual / user_locked / owner-manual)
// otherwise.
func (f *junkTitleFixer) Plan(ctx context.Context, _ json.RawMessage, rep registry.Reporter) ([]repairs.Row, error) {
	idx, cands, err := f.buildJunkIndex()
	if err != nil {
		return nil, err
	}
	f.idxMu.Lock()
	f.idx, f.idxBuiltAt = idx, time.Now()
	f.idxMu.Unlock()

	rows := make([]repairs.Row, len(cands))
	var done atomic.Int64
	// Each worker writes only rows[i] for its own i; idx is read-only here
	// apart from the author-name cache, which authorName locks.
	runErr := registry.RunItems(ctx, rep, indexesOf(len(cands)), func(_ context.Context, i int) error {
		defer done.Add(1)
		r, err := f.evaluate(idx, cands[i])
		if err != nil {
			r = repairs.Row{RowID: cands[i].ID, BookIDs: []string{cands[i].ID}, Title: cands[i].Title,
				Skipped: "error", SkipReason: err.Error(), Reason: err.Error(), Risk: repairs.RiskReview}
			r.Fingerprint = junkFingerprint(r, "")
		}
		rows[i] = r
		return nil
	}, registry.RunItemsOptions{
		Concurrency: titleRepairWorkers(),
		ErrMode:     registry.ErrModeCollect,
		Label:       func(_, total int) string { return fmt.Sprintf("Junk titles %d/%d", done.Load(), total) },
	})
	if runErr != nil && ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Two junk rows proposing the same title for the same author are two
	// pieces of one book: neither may be retitled into a duplicate.
	proposals := map[string][]int{}
	for i := range rows {
		if rows[i].Applicable() && rows[i].Proposed != nil {
			k := junkTitleKey(cands[i].AuthorID, rows[i].Proposed["title"])
			proposals[k] = append(proposals[k], i)
		}
	}
	for _, ix := range proposals {
		if len(ix) < 2 {
			continue
		}
		for _, i := range ix {
			why := fmt.Sprintf("fragment — use the consolidation fixer: %d junk-titled books of this author would all be retitled %q",
				len(ix), rows[i].Proposed["title"])
			rows[i].Skipped, rows[i].SkipReason, rows[i].Reason = junkSkipFragment, why, why
			rows[i].Fingerprint = junkFingerprint(rows[i], why)
		}
	}
	return rows, nil
}

func indexesOf(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// Replan re-reads the book and decides it again with the (short-lived)
// cached index. It always returns planned.RowID: a book that is gone or no
// longer junk comes back as a skipped row whose fingerprint differs, so the
// engine reports changed_since_plan rather than failed, and a re-run of an
// interrupted apply passes over the rows it already wrote.
func (f *junkTitleFixer) Replan(_ context.Context, _ json.RawMessage, planned repairs.Row, _ registry.Reporter) (repairs.Row, error) {
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
		r.Fingerprint = junkFingerprint(r, "gone")
		return r, nil
	}
	idx, err := f.cachedIndex()
	if err != nil {
		return repairs.Row{}, err
	}
	r, err := f.evaluate(idx, b.Core())
	if err != nil {
		return repairs.Row{}, err
	}
	if r.Applicable() && f.wasApplied(b.AuthorID, r.Proposed["title"]) {
		why := fmt.Sprintf("fragment — use the consolidation fixer: this apply already gave another book of this author the title %q",
			r.Proposed["title"])
		r.Skipped, r.SkipReason, r.Reason = junkSkipFragment, why, why
		r.Fingerprint = junkFingerprint(r, why)
	}
	return r, nil
}

// junkDecision travels from Replan to Apply in Row.Detail.
type junkDecision struct {
	bookID, oldTitle, newTitle string
	authorID                   *int
}

// Apply writes the title, and only the title, while it is still the junk one
// the replacement was derived from.
func (f *junkTitleFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*junkDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", junkTitlesFixerID, fresh.RowID)
	}
	if err := writeTitleOnly(w, d.bookID, d.oldTitle, d.newTitle); err != nil {
		return err
	}
	f.appliedMu.Lock()
	f.applied[junkTitleKey(d.authorID, d.newTitle)] = true
	f.appliedMu.Unlock()
	return nil
}

func (f *junkTitleFixer) wasApplied(authorID *int, title string) bool {
	f.appliedMu.Lock()
	defer f.appliedMu.Unlock()
	return f.applied[junkTitleKey(authorID, title)]
}

// writeTitleOnly sets Title through the framework writer, which records the
// history row undo reverts. A title that moved since the re-plan is refused
// as changed_since_plan: returning database.ErrSkipBookWrite here would make
// Modify report success with nothing written.
func writeTitleOnly(w *repairs.Writer, bookID, oldTitle, newTitle string) error {
	changed, err := w.Modify(bookID, func(cur *database.Book) error {
		if cur.Title != oldTitle {
			return fmt.Errorf("%w: title is now %q", repairs.ErrChangedSincePlan, cur.Title)
		}
		cur.Title = newTitle
		return nil
	})
	if err != nil {
		return err
	}
	if !slices.Contains(changed, "title") {
		return fmt.Errorf("book %s: the write committed but recorded no title change", bookID)
	}
	return nil
}

func (f *junkTitleFixer) authorName(idx *junkIndex, id *int) string {
	if id == nil {
		return ""
	}
	f.idxMu.Lock()
	n, ok := idx.authors[*id]
	f.idxMu.Unlock()
	if ok {
		return n
	}
	if store := f.p.deps.OpsStore(); store != nil {
		if a, err := store.GetAuthorByID(*id); err == nil && a != nil {
			n = a.Name
		}
	}
	f.idxMu.Lock()
	idx.authors[*id] = n
	f.idxMu.Unlock()
	return n
}

var trailingNumberRe = regexp.MustCompile(`(\d+)\s*$`)

// evaluate decides one book. Plan and Replan both call it, so a row planned
// and a row re-planned from the same state carry the same fingerprint.
func (f *junkTitleFixer) evaluate(idx *junkIndex, b database.BookCore) (repairs.Row, error) {
	store := f.p.deps.OpsStore()
	if store == nil {
		return repairs.Row{}, fmt.Errorf("database not initialized")
	}
	author := f.authorName(idx, b.AuthorID)
	r := repairs.Row{RowID: b.ID, BookIDs: []string{b.ID}, Title: b.Title, Author: author, Risk: repairs.RiskReview,
		Current: map[string]string{"title": b.Title}}
	finish := func(skip, why string) (repairs.Row, error) {
		r.Skipped, r.SkipReason = skip, why
		if r.Reason == "" {
			r.Reason = why
		}
		r.Fingerprint = junkFingerprint(r, why)
		return r, nil
	}

	kind := metadata.ClassifyJunkTitle(b.Title)
	if kind == metadata.JunkNone {
		return finish(junkSkipNotJunk, "the title is no longer junk")
	}
	r.Reason = fmt.Sprintf("junk title (%s)", kind)

	// The old op's owner-manual rule also read the TITLE: "Big Finish
	// Ident" is itself Big Finish content, which the path guard may not see.
	if junkTitleOwnerManual("", b.Title, "") {
		return finish(repairs.SkipOwnerManual, "the title marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand")
	}

	states, err := store.GetMetadataFieldStates(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read field states of %s: %w", b.ID, err)
	}
	providerTitle := false
	for i := range states {
		if states[i].Field != "title" {
			continue
		}
		if states[i].HasUserOverride() {
			return finish(junkSkipUserLocked, "the title carries a user override; it is never rewritten")
		}
		providerTitle = states[i].HasProviderValue()
	}

	files, err := store.GetBookFiles(b.ID)
	if err != nil {
		return repairs.Row{}, fmt.Errorf("read files of %s: %w", b.ID, err)
	}
	var paths []string
	transcribed := ""
	if b.TranscribedTitle != nil {
		transcribed = strings.TrimSpace(*b.TranscribedTitle)
	}
	for i := range files {
		if p := strings.TrimSpace(files[i].FilePath); p != "" {
			paths = append(paths, p)
		}
		if transcribed == "" && files[i].TranscribedTitle != nil {
			transcribed = strings.TrimSpace(*files[i].TranscribedTitle)
		}
	}
	sort.Strings(paths)
	if len(paths) == 0 && b.FilePath != "" {
		paths = []string{b.FilePath}
	}

	// ---- fragment: a chapter file of a bigger book is not retitled ----
	if why, err := f.fragmentReason(idx, b, kind, author, paths); err != nil {
		return repairs.Row{}, err
	} else if why != "" {
		return finish(junkSkipFragment, "fragment — use the consolidation fixer: "+why)
	}

	// ---- proposals, in priority order ----
	people := []string{author}
	if b.Narrator != nil {
		people = append(people, splitCreditNames(*b.Narrator)...)
	}
	links, err := store.GetBookAuthors(b.ID)
	if err != nil {
		// Fail closed: without the linked names the person check is incomplete.
		return repairs.Row{}, fmt.Errorf("read linked authors of %s: %w", b.ID, err)
	}
	for _, l := range links {
		id := l.AuthorID
		people = append(people, f.authorName(idx, &id))
	}
	var refused []string
	accept := func(cand string) (string, bool) {
		c := strings.TrimSpace(cand)
		switch {
		case len([]rune(c)) < 2, strings.EqualFold(c, strings.TrimSpace(b.Title)):
			return "", false
		case metadata.ClassifyJunkTitle(c) != metadata.JunkNone:
			return "", false
		case titleNamesAPerson(c, people):
			refused = append(refused, fmt.Sprintf("%q names the author or narrator", c))
			return "", false
		}
		return c, true
	}

	type proposal struct{ title, source, risk string }
	var props []proposal
	if t, ok := accept(transcribed); ok {
		props = append(props, proposal{t, junkSrcTranscribed, repairs.RiskReview})
	}
	if t, ok := f.candidateTitle(b.ID, author); ok {
		if t, ok = accept(t); ok {
			props = append(props, proposal{t, junkSrcCandidate, repairs.RiskReview})
		}
	}
	if kind.HasRealTitleInside() {
		if t, ok := metadata.StripJunkTitlePrefix(b.Title); ok {
			if t, ok = accept(t); ok {
				props = append(props, proposal{t, junkSrcStripped, repairs.RiskLow})
			}
		}
	}
	if kind.IsChapterKind() && len(paths) > 0 {
		// "" is chapter-only to ChapterTitleFromDirectory, which then reads
		// the folder whatever the stored chapter spelling ("Part IV").
		if t, _, ok := metadata.ChapterTitleFromDirectory(paths[0], ""); ok {
			if t, ok = accept(t); ok {
				props = append(props, proposal{t, junkSrcFolder, repairs.RiskReview})
			}
		}
	} else if t, method, ok := DeriveJunkTitleReplacement(b.Title, author, paths); ok {
		if t, ok = accept(t); ok {
			src := junkSrcFolder
			if method == "filename" {
				src = junkSrcFilename
			}
			props = append(props, proposal{t, src, repairs.RiskReview})
		}
	}
	if kind.IsChapterKind() && b.SeriesID != nil && idx.series[*b.SeriesID] != "" {
		if m := trailingNumberRe.FindStringSubmatch(b.Title); m != nil {
			// A digits-only match cannot fail to parse except by overflow,
			// which is no book number: no proposal then.
			if n, perr := strconv.Atoi(m[1]); perr == nil {
				if t, ok := accept(fmt.Sprintf("%s, Book %d", idx.series[*b.SeriesID], n)); ok {
					props = append(props, proposal{t, junkSrcSeriesNum, repairs.RiskReview})
				}
			}
		}
	}
	if len(props) == 0 {
		why := "no trustworthy replacement title (no transcription, matching candidate, usable folder or series)"
		if len(refused) > 0 {
			why += "; refused: " + strings.Join(refused, ", ")
		}
		return finish(junkSkipNeedsManual, why)
	}
	best := props[0]
	if idx.titles[junkTitleKey(b.AuthorID, best.title)] {
		return finish(junkSkipFragment, fmt.Sprintf(
			"fragment — use the consolidation fixer: this author already has a book titled %q; this one is most likely part of it",
			best.title))
	}
	r.Proposed = map[string]string{"title": best.title}
	r.Risk = best.risk
	if providerTitle {
		// A metadata provider supplied the current title: read it first.
		r.Risk = repairs.RiskReview
	}
	var also []string
	for _, p := range props[1:] {
		if p.title != best.title {
			also = append(also, fmt.Sprintf("%s: %q", p.source, p.title))
		}
	}
	r.Reason = fmt.Sprintf("junk title (%s); proposed from %s", kind, best.source)
	if len(also) > 0 {
		r.Reason += "; other evidence: " + strings.Join(also, ", ")
	}
	r.Detail = &junkDecision{bookID: b.ID, oldTitle: b.Title, newTitle: best.title, authorID: b.AuthorID}
	r.Fingerprint = junkFingerprint(r, string(kind)+"|"+best.source+"|"+strconv.FormatBool(providerTitle))
	return r, nil
}

// fragmentReason says why a junk-titled book is a chapter file of a bigger
// book, or "" when it stands on its own. Wrongly calling a book a fragment
// only defers it to the consolidation fixer; wrongly retitling a fragment
// makes a duplicate of its parent, so the default for a chapter title is
// "fragment" unless the book is a whole multi-file folder of its own.
func (f *junkTitleFixer) fragmentReason(idx *junkIndex, b database.BookCore, kind metadata.JunkTitleKind, author string, paths []string) (string, error) {
	store := f.p.deps.OpsStore()
	for _, p := range paths {
		row, err := store.GetBookFileByPath(p)
		if err != nil {
			return "", fmt.Errorf("read owner of %s: %w", p, err)
		}
		if row != nil && row.BookID != "" && row.BookID != b.ID {
			return fmt.Sprintf("its file %s is also a file of book %s", p, row.BookID), nil
		}
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[junkWorkDir(p)] = true
	}
	if d := junkWorkDir(b.FilePath); d != "" {
		dirs[d] = true
	}
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		keys = append(keys, d)
	}
	sort.Strings(keys)
	for _, d := range keys {
		if d == "" {
			continue
		}
		// A chapter-titled book sharing its folder with any other book is a
		// piece of it. Any other junk title ("Opening", "read by narrator")
		// is a fragment only beside chapter-titled siblings: single-file
		// books of different works often share an author folder.
		others := idx.dirChapters[d]
		// A track-number prefix beside other books is the tracks-of-one-book
		// shape too ("01 - Chapter One", "02 - Chapter Two").
		if kind.IsChapterKind() || kind == metadata.JunkNumberPrefix {
			others = idx.dirBooks[d] - 1
		}
		if others >= 1 {
			return fmt.Sprintf("%d books share the folder %s", idx.dirBooks[d], d), nil
		}
		if a := strings.TrimSpace(author); a != "" && !authorname.IsPlaceholderAuthor(a) && strings.EqualFold(a, filepath.Base(d)) {
			return fmt.Sprintf("its author %q is the name of its folder, the shape a chapter file parsed as its own book gets", a), nil
		}
	}
	if kind.IsChapterKind() && len(paths) < 2 {
		return "a single file titled only by a chapter position", nil
	}
	return "", nil
}

// candidateTitle returns the best cached metadata candidate's title when it
// is trustworthy: score at least junkCandidateMinScore, no runtime mismatch,
// and the same author as the book (the search ran on the junk title, so a
// candidate that disagrees on the author is noise).
func (f *junkTitleFixer) candidateTitle(bookID, author string) (string, bool) {
	cs := f.p.deps.MetadataCacheStore()
	if cs == nil {
		return "", false
	}
	entry, err := cs.GetMetadataCache(bookID)
	if err != nil || entry == nil {
		return "", false
	}
	a := strings.TrimSpace(author)
	if a == "" || authorname.IsPlaceholderAuthor(a) {
		return "", false
	}
	for _, raw := range entry.Candidates {
		var c struct {
			Title            string  `json:"title"`
			Author           string  `json:"author"`
			Score            float64 `json:"score"`
			DurationMismatch bool    `json:"duration_mismatch"`
		}
		if json.Unmarshal(raw, &c) != nil || c.Score < junkCandidateMinScore || c.DurationMismatch {
			continue
		}
		if !strings.EqualFold(util.NormalizeAuthor(c.Author), util.NormalizeAuthor(a)) {
			continue
		}
		return c.Title, true
	}
	return "", false
}

// junkFingerprint hashes the decision's inputs and outputs.
func junkFingerprint(r repairs.Row, extra string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n%s\n%s\n%s\n", r.RowID, r.Current["title"], r.Skipped, r.Proposed["title"], extra)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])[:32]
}
