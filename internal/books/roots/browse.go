// file: internal/books/roots/browse.go
// version: 0.1.0
// guid: e1b28a7a-70df-4adb-b7b8-7144c9324652
// last-edited: 2026-11-20

package roots

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"

	"golang.org/x/sync/errgroup"

	"github.com/falkcorp/mediacore/library"
)

// ExclusionMarker is the file whose presence in a directory makes books' scanner
// skip it (HEAD's fileops marker, kept so existing markers keep working).
const ExclusionMarker = ".jabexclude"

// excludedProbeConcurrency bounds the Stat calls of ExcludedAmong. A picker
// listing is capped at a few thousand directories by core, and each probe is a
// filesystem round trip that may be a network one, so the fan-out is small and
// fixed: parallel enough to hide latency, too small to hammer a share.
const excludedProbeConcurrency = 8

// ErrPathNotAllowed is returned by AllowedPath for a host path that is under no
// root books may read. It keeps HEAD's fileops.ErrPathNotAllowed message.
var ErrPathNotAllowed = errors.New("path not in allowed directories")

// ErrExclusionNotFound is returned by RemoveExclusion when the directory has no
// marker.
var ErrExclusionNotFound = errors.New("exclusion not found")

// ErrNotDirectory is returned by CreateExclusion for a path that is not a
// directory.
var ErrNotDirectory = errors.New("path must be a directory")

// PathSource is the part of library.Scope the browse half calls; library.Scope
// satisfies it.
type PathSource interface {
	Resolve(abs string) (library.Path, error)
	FS(id library.RootID) (library.RootFS, error)
}

var _ PathSource = library.Scope(nil)

// AllowedPath is the gate for a host path a request names: it resolves the
// path to the root it is under and reports it as a library.Path. It replaces
// fileops.ValidateUserPath and IsAllowedPath, whose allow-list was a fixed set of
// prefixes, RootDir and the import paths. Now the list is the registered roots
// books may read, and a path under a root's app or quarantine area is refused
// too, as Scope.Resolve does. A relative path is refused (HEAD resolved it
// against the working directory). Any refusal wraps ErrPathNotAllowed, and
// library's own error when it made the refusal.
func AllowedPath(src PathSource, abs string) (library.Path, error) {
	if abs == "" {
		return library.Path{}, fmt.Errorf("path is required")
	}
	// HEAD resolved a relative path against the server's working directory
	// (filepath.Abs) before checking it. That makes the answer depend on where the
	// process was started, so a relative path is refused instead.
	if !filepath.IsAbs(abs) {
		return library.Path{}, fmt.Errorf("%w: %q is not an absolute path", ErrPathNotAllowed, abs)
	}
	p, err := src.Resolve(filepath.Clean(abs))
	if err != nil {
		return library.Path{}, fmt.Errorf("%w: %w", ErrPathNotAllowed, err)
	}
	return p, nil
}

// statName is the name a directory has for Stat: "." for the root itself.
func statName(dir library.Path) string {
	if dir.Rel == "" {
		return "."
	}
	return dir.Rel
}

// markerName is the root-relative name of dir's marker.
func markerName(dir library.Path) string {
	if dir.Rel == "" {
		return ExclusionMarker
	}
	return dir.Rel + "/" + ExclusionMarker
}

// Excluded reports whether dir holds the marker. A Stat error other than "does
// not exist" is returned and not read as "not excluded": a directory whose marker
// cannot be checked must not be scanned as if it had none.
func Excluded(src PathSource, dir library.Path) (bool, error) {
	fsys, err := src.FS(dir.Root)
	if err != nil {
		return false, err
	}
	return excluded(fsys, dir)
}

func excluded(fsys library.RootFS, dir library.Path) (bool, error) {
	_, err := fsys.Stat(markerName(dir))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	}
	return false, fmt.Errorf("roots: check the exclusion of %s: %w", dir, err)
}

// ExcludedAmong answers Excluded for each of dirs, in order: the flag a
// directory listing shows. It probes with a bounded pool (see
// excludedProbeConcurrency), each probe writing its own slot of the result. The
// first error cancels the pool: no further probe is started (a dead share is a
// handful of failed Stats, not one per directory), and the probes already running
// finish before the error is returned.
func ExcludedAmong(ctx context.Context, src PathSource, dirs []library.Path) ([]bool, error) {
	fsOf := map[library.RootID]library.RootFS{}
	for _, d := range dirs {
		if _, ok := fsOf[d.Root]; ok {
			continue
		}
		fsys, err := src.FS(d.Root)
		if err != nil {
			return nil, err
		}
		fsOf[d.Root] = fsys
	}
	out := make([]bool, len(dirs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(excludedProbeConcurrency)
	for i, d := range dirs {
		if gctx.Err() != nil {
			break
		}
		g.Go(func() error {
			if gctx.Err() != nil { // cancelled while this probe waited for a slot
				return nil
			}
			ex, err := excluded(fsOf[d.Root], d)
			out[i] = ex
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateExclusion writes the marker in dir. It is idempotent (HEAD rewrote the
// file), and refused when dir is not a directory or the root is read-only.
func CreateExclusion(src PathSource, dir library.Path) error {
	fsys, err := src.FS(dir.Root)
	if err != nil {
		return err
	}
	info, err := fsys.Stat(statName(dir))
	if err != nil {
		return fmt.Errorf("path not found or inaccessible: %w", err)
	}
	if !info.IsDir() {
		return ErrNotDirectory
	}
	f, err := fsys.CreateExclusive(markerName(dir), 0o644)
	switch {
	case errors.Is(err, fs.ErrExist):
		return nil
	case err != nil:
		return fmt.Errorf("roots: create the exclusion of %s: %w", dir, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("roots: create the exclusion of %s: %w", dir, err)
	}
	return nil
}

// RemoveExclusion removes the marker from dir, ErrExclusionNotFound when there
// is none.
func RemoveExclusion(src PathSource, dir library.Path) error {
	fsys, err := src.FS(dir.Root)
	if err != nil {
		return err
	}
	switch err := fsys.Remove(markerName(dir)); {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return ErrExclusionNotFound
	default:
		return fmt.Errorf("roots: remove the exclusion of %s: %w", dir, err)
	}
}
