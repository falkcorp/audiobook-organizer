// file: core/integrations/download/download.go
// version: 1.0.0
// guid: 5f1c9b0e-6a42-4d3b-8c71-2e9a4b7d0c15
// last-edited: 2026-11-11

// Package download is the media-neutral download-client contract
// (05-download): the normalized item state machine, the Client every adapter
// instance implements, the optional FileLister and Relocator capabilities,
// path mapping between what a client reports and what this host sees, and the
// Pool through which modules reach every download instance.
//
// This is phase 1, what ABO uses today (05-download §1): Client (Caps, Status,
// Items), FileLister, Relocator, Adapter, path mapping and Pool (Instances,
// Lease, Items, Files, Relocate). Nothing of phase 2 is built: no Adder,
// Remover, Categorizer, Pauser or SeedLimiter, no grab types, no
// Pool.Grab/Remove/MarkImported/Refresh/Tracked, no Watch, no monitor and no
// core.download.* topic, kind or key (05-download §1 "Deferred-build
// members").
//
// The package is dark until the integrations flip (F7-09): nothing in an app
// registers core.deluge or builds a Pool before it.
//
// Layer L4 (03 R4): a public sub-package of integrations takes its layer. It
// holds no package-level mutable state (R5): a Pool is per App.
package download

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/falkcorp/mediacore/integrations"
)

// Protocol is the wire family of a download client.
type Protocol uint8

// The protocols.
const (
	Torrent Protocol = iota + 1
	Usenet
)

// String is the protocol's name, for logs and errors.
func (p Protocol) String() string {
	switch p {
	case Torrent:
		return "torrent"
	case Usenet:
		return "usenet"
	}
	return "unknown"
}

// ItemID is the client's own identifier: torrent info-hash (lowercase hex v1,
// or v2 where the client reports it), SABnzbd nzo_id, NZBGet NZBID.
type ItemID string

// ItemRef is the stable, cross-instance identity of a download. Phase 1:
// books keeps its hash fields unchanged and builds
// ItemRef{Instance: download/deluge, ID: ItemID(hash)} at the call site
// (Pool.Files, Pool.Relocate). Storing ItemRef instead of a bare hash (which
// is ambiguous only once a second instance exists) is the phase-2 books
// migration (05-download §1 table, SIMP2-7).
type ItemRef struct {
	Instance integrations.Ref
	ID       ItemID
}

// String is "<kind>/<instance>#<id>".
func (r ItemRef) String() string { return r.Instance.String() + "#" + string(r.ID) }

// State is the normalized client item state (05-download §4.2).
type State uint8

// The states. Unknown client values map to StateWarning, never to
// StateCompleted (a wrong Completed triggers an import).
const (
	StateUnknown State = iota
	StateQueued
	StatePaused
	StateDownloading
	StateCompleted // all data present (seeding or stopped); usenet: post-processing finished OK
	StateFailed
	StateWarning // stalled, missing files, encrypted, client-reported error that may clear
)

// String is the state's name ("downloading", "completed", ...), for logs and
// metric labels.
func (s State) String() string {
	switch s {
	case StateUnknown:
		return "unknown"
	case StateQueued:
		return "queued"
	case StatePaused:
		return "paused"
	case StateDownloading:
		return "downloading"
	case StateCompleted:
		return "completed"
	case StateFailed:
		return "failed"
	case StateWarning:
		return "warning"
	}
	return "state(" + strconv.Itoa(int(s)) + ")"
}

// RawUnknownState is the Item.Raw key an adapter sets, to the client's own
// value, on an item whose state the adapter's table does not know and which it
// therefore reports as StateWarning (05-download §4.2). The Pool counts these
// items in mediacore_download_unknown_state_total; the adapter holds no
// metrics handle (an Env carries none), so the key is the signal. This key is
// a name coined by F7-04.
const RawUnknownState = "unknown_state"

// Item is one download as a client reports it.
//
// LocalPath is a plain host path in this phase: 05-download §3 types it
// library.Path, which does not exist until F7-06 completes core/library. F7-06
// or F7-09 re-types LocalPath then (an F7-04 seam, seams.d/F7-04.md).
type Item struct {
	Ref           ItemRef
	Protocol      Protocol
	Name          string
	Category      string
	Tags          []string
	State         State
	Seeding       bool
	SeedGoalMet   bool
	Progress      float64 // 0..1
	TotalSize     int64
	RemainingSize int64
	ETA           time.Duration // 0 = unknown
	Ratio         float64       // torrent
	AddedAt       time.Time
	CompletedAt   time.Time
	RemotePath    string            // content path as the CLIENT reports it
	LocalPath     LocalPath         // RemotePath mapped to this host; "" when unmapped (§4.4)
	Message       string            // client status text, redacted
	CanMoveFiles  bool              // torrent seeding must continue → hardlink/copy, not move
	CanBeRemoved  bool              // torrent: completed and the client has stopped seeding
	Raw           map[string]string // adapter-native fields for diagnostics (bounded)
}

// File is one file of an item.
type File struct {
	Path      string // relative to Item.RemotePath
	Size      int64
	Progress  float64 // 0..1
	Priority  int
	LocalPath LocalPath // mapped; "" when unmapped
}

// ItemFilter selects items. Every non-empty field narrows the result (all
// fields must match; within IDs, Categories and States any one value matches).
// Tags is the exception: an item must carry every listed tag. Categories
// compare case-insensitively, as HEAD's label listing does
// (internal/deluge ListTorrentsByLabel); IDs and tags compare exactly. The
// zero filter selects every item.
type ItemFilter struct {
	IDs        []ItemID
	Categories []string
	Tags       []string
	States     []State
}

// Match reports whether it passes f. Adapters and the fake share it, so every
// client filters alike.
func (f ItemFilter) Match(it Item) bool {
	if len(f.IDs) > 0 && !slices.Contains(f.IDs, it.Ref.ID) {
		return false
	}
	if len(f.Categories) > 0 && !slices.ContainsFunc(f.Categories, func(c string) bool {
		return strings.EqualFold(strings.TrimSpace(c), it.Category)
	}) {
		return false
	}
	for _, tag := range f.Tags {
		if !slices.Contains(it.Tags, tag) {
			return false
		}
	}
	if len(f.States) > 0 && !slices.Contains(f.States, it.State) {
		return false
	}
	return true
}

// ClientStatus is what a client reports about itself.
type ClientStatus struct {
	Version         string
	DefaultSavePath string            // remote
	Categories      map[string]string // category → remote save path ("" when the client keeps none)
	FreeSpace       int64             // -1 unknown
}

// Caps is static per adapter plus what discovery found (version-gated
// features). Phase 1 builds only the fields phase-1 code reads; the phase-2
// fields are appended with the capabilities that use them (an additive
// change, SIMP3-4): Add, ClientFetchesURL, CreateCategory, Tags, Pause,
// SeedGoals, RemoveWithData, PostImportCategory, Priority.
type Caps struct {
	Protocol   Protocol
	Files      bool
	Relocate   bool
	Categories bool
}

// Client is what every download adapter instance implements (phase 1).
// Everything else is an optional capability below: the Pool discovers it by
// type assertion and returns ClassUnsupported when it is absent. Every method
// takes a ctx and honours it.
type Client interface {
	integrations.Instance
	Caps(ctx context.Context) (Caps, error) // cached by Pool (PoolOptions.CapsTTL)
	Status(ctx context.Context) (ClientStatus, error)
	Items(ctx context.Context, f ItemFilter) ([]Item, error) // RemotePath only; Pool fills LocalPath
}

// FileLister is the optional capability behind Pool.Files (ABO: details and
// files). Caps.Files says whether the instance has it.
type FileLister interface {
	Files(ctx context.Context, id ItemID) ([]File, error)
}

// Relocator is the optional capability behind Pool.Relocate (ABO:
// move-after-organize). Caps.Relocate says whether the instance has it.
type Relocator interface {
	SetLocation(ctx context.Context, ids []ItemID, remoteDir string) error
}

// Adapter builds a descriptor for Registrar.Adapter / app.Options.Adapters.
// It is the download kind's typed constructor (05-metadata M1.3): the kind is
// always integrations.KindDownload, whatever m.Kind says, and the built
// instance must be a Client.
func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Client, error),
) integrations.Descriptor {
	m.Kind = integrations.KindDownload
	return integrations.NewDescriptor(m, build)
}
