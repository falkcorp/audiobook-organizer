// file: core/health/diagnostics/download.go
// version: 1.0.1
// guid: e2ff7848-fe31-45cc-b7b9-a67d28dad6a2
// last-edited: 2026-11-13

package diagnostics

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/rbac"
)

// ErrExpired is OpenBundle's answer for a bundle that is gone: pruned after
// core.diagnostics.retention, never written, or not a bundle file name. The
// route maps it to 410.
var ErrExpired = errors.New("diagnostics: bundle expired")

// ErrCorrupt is a file that is not a readable bundle.
var ErrCorrupt = errors.New("diagnostics: bundle is not readable")

// OmittedAtDownloadFile lists what a download left out. It is added to the
// streamed zip, so the reader knows the file is a view of the bundle.
const OmittedAtDownloadFile = ManifestSectionName + "/omitted-at-download.json"

// Bundle is an opened bundle zip, read through the diagnostics os.Root.
type Bundle struct {
	f        *os.File
	zr       *zip.Reader
	Manifest Manifest
	ModTime  time.Time
}

// OpenBundle opens file ("<run-id>.zip") inside root and reads its manifest. A
// name that is not a plain bundle name is ErrExpired, never a path: the root
// would refuse an escape, and the name is refused before it gets that far.
func OpenBundle(root *os.Root, file string) (*Bundle, error) {
	if id, ok := strings.CutSuffix(file, ".zip"); !ok || !plainName(id) {
		return nil, ErrExpired
	}
	f, err := root.Open(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrExpired
		}
		return nil, fmt.Errorf("diagnostics: open %s: %w", file, err)
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("diagnostics: stat %s: %w", file, err), f.Close())
	}
	zr, err := zip.NewReader(f, fi.Size())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("%w: %v", ErrCorrupt, err), f.Close())
	}
	b := &Bundle{f: f, zr: zr, ModTime: fi.ModTime()}
	if err := b.readManifest(); err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return b, nil
}

func (b *Bundle) readManifest() error {
	for _, zf := range b.zr.File {
		if zf.Name != ManifestFile {
			continue
		}
		rc, err := zf.Open()
		if err != nil {
			return fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
		}
		defer func() { _ = rc.Close() }()
		if err := json.NewDecoder(io.LimitReader(rc, 16<<20)).Decode(&b.Manifest); err != nil {
			return fmt.Errorf("%w: manifest: %v", ErrCorrupt, err)
		}
		return nil
	}
	return fmt.Errorf("%w: no manifest", ErrCorrupt)
}

// Close releases the file.
func (b *Bundle) Close() error { return b.f.Close() }

// Stream copies the bundle to dst, keeping a section only if can satisfies the
// section's recorded Perm: the effective view is the start-time viewer's
// sections (what is in the zip) intersected with the download-time checker
// (SEC2-13), so a role removed after the run started cannot fetch what it could
// no longer read. Entries are copied compressed, unchanged. A section the
// manifest does not know, or an entry whose name is not a plain relative path,
// is dropped. It returns the sections it left out, which the streamed zip also
// lists in OmittedAtDownloadFile.
func (b *Bundle) Stream(dst io.Writer, can func(rbac.Permission) bool) (omitted []string, err error) {
	perms := map[string]PermSpec{}
	for _, s := range b.Manifest.Sections {
		perms[s.Name] = s.Perm
	}
	zw := zip.NewWriter(dst)
	dropped := map[string]bool{}
	for _, zf := range b.zr.File {
		section, _, ok := strings.Cut(zf.Name, "/")
		if !ok || !validEntryName(zf.Name) {
			continue
		}
		if section == ManifestSectionName {
			if zf.Name == ManifestFile {
				if err := zw.Copy(zf); err != nil {
					return nil, fmt.Errorf("diagnostics: copy manifest: %w", err)
				}
			}
			continue
		}
		spec, known := perms[section]
		if !known || !satisfied(spec, can) {
			dropped[section] = true
			continue
		}
		if err := zw.Copy(zf); err != nil {
			return nil, fmt.Errorf("diagnostics: copy %s: %w", zf.Name, err)
		}
	}
	for s := range dropped {
		omitted = append(omitted, s)
	}
	slices.Sort(omitted)
	w, err := zw.Create(OmittedAtDownloadFile)
	if err != nil {
		return nil, fmt.Errorf("diagnostics: create omitted listing: %w", err)
	}
	if omitted == nil {
		omitted = []string{}
	}
	if err := json.NewEncoder(w).Encode(map[string]any{"omitted": omitted}); err != nil {
		return nil, fmt.Errorf("diagnostics: write omitted listing: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("diagnostics: finish stream: %w", err)
	}
	return omitted, nil
}

// isBundleName is ^<plain name>\.zip(\.tmp)?$: a finished bundle or the partial
// of a run that stopped.
func isBundleName(name string) bool {
	name = strings.TrimSuffix(name, ".tmp")
	id, ok := strings.CutSuffix(name, ".zip")
	return ok && plainName(id)
}

// Prune removes bundle files (and abandoned ".zip.tmp" partials) whose
// modification time is older than retention at now. It touches only names
// that match a bundle's, through the root. It returns how many it removed;
// a file it could not remove is reported with the others removed.
func Prune(root *os.Root, retention time.Duration, now time.Time) (removed int, err error) {
	d, err := root.Open(".")
	if err != nil {
		return 0, fmt.Errorf("diagnostics: open dir: %w", err)
	}
	ents, err := d.ReadDir(-1)
	err = errors.Join(err, d.Close())
	if err != nil {
		return 0, fmt.Errorf("diagnostics: read dir: %w", err)
	}
	var errs []error
	for _, de := range ents {
		if de.IsDir() || !isBundleName(de.Name()) {
			continue
		}
		fi, ierr := de.Info()
		if ierr != nil || now.Sub(fi.ModTime()) <= retention {
			continue
		}
		if rerr := root.Remove(de.Name()); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			errs = append(errs, rerr)
			continue
		}
		removed++
	}
	return removed, errors.Join(errs...)
}
