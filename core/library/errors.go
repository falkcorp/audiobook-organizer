// file: core/library/errors.go
// version: 1.0.1
// guid: 70a43d37-5dd5-4868-b9d1-c1b69a14827d
// last-edited: 2026-11-11

package library

import (
	"errors"

	"github.com/falkcorp/mediacore/internal/libfs"
)

// The sentinels of 05-library 5L.3. Every message follows Go's "pkg: message"
// form: "library:" is also an ABO key family, and the key-name gate matches a
// family prefix only when the next character is not a space, so these pass
// while a key literal such as "library:" + id would not (MOD5-3).
var (
	ErrNotUnderRoot = errors.New("library: path is under no registered root")
	ErrEscapesRoot  = errors.New("library: path escapes its root")
	ErrReadOnlyRoot = errors.New("library: caller may not write this root")
	ErrNotMounted   = errors.New("library: root sentinel missing or mismatched (not mounted, or the wrong volume)")
	ErrRootOverlap  = errors.New("library: root overlaps a root of another owner")
	ErrUnknownRoot  = errors.New("library: unknown root")
)

// ErrReflinkUnsupported and ErrCrossDevice are libfs's sentinels, the ones the
// byte layer returns, so errors.Is holds through both packages.
var (
	ErrReflinkUnsupported = libfs.ErrReflinkUnsupported //archtest:allow-var the spec's error, shared with the byte layer that returns it
	ErrCrossDevice        = libfs.ErrCrossDevice        //archtest:allow-var the spec's error, shared with the byte layer that returns it
)

// Names beyond 05-library 5L.3 (listed in seams.d/F7-06.md).
var (
	// ErrNotReadable: the caller may not read this root (it neither owns it nor
	// holds a share, and it is not a download root the caller may move out of).
	ErrNotReadable = errors.New("library: caller may not read this root")
	// ErrOwnerDisabled: the owner of the root is a disabled module, so the
	// root takes no transfer, quarantine or watch (05-library 5L.3.8, G1).
	ErrOwnerDisabled = errors.New("library: the owner of this root is disabled")
	// ErrRootDisabled: the root failed registration or its boot re-check (an
	// unresolvable path, an overlap, an unmounted sentinel at load) and is
	// retained, inert, until its entry changes.
	ErrRootDisabled = errors.New("library: root is disabled")
	// ErrInvalidOptions: a TransferOptions value with a zero mode, permission
	// policy or verify level. There is no default for any of them.
	ErrInvalidOptions = errors.New("library: invalid transfer options")
	// ErrBadTransfer: a transfer whose paths cannot be moved (a root directory,
	// the same name, a name inside itself).
	ErrBadTransfer = errors.New("library: invalid transfer paths")
	// ErrUnknownQuarantine: no quarantine record has this ID.
	ErrUnknownQuarantine = errors.New("library: unknown quarantine record")
	// ErrJournal: the transfer journal could not be written, so no file
	// operation was started (05-library 5L.5: closed).
	ErrJournal = errors.New("library: transfer journal write failed")
	// ErrNotSupported: the operation is not available on this root's
	// filesystem or platform.
	ErrNotSupported = errors.New("library: operation not supported")
)

// The transfer sentinels of 05-library 5L.3.3.
var (
	ErrSourceProtected   = errors.New("library: source is protected (seeding, external root, or protection state unknown)")
	ErrInsufficientSpace = errors.New("library: destination would drop below its root's free-space floor")
	ErrVerifyFailed      = errors.New("library: destination does not match source after transfer")
	ErrReplaceMismatch   = errors.New("library: destination changed; refusing to replace")
	// ErrUnmapped: a remote path matches no mapping and no root.
	ErrUnmapped = errors.New("library: remote path matches no mapping and no root")
)
