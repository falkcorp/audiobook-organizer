// file: core/storage/reset.go
// version: 1.0.0
// guid: 0d8e3a51-7c2b-4f6e-9a14-5b2d8c6e1f73
// last-edited: 2026-11-08

package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// The factory reset's contract (05-storage §5S.3.15's Go block): one
// request type every caller passes unchanged, what a staged reset answers,
// the module's reset hook and what it receives. Staging and the swap are
// core/internal/restoreswap's and the kernel's (COR8-5): storage names no
// module type and storage.Admin has no staging or swap method.

// ResetRequest is the one factory-reset confirmation contract (R9-6): the
// core route decodes its body into it, the offline command builds it, and
// kernel.stageReset, storagehttp.Stager.StageReset and ResetHook.Plan all
// receive it unchanged. 05-config, 05-rbac §6.2 and 05-http-api cite it.
type ResetRequest struct {
	Kind               restorejournal.ResetKind `json:"kind"`                           // "factory" (the route's default) or "store"
	Confirm            string                   `json:"confirm"`                        // must be "RESET"
	DeleteLibraryFiles bool                     `json:"delete_library_files,omitempty"` // A-17; refused for ResetStore
	LibraryRoot        string                   `json:"library_root,omitempty"`         // must equal the preflight's root
	LibraryFileCount   int                      `json:"library_file_count,omitempty"`   // must equal the preflight's count
}

// ResetConfirm is the one confirmation token of a reset (HEAD's
// {"confirm":"RESET"}, kept).
const ResetConfirm = "RESET"

// Validate checks what needs no preflight: ErrResetConfirmRequired, or
// ErrResetRequestInvalid for an unknown kind or ResetStore with
// DeleteLibraryFiles.
func (r ResetRequest) Validate() error {
	if r.Confirm != ResetConfirm {
		return ErrResetConfirmRequired
	}
	switch r.Kind {
	case restorejournal.ResetFactory:
	case restorejournal.ResetStore:
		if r.DeleteLibraryFiles {
			return fmt.Errorf("%w: a store reset deletes no file; delete_library_files needs kind factory", ErrResetRequestInvalid)
		}
	default:
		return fmt.Errorf("%w: kind %q is neither %q nor %q", ErrResetRequestInvalid, r.Kind,
			restorejournal.ResetFactory, restorejournal.ResetStore)
	}
	return nil
}

// ResetStaged is what a staged reset answers: the intent's ID and, only when
// the library wipe is staged, the root and count it will remove.
type ResetStaged struct {
	ID               string `json:"reset_id"`
	LibraryRoot      string `json:"library_root,omitempty"`
	LibraryFileCount int    `json:"library_file_count,omitempty"`
}

// ResetHook is a module's part of a factory reset, declared on
// module.Manifest.ResetHook (06; at most one module of an App: books for
// ABO). Plan runs at staging, Run on the reset boot (§5S.3.15).
type ResetHook struct {
	// Plan receives the one request and returns what the hook will need;
	// with req.DeleteLibraryFiles it runs the preflight. An error wrapping
	// ErrResetRefused refuses the reset (400, nothing staged).
	Plan func(ctx context.Context, req ResetRequest) (ResetPlan, error)
	// Run completes an applied ResetFactory swap at 06 §6.5.2 Run step 2.
	Run func(ctx context.Context, run ResetRun) error
}

// ResetPlan is recorded in the reset intent (hook_args, hook_paths).
type ResetPlan struct {
	// Hook is opaque to the kernel (books: the Open Library dump directory
	// and, only with req.DeleteLibraryFiles, the library root and count the
	// preflight found); nil for ResetStore.
	Hook []byte
	// Paths are the directories the hook removes entries under that lie
	// outside the state directory and <db parent>, absolute (R10-6,
	// COR10-4); the kernel records them as hook_paths and bounds StateFiles
	// by them, so it never parses Hook. Nil for ResetStore.
	Paths []string
	// LibraryRoot and LibraryFiles are the preflight's fresh values, set only
	// with req.DeleteLibraryFiles; kernel.stageReset compares them with the
	// request.
	LibraryRoot  string
	LibraryFiles int
}

// ResetRun is the applied ResetFactory swap a reset hook completes. Done
// journals hook_done with the entries the hook could not remove and deletes
// the in-flight file; the hook calls it once every action has been
// attempted, whatever failed, so only a crash reruns it (COR8-4).
type ResetRun struct {
	ID                 string
	Args               []byte     // ResetPlan.Hook
	Delete             []string   // Recreatable auxiliary DBs outside the DB's filesystem, to remove
	DeleteLibraryFiles bool       // the intent's delete_library_files (A-17)
	Files              StateFiles // the confined state filesystem (row "Hook confinement")
	ExtendStart        func(step string)
	Done               func(ctx context.Context, failed []string) error
}

// StateFiles is the confined filesystem a reset hook removes entries
// through (05-storage §5S.3.15 "Hook confinement"; at R1 the kernel's
// confinement over the one boot filesystem, core/internal/restoreswap); a
// path outside the confinement is refused with ErrConfined, and removing a
// kept path (rule (4)) is refused with ErrKeptPath, which the hook reports
// as a skip, not a failure.
type StateFiles interface {
	ReadDir(path string) ([]fs.DirEntry, error)
	RemoveAll(path string) error
}

// LibraryConfirmError is the preflight mismatch (422
// library_files_confirm_required): the fresh values the client must confirm.
// Nothing is staged. errors.Is(err, ErrLibraryConfirmRequired) holds.
type LibraryConfirmError struct {
	Root  string
	Files int
}

func (e *LibraryConfirmError) Error() string {
	return fmt.Sprintf("%v: the library root is %s with %d top-level entries; confirm them with --library-root and --library-file-count",
		ErrLibraryConfirmRequired, e.Root, e.Files)
}

// Is matches ErrLibraryConfirmRequired.
func (e *LibraryConfirmError) Is(target error) bool { return target == ErrLibraryConfirmRequired }

var (
	ErrResetConfirmRequired   = errors.New("storage: the factory reset needs confirm \"RESET\"")
	ErrResetRequestInvalid    = errors.New("storage: the factory reset request is invalid")
	ErrLibraryConfirmRequired = errors.New("storage: deleting library files needs the current root and count confirmed")
	// ErrResetRefused wraps a reset hook's refusal at staging (books: a
	// dangerous library RootDir); the route answers 400 and nothing is staged.
	ErrResetRefused = errors.New("storage: the app refused the factory reset")
	// ErrConfined: a reset hook named a path outside its confinement (the
	// state directory, <db parent>, the intent's delete entries and its
	// hook_paths). The hook records it as a failure.
	ErrConfined = errors.New("storage: the path is outside the reset hook's confinement")
	// ErrKeptPath: removing the path would remove what a reset keeps (rule
	// (4) of the reset hook: <db parent>, the state directory, restore/, a
	// swap's private directory, a set-aside destination, the fresh store).
	// The hook records it as a skip, not a failure.
	ErrKeptPath = errors.New("storage: the path is kept by the reset")
)
