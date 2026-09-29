// file: internal/plugins/maintenance/junk_title_fixer.go
// version: 1.6.0
// guid: 7c3e9a15-2b6d-4f48-a9e1-5d0b8c4f7a26
// last-edited: 2026-09-29

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
	"unicode"

	"github.com/falkcorp/audiobook-organizer/internal/authorname"
	"github.com/falkcorp/audiobook-organizer/internal/config"
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

// Skip kinds of the junk-title fixer's own rows, named like the framework's
// (repairs.SkipITunes = "skipped_itunes") and the fragment-consolidation
// fixer's ("skipped_ambiguous", ...), so the Repairs tab lists one family.
const (
	// junkSkipFragment: PROVEN to be a piece of another book — another live
	// book has a book_file row at one of this book's paths. The
	// fragment-consolidation fixer folds it back in; it is never retitled.
	junkSkipFragment = "skipped_fragment"
	// junkSkipPossibleFragment: the shape of a chapter file of a bigger book
	// (a folder shared with other books, a lone chapter-titled file, the
	// author named like the folder, a proposal that duplicates another book),
	// but no parent is proven. Left for a person to decide.
	junkSkipPossibleFragment = "skipped_possible_fragment"
	// junkSkipNeedsManual: no trustworthy replacement title was found.
	junkSkipNeedsManual = "skipped_needs_manual"
	// junkSkipUserLocked: the title carries a user override; never touched.
	junkSkipUserLocked = "skipped_user_locked"
	// junkSkipProviderTitle: a metadata provider supplied the title. The
	// fixer only repairs file-derived junk; a provider value is somebody's
	// answer and is never overwritten from a folder or a prefix strip.
	junkSkipProviderTitle = "skipped_provider_title"
	// junkSkipNotJunk / junkSkipGone: what a re-plan reports for a book
	// whose title stopped being junk, or that no longer exists. A plan never
	// lists either.
	junkSkipNotJunk = "skipped_not_junk"
	junkSkipGone    = "skipped_gone"
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
// title, so a candidate must also agree on the author (candidateTitle).
const junkCandidateMinScore = 0.5

// junkIndexTTL bounds how long Replan reuses the library-wide index during an
// apply. Rebuilding it lists every book, so it is not done per row.
const junkIndexTTL = 2 * time.Minute

// junkTitleFixer finds books whose stored title is not a title (a chapter
// number, a narrator credit, a track tag, a placeholder, a bare roman numeral,
// a junk prefix) and proposes the real one. It absorbed the
// maintenance.repair-junk-titles op, which matched an exact five-string set.
//
// It keeps no memory of its own writes: two rows that would propose the same
// title for the same author are both refused at plan time, so an apply can
// never write one title twice, and an undone row can be applied again.
type junkTitleFixer struct {
	p *Plugin

	idxMu      sync.Mutex
	idx        *junkIndex
	idxBuiltAt time.Time
}

func newJunkTitleFixer(p *Plugin) *junkTitleFixer {
	return &junkTitleFixer{p: p}
}

var _ repairs.Fixer = (*junkTitleFixer)(nil)

func (f *junkTitleFixer) ID() string    { return junkTitlesFixerID }
func (f *junkTitleFixer) Title() string { return "Junk titles" }
func (f *junkTitleFixer) Description() string {
	return "Books titled with a chapter number (\"01\", \"Chapter 12\", \"Disc 2\"), a narrator credit " +
		"(\"read by narrator\"), a track tag (\"Opening\", \"Intro\"), a placeholder (\"Unknown Title\"), a bare " +
		"roman numeral, or a title behind a track-number or punctuation prefix. Proposes the real title from, in " +
		"order: the intro transcription, the title without its prefix, a matching metadata candidate, the folder or " +
		"filename, the series and number. A book another book owns a file of is a fragment for the consolidation " +
		"fixer; one that only looks like a chapter file is listed for a person. Writes the title only, never a " +
		"user-locked or provider-supplied one."
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
	// owners maps each file path of a junk-titled book to every live book
	// with a book_file row at that path. GetBookFileByPath cannot answer
	// this: its index holds one id per path and the last writer wins, which
	// is the fragment itself when the scanner imported a chapter the parent
	// already owned.
	owners map[string][]string
	// roots are the library root and every import path, cleaned. A proposal
	// that names one of their segments is a path, not a title.
	roots []string
	// authorRoot is the library root when the organizer files books
	// author-first ("{author}/…", the default folder pattern): the folder
	// right below it is an author folder, never a title. Import paths are
	// not in it; their layouts are whatever the source had, often flat.
	authorRoot string
}

// junkWorkDir is the folder that names the work a book path belongs to: the
// book's folder (the file's folder for a file path), climbing past up to two
// disc/part/chapter-number folders ("Eldest/CD1", the organizer-moved
// "Eldest/02/98/98.mp3").
//
// A number folder named like the book's own title is that book's folder, not
// a chapter folder: "Kelley Armstrong/13/13.m4b" is the book "13" filed
// author/title, so the climb stops there — unless another number folder sits
// right above it, which is the organizer-moved chapter shape ("02/98/98").
func junkWorkDir(p, title string) string {
	if p == "" {
		return ""
	}
	d := p
	if _, audio := audioExts[strings.ToLower(filepath.Ext(p))]; audio {
		d = filepath.Dir(p)
	}
	for range 2 {
		base := filepath.Base(d)
		if !metadata.IsChapterOnlyTitle(base) {
			break
		}
		if strings.EqualFold(strings.TrimSpace(base), strings.TrimSpace(title)) &&
			!metadata.IsChapterOnlyTitle(filepath.Base(filepath.Dir(d))) {
			break
		}
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

// narratorsOf splits a book's denormalized narrator credit into names, for
// ClassifyJunkTitleFor ("Read by Kate Reading" is junk only on her book).
func narratorsOf(n *string) []string {
	if n == nil || strings.TrimSpace(*n) == "" {
		return nil
	}
	return splitCreditNames(*n)
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
	imports, err := store.GetAllImportPaths()
	if err != nil {
		// Fail closed: without the roots the path-segment refusal is blind.
		return nil, nil, fmt.Errorf("GetAllImportPaths: %w", err)
	}
	idx := &junkIndex{dirBooks: map[string]int{}, dirChapters: map[string]int{}, titles: map[string]bool{},
		authors: map[int]string{}, series: map[int]string{}, owners: map[string][]string{}}
	if config.AppConfig.RootDir != "" {
		root := filepath.Clean(config.AppConfig.RootDir)
		idx.roots = append(idx.roots, root)
		pattern := strings.TrimSpace(config.AppConfig.FolderNamingPattern)
		if pattern == "" {
			pattern = config.DefaultFolderNamingPattern
		}
		if strings.HasPrefix(pattern, "{author}") {
			idx.authorRoot = root
		}
	}
	for i := range imports {
		if p := strings.TrimSpace(imports[i].Path); p != "" {
			idx.roots = append(idx.roots, filepath.Clean(p))
		}
	}
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
		kind := metadata.ClassifyJunkTitleFor(b.Title, narratorsOf(b.Narrator))
		if d := junkWorkDir(b.FilePath, b.Title); d != "" {
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

	// File ownership: two passes over one listing of every book_file row.
	// The first keeps the paths of junk-titled books, the second collects
	// every live owner of those paths. Both are plain map lookups per row.
	live := make(map[string]bool, len(all))
	for i := range all {
		if !all[i].IsSoftDeleted() {
			live[all[i].ID] = true
		}
	}
	candIDs := make(map[string]bool, len(cands))
	for i := range cands {
		candIDs[cands[i].ID] = true
	}
	files, err := store.GetAllBookFilesCore()
	if err != nil {
		return nil, nil, fmt.Errorf("GetAllBookFilesCore: %w", err)
	}
	for i := range files {
		if candIDs[files[i].BookID] && files[i].FilePath != "" {
			idx.owners[files[i].FilePath] = nil
		}
	}
	for i := range files {
		if _, want := idx.owners[files[i].FilePath]; want && live[files[i].BookID] {
			idx.owners[files[i].FilePath] = append(idx.owners[files[i].FilePath], files[i].BookID)
		}
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
// was found, skipped (fragment / possible_fragment / needs_manual /
// user_locked / provider_title / owner-manual) otherwise.
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
	// Two junk rows proposing the same title for the same author look like
	// two pieces of one book, but nothing proves a parent: neither is
	// retitled into a duplicate, and a person decides.
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
			why := fmt.Sprintf("possible fragment: %d junk-titled books of this author would all be retitled %q",
				len(ix), rows[i].Proposed["title"])
			rows[i].Skipped, rows[i].SkipReason, rows[i].Reason = junkSkipPossibleFragment, why, why
			rows[i].Detail = nil
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
	return f.evaluate(idx, b.Core())
}

// junkDecision travels from Replan to Apply in Row.Detail.
type junkDecision struct {
	bookID, oldTitle, newTitle string
}

// Apply writes the title, and only the title, while it is still the junk one
// the replacement was derived from and no user lock has appeared.
func (f *junkTitleFixer) Apply(_ context.Context, w *repairs.Writer, fresh repairs.Row) error {
	d, ok := fresh.Detail.(*junkDecision)
	if !ok || d == nil {
		return fmt.Errorf("%s: row %s carries no decision", junkTitlesFixerID, fresh.RowID)
	}
	return writeTitleOnly(w, f.p.deps.OpsStore(), d.bookID, d.oldTitle, d.newTitle)
}

// titleStateReader reads a book's field provenance; writeTitleOnly re-checks
// the user lock with it inside the write.
type titleStateReader interface {
	GetMetadataFieldStates(bookID string) ([]database.MetadataFieldState, error)
}

// writeTitleOnly sets Title through the framework writer, which records the
// history row undo reverts. A title that moved since the re-plan, or that a
// user locked since, is refused as changed_since_plan: returning
// database.ErrSkipBookWrite here would make Modify report success with
// nothing written.
//
// The lock re-check runs inside the ModifyBook callback, so it is as late as
// the write can make it. Field-state writes do not take the book stripe, so a
// lock landing between this read and the commit still races; the window is
// the width of one Pebble batch instead of a whole apply.
func writeTitleOnly(w *repairs.Writer, states titleStateReader, bookID, oldTitle, newTitle string) error {
	if states == nil {
		return fmt.Errorf("book %s: no field-state reader to check the title lock with", bookID)
	}
	changed, err := w.Modify(bookID, func(cur *database.Book) error {
		if cur.Title != oldTitle {
			return fmt.Errorf("%w: title is now %q", repairs.ErrChangedSincePlan, cur.Title)
		}
		sts, err := states.GetMetadataFieldStates(bookID)
		if err != nil {
			return fmt.Errorf("read field states of %s: %w", bookID, err)
		}
		for i := range sts {
			if sts[i].Field == "title" && sts[i].HasUserOverride() {
				return fmt.Errorf("%w: the title is now user-locked", repairs.ErrChangedSincePlan)
			}
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

var (
	trailingNumberRe = regexp.MustCompile(`(\d+)\s*$`)
	// bareNumberRe is an unpadded number and nothing else ("13", "300"). It
	// is as likely a real title as a chapter position, so it is never called
	// a possible fragment on shape alone.
	bareNumberRe = regexp.MustCompile(`^[1-9]\d*$`)
)

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

	narrators := narratorsOf(b.Narrator)
	kind := metadata.ClassifyJunkTitleFor(b.Title, narrators)
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
	for i := range states {
		if states[i].Field != "title" {
			continue
		}
		if states[i].HasUserOverride() {
			return finish(junkSkipUserLocked, "the title carries a user override; it is never rewritten")
		}
		if states[i].HasProviderValue() {
			return finish(junkSkipProviderTitle,
				"a metadata provider supplied the title; this fixer only repairs file-derived titles")
		}
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

	// ---- fragments: a chapter file of a bigger book is not retitled ----
	skip, why, err := f.fragmentReason(idx, b, kind, author, paths)
	if err != nil {
		return repairs.Row{}, err
	}
	switch skip {
	case junkSkipFragment:
		return finish(skip, "fragment — use the consolidation fixer: "+why)
	case junkSkipPossibleFragment:
		return finish(skip, "possible fragment: "+why)
	case junkSkipNeedsManual:
		return finish(skip, why)
	}

	// ---- proposals, in priority order ----
	people := []string{author}
	people = append(people, narrators...)
	links, err := store.GetBookAuthors(b.ID)
	if err != nil {
		// Fail closed: without the linked names the person check is incomplete.
		return repairs.Row{}, fmt.Errorf("read linked authors of %s: %w", b.ID, err)
	}
	for _, l := range links {
		id := l.AuthorID
		people = append(people, f.authorName(idx, &id))
	}
	pc := junkProposalCheck{stored: b.Title, narrators: narrators, people: people, roots: idx.roots,
		authorRoot: idx.authorRoot, paths: paths}
	var refused []string
	ownerManual := ""
	accept := func(cand, source string) (string, bool) {
		c := strings.TrimSpace(cand)
		if c != "" && junkTitleOwnerManual("", c, "") {
			// The proposal itself says Doctor Who / Big Finish / Torchwood:
			// the whole book is the owner's, not only this proposal.
			ownerManual = c
			return "", false
		}
		if why := pc.refusal(c, source); why != "" {
			if why != "-" {
				refused = append(refused, why)
			}
			return "", false
		}
		return c, true
	}

	type proposal struct{ title, source, risk string }
	var props []proposal
	// An embedded kind still carries (or sits in a folder that names) the
	// real title; the cached candidate search ran on the junk title and its
	// top hit is often the series' first book ("01 - Eldest" → "Eragon"), so
	// for an embedded kind a candidate must agree with that evidence to count
	// at all.
	embedded := kind.HasRealTitleInside() || kind.IsChapterKind()
	var agreeWith []string
	stripped := ""
	if kind.HasRealTitleInside() {
		if t, ok := metadata.StripJunkTitlePrefix(b.Title); ok {
			agreeWith = append(agreeWith, t)
			stripped, _ = accept(t, junkSrcStripped)
		}
	}
	folderTitle, folderSrc, titleDir := "", "", ""
	if kind.IsChapterKind() && len(paths) > 0 {
		// "" is chapter-only to ChapterTitleFromDirectory, which then reads
		// the folder whatever the stored chapter spelling ("Part IV").
		if t, dir, ok := metadata.ChapterTitleFromDirectory(paths[0], ""); ok {
			folderTitle, folderSrc, titleDir = t, junkSrcFolder, dir
		}
	} else if t, method, ok := DeriveJunkTitleReplacement(b.Title, author, paths); ok {
		folderTitle, folderSrc = t, junkSrcFolder
		if method == "filename" {
			folderSrc = junkSrcFilename
		}
	}
	// A folder named like the book's series ("The Stormlight Archive/") says
	// which series, not which book: it is neither evidence nor a proposal.
	if folderSrc == junkSrcFolder && b.SeriesID != nil && idx.series[*b.SeriesID] != "" &&
		strings.EqualFold(strings.TrimSpace(folderTitle), strings.TrimSpace(idx.series[*b.SeriesID])) {
		folderTitle, folderSrc, titleDir = "", "", ""
	}
	if folderSrc == junkSrcFolder {
		agreeWith = append(agreeWith, folderTitle)
	}
	if len(paths) > 0 {
		agreeWith = append(agreeWith, filepath.Base(junkWorkDir(paths[0], b.Title)))
	}
	folder := ""
	if folderSrc != "" {
		folder, _ = accept(folderTitle, folderSrc)
	}
	// weakFolder: a filename stem that looks like a rip's file name
	// ("hp1", "dune_messiah_64kbps", "final", "audible_download_2019"), not a
	// title. It never outranks a transcription or candidate.
	weakFolder := folder != "" && folderSrc == junkSrcFilename && isWeakFileStem(folder)

	spoken, _ := accept(transcribed, junkSrcTranscribed)
	candidate := ""
	if t, score, ok := f.candidateTitle(b.ID, author); ok {
		switch {
		case embedded && !titleAgreesWithAny(t, agreeWith):
			refused = append(refused, fmt.Sprintf("candidate %q (score %.2f) disagrees with the title's own evidence", t, score))
		default:
			candidate, _ = accept(t, junkSrcCandidate)
		}
	}

	// An owner-manual proposal decides the book before any conflict does.
	if ownerManual != "" {
		return finish(repairs.SkipOwnerManual, fmt.Sprintf(
			"the proposed title %q marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand", ownerManual))
	}

	// Path evidence is what the book's own path says its title is: the
	// stripped title, the folder, a filename stem that reads like a title.
	// When it and a transcription or candidate disagree, neither is
	// proposed: each side has a known way to be wrong ("New Folder (2)",
	// "Libation" vs "Audible Studios presents", a series' first book), and
	// picking one would write the other's mistake half the time.
	type evidence struct{ source, title string }
	var pathEv []evidence
	if stripped != "" {
		pathEv = append(pathEv, evidence{junkSrcStripped, stripped})
	}
	if folder != "" && !weakFolder {
		pathEv = append(pathEv, evidence{folderSrc, folder})
	}
	if len(pathEv) > 0 {
		pathTitles := make([]string, len(pathEv))
		for i := range pathEv {
			pathTitles[i] = pathEv[i].title
		}
		for _, other := range []evidence{{junkSrcTranscribed, spoken}, {junkSrcCandidate, candidate}} {
			if other.title == "" || titleAgreesWithAny(other.title, pathTitles) {
				continue
			}
			var sides []string
			for _, e := range pathEv {
				sides = append(sides, fmt.Sprintf("%s %q", e.source, e.title))
			}
			return finish(junkSkipNeedsManual, fmt.Sprintf("conflicting evidence: %s vs %s %q",
				strings.Join(sides, ", "), other.source, other.title))
		}
	}

	if spoken != "" {
		props = append(props, proposal{spoken, junkSrcTranscribed, repairs.RiskReview})
	}
	if stripped != "" {
		props = append(props, proposal{stripped, junkSrcStripped, repairs.RiskLow})
	}
	if candidate != "" {
		props = append(props, proposal{candidate, junkSrcCandidate, repairs.RiskReview})
	}
	if folder != "" {
		// A folder reached by climbing past a chapter folder that sits right
		// under a library or import root is as likely the AUTHOR folder
		// ("/imports/dl/Robin Hobb/01/a.mp3"): only a transcription or
		// candidate naming the same work makes it a title.
		climbed := titleDir != "" && titleDir != filepath.Dir(paths[0])
		if climbed && slices.Contains(idx.roots, filepath.Dir(titleDir)) {
			corroborated := false
			for _, c := range []string{spoken, candidate} {
				if c != "" && titleAgreesWithAny(c, []string{folder}) {
					corroborated = true
				}
			}
			if !corroborated {
				return finish(junkSkipNeedsManual, fmt.Sprintf(
					"the folder %q sits directly under the root %s above a chapter folder and may be the author; "+
						"no transcription or candidate corroborates it", folder, filepath.Dir(titleDir)))
			}
		}
		// A weak filename stem ("hp1", "final") is never a title, not even
		// as a last resort.
		if !weakFolder {
			props = append(props, proposal{folder, folderSrc, repairs.RiskReview})
		}
	}
	if kind.IsChapterKind() && b.SeriesID != nil && idx.series[*b.SeriesID] != "" {
		if m := trailingNumberRe.FindStringSubmatch(b.Title); m != nil {
			// A digits-only match cannot fail to parse except by overflow,
			// which is no book number: no proposal then.
			if n, perr := strconv.Atoi(m[1]); perr == nil {
				if t, ok := accept(fmt.Sprintf("%s, Book %d", idx.series[*b.SeriesID], n), junkSrcSeriesNum); ok {
					props = append(props, proposal{t, junkSrcSeriesNum, repairs.RiskReview})
				}
			}
		}
	}
	if ownerManual != "" {
		return finish(repairs.SkipOwnerManual, fmt.Sprintf(
			"the proposed title %q marks Big Finish / Doctor Who / Torchwood content; owner applies these by hand", ownerManual))
	}
	if len(props) == 0 && weakFolder {
		return finish(junkSkipNeedsManual, "only a weak filename stem: "+folder)
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
		return finish(junkSkipPossibleFragment, fmt.Sprintf(
			"possible fragment: this author already has a book titled %q; this one may be part of it", best.title))
	}
	r.Proposed = map[string]string{"title": best.title}
	r.Risk = best.risk
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
	r.Detail = &junkDecision{bookID: b.ID, oldTitle: b.Title, newTitle: best.title}
	r.Fingerprint = junkFingerprint(r, string(kind)+"|"+best.source)
	return r, nil
}

// titleAgreesWithAny reports whether a candidate title names the same work
// as any of the evidence titles: equal sort keys, or one is the other plus a
// subtitle after ":", " (", " - " or "," ("Eldest" vs "Eldest: Inheritance,
// Book 2"). A longer title that merely starts with the word ("Eldest Son")
// is a different work. Apostrophes are ignored: a folder cannot always hold
// one ("Assassins Apprentice" is "Assassin's Apprentice").
func titleAgreesWithAny(cand string, evidence []string) bool {
	c := util.TitleSortKey(dropApostrophes(cand))
	if c == "" {
		return false
	}
	lc := strings.ToLower(strings.TrimSpace(dropApostrophes(cand)))
	for _, e := range evidence {
		k := util.TitleSortKey(dropApostrophes(e))
		if k == "" {
			continue
		}
		le := strings.ToLower(strings.TrimSpace(dropApostrophes(e)))
		if c == k || hasSubtitle(lc, le) || hasSubtitle(le, lc) {
			return true
		}
	}
	return false
}

var apostropheReplacer = strings.NewReplacer("'", "", "\u2019", "", "\u2018", "", "`", "")

func dropApostrophes(s string) string { return apostropheReplacer.Replace(s) }

// weakStemTokenRe: a filename token that marks a rip, not a title: a
// bitrate, a download/final/merged/output marker.
var weakStemTokenRe = regexp.MustCompile(`(?i)(?:^|[^a-z])(?:\d+\s*kbps|kbps|bitrate|download|downloaded|final|merged|output)(?:$|[^a-z])`)

// isWeakFileStem reports whether a filename stem reads like a file name
// rather than a title: short (3 characters or fewer), underscore-joined, a
// word mixing digits and letters ("hp1", "64kbps"), or a rip marker.
func isWeakFileStem(stem string) bool {
	s := strings.TrimSpace(stem)
	if len([]rune(s)) <= 3 || strings.Contains(s, "_") || weakStemTokenRe.MatchString(s) {
		return true
	}
	for _, w := range strings.Fields(s) {
		hasDigit := strings.IndexFunc(w, unicode.IsDigit) >= 0
		hasLetter := strings.IndexFunc(w, unicode.IsLetter) >= 0
		if hasDigit && hasLetter {
			return true
		}
	}
	return false
}

// hasSubtitle: s is p followed by a subtitle separator.
func hasSubtitle(s, p string) bool {
	if p == "" || len(s) <= len(p) || !strings.HasPrefix(s, p) {
		return false
	}
	rest := s[len(p):]
	for _, sep := range []string{":", " (", " - ", ","} {
		if strings.HasPrefix(rest, sep) {
			return true
		}
	}
	return false
}

// junkProposalCheck refuses a proposed title that is not a title.
type junkProposalCheck struct {
	stored     string
	narrators  []string
	people     []string
	roots      []string
	authorRoot string
	paths      []string
}

var (
	// formatWordRe: a container, format or edition word, a side/tape/disc
	// position or range. A folder or filename carrying one names a rip, not
	// a work ("Dune (Unabridged)", "Dune MP3", "Side A", "Tape 3", "CD01-02").
	formatWordRe = regexp.MustCompile(`(?i)\b(?:mp3|m4b|m4a|aac|flac|ogg|opus|wma|unabridged|abridged|side\s+[a-d0-9]|tape\s*\d+|(?:cd|disc|disk)\s*\d+(?:\s*-\s*\d+)?)\b`)
	// idCodeRe: an ASIN (B0 + 8), an ISBN-10/13, or a trailing bracketed
	// 10-character id ("Dune [B002V1OF70]").
	idCodeRe = regexp.MustCompile(`(?i)\bB0[0-9A-Z]{8}\b|\b(?:97[89][- ]?)?\d{9}[\dX]\b|\[[0-9A-Z]{10}\]\s*$`)
	// genericStems are tool-output stems that name no work.
	genericStems = map[string]bool{"merged": true, "output": true, "book": true, "audio": true, "audiobook": true,
		"track": true, "untitled": true, "new folder": true}
)

// refusal returns why c is refused as a proposal from source, "-" for a
// refusal not worth listing (empty, same as stored), or "" to accept it.
func (pc junkProposalCheck) refusal(c, source string) string {
	switch {
	case len([]rune(c)) < 2, strings.EqualFold(c, strings.TrimSpace(pc.stored)):
		return "-"
	case metadata.ClassifyJunkTitleFor(c, pc.narrators) != metadata.JunkNone:
		return fmt.Sprintf("%q is junk itself", c)
	case metadata.IsGenericDirName(c), genericStems[strings.ToLower(c)]:
		// A generic folder ("Books", "Audiobooks", a library root) is what
		// the one-level climb in DeriveJunkTitleReplacement lands on above a
		// junk-named folder; it is never a title.
		return fmt.Sprintf("%q is a generic name", c)
	case titleNamesAPerson(c, pc.people) || pc.namesAPersonNormalized(c):
		return fmt.Sprintf("%q names the author or narrator", c)
	case pc.isPathRoot(c):
		return fmt.Sprintf("%q is a library or path root", c)
	}
	if source == junkSrcFolder || source == junkSrcFilename {
		switch {
		case formatWordRe.MatchString(c):
			return fmt.Sprintf("%q carries a format, edition or disc marker", c)
		case idCodeRe.MatchString(c):
			return fmt.Sprintf("%q carries an ASIN or ISBN", c)
		}
		if head, _, ok := strings.Cut(c, " - "); ok && pc.namesAPersonNormalized(head) {
			return fmt.Sprintf("%q starts with a person's name (an \"Author - …\" filename)", c)
		}
	}
	return ""
}

// namesAPersonNormalized compares normalized names, and a "Last, First"
// spelling in either position ("Herbert, Frank" is Frank Herbert).
func (pc junkProposalCheck) namesAPersonNormalized(s string) bool {
	forms := func(n string) []string {
		out := []string{util.NormalizeAuthor(n)}
		if last, first, ok := strings.Cut(n, ","); ok && !strings.Contains(first, ",") &&
			strings.TrimSpace(first) != "" && strings.TrimSpace(last) != "" {
			out = append(out, util.NormalizeAuthor(strings.TrimSpace(first)+" "+strings.TrimSpace(last)))
		}
		return out
	}
	mine := forms(s)
	for _, p := range pc.people {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		for _, a := range forms(p) {
			for _, b := range mine {
				if a != "" && a == b {
					return true
				}
			}
		}
	}
	return false
}

// isPathRoot: c is the first segment of one of the book's paths ("lib" of
// "/lib/A/intro.mp3"), a segment of a configured library root or import
// path, or the author folder right below an author-first library root.
func (pc junkProposalCheck) isPathRoot(c string) bool {
	eq := func(seg string) bool { return seg != "" && strings.EqualFold(seg, c) }
	for _, p := range pc.paths {
		if first, _, _ := strings.Cut(strings.TrimPrefix(filepath.ToSlash(p), "/"), "/"); eq(first) {
			return true
		}
	}
	for _, root := range pc.roots {
		for seg := range strings.SplitSeq(filepath.ToSlash(root), "/") {
			if eq(seg) {
				return true
			}
		}
	}
	if pc.authorRoot == "" {
		return false
	}
	for _, p := range pc.paths {
		rel, err := filepath.Rel(pc.authorRoot, p)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			continue
		}
		if first, _, more := strings.Cut(filepath.ToSlash(rel), "/"); more && eq(first) {
			return true
		}
	}
	return false
}

// fragmentReason decides whether a junk-titled book is (or may be) a chapter
// file of a bigger book. It returns junkSkipFragment only when another live
// book owns one of its paths; junkSkipPossibleFragment for the shapes that
// suggest it; junkSkipNeedsManual for a bare unpadded number, which is as
// likely a real title ("13", "300"); "" when the book stands on its own.
func (f *junkTitleFixer) fragmentReason(idx *junkIndex, b database.BookCore, kind metadata.JunkTitleKind, author string, paths []string) (skip, why string, err error) {
	store := f.p.deps.OpsStore()
	for _, p := range paths {
		owners := append([]string(nil), idx.owners[p]...)
		sort.Strings(owners)
		for _, o := range owners {
			if o != b.ID {
				return junkSkipFragment, fmt.Sprintf("its file %s is also a file of book %s", p, o), nil
			}
		}
	}
	for _, p := range paths {
		row, err := store.GetBookFileByPath(p)
		if err != nil {
			return "", "", fmt.Errorf("read owner of %s: %w", p, err)
		}
		if row != nil && row.BookID != "" && row.BookID != b.ID {
			return junkSkipFragment, fmt.Sprintf("its file %s is also a file of book %s", p, row.BookID), nil
		}
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		dirs[junkWorkDir(p, b.Title)] = true
	}
	if d := junkWorkDir(b.FilePath, b.Title); d != "" {
		dirs[d] = true
	}
	keys := make([]string, 0, len(dirs))
	for d := range dirs {
		if d != "" {
			keys = append(keys, d)
		}
	}
	sort.Strings(keys)
	// The organizer-moved chapter: author "Eldest", filed at
	// Eldest/02/98/98.mp3 (work folder "Eldest", junkWorkDir). The author
	// named like the work folder is the shape a chapter file parsed as its
	// own book gets. Only the work folder: the folder above it is the author
	// folder of every correctly filed book. And not for a single file that
	// sits directly in its folder: "/lib/Author A/13.m4b" is filed in the
	// author folder by design.
	if a := strings.TrimSpace(author); a != "" && !authorname.IsPlaceholderAuthor(a) {
		for _, p := range append(slices.Clone(paths), b.FilePath) {
			d := junkWorkDir(p, b.Title)
			if d == "" {
				continue
			}
			if _, audio := audioExts[strings.ToLower(filepath.Ext(p))]; audio && d == filepath.Dir(p) {
				continue
			}
			if strings.EqualFold(a, filepath.Base(d)) {
				return junkSkipPossibleFragment, fmt.Sprintf(
					"its author %q is the name of its folder %s, the shape a chapter file parsed as its own book gets", a, d), nil
			}
		}
	}
	bare := bareNumberRe.MatchString(strings.TrimSpace(b.Title))
	for _, d := range keys {
		// A chapter-titled book sharing its folder with any other book may be
		// a piece of it. Any other junk title ("Opening", "read by narrator")
		// is suspect only beside chapter-titled siblings: single-file books
		// of different works often share an author folder.
		others := idx.dirChapters[d]
		// A track-number prefix beside other books is the tracks-of-one-book
		// shape too ("01 - Chapter One", "02 - Chapter Two").
		if kind.IsChapterKind() || kind == metadata.JunkNumberPrefix {
			others = idx.dirBooks[d] - 1
		}
		// A bare unpadded number ("13") is as likely a real title: beside
		// real-titled books of its author it stays on its own; only beside
		// other chapter-titled books ("1".."20") is it a possible fragment.
		if bare {
			others = idx.dirChapters[d] - 1
		}
		if others >= 1 {
			return junkSkipPossibleFragment, fmt.Sprintf("%d books share the folder %s", idx.dirBooks[d], d), nil
		}
	}
	// A bare unpadded number alone in its folder may be the real title
	// ("13", "300"); beside sibling books ("1".."20" in one folder) it was
	// caught above as a possible fragment.
	if bare {
		return junkSkipNeedsManual, "the title is a bare number, which may be the real title; nothing proves it is a chapter", nil
	}
	if kind.IsChapterKind() && len(paths) < 2 {
		return junkSkipPossibleFragment, "a single file titled only by a chapter position", nil
	}
	return "", "", nil
}

// candidateTitle returns the highest-scoring cached metadata candidate's
// title when it is trustworthy: score at least junkCandidateMinScore, no
// runtime mismatch, and the same author as the book (the search ran on the
// junk title, so a candidate that disagrees on the author is noise). Ties
// keep the cache's order.
func (f *junkTitleFixer) candidateTitle(bookID, author string) (string, float64, bool) {
	cs := f.p.deps.MetadataCacheStore()
	if cs == nil {
		return "", 0, false
	}
	entry, err := cs.GetMetadataCache(bookID)
	if err != nil || entry == nil {
		return "", 0, false
	}
	a := strings.TrimSpace(author)
	if a == "" || authorname.IsPlaceholderAuthor(a) {
		return "", 0, false
	}
	best, bestScore, found := "", 0.0, false
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
		if !found || c.Score > bestScore {
			best, bestScore, found = c.Title, c.Score, true
		}
	}
	return best, bestScore, found
}

// junkFingerprint hashes the decision's inputs and outputs.
func junkFingerprint(r repairs.Row, extra string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s\n%s\n%s\n%s\n%s\n", r.RowID, r.Current["title"], r.Skipped, r.Proposed["title"], extra)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])[:32]
}
