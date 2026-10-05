// file: internal/repairs/guards.go
// version: 1.9.0
// guid: 5a2c9e14-6f3b-4d87-b0e1-9c7d4a8f2e56
// last-edited: 2026-10-04

package repairs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/franchise"
	"github.com/falkcorp/audiobook-organizer/internal/pathutil"
)

// Guard skip kinds. They are also the decision kinds
// maintenance.version-group-primary-repair reports for the same groups, so a
// row skipped here reads the same in both places.
const (
	// SkipITunes: a book of the row has a file (active or missing) under
	// books/itunes/**, the live iTunes library, which is hands-off.
	SkipITunes = "skipped_itunes"
	// SkipOwnerManual: a book of the row is Doctor Who / Big Finish /
	// Torchwood by path, series or title; the owner applies those by hand.
	SkipOwnerManual = "skipped_owner_manual"
)

// GuardBookPaths is the one hands-off check for a single book. paths should
// hold Book.FilePath and the path of every book_file row, missing rows
// included: the rules are about where the book lives, not whether its file
// is on disk right now. seriesName is the book's series name ("" for none).
// It returns "" when the book may be touched.
//
// It resolves symlinks with a fresh PathResolver; a caller checking many books
// in one run passes its own through GuardBookPathsWith.
func GuardBookPaths(bookID string, paths []string, seriesName string) (kind, reason string) {
	return GuardBookPathsWith(nil, bookID, paths, seriesName)
}

// GuardBookPathsWith is GuardBookPaths resolving symlinks through res (nil:
// a fresh resolver for this call).
func GuardBookPathsWith(res *PathResolver, bookID string, paths []string, seriesName string) (kind, reason string) {
	return guardBookPaths(res, bookID, paths, seriesName, false)
}

// ITunesDatabaseOnly is implemented by a fixer the owner has cleared to write
// the DATABASE rows of books whose files sit under books/itunes/** (owner
// decision 2026-10-01, folder-books only). For such a fixer the guard skips
// the iTunes path check; Doctor Who / Big Finish / Torchwood stays guarded.
// The fixer itself must never move, rename or delete anything there and never
// change an iTunes persistent id.
type ITunesDatabaseOnly interface {
	ITunesDatabaseOnly() bool
}

// AllowsITunesDatabaseOnly reports whether f opted out of the iTunes path
// guard (ITunesDatabaseOnly).
func AllowsITunesDatabaseOnly(f Fixer) bool {
	x, ok := f.(ITunesDatabaseOnly)
	return ok && x.ITunesDatabaseOnly()
}

// BookTagsOnly is implemented by a fixer whose apply writes nothing but
// book_tag rows (maintenance.tag-franchise): no book field, no book_file row,
// no file on disk and nothing an iTunes library file reads. Tagging Doctor
// Who / Big Finish / Torchwood books -- iTunes ones included -- is that
// fixer's whole job, so for it the framework guard is skipped entirely: the
// owner-manual and iTunes rules protect metadata, files and the ITL, none of
// which it can reach. Writer gives it the tag primitive (AddBookTag) and
// nothing else it uses.
type BookTagsOnly interface {
	BookTagsOnly() bool
}

// AllowsBookTagsOnly reports whether f opted out of the framework guard
// (BookTagsOnly).
func AllowsBookTagsOnly(f Fixer) bool {
	x, ok := f.(BookTagsOnly)
	return ok && x.BookTagsOnly()
}

func guardBookPaths(res *PathResolver, bookID string, paths []string, seriesName string, allowITunes bool) (kind, reason string) {
	if res == nil {
		res = NewPathResolver()
	}
	paths, doubt := res.withResolved(paths)
	for _, p := range paths {
		if allowITunes {
			break
		}
		if p != "" && pathutil.UnderFrozenITunesTree(p) {
			return SkipITunes, fmt.Sprintf("member %s has a file under books/itunes/** (hands-off): %s", bookID, p)
		}
	}
	// Fail closed: a path whose location could not be settled may be under
	// books/itunes/** or a Doctor Who / Big Finish / Torchwood folder behind a
	// symlink, so the row is skipped, never cleared -- for an iTunes-cleared
	// fixer too.
	if doubt != nil {
		return SkipGuardUnreadable, fmt.Sprintf("member %s: could not tell whether a file is under books/itunes/**: %v", bookID, doubt)
	}
	for _, p := range paths {
		if applygate.IsOwnerManualOnly(p, seriesName) {
			return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (path %q, series %q); owner applies these by hand", bookID, p, seriesName)
		}
	}
	// A series match with no paths at all still counts.
	if len(paths) == 0 && applygate.IsOwnerManualOnly("", seriesName) {
		return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (series %q); owner applies these by hand", bookID, seriesName)
	}
	return "", ""
}

// PathResolver resolves symlinks for the guard, memoizing each directory's
// resolution: a plan or apply checks thousands of files that share a few
// hundred folders, and EvalSymlinks walks (lstats) every component of every
// path it is given. A file is resolved through its folder's cached answer,
// with one Lstat of the file itself; a link is followed hop by hop only when
// EvalSymlinks cannot resolve it (it is dangling). Built per plan or apply
// run, so a link created since an earlier run is seen. Safe for concurrent
// use.
//
// It fails closed: whenever it cannot tell where a path lives (a folder or
// link it cannot read, a link it cannot read the text of, or a chain longer
// than maxLinkHops) it reports doubt, and the guard skips the row
// (SkipGuardUnreadable) instead of clearing it.
type PathResolver struct {
	mu   sync.Mutex
	dirs map[string]resolvedDir
}

type resolvedDir struct {
	path string
	// ok: the folder exists and resolved to path.
	ok bool
	// doubt: EvalSymlinks failed for a reason other than a missing
	// component (a permission error, a loop), so whether the folder exists
	// or where it points is unknown.
	doubt error
}

// NewPathResolver returns an empty resolver.
func NewPathResolver() *PathResolver { return &PathResolver{dirs: map[string]resolvedDir{}} }

// missing reports an error that means a path component does not exist, which
// is an answer, not doubt. ENAMETOOLONG counts: a path with a component (or a
// whole) longer than the system allows cannot exist on disk.
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) ||
		errors.Is(err, syscall.ENAMETOOLONG)
}

// evalSymlinks is filepath.EvalSymlinks; a test counts the calls through it.
var evalSymlinks = filepath.EvalSymlinks

func (r *PathResolver) dir(d string) resolvedDir {
	r.mu.Lock()
	got, hit := r.dirs[d]
	r.mu.Unlock()
	if hit {
		return got
	}
	p, err := evalSymlinks(d)
	switch {
	case err == nil:
		got = resolvedDir{path: p, ok: true}
	case missing(err):
		got = resolvedDir{}
	default:
		got = resolvedDir{doubt: fmt.Errorf("resolve folder %s: %w", d, err)}
	}
	r.mu.Lock()
	r.dirs[d] = got
	r.mu.Unlock()
	return got
}

// maxLinkHops bounds the links one path's walk follows by hand. A chain the
// kernel refuses (more than its 40) is still followed, so a long chain into
// books/itunes/** is caught; one longer than this is doubt.
const maxLinkHops = 255

// resolveWalk is the state of one path's walk: the hops left, the links
// already followed (a revisit is a loop, which points nowhere: not doubt),
// and whether a link has been followed by hand yet.
type resolveWalk struct {
	hops    int
	visited map[string]bool
	// byHand: EvalSymlinks already failed on the path the walk started
	// from, so every later hop is on that same chain and would fail too.
	// Asking it again at each hop re-walks the rest of the chain every time
	// (quadratic in the chain's length); the walk goes on by hand instead.
	byHand bool
}

// spellings returns the paths p resolves to, besides p: the resolved path of
// a live path, and for a dangling link or a missing path every path its link
// texts name, each resolved through its longest existing ancestor. doubt is
// non-nil when some step could not be read; the spellings found so far are
// still returned.
func (r *PathResolver) spellings(p string) (out []string, doubt error) {
	w := &resolveWalk{hops: maxLinkHops, visited: map[string]bool{}}
	return r.walk(p, w)
}

func (r *PathResolver) walk(p string, w *resolveWalk) ([]string, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		if !missing(err) {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		return r.walkMissing(p, w)
	}
	if fi.Mode()&os.ModeSymlink != 0 && !w.byHand {
		if rp, err := evalSymlinks(p); err == nil {
			return []string{rp}, nil
		}
	}
	// p exists: where it sits is its folder's resolution. A dangling link
	// also lives wherever its text points.
	d := r.dir(filepath.Dir(p))
	if !d.ok {
		if d.doubt != nil {
			return nil, d.doubt
		}
		return nil, fmt.Errorf("resolve %s: its folder vanished during the check", p)
	}
	own := filepath.Join(d.path, filepath.Base(p))
	if fi.Mode()&os.ModeSymlink == 0 {
		return []string{own}, nil
	}
	targets, doubt := r.follow(p, w)
	return append([]string{own}, targets...), doubt
}

// walkMissing resolves a path that does not exist through its longest
// existing ancestor A. Below A, the first component c does not exist per
// EvalSymlinks, but may be a dangling link: then the path lives wherever that
// link points, with the rest rejoined. Otherwise nothing below A exists, so
// nothing below it can be a link, and A's resolution plus the rest is the
// answer.
func (r *PathResolver) walkMissing(p string, w *resolveWalk) ([]string, error) {
	dir, rest := filepath.Dir(p), ""
	c := filepath.Base(p)
	for {
		d := r.dir(dir)
		if d.doubt != nil {
			return nil, d.doubt
		}
		if d.ok {
			first := filepath.Join(d.path, c)
			if fi, err := os.Lstat(first); err == nil && fi.Mode()&os.ModeSymlink != 0 {
				targets, doubt := r.follow(first, w)
				out := []string{filepath.Join(first, rest)}
				for _, t := range targets {
					out = append(out, filepath.Join(t, rest))
				}
				return out, doubt
			} else if err != nil && !missing(err) {
				return nil, fmt.Errorf("read %s: %w", first, err)
			}
			return []string{filepath.Join(first, rest)}, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil
		}
		rest = filepath.Join(c, rest)
		c = filepath.Base(dir)
		dir = parent
	}
}

// follow reads the dangling link's text, joins a relative text to the link's
// resolved folder, and walks the target: every path it names is returned.
func (r *PathResolver) follow(link string, w *resolveWalk) ([]string, error) {
	if w.visited[link] {
		return nil, nil // a loop points nowhere
	}
	w.visited[link] = true
	w.byHand = true
	if w.hops == 0 {
		return nil, fmt.Errorf("resolve %s: more than %d links", link, maxLinkHops)
	}
	w.hops--
	text, err := os.Readlink(link)
	if err != nil {
		return nil, fmt.Errorf("read link %s: %w", link, err)
	}
	if !filepath.IsAbs(text) {
		d := r.dir(filepath.Dir(link))
		base := filepath.Dir(link)
		switch {
		case d.ok:
			base = d.path
		case d.doubt != nil:
			return nil, d.doubt
		}
		text = filepath.Join(base, text)
	}
	text = filepath.Clean(text)
	more, doubt := r.walk(text, w)
	return append([]string{text}, more...), doubt
}

// withResolved adds, after each path, the other paths it resolves to (see
// spellings): a library symlink into books/itunes/** is under the frozen tree
// however it is spelled, dead or alive. doubt is the first path the resolver
// could not settle.
func (r *PathResolver) withResolved(paths []string) (out []string, doubt error) {
	out = make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, p)
		if p == "" {
			continue
		}
		more, err := r.spellings(p)
		for _, m := range more {
			if m != p {
				out = append(out, m)
			}
		}
		if err != nil && doubt == nil {
			doubt = err
		}
	}
	return out, doubt
}

// GuardBookTitle is the owner-manual check on a book's title. Paths and
// series miss a Doctor Who / Big Finish / Torchwood book whose files sit on a
// neutral path with no series row; its title still names it.
//
// "Doctor Who" anywhere in the title counts ("Nelvana Doctor Who", "The
// Language of Doctor Who"), except the prose shape "The Doctor Who Fooled
// the World" (franchise.MatchTitle); the check fails toward skipping, since a false
// positive only holds a row. Torchwood must lead ("Torchwood: ...") or be a
// separated tag ("... - Torchwood"), and the studio must be named in full
// ("Big Finish Productions"): "Secrets of the Torchwood Estate" and "Big
// Finish to the Season" are prose.
func GuardBookTitle(bookID, title string) (kind, reason string) {
	// The rule lives in internal/franchise (MatchTitle), with the census
	// range terms ("Genesis of the Cybermen", "Short Trips - ...").
	if _, ok := franchise.MatchTitle(title); ok {
		return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (title %q); owner applies these by hand", bookID, title)
	}
	return "", ""
}

// GuardBookCredits is the owner-manual check on a book's credits: its
// publisher, its authors and its narrators (BookNarratorNames: the narrator
// field and the book_narrators rows), through the same pattern as its paths
// and series (applygate.IsOwnerManualOnly). A Big Finish release on a neutral
// path with a neutral title ("Michael Fenton Stevens / The Ultimate Foe",
// publisher "Big Finish Productions") names the studio only there.
func GuardBookCredits(bookID, publisher string, narrators, authors []string) (kind, reason string) {
	check := func(field, v string) (string, string) {
		if v != "" && applygate.IsOwnerManualOnly(v, "") {
			return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (%s %q); owner applies these by hand", bookID, field, v)
		}
		return "", ""
	}
	if k, w := check("publisher", publisher); k != "" {
		return k, w
	}
	for _, a := range authors {
		if k, w := check("author", a); k != "" {
			return k, w
		}
	}
	for _, n := range narrators {
		if k, w := check("narrator", n); k != "" {
			return k, w
		}
	}
	return "", ""
}

// GuardReader is what the framework guard reads.
type GuardReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
	// The credits check (GuardBookCredits) names a book's authors.
	GetBookAuthors(bookID string) ([]database.BookAuthor, error)
	GetAuthorByID(id int) (*database.Author, error)
	// And its narrators (BookNarratorNames).
	GetBookNarrators(bookID string) ([]database.BookNarrator, error)
	GetNarratorByID(id int) (*database.Narrator, error)
}

// BookNarratorNames reads every narrator name of b: its narrator field and
// its book_narrators rows. A read error is returned: the credits check cannot
// clear a book whose narrators it could not see.
func BookNarratorNames(r GuardReader, b *database.Book) ([]string, error) {
	var names []string
	if n := strOf(b.Narrator); n != "" {
		names = append(names, n)
	}
	links, err := r.GetBookNarrators(b.ID)
	if err != nil {
		return nil, fmt.Errorf("guard: read narrators of %s: %w", b.ID, err)
	}
	ids := make([]int, 0, len(links))
	for _, l := range links {
		ids = append(ids, l.NarratorID)
	}
	sort.Ints(ids)
	for _, id := range ids {
		n, err := r.GetNarratorByID(id)
		if err != nil {
			return nil, fmt.Errorf("guard: read narrator %d of %s: %w", id, b.ID, err)
		}
		if n != nil && n.Name != "" {
			names = append(names, n.Name)
		}
	}
	return names, nil
}

// BookAuthorNames reads every author name of b: its primary author and its
// book_authors rows. A read error is returned: the credits check cannot clear
// a book whose authors it could not see.
func BookAuthorNames(r GuardReader, b *database.Book) ([]string, error) {
	ids := map[int]bool{}
	if b.AuthorID != nil {
		ids[*b.AuthorID] = true
	}
	links, err := r.GetBookAuthors(b.ID)
	if err != nil {
		return nil, fmt.Errorf("guard: read authors of %s: %w", b.ID, err)
	}
	for _, l := range links {
		ids[l.AuthorID] = true
	}
	sorted := make([]int, 0, len(ids))
	for id := range ids {
		sorted = append(sorted, id)
	}
	sort.Ints(sorted)
	var names []string
	for _, id := range sorted {
		a, err := r.GetAuthorByID(id)
		if err != nil {
			return nil, fmt.Errorf("guard: read author %d of %s: %w", id, b.ID, err)
		}
		if a != nil && a.Name != "" {
			names = append(names, a.Name)
		}
	}
	return names, nil
}

func strOf(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// SeriesNamer resolves a series id to its name. Built once per run.
type SeriesNamer func(id int) string

// SeriesNamesFrom builds a SeriesNamer from a series list.
func SeriesNamesFrom(all []database.Series) SeriesNamer {
	m := make(map[int]string, len(all))
	for _, s := range all {
		m[s.ID] = s.Name
	}
	return func(id int) string { return m[id] }
}

// GuardTagReader reads a book's tag rows for the framework guard: a book
// carrying a franchise: tag (maintenance.tag-franchise's, or a person's) is
// owner-manual whatever its title or path says now.
type GuardTagReader interface {
	GetBookTagsDetailed(bookID string) ([]database.BookTag, error)
}

// GuardBookTags is the owner-manual check on a book's tags.
func GuardBookTags(bookID string, tags []database.BookTag) (kind, reason string) {
	for _, t := range tags {
		if tag, ok := franchise.HeldByTags([]string{t.Tag}); ok {
			return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (tag %q); owner applies these by hand", bookID, tag)
		}
	}
	return "", ""
}

// GuardBookTranscribed is the owner-manual check on what the intro
// transcription heard, on the book and on each of its files: a blank-titled
// Big Finish rip whose intro says "Doctor Who: The Chimes of Midnight" or
// "Big Finish Productions presents" names it nowhere else.
func GuardBookTranscribed(bookID string, b *database.Book, files []database.BookFile) (kind, reason string) {
	check := func(field string, v *string) (string, string) {
		if v != nil && *v != "" && applygate.IsOwnerManualOnly(*v, "") {
			return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (%s %q); owner applies these by hand", bookID, field, *v)
		}
		return "", ""
	}
	if k, w := check("transcribed title", b.TranscribedTitle); k != "" {
		return k, w
	}
	if k, w := check("transcribed author", b.TranscribedAuthor); k != "" {
		return k, w
	}
	for i := range files {
		if k, w := check("file transcribed title", files[i].TranscribedTitle); k != "" {
			return k, w
		}
		if k, w := check("file transcribed author", files[i].TranscribedAuthor); k != "" {
			return k, w
		}
	}
	return "", ""
}

// GuardBooks runs GuardBookPathsWith (through res) over every book id, reading each book and
// its files fresh. A book that is gone or soft-deleted is not checked (it
// has nothing left to protect), matching the vg op's member guard. A read
// error is returned: without the paths the guard cannot see a hands-off book,
// so the caller must not treat the row as clear.
//
// tags may be nil (a caller with no tag store); the tag check is then
// skipped. Every production caller (repairs_ops.go) passes one.
func GuardBooks(r GuardReader, tags GuardTagReader, series SeriesNamer, res *PathResolver, bookIDs []string) (kind, reason string, err error) {
	return guardBooks(r, tags, series, res, bookIDs, false)
}

// GuardBooksFor is GuardBooks for fixer f: the iTunes path check is skipped
// when f opted out of it (ITunesDatabaseOnly), and the whole guard when f
// writes only book tags (BookTagsOnly).
func GuardBooksFor(f Fixer, r GuardReader, tags GuardTagReader, series SeriesNamer, res *PathResolver, bookIDs []string) (kind, reason string, err error) {
	if AllowsBookTagsOnly(f) {
		return "", "", nil
	}
	return guardBooks(r, tags, series, res, bookIDs, AllowsITunesDatabaseOnly(f))
}

func guardBooks(r GuardReader, tags GuardTagReader, series SeriesNamer, res *PathResolver, bookIDs []string, allowITunes bool) (kind, reason string, err error) {
	ids := append([]string(nil), bookIDs...)
	sort.Strings(ids)
	for _, id := range ids {
		b, err := r.GetBookByID(id)
		if err != nil {
			return "", "", fmt.Errorf("guard: read book %s: %w", id, err)
		}
		if b == nil || b.IsSoftDeleted() {
			continue
		}
		files, err := r.GetBookFiles(id)
		if err != nil {
			return "", "", fmt.Errorf("guard: read files of %s: %w", id, err)
		}
		paths := make([]string, 0, len(files)+1)
		paths = append(paths, b.FilePath)
		for _, f := range files {
			paths = append(paths, f.FilePath)
		}
		name := ""
		if b.SeriesID != nil && series != nil {
			name = series(*b.SeriesID)
		}
		if k, why := guardBookPaths(res, id, paths, name, allowITunes); k != "" {
			return k, why, nil
		}
		// The title is evidence too: "Doctor Who: Placebo Effect" on a
		// neutral path with no series row is still a Doctor Who book.
		if k, why := GuardBookTitle(id, b.Title); k != "" {
			return k, why, nil
		}
		// And the credits: a Big Finish publisher, author or narrator.
		authors, err := BookAuthorNames(r, b)
		if err != nil {
			return "", "", err
		}
		narrators, err := BookNarratorNames(r, b)
		if err != nil {
			return "", "", err
		}
		if k, why := GuardBookCredits(id, strOf(b.Publisher), narrators, authors); k != "" {
			return k, why, nil
		}
		// What the intro transcription heard, on the book and its files.
		if k, why := GuardBookTranscribed(id, b, files); k != "" {
			return k, why, nil
		}
		// And its tags: a franchise: tag holds it whatever else changed.
		if tags != nil {
			rows, err := tags.GetBookTagsDetailed(id)
			if err != nil {
				return "", "", fmt.Errorf("guard: read tags of %s: %w", id, err)
			}
			if k, why := GuardBookTags(id, rows); k != "" {
				return k, why, nil
			}
		}
	}
	return "", "", nil
}
