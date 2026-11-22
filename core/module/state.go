// file: core/module/state.go
// version: 0.1.0
// guid: 2c7e9b14-3f08-4a5d-b6e1-91d4a07c58f3
// last-edited: 2026-11-22

package module

import (
	"context"
	"errors"
	"fmt"
)

// StateFS is Deps.State: the labelled state filesystem (05-storage §5S.3.16,
// 06 §6.5.1), the way a module's boot-time writes outside Pebble reach the
// disk so the crash-matrix kit (coretest/crashmatrix) sees, labels, kills and
// fails them like every other boot writer's. A module never opens a file of
// the state directory itself for such a write: books' R3 start hook
// legacy-file-cleanup deletes HEAD's `.encryption_key` and `library.bleve`
// through it, each under its own crash label (06 §6.5.1's writer table, "Run
// 2 (R3+)").
//
// Both calls take an absolute path and refuse, with ErrStateConfined, one
// that is not inside the confinement the reset hook's StateFiles has
// (05-storage §5S.3.15 "Hook confinement"): the state directory
// (core.paths.state_dir) and the directory that holds the store
// (core.storage.path's parent, "<db parent>"). A name that leaves the
// directory it is under, through ".." or a symbolic link, is refused too, and
// a symbolic link as the last element is removed as a link and never
// followed. Neither root itself can be removed.
//
// Both refuse, with ErrStateCoreOwned, a path that is, lies inside or contains
// something core owns: the open store and its sidecars, checkpoints and
// restore directories, the swap lock, the keyring and its export record, the
// config boot record, the bootstrap token and the backup location file. The
// owners' own constants name them (kernel/stateowned.go), so the list moves
// with a rename. An app's own state files (Config.StorageStateFiles) are not
// core's: a hook may remove those.
//
// label is the crash-matrix label of the writer (the writer-table row the app
// registers, for example "<module>.legacy-file-cleanup.<file>"); an empty
// one is refused with ErrStateUnlabelled, because an operation with no label
// is one the kit fails the boot for. Every durable operation of the call runs
// inside that region, the parent directory's fsync included, so a removal is
// durable when the call returns. A path that is already gone is done with no
// operation at all (so a second boot over a finished cleanup writes nothing).
//
// Safe for concurrent use. A call after the app stopped is the caller's
// error: ctx is checked first, and the kernel does not keep a file open
// between calls.
type StateFS interface {
	// Remove removes the file, symbolic link or empty directory at path. A
	// path that does not exist is an error satisfying errors.Is(err,
	// fs.ErrNotExist), as os.Remove.
	Remove(ctx context.Context, label, path string) error
	// RemoveAll removes path and everything under it. A path that does not
	// exist is success, as os.RemoveAll; one that is partly removed (a crash
	// part way, an error part way) is finished by the next call.
	RemoveAll(ctx context.Context, label, path string) error
}

// ErrStateConfined is what a StateFS call returns for a path outside the
// state directory and the store's directory, or one that names a root itself.
var ErrStateConfined = errors.New("module: the path is outside the state filesystem's confinement")

// ErrStateCoreOwned is what a StateFS call returns for a path that is, lies
// inside or contains something core owns: the open store (and its sidecars,
// swap lock, checkpoints and restore directories), the keyring and its export
// record, the config boot record, the bootstrap token and the backup location
// file. A module's start hook must never remove those, so the handle refuses
// them whatever the label. It wraps ErrStateConfined: errors.Is(err,
// ErrStateConfined) holds too, so a caller that only checks confinement
// refuses as well.
var ErrStateCoreOwned = fmt.Errorf("%w: it is core's own state", ErrStateConfined)

// ErrStateUnlabelled is what a StateFS call returns for an empty crash label.
var ErrStateUnlabelled = errors.New("module: a state filesystem call needs a crash-matrix label")
