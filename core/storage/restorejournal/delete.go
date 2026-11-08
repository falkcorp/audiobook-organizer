// file: core/storage/restorejournal/delete.go
// version: 1.0.0
// guid: 585f6cae-e0dd-43fe-9ba2-47dc5e313ba9
// last-edited: 2026-11-08

package restorejournal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrDeleteRefused: a reset intent's delete entry may not be removed by rule
// (05-storage §5S.3.15): it is not absolute, it is or holds a kept path, or
// it is on the DB's filesystem, where the stager sets an item aside instead.
var ErrDeleteRefused = errors.New("storage: a reset delete entry is refused")

// InTree reports whether p is root or lies inside it, comparing cleaned
// absolute paths. A relative p is in no tree.
func InTree(p, root string) bool {
	if !filepath.IsAbs(p) || !filepath.IsAbs(root) {
		return false
	}
	p, root = filepath.Clean(p), filepath.Clean(root)
	if p == root {
		return true
	}
	_, ok := inside(p, root)
	return ok
}

// Kept is rule (4) of the reset hook (05-storage §5S.3.15), which the kernel
// applies too when no hook runs: removing p would remove what the reset
// keeps. That is p being or containing <db parent> or the state directory,
// or p being, containing or lying inside one of these trees:
//   - restore/, the slot and every swap's journal directory;
//   - every swap's private directory .mediacore-restore beside <db parent>
//     and beside each of the intent's targets, where previous/ (the only
//     undo) and failed/ live;
//   - each of the intent's set-aside destinations;
//   - the live store at DBPath and its sidecars (the core suffixes and the
//     build's legacySuffixes): the fresh install a reset boot made.
//
// The statefs confinement of a reset hook (R1-25) reads the same rule.
func Kept(stateDir string, in Intent, legacySuffixes []string, p string) bool {
	if !filepath.IsAbs(p) {
		return false
	}
	db := filepath.Clean(in.DBPath)
	dbParent := filepath.Dir(db)
	for _, root := range []string{dbParent, stateDir} {
		if InTree(root, p) {
			return true
		}
	}
	trees := []string{Dir(stateDir), filepath.Join(dbParent, asideDir), db}
	for _, s := range append(CoreSidecarSuffixes(), legacySuffixes...) {
		trees = append(trees, db+s)
	}
	for _, t := range in.Targets {
		trees = append(trees, filepath.Join(filepath.Dir(filepath.Clean(t.From)), asideDir), t.To)
	}
	for _, t := range trees {
		if InTree(p, t) || InTree(t, p) {
			return true
		}
	}
	return false
}

// ValidateDelete is Stage's check of an intent's delete entries (05-storage
// §5S.3.15 "Stage"; R1-21 review M1): only a KindReset intent lists any, and
// each is absolute, not Kept, and, with a probe, on another filesystem than
// <db parent> (the stager lists only a Recreatable auxiliary DB outside the
// DB's filesystem in delete; one on it is set aside, which is undoable). An
// entry that does not exist is not probed, as Validate skips an absent
// From, and a nil probe (an in-memory filesystem) skips the device check.
// Every refusal is listed, each wrapping ErrDeleteRefused.
func ValidateDelete(p Probe, stateDir string, in Intent, legacySuffixes []string) error {
	if len(in.Delete) > 0 && in.Kind != KindReset {
		return fmt.Errorf("%w: a %s intent deletes nothing; only a reset lists delete entries", ErrDeleteRefused, in.Kind)
	}
	var errs []error
	refuse := func(path, why string) { errs = append(errs, fmt.Errorf("%w: %s %s", ErrDeleteRefused, path, why)) }
	for _, e := range in.Delete {
		switch {
		case !filepath.IsAbs(e):
			refuse(e, "is not absolute")
			continue
		case Kept(stateDir, in, legacySuffixes, e):
			refuse(e, "is or holds a path the reset keeps (the state directory, <db parent>, restore/, a set-aside or the live store)")
			continue
		case p == nil:
			continue
		}
		dev, err := p.ItemDev(e)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			refuse(e, "cannot be probed: "+err.Error())
			continue
		}
		_, ddev, err := nearestDir(p, filepath.Dir(filepath.Clean(in.DBPath)))
		if err != nil {
			refuse(e, "cannot be compared with the DB's filesystem: "+err.Error())
			continue
		}
		if dev == ddev {
			refuse(e, "is on the DB's filesystem; set it aside instead")
		}
	}
	return errors.Join(errs...)
}
