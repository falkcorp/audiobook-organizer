// file: core/internal/libraryhttp/browse.go
// version: 1.1.1
// guid: 969840c6-2d7a-4a37-85c0-43a9e1298b2f
// last-edited: 2026-11-11

package libraryhttp

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"sort"
	"strings"

	"github.com/falkcorp/mediacore/library"
)

// maxEntries caps one listing; a longer directory answers truncated: true. A
// truncated listing is the first maxEntries directories in the disk's own order,
// sorted by name: the read stops once one more than the cap has been kept, so
// the cost of a request is bounded whatever the size of the directory, and the
// entries after the cap are not the alphabetically later ones.
const maxEntries = 5000

// dirReadChunk is how many entries one ReadDir call reads, so a directory of
// millions of entries streams instead of being held whole.
const dirReadChunk = 256

// registryCaller filters the registry for the roots listing: core sees every
// registered root. It is the registry's reading right, never a filesystem
// view: a request-driven read of a directory goes through Root.GuardedFS,
// the read-only view that hides the root's AppDir and Quarantine area and
// examines the opened descriptor, because the route's caller is a person and
// not core, and core's own unguarded view is for core's own code.
const registryCaller = library.CoreOwner

// rootInfo is one entry of the roots listing.
type rootInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	Path string `json:"path"` // "<root id>:", the root directory as a path
}

type rootsResponse struct {
	Roots []rootInfo `json:"roots"`
}

// pathRef names a directory: a root and a root-relative path ("" is the root).
type pathRef struct {
	Root string `json:"root"`
	Rel  string `json:"rel"`
}

// dirEntry is one listed directory. Only directories are listed, so IsDir is
// always true; the field keeps the picker's shape explicit.
type dirEntry struct {
	Name  string `json:"name"`
	Rel   string `json:"rel"`
	IsDir bool   `json:"is_dir"`
}

type listingResponse struct {
	Root      string     `json:"root"`
	Rel       string     `json:"rel"`
	Parent    *pathRef   `json:"parent"` // null for the root itself
	Entries   []dirEntry `json:"entries"`
	Truncated bool       `json:"truncated,omitempty"`
}

// browse answers GET /browse: the readable roots without a root parameter,
// otherwise the directories directly under root/rel.
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rootParam := q.Get("root")
	if rootParam == "" {
		if q.Get("rel") != "" {
			s.write(w, r, problem{Status: http.StatusBadRequest, Code: "library.invalid_path", Detail: "rel needs root"})
			return
		}
		s.listRoots(w)
		return
	}
	rel := q.Get("rel")
	if rel != "" {
		if err := validWireRel(rel); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	root, err := s.lib.Root(library.RootID(rootParam))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p := hiddenPath(root.Spec(), rel); p != nil {
		s.write(w, r, *p)
		return
	}
	// GuardedFS refuses a disabled root; reads are not gated on the mount, so an
	// unmounted share (which reads as an empty tree) is refused by Mounted first.
	if err := root.Mounted(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	fsys, err := root.GuardedFS()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if p := s.checkDirs(fsys, rel); p != nil {
		s.write(w, r, *p)
		return
	}
	dir := rel
	if dir == "" {
		dir = "."
	}
	f, err := fsys.Open(dir)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer s.closeLogged(f, rel)
	rd, ok := f.(fs.ReadDirFile)
	if !ok {
		s.fail(w, r, errors.New("libraryhttp: the root's files do not list"))
		return
	}
	entries, truncated, err := readDirs(rd, root.Spec(), rel, maxEntries)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.json(w, r, listingResponse{Root: rootParam, Rel: rel, Parent: parentOf(rootParam, rel), Entries: entries, Truncated: truncated})
}

// readDirs streams the directories directly under rel (the directory d lists),
// dirReadChunk entries at a time, keeping only what the picker shows: a real
// directory (a symlink is not one, whatever it points at), not dot-prefixed, not
// the root's AppDir or Quarantine area. It stops reading as soon as one more
// than limit has been kept, and reports truncated, so the work is bounded by the
// cap and not by the directory. The kept entries are sorted by name.
func readDirs(d fs.ReadDirFile, spec library.RootSpec, rel string, limit int) (entries []dirEntry, truncated bool, err error) {
	entries = []dirEntry{}
	for {
		ents, rerr := d.ReadDir(dirReadChunk)
		for _, e := range ents {
			name := e.Name()
			// DirEntry.Type comes from the directory read without following
			// links, so a symlink (to a directory or not) is not IsDir.
			if !e.Type().IsDir() || strings.HasPrefix(name, ".") {
				continue
			}
			erel := name
			if rel != "" {
				erel = rel + "/" + name
			}
			if isReserved(spec, erel) {
				continue
			}
			if len(entries) == limit {
				sortEntries(entries)
				return entries, true, nil
			}
			entries = append(entries, dirEntry{Name: name, Rel: erel, IsDir: true})
		}
		switch {
		case rerr == nil:
		case errors.Is(rerr, io.EOF):
			sortEntries(entries)
			return entries, false, nil
		default:
			return nil, false, rerr
		}
	}
}

func sortEntries(entries []dirEntry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
}

func (s *Server) listRoots(w http.ResponseWriter) {
	// Readable with an Owner keeps the roots that owner may read; for core
	// that is every registered root.
	roots := s.lib.Roots(library.RootFilter{Owner: registryCaller, Readable: true})
	out := rootsResponse{Roots: make([]rootInfo, 0, len(roots))}
	for _, rt := range roots {
		sp := rt.Spec()
		name := sp.Name
		if name == "" {
			name = string(sp.ID)
		}
		out.Roots = append(out.Roots, rootInfo{ID: string(sp.ID), Name: name, Role: sp.Role.String(), Path: string(sp.ID) + ":"})
	}
	b, err := json.Marshal(out)
	if err != nil {
		s.log.Error("libraryhttp: encode roots", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, werr := w.Write(append(b, '\n')); werr != nil {
		s.log.Warn("libraryhttp: write roots", "err", werr)
	}
}

// closeLogged closes the directory a request listed and logs a failure: the
// listing was already read, so a close error changes no answer, but it is
// evidence about the disk and must not vanish.
func (s *Server) closeLogged(c io.Closer, what string) {
	if err := c.Close(); err != nil {
		s.log.Warn("libraryhttp: could not close a directory", "dir", what, "err", err)
	}
}

// json writes v as the 200 body.
func (s *Server) json(w http.ResponseWriter, r *http.Request, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(append(b, '\n')); err != nil {
		s.log.Warn("libraryhttp: write response", "err", err)
	}
}

// validWireRel is library.ValidateRel plus the backslash: a Windows separator
// is a path the caller got wrong, never a directory name worth reaching.
func validWireRel(rel string) error {
	if strings.ContainsRune(rel, '\\') {
		return library.ErrBadRel
	}
	return library.ValidateRel(rel)
}

// parentOf is the directory above rel: nil for the root itself, the root for a
// top-level directory.
func parentOf(root, rel string) *pathRef {
	if rel == "" {
		return nil
	}
	return &pathRef{Root: root, Rel: library.Path{Root: library.RootID(root), Rel: rel}.Dir().Rel}
}

// hiddenPath is the refusal for a rel that is, or is under, a dot-prefixed
// directory or the root's AppDir or Quarantine area. It is purely syntactic,
// so the answer is the same whether or not the name exists.
func hiddenPath(spec library.RootSpec, rel string) *problem {
	if rel == "" {
		return nil
	}
	hidden := isReserved(spec, rel)
	for c := range strings.SplitSeq(rel, "/") {
		if strings.HasPrefix(c, ".") {
			hidden = true
		}
	}
	if !hidden {
		return nil
	}
	return &problem{Status: http.StatusForbidden, Code: "library.path_hidden", Detail: "this directory is not browsable"}
}

// isReserved reports whether rel is the root's AppDir or Quarantine area or
// under one. The match ignores case: a case-insensitive volume reaches the
// same directory by another spelling.
func isReserved(spec library.RootSpec, rel string) bool {
	for _, dir := range []string{spec.AppDir, spec.Quarantine} {
		if dir == "" {
			continue
		}
		if strings.EqualFold(rel, dir) {
			return true
		}
		if len(rel) > len(dir) && rel[len(dir)] == '/' && strings.EqualFold(rel[:len(dir)], dir) {
			return true
		}
	}
	return false
}

// checkDirs requires every component of rel to be a real directory: Lstat does
// not follow, so a symlink to a directory, on the way or at the end, is
// refused instead of followed.
func (s *Server) checkDirs(fsys library.RootReader, rel string) *problem {
	if rel == "" {
		return nil
	}
	parts := strings.Split(rel, "/")
	for i := range parts {
		fi, err := fsys.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil {
			p, ok := mapError(err)
			if !ok {
				s.log.Error("libraryhttp: lstat", "err", err)
				p = problem{Status: http.StatusInternalServerError, Code: "internal"}
			}
			return &p
		}
		if !fi.Mode().IsDir() {
			return &problem{Status: http.StatusBadRequest, Code: "library.not_a_directory", Detail: "not a directory"}
		}
	}
	return nil
}
