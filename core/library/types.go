// file: core/library/types.go
// version: 1.1.0
// guid: a5bb8543-563b-49d6-a0ce-f53cebd0d920
// last-edited: 2026-11-11

package library

import (
	"context"
	"fmt"
	"time"

	"github.com/falkcorp/mediacore/storage"
)

// TransferMode is how a transfer puts the source's bytes at the destination
// (05-library 5L.3.3).
type TransferMode uint8

// The transfer modes.
const (
	TransferInvalid TransferMode = iota
	// Move: same device, a no-clobber rename; cross-device, copy, verify,
	// fsync, then remove the source.
	Move
	Copy
	Hardlink // same device only; else ErrCrossDevice
	Reflink  // else ErrReflinkUnsupported
	Symlink
	Auto // reflink, then hardlink, then copy
)

func modeNames() [7]string {
	return [...]string{
		TransferInvalid: "invalid", Move: "move", Copy: "copy", Hardlink: "hardlink",
		Reflink: "reflink", Symlink: "symlink", Auto: "auto",
	}
}

// String is the mode's metrics and log label.
func (m TransferMode) String() string {
	if int(m) < len(modeNames()) {
		return modeNames()[m]
	}
	return fmt.Sprintf("mode(%d)", uint8(m))
}

// PermPolicy is whose permission bits the copy path gives the new file. There
// is no default: a zero PermPolicy is ErrInvalidOptions (a copy that silently
// inherited a 0600 source made imported files unreadable to the library's
// group, the incident behind fileops.PermFromSource).
type PermPolicy uint8

// The permission policies.
const (
	PermInvalid    PermPolicy = iota
	PermFromSource            // a sibling of the same file (backup, temp-and-rename)
	PermLibrary               // ingest: 0664, then the umask
)

// VerifyLevel is how the copy path proves the destination matches the source.
type VerifyLevel uint8

// The verify levels.
const (
	VerifyInvalid VerifyLevel = iota
	VerifySize
	VerifySHA256
)

// ReplaceSpec turns "destination exists" from a refusal into a
// compare-and-swap: dst is replaced only while its SHA-256 still equals
// ExpectSHA256.
type ReplaceSpec struct{ ExpectSHA256 string }

// TransferOptions is every choice of one transfer. Mode, Perm and Verify have
// no default: build the value with NewTransferOptions.
type TransferOptions struct {
	Mode   TransferMode
	Perm   PermPolicy
	Verify VerifyLevel
	// Replace, when set, allows overwriting dst only if dst's current SHA-256
	// equals Replace.ExpectSHA256; the old file is kept as
	// <dst>.mediacore-replaced-<unix> until the transfer is durable.
	Replace  *ReplaceSpec
	MkdirAll bool
	// Journal is true via NewTransferOptions; false only for temp files inside
	// an AppDir, which no recovery needs to find.
	Journal bool
}

// NewTransferOptions is the options value with the journal on.
func NewTransferOptions(mode TransferMode, perm PermPolicy, verify VerifyLevel) TransferOptions {
	return TransferOptions{Mode: mode, Perm: perm, Verify: verify, Journal: true}
}

// validate rejects a zero mode, permission policy or verify level.
func (o TransferOptions) validate() error {
	switch {
	case o.Mode == TransferInvalid || o.Mode > Auto:
		return fmt.Errorf("%w: transfer mode %d", ErrInvalidOptions, o.Mode)
	case o.Perm != PermFromSource && o.Perm != PermLibrary:
		return fmt.Errorf("%w: permission policy %d (there is no default)", ErrInvalidOptions, o.Perm)
	case o.Verify != VerifySize && o.Verify != VerifySHA256:
		return fmt.Errorf("%w: verify level %d (there is no default)", ErrInvalidOptions, o.Verify)
	case o.Replace != nil && len(o.Replace.ExpectSHA256) != 64:
		return fmt.Errorf("%w: Replace needs a SHA-256 hex digest", ErrInvalidOptions)
	}
	return nil
}

// TransferResult says what a transfer did.
type TransferResult struct {
	Did      TransferMode // what actually happened (Auto resolves to one of the others)
	Bytes    int64
	SHA256   string // set when Verify == VerifySHA256 or the copy path hashed anyway
	Duration time.Duration
}

// TransferRecord is a finished TransferIn whose caller batch never committed
// (Scope.PendingTransfers).
type TransferRecord struct {
	ID       string
	Src, Dst Path
	Did      TransferMode
	SHA256   string
	At       time.Time
}

// ProtectionOracle is implemented by download adapters (05-download) and by the
// static protected-path list. loaded=false means "unknown" and is treated as
// protected for Move and Remove.
type ProtectionOracle interface {
	Protected(ctx context.Context, p Path) (protected, loaded bool, err error)
}

// SubtreeOracle is a ProtectionOracle that can also answer for a whole
// directory: ProtectedUnder reports whether p or anything under it is
// protected, with the same loaded=false meaning "unknown". Moving or
// quarantining a directory carries everything under it, so an oracle that does
// not implement this cannot vouch for one: a directory Move or Quarantine is
// refused while any such oracle is registered (5L.5: closed).
type SubtreeOracle interface {
	ProtectionOracle
	ProtectedUnder(ctx context.Context, dir Path) (protected, loaded bool, err error)
}

// Digest is a file's SHA-256 and the size of the bytes that were hashed.
type Digest struct {
	SHA256 string
	Size   int64
}

// QuarantineID names a quarantine record; a ULID.
type QuarantineID string

// QuarantineRecord is one held file or directory.
type QuarantineRecord struct {
	ID       QuarantineID
	Original Path
	Held     Path // <root>/<Quarantine>/<id>/<rel>
	Owner    storage.Owner
	Reason   string
	At       time.Time
}

// Mapping says how a remote system's view of a path corresponds to a root.
type Mapping struct {
	// Source is "download:<client-instance-id>", "worker:fingerprint",
	// "itunes", "worker:whisper".
	Source string `json:"source"`
	// Remote is the prefix as the remote side writes it: "<dl-root>/complete", a
	// Windows drive form, or a file URL of either.
	Remote string `json:"remote"`
	Local  Path   `json:"local"`          // root + rel prefix it corresponds to
	Fold   bool   `json:"fold,omitempty"` // case-insensitive match (Windows remotes only)
}

// PathMapper translates between a remote system's paths and library Paths.
type PathMapper interface {
	// ToLocal picks the longest Remote prefix of this Source on a separator
	// boundary, normalises remote separators to '/' and percent-decodes
	// file:// URLs per segment. The result passes ValidateRel. With no mapping,
	// an absolute remote path that is itself under a root resolves identically.
	ToLocal(source, remote string) (Path, error)
	ToRemote(source string, p Path) (string, error)
	Mappings(source string) []Mapping
}

// WatchOptions tunes Manager.Watch.
type WatchOptions struct {
	Debounce   time.Duration // default core.library.watch.debounce (5s)
	Extensions []string      // nil = all files
	Recursive  bool          // the zero value watches the root directory only
	OnError    func(error)   // also surfaced in health and the activity log
}

// EventKind is what a watch event says.
type EventKind uint8

// The event kinds.
const (
	EventInvalid EventKind = iota
	EventChanged           // create, write, rename, remove of a matching file
	EventRescan            // events were lost: rescan the root
)

// String is the metrics label.
func (k EventKind) String() string {
	switch k {
	case EventChanged:
		return "changed"
	case EventRescan:
		return "rescan"
	}
	return "invalid"
}

// Event is one coalesced directory of a debounced batch.
type Event struct {
	Kind EventKind
	Dir  Path // coalesced by directory; the root Path for EventRescan
}
