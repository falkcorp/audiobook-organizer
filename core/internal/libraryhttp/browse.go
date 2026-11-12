// file: core/internal/libraryhttp/browse.go
// version: 1.0.0
// guid: 969840c6-2d7a-4a37-85c0-43a9e1298b2f
// last-edited: 2026-11-11

package libraryhttp

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/falkcorp/mediacore/library"
)

// maxEntries caps one listing; a longer directory answers truncated: true.
const maxEntries = 5000

// browseCaller is who the picker reads the filesystem as. The route's caller
// is a human holding core:library.browse, not a module, so it acts as core:
// manager.canRead lets library.CoreOwner read every registered root, and
// Root.FS(CoreOwner) is the confined RootFS. The route's own permission is
// the allow-list's gate; the filesystem views stay read-only for listing
// because this package only calls ReadDir and Lstat.
const browseCaller = library.CoreOwner

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
	// FS refuses a caller that may not read the root and a disabled root; reads
	// are not gated on the mount, so an unmounted share (which reads as an
	// empty tree) is refused by Mounted first.
	if err := root.Mounted(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	fsys, err := root.FS(browseCaller)
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
	ents, err := fsys.ReadDir(dir)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	spec := root.Spec()
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	out := listingResponse{Root: rootParam, Rel: rel, Parent: parentOf(rootParam, rel), Entries: []dirEntry{}}
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
		if len(out.Entries) == maxEntries {
			out.Truncated = true
			break
		}
		out.Entries = append(out.Entries, dirEntry{Name: name, Rel: erel, IsDir: true})
	}
	s.json(w, r, out)
}

func (s *Server) listRoots(w http.ResponseWriter) {
	// Readable with an Owner keeps the roots that owner may read; for core
	// that is every registered root.
	roots := s.lib.Roots(library.RootFilter{Owner: browseCaller, Readable: true})
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
	_, _ = w.Write(append(b, '\n'))
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
	for _, c := range strings.Split(rel, "/") {
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
func (s *Server) checkDirs(fsys library.RootFS, rel string) *problem {
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
