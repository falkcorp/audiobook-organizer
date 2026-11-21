// file: core/kernel/restore_staging_sweep.go
// version: 0.1.0
// guid: 6c1d9b47-2e3a-4f85-9a70-b4e8d2c51f36
// last-edited: 2026-11-21

package kernel

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// sweepOrphanStaging removes what an earlier storage restore-offline staged
// and never linked an intent for: a restore killed after it staged and
// before Stage linked its intent, or a killed escape (an escape journals no
// staging path, and the rerun stages again under a fresh id). Such a
// directory holds copies of state files, the encryption key among them, and
// nothing else ever removes it: every rerun takes a fresh id.
//
// The caller holds the swap lock exclusively and the slot check passed, so
// no stager runs and no intent is about to link. It looks under the
// .mediacore-restore directory of each place a restore stages
// (stagingParents) and removes <id>/ only when all of these hold:
//   - <id> is a restore id and <id>/ is a directory, not a symbolic link;
//   - <id>/ holds staging/ and nothing else (no previous/, the only undo,
//     no failed/, no stray file);
//   - no restore/<id> exists under the state directory, or restore/<id> is
//     an empty directory (not a symbolic link, no entries at all, so no
//     journal) that the slot does not name: Stage creates restore/<id>
//     before it links the intent, and a kill between the two leaves it bare.
//     Such a bare restore/<id> is removed too, before <id>/;
//   - everything under staging/ is a directory or a regular file, none a
//     symbolic link.
//
// It fails safe. Anything that holds staging/ but misses one of these, and
// anything it cannot read, is left in place and named in a returned
// warning; it never stops the restore. A directory with no staging/ (an
// applied restore's previous/, a failed one's failed/) is the swap's own
// and is passed over without a word, and so is one whose intent was linked:
// restore/<id> holds the journal, or the slot names <id>. A restore/<id>
// with neither that is not empty, or not a plain directory, is left with
// <id>/ and both are named. A target outside the state directory and the
// DB's parent that the current config and request do not name is not
// visited.
func (k *Kernel) sweepOrphanStaging(fsys vfs.FS, req RestoreOfflineRequest) []string {
	var warnings []string
	slot, slotErr := restorejournal.ReadSlot(fsys, k.cfg.StateDir)
	linked := func(id string) (bool, error) {
		switch _, err := fsys.Stat(filepath.Join(restorejournal.JournalDir(k.cfg.StateDir, id), restorejournal.JournalName)); {
		case err == nil:
			return true, nil
		case !errors.Is(err, os.ErrNotExist):
			return false, err
		case slotErr != nil:
			return false, fmt.Errorf("the restore slot: %w", slotErr)
		case slot.Pending != nil && slot.Pending.ID == id:
			return true, nil
		}
		for _, e := range slot.InFlight {
			if e.ID == id {
				return true, nil
			}
		}
		return false, nil
	}
	for _, parent := range k.stagingParents(req) {
		dir := filepath.Join(parent, restorejournal.AsideDirName)
		fi, err := lstatFS(fsys, dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			warnings = append(warnings, fmt.Sprintf("left in place: %s could not be checked for an earlier restore's staging: %v", dir, err))
			continue
		case !fi.IsDir():
			warnings = append(warnings, fmt.Sprintf("left in place: %s is not a directory (%s)", dir, fi.Mode().Type()))
			continue
		}
		names, err := fsys.List(dir)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("left in place: %s could not be listed: %v", dir, err))
			continue
		}
		slices.Sort(names)
		for _, n := range names {
			root := filepath.Join(dir, n)
			orphan, bare, why, err := k.orphanStaging(fsys, root, n, linked)
			switch {
			case err != nil:
				warnings = append(warnings, fmt.Sprintf("left in place: %s could not be checked: %v", root, err))
			case why != "":
				warnings = append(warnings, fmt.Sprintf("left in place: %s %s", root, why))
			case orphan:
				// The bare restore/<id> first: a crash between the two
				// removes leaves the plain orphan shape, which the next
				// sweep finds; the other order would leave restore/<id>
				// where no sweep looks.
				if bare != "" {
					if err := fsys.Remove(bare); err != nil {
						warnings = append(warnings, fmt.Sprintf("left in place: %s and the staging at %s: the empty %s was not removed: %v", bare, root, bare, err))
						continue
					}
					k.log.Warn("kernel: removed the empty journal directory a stopped Stage left", "path", bare)
					warnings = append(warnings, fmt.Sprintf("removed %s: Stage created it and was stopped before it linked an intent", bare))
				}
				if err := fsys.RemoveAll(root); err != nil {
					warnings = append(warnings, fmt.Sprintf("the staging an earlier restore left at %s was not removed: %v", root, err))
					continue
				}
				k.log.Warn("kernel: removed the staging an earlier restore left without linking an intent", "path", root)
				warnings = append(warnings, fmt.Sprintf("removed %s: an earlier restore staged it and never linked an intent for it", root))
			}
		}
	}
	return warnings
}

// stagingParents are the directories whose .mediacore-restore/ a restore
// stages into (restorejournal.StagingDest, StagingDir): the state directory,
// the DB's parent, and the directory of each state file the config declares
// and of each file the request's --keyring and --gap-fill bring back that
// lies outside both. Deduplicated, in that order.
func (k *Kernel) stagingParents(req RestoreOfflineRequest) []string {
	out := []string{filepath.Clean(k.cfg.StateDir), filepath.Dir(filepath.Clean(k.cfg.StoragePath))}
	var targets []string
	for _, f := range k.cfg.StorageStateFiles {
		if f.Path != "" {
			targets = append(targets, f.Path)
		}
	}
	for _, f := range k.archiveFiles(req) {
		targets = append(targets, f.to)
	}
	for _, t := range targets {
		// StagingRoots' own rule: a root per filesystem side a target
		// stages on; the id is a placeholder and only the parent is kept.
		for _, r := range restorejournal.StagingRoots(k.cfg.StateDir, k.cfg.StoragePath, "x", []string{t}) {
			if p := filepath.Dir(filepath.Dir(r)); !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	return out
}

// orphanStaging decides one entry root (named name) of a .mediacore-restore
// directory: orphan when sweepOrphanStaging's conditions all hold, with
// bare the empty restore/<id> to remove first ("" when there is none); a
// non-empty why, the reason it is left in place, when it holds staging/ but
// misses one; neither when it is the swap's own (no staging/, or linked
// reports an intent linked for it).
func (k *Kernel) orphanStaging(fsys vfs.FS, root, name string, linked func(id string) (bool, error)) (orphan bool, bare, why string, err error) {
	fi, err := lstatFS(fsys, root)
	switch {
	case err != nil:
		return false, "", "", err
	case fi.Mode()&fs.ModeSymlink != 0:
		return false, "", "is a symbolic link", nil
	case !fi.IsDir():
		return false, "", fmt.Sprintf("is not a directory (%s)", fi.Mode().Type()), nil
	}
	names, err := fsys.List(root)
	if err != nil {
		return false, "", "", err
	}
	if !slices.Contains(names, leafStaging) {
		return false, "", "", nil
	}
	if !restorejournal.ValidID(name) || strings.HasPrefix(name, ".") {
		return false, "", "holds staging/ but is not named for a restore id", nil
	}
	jdir := restorejournal.JournalDir(k.cfg.StateDir, name)
	switch _, err := fsys.Stat(jdir); {
	case err == nil:
		ok, err := linked(name)
		switch {
		case err != nil:
			return false, "", "", fmt.Errorf("the journal of %s: %w", name, err)
		case ok:
			return false, "", "", nil // an intent was linked: the swap's
		}
		if why, err := emptyPlainDir(fsys, jdir); err != nil || why != "" {
			if err != nil {
				return false, "", "", fmt.Errorf("restore/%s: %w", name, err)
			}
			return false, "", fmt.Sprintf("has restore/%s with no journal, the slot does not name it, and it %s", name, why), nil
		}
		bare = jdir
	case !errors.Is(err, os.ErrNotExist):
		return false, "", "", fmt.Errorf("the journal of %s: %w", name, err)
	}
	if len(names) != 1 {
		var other []string
		for _, n := range names {
			if n != leafStaging {
				other = append(other, n)
			}
		}
		slices.Sort(other)
		return false, "", fmt.Sprintf("holds %s beside staging/ and no intent was linked for it", strings.Join(other, ", ")), nil
	}
	if why, err := plainTree(fsys, filepath.Join(root, leafStaging)); err != nil || why != "" {
		return false, "", why, err
	}
	return true, bare, "", nil
}

// leafStaging is the staging leaf of a restore's private directory
// (restorejournal.StagingDir's last element).
const leafStaging = "staging"

// plainTree reports, in why, the first entry under dir (dir included) that
// is neither a directory nor a regular file, a symbolic link among them.
func plainTree(fsys vfs.FS, dir string) (why string, err error) {
	fi, err := lstatFS(fsys, dir)
	if err != nil {
		return "", err
	}
	switch {
	case fi.Mode().IsRegular():
		return "", nil
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Sprintf("holds a symbolic link at %s", dir), nil
	case !fi.IsDir():
		return fmt.Sprintf("holds %s, which is not a regular file (%s)", dir, fi.Mode().Type()), nil
	}
	names, err := fsys.List(dir)
	if err != nil {
		return "", err
	}
	slices.Sort(names)
	for _, n := range names {
		if why, err := plainTree(fsys, filepath.Join(dir, n)); err != nil || why != "" {
			return why, err
		}
	}
	return "", nil
}

// emptyPlainDir reports, in why, how dir is not an empty directory that is
// not a symbolic link ("" when it is one).
func emptyPlainDir(fsys vfs.FS, dir string) (why string, err error) {
	fi, err := lstatFS(fsys, dir)
	switch {
	case err != nil:
		return "", err
	case fi.Mode()&fs.ModeSymlink != 0:
		return "is a symbolic link", nil
	case !fi.IsDir():
		return fmt.Sprintf("is not a directory (%s)", fi.Mode().Type()), nil
	}
	names, err := fsys.List(dir)
	if err != nil {
		return "", err
	}
	if len(names) > 0 {
		slices.Sort(names)
		return "holds " + strings.Join(names, ", "), nil
	}
	return "", nil
}

// lstatFS is Stat that does not follow a final symbolic link. vfs.FS has no
// Lstat; on the real filesystem it is os.Lstat, and a filesystem that is
// not the real one (an in-memory one) has no symbolic links to follow.
func lstatFS(fsys vfs.FS, p string) (os.FileInfo, error) {
	if vfs.Root(fsys) == vfs.Default {
		return os.Lstat(p)
	}
	return fsys.Stat(p)
}
