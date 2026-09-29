// file: internal/repairs/guards.go
// version: 1.5.0
// guid: 5a2c9e14-6f3b-4d87-b0e1-9c7d4a8f2e56
// last-edited: 2026-09-29

package repairs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"

	"github.com/falkcorp/audiobook-organizer/internal/applygate"
	"github.com/falkcorp/audiobook-organizer/internal/database"
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
	if res == nil {
		res = NewPathResolver()
	}
	paths = res.withResolved(paths)
	for _, p := range paths {
		if p != "" && pathutil.UnderFrozenITunesTree(p) {
			return SkipITunes, fmt.Sprintf("member %s has a file under books/itunes/** (hands-off): %s", bookID, p)
		}
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
// with one Lstat of the file itself; only a file that is itself a symlink is
// resolved in full. Built per plan or apply run, so a link created since an
// earlier run is seen. Safe for concurrent use.
type PathResolver struct {
	mu   sync.Mutex
	dirs map[string]resolvedDir
}

type resolvedDir struct {
	path string
	ok   bool
}

// NewPathResolver returns an empty resolver.
func NewPathResolver() *PathResolver { return &PathResolver{dirs: map[string]resolvedDir{}} }

func (r *PathResolver) dir(d string) (string, bool) {
	r.mu.Lock()
	got, hit := r.dirs[d]
	r.mu.Unlock()
	if hit {
		return got.path, got.ok
	}
	p, err := filepath.EvalSymlinks(d)
	got = resolvedDir{path: p, ok: err == nil}
	r.mu.Lock()
	r.dirs[d] = got
	r.mu.Unlock()
	return got.path, got.ok
}

// Resolve returns the path p's symlinks resolve to. ok is false when neither
// p (if it is itself a link) nor its folder can be resolved: p is gone with
// its folder, or unreadable; the guard then checks it lexically only.
func (r *PathResolver) Resolve(p string) (string, bool) {
	if fi, err := os.Lstat(p); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp, true
		}
		// A dangling link: fall back to its folder's resolution, so a dead
		// link inside a folder linked into books/itunes/** still guards.
	}
	d, ok := r.dir(filepath.Dir(p))
	if !ok {
		return "", false
	}
	return filepath.Join(d, filepath.Base(p)), true
}

// withResolved adds, after each path, the path its symlinks resolve to when
// that differs: a library symlink into books/itunes/** is under the frozen
// tree however it is spelled. A path that cannot be resolved is checked
// lexically only.
func (r *PathResolver) withResolved(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, p)
		if p == "" {
			continue
		}
		if rp, ok := r.Resolve(p); ok && rp != p {
			out = append(out, rp)
		}
	}
	return out
}

// manualOnlyTitleRe: see GuardBookTitle. Separators inside the names vary
// across rips ("Doctor.Who", "DoctorWho") as in applygate's path pattern.
var manualOnlyTitleRe = regexp.MustCompile(`(?i)` +
	`^\s*torchwood\b` + // leading
	`|[-–—:|(\[]\s*torchwood\s*[)\]]?\s*$` + // trailing tag
	`|\bbig[\s._-]*finish[\s._-]*(?:productions|ident|audio)\b`) // the studio

// doctorWhoTitleRe finds "Doctor Who" anywhere in a title; a title naming it
// is manual-only unless every mention is the prose shape doctorWhoProseRe.
var doctorWhoTitleRe = regexp.MustCompile(`(?i)\bdoctor[\s._-]*who\b`)

// doctorWhoProseRe is the one known false-positive shape: an article, then
// "doctor who" and a past-tense verb ("The Doctor Who Fooled the World", "A
// Doctor Who Cared") -- a doctor, not the franchise.
var doctorWhoProseRe = regexp.MustCompile(`(?i)\b(?:the|a|an)\s+(doctor\s+who)\s+[a-z]+ed\b`)

// namesDoctorWho reports whether title mentions Doctor Who other than in the
// prose shape. It fails toward true: a false positive only skips a row, a
// miss bulk-applies owner-manual content. The caller folds "_" first
// (applygate.FoldUnderscores).
func namesDoctorWho(title string) bool {
	prose := map[int]bool{}
	for _, m := range doctorWhoProseRe.FindAllStringSubmatchIndex(title, -1) {
		prose[m[2]] = true
	}
	for _, m := range doctorWhoTitleRe.FindAllStringIndex(title, -1) {
		if !prose[m[0]] {
			return true
		}
	}
	return false
}

// GuardBookTitle is the owner-manual check on a book's title. Paths and
// series miss a Doctor Who / Big Finish / Torchwood book whose files sit on a
// neutral path with no series row; its title still names it.
//
// "Doctor Who" anywhere in the title counts ("Nelvana Doctor Who", "The
// Language of Doctor Who"), except the prose shape "The Doctor Who Fooled
// the World" (namesDoctorWho); the check fails toward skipping, since a false
// positive only holds a row. Torchwood must lead ("Torchwood: ...") or be a
// separated tag ("... - Torchwood"), and the studio must be named in full
// ("Big Finish Productions"): "Secrets of the Torchwood Estate" and "Big
// Finish to the Season" are prose.
func GuardBookTitle(bookID, title string) (kind, reason string) {
	// "_" is a regexp word character, so \b misses "Doctor Who_ Mindwarp"
	// (the organizer's "_ " for a colon) until it is folded to a space.
	folded := applygate.FoldUnderscores(title)
	if namesDoctorWho(folded) || manualOnlyTitleRe.MatchString(folded) {
		return SkipOwnerManual, fmt.Sprintf("member %s is Doctor Who / Big Finish / Torchwood (title %q); owner applies these by hand", bookID, title)
	}
	return "", ""
}

// GuardReader is what the framework guard reads.
type GuardReader interface {
	GetBookByID(id string) (*database.Book, error)
	GetBookFiles(bookID string) ([]database.BookFile, error)
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

// GuardBooks runs GuardBookPathsWith (through res) over every book id, reading each book and
// its files fresh. A book that is gone or soft-deleted is not checked (it
// has nothing left to protect), matching the vg op's member guard. A read
// error is returned: without the paths the guard cannot see a hands-off book,
// so the caller must not treat the row as clear.
func GuardBooks(r GuardReader, series SeriesNamer, res *PathResolver, bookIDs []string) (kind, reason string, err error) {
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
		if k, why := GuardBookPathsWith(res, id, paths, name); k != "" {
			return k, why, nil
		}
		// The title is evidence too: "Doctor Who: Placebo Effect" on a
		// neutral path with no series row is still a Doctor Who book.
		if k, why := GuardBookTitle(id, b.Title); k != "" {
			return k, why, nil
		}
	}
	return "", "", nil
}
