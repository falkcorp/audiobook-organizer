// file: core/storage/restorejournal/validate.go
// version: 1.0.0
// guid: 2636a2ca-7d1d-4041-9c90-1a77996a3dcd
// last-edited: 2026-11-07

package restorejournal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrRenameRefused: a swap target fails the rename pre-check (another
// filesystem, read-only, a mount point or a single-file bind mount; DATA-23).
// Defined in this L0 leaf, whose Validate returns it, and re-exported as
// storage.ErrRenameRefused, as ErrSwapPending is.
var ErrRenameRefused = errors.New("storage: a swap target cannot be moved by rename")

// Probe reads what the rename pre-check needs from the real filesystem: an
// item's device, a directory's device and whether a directory admits new and
// removed entries. OSProbe is the real one; a swap over an in-memory
// filesystem has no devices and passes none.
type Probe interface {
	// ItemDev is the device of the item at path itself (lstat: a rename
	// moves a symlink, not what it names). An absent path returns an error
	// matching os.ErrNotExist.
	ItemDev(path string) (uint64, error)
	// DirDev is the device of the directory at path (stat).
	DirDev(path string) (uint64, error)
	// Writable is nil when entries can be created in and removed from dir.
	Writable(dir string) error
}

// Validate is the rename pre-check of 05-backup §5B.3.4 step 6 and 05-storage
// §5S.3.15 "Stage", run at staging over every rename the swap will make (a
// restore's set-asides and placements, a reset's targets): for each move
// whose From exists, From is not a mount point and not a single-file bind
// mount (its device equals its directory's), From's directory is writable,
// and To's nearest existing ancestor is on From's device and writable, so
// rename(2) cannot fail with EXDEV, EBUSY or EACCES at P0a for a reason
// staging could have seen. A From that does not exist is skipped: an absent
// sidecar has nothing to move. Every failure is listed, each wrapping
// ErrRenameRefused and naming the path.
func Validate(p Probe, moves []Target) error {
	if p == nil {
		return nil
	}
	var errs []error
	refuse := func(path, why string, err error) {
		if err != nil {
			why += ": " + err.Error()
		}
		errs = append(errs, fmt.Errorf("%w: %s: %s", ErrRenameRefused, path, why))
	}
	for _, m := range moves {
		dev, err := p.ItemDev(m.From)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			refuse(m.From, "cannot stat", err)
			continue
		}
		parent := filepath.Dir(m.From)
		pdev, err := p.DirDev(parent)
		if err != nil {
			refuse(parent, "cannot stat its directory", err)
			continue
		}
		if pdev != dev {
			refuse(m.From, "is a mount point or a single-file bind mount", nil)
			continue
		}
		if err := p.Writable(parent); err != nil {
			refuse(parent, "is not writable", err)
			continue
		}
		anc, adev, err := nearestDir(p, filepath.Dir(m.To))
		if err != nil {
			refuse(m.To, "no existing ancestor", err)
			continue
		}
		if adev != dev {
			refuse(m.To, fmt.Sprintf("is on another filesystem than %s", m.From), nil)
			continue
		}
		if err := p.Writable(anc); err != nil {
			refuse(anc, "is not writable", err)
		}
	}
	return errors.Join(errs...)
}

// nearestDir is dir or its nearest existing ancestor, with its device.
func nearestDir(p Probe, dir string) (string, uint64, error) {
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		dev, err := p.DirDev(d)
		if err == nil {
			return d, dev, nil
		}
		if !errors.Is(err, os.ErrNotExist) || filepath.Dir(d) == d {
			return "", 0, err
		}
	}
}
