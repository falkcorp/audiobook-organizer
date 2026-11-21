// file: internal/books/roots/reader.go
// version: 0.1.0
// guid: 651d0286-f7dc-4831-8a5a-f92d8d4b8f0b
// last-edited: 2026-11-20

package roots

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/storage"
)

// ErrNoLibraryRoot is returned when no books-library root is registered (a fresh
// install, before the admin sets one).
var ErrNoLibraryRoot = errors.New("roots: no books-library root is registered")

// ErrNoSuchRoot is returned for an import root that is not registered.
var ErrNoSuchRoot = errors.New("roots: no such import root")

// RootLister is the part of library.Scope the reader calls; library.Scope
// satisfies it.
type RootLister interface {
	Owner() storage.Owner
	Roots(filter library.RootFilter) []*library.ModuleRoot
}

var _ RootLister = library.Scope(nil)

// Root is one root as books reads it: the registered spec's fields books uses,
// as a plain value (a library.ModuleRoot cannot be built outside library).
type Root struct {
	ID    library.RootID
	Name  string
	Path  string // the configured absolute host path (RootSpec.Path)
	Role  library.Role
	Watch library.WatchMode
	// Disabled is why library disabled the root (an unmounted share, a failed
	// overlap check), or nil.
	Disabled error
}

// ImportRoot is an import root with its scan state, with the fields of HEAD's
// database.ImportPath so a caller changes where it reads and not what it does
// with the answer.
type ImportRoot struct {
	// ID is HEAD's import-path ID n, the number in the root ID; 0 for a root
	// whose ID is not import-<n> (one the admin named through core's config).
	ID        int
	RootID    library.RootID
	Path      string
	Name      string
	Enabled   bool
	CreatedAt time.Time
	LastScan  *time.Time
	BookCount int

	// HasScan is whether the root has a rootscan row. Without one Enabled is
	// whether library watches the root (Watch is not off), CreatedAt is zero and
	// BookCount is 0.
	HasScan bool
	// ScanErr is why the root's row could not be read (wrapping ErrBadScanRow);
	// the scan fields are then the no-row values.
	ScanErr error
	// Disabled is library's reason the root is disabled, or nil.
	Disabled error
	// Watch is the root's watch mode.
	Watch library.WatchMode
}

// Reader is books' read side of its roots.
type Reader struct {
	list  func() []Root
	scans *ScanStore
}

// NewReader builds a Reader over books' Scope and scan store.
func NewReader(src RootLister, scans *ScanStore) *Reader {
	return newReader(snapshot(src), scans)
}

func newReader(list func() []Root, scans *ScanStore) *Reader {
	return &Reader{list: list, scans: scans}
}

// snapshot reads the roots books owns through src.
func snapshot(src RootLister) func() []Root {
	return func() []Root {
		mrs := src.Roots(library.RootFilter{Owner: src.Owner()})
		out := make([]Root, 0, len(mrs))
		for _, mr := range mrs {
			sp := mr.Spec()
			out = append(out, Root{ID: sp.ID, Name: sp.Name, Path: sp.Path, Role: sp.Role, Watch: sp.Watch, Disabled: mr.Disabled()})
		}
		return out
	}
}

// LibraryRoot is the books-library root.
func (r *Reader) LibraryRoot() (Root, error) {
	for _, rt := range r.list() {
		if rt.ID == BooksLibraryRoot && rt.Role == library.RoleLibrary {
			return rt, nil
		}
	}
	return Root{}, ErrNoLibraryRoot
}

// RootDir is the books library's host path, "" when no books-library root is
// registered: the replacement for HEAD's AppConfig.RootDir, with the same empty
// meaning, so a reader changes only its source. A disabled root still answers its
// path (HEAD had no such state); LibraryRoot says whether it is disabled.
func (r *Reader) RootDir() string {
	rt, err := r.LibraryRoot()
	if err != nil {
		return ""
	}
	return rt.Path
}

// ImportRoots lists the import roots books owns, sorted as HEAD sorted import
// paths: by lower-cased name, then by ID.
func (r *Reader) ImportRoots(ctx context.Context) ([]ImportRoot, error) {
	scans, err := r.scans.All(ctx)
	if err != nil {
		return nil, err
	}
	var out []ImportRoot
	for _, rt := range r.list() {
		if rt.Role != library.RoleImport {
			continue
		}
		ir := ImportRoot{
			RootID: rt.ID, Path: rt.Path, Name: rt.Name, Disabled: rt.Disabled, Watch: rt.Watch,
			Enabled: rt.Watch != library.WatchOff,
		}
		ir.ID, _ = ImportPathID(rt.ID)
		if e, ok := scans[rt.ID]; ok {
			if e.Err != nil {
				ir.ScanErr = e.Err
			} else {
				ir.HasScan, ir.Enabled = true, e.State.Enabled
				ir.CreatedAt, ir.LastScan, ir.BookCount = e.State.CreatedAt, e.State.LastScan, e.State.BookCount
			}
		}
		out = append(out, ir)
	}
	slices.SortFunc(out, func(a, b ImportRoot) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		if a.ID != b.ID {
			return a.ID - b.ID
		}
		return strings.Compare(string(a.RootID), string(b.RootID))
	})
	return out, nil
}

// ImportRoot is the import root with this ID, ErrNoSuchRoot when there is none.
func (r *Reader) ImportRoot(ctx context.Context, id library.RootID) (ImportRoot, error) {
	return r.find(ctx, func(ir ImportRoot) bool { return ir.RootID == id }, string(id))
}

// ImportRootByLegacyID is the import root HEAD called n.
func (r *Reader) ImportRootByLegacyID(ctx context.Context, n int) (ImportRoot, error) {
	return r.ImportRoot(ctx, RootIDFor(n))
}

// ImportRootByPath is the import root at path (compared after filepath.Clean),
// ErrNoSuchRoot when there is none: the replacement for GetImportPathByPath.
func (r *Reader) ImportRootByPath(ctx context.Context, path string) (ImportRoot, error) {
	want := filepath.Clean(path)
	return r.find(ctx, func(ir ImportRoot) bool { return filepath.Clean(ir.Path) == want }, path)
}

func (r *Reader) find(ctx context.Context, match func(ImportRoot) bool, what string) (ImportRoot, error) {
	all, err := r.ImportRoots(ctx)
	if err != nil {
		return ImportRoot{}, err
	}
	if i := slices.IndexFunc(all, match); i >= 0 {
		return all[i], nil
	}
	return ImportRoot{}, fmt.Errorf("%w: %s", ErrNoSuchRoot, what)
}

// WatchRoots is the import roots books wants watched now: enabled, watched by
// library (Watch is not off) and not disabled. It is the Watcher's default
// desired set; HEAD watched the enabled import paths.
func (r *Reader) WatchRoots(ctx context.Context) ([]library.RootID, error) {
	all, err := r.ImportRoots(ctx)
	if err != nil {
		return nil, err
	}
	var out []library.RootID
	for _, ir := range all {
		if ir.Enabled && ir.Watch != library.WatchOff && ir.Disabled == nil {
			out = append(out, ir.RootID)
		}
	}
	return out, nil
}

// NextImportN is the n of the next import root: above every registered import
// root and every rootscan row.
func (r *Reader) NextImportN(ctx context.Context) (int, error) {
	var ids []library.RootID
	for _, rt := range r.list() {
		ids = append(ids, rt.ID)
	}
	return r.scans.NextImportN(ctx, ids)
}
