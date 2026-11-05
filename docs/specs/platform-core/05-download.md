<!-- file: docs/specs/platform-core/05-download.md -->
<!-- version: 1.12.1 -->
<!-- guid: 513fb395-c640-4bb6-b76c-d9fe80b3224e -->
<!-- last-edited: 2026-11-04 -->

# 05 — Download clients (`integrations/download`)

Evidence commit `748fd787a`. Raw inputs: [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) §1, §4;
[`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md) §4.5-§4.8, §7.1 rows 9-13, §7.2;
[`raw/06-tasks-ops.md`](../../plans/platform-core/raw/06-tasks-ops.md) §6 rows 57-60, 207.
Builds on [05-metadata](05-metadata.md) §M1 (the one instance store, secrets, health, egress trust, error taxonomy)
and §M2 (`providerhttp`).

## 1. Purpose & scope

A media-neutral download-client contract — add a grab, list/poll items with a **normalized state machine**,
list files, relocate, categorize, remove — with five adapters (Deluge, qBittorrent, Transmission, SABnzbd,
NZBGet), per-instance configuration (a user may run two qBittorrent instances), **path mapping** between
what the client reports and what this host sees, a **monitor** that turns client state changes into durable
outbox messages, and the **handoff** of completed downloads to the owning app's import.

Two state machines exist and must not be confused:

| Level | Owner | States |
|---|---|---|
| **Client item state** (what the client says) | this file | `Queued, Paused, Downloading, Completed, Failed, Warning` (+ `Seeding` flag) — same set as Sonarr's `DownloadItemStatus` (raw/16 §4.6) |
| **Tracked-download state** (what the app did with it) | `acquire`, [10-tv-movies-proof](10-tv-movies-proof.md) | `Downloading, ImportPending, Importing, Imported, ImportBlocked, FailedPending, Failed, Ignored` (raw/16 §4.7) |

ABO needs only the first level (it never grabs; it discovers and follows existing torrents). TV/Movies need
both; the second is built on the first.

**Single owner (COR-3).** This package alone talks to download clients for the acquisition flow: it owns
client polling, the client-item snapshots, grab records and their crash recovery, and the durable
completion events. `acquire` ([10](10-tv-movies-proof.md) §10.5, a design sketch (non-normative), deferred-build) owns grab *decisions*,
tracked-download records and the import hand-off; it calls `Pool.Grab`, consumes this package's events,
and runs no poller and no recovery of its own.

**Build scope (SIMP-12, cross-cutting resolution 1).** Phase 1 is what ABO uses today and is the only part
R2's integrations flip ([09](09-abo-on-core.md)) builds:

| Pool feature | Phase | Consumer |
|---|---|---|
| `Client` (`Caps`, `Status`, `Items`), the optional `FileLister` and `Relocator`, the phase-1 `Caps` fields, `Adapter`, instances (05-metadata M1), path mapping (§4.4) | **1** | ABO (Deluge) |
| `Pool.Instances`, `Lease`, `Items`, `Files`, `Relocate` | **1** | ABO: discovery by label, details/files, move-after-organize (§10) |
| The optional `Adder`, `Remover`, `Categorizer`, `Pauser`, `SeedLimiter` and their `Caps` fields; the grab types `GrabRequest`, `ResolvedGrab`, `GrabID`, `AddResult`, `Source`, `Priority`, `SeedGoal`, `RemoveOptions`; `Pool.Grab`, grab records and recovery (§4.3), monitor/poll task and the `core.download.item.transitioned.v1` topic (§4.5), `Watch`, `Tracked`, `Refresh`, `MarkImported`, `Pool.Remove`, seed goals, the `core.download.*` poll keys and the `core.download.schedules.poll` schedule key | **2 — deferred-build: not in the ABO extraction plan** | `acquire` (TV/Movies) |
| books' `BookVersion.TorrentHash`/`BookFile.DelugeHash` → stored `download.ItemRef` (a library-scale books row rewrite) | **2** — when a second download instance can exist; ships as a books format change of its own: one-way, checkpointed, behind the per-owner level guard, with its own stage entry and done-check (R3-1; no mirror) | books (SIMP2-7) |

**Phase 2 is a design sketch (non-normative; A-15).** Its contracts below are kept at proof depth so `acquire`
can be checked against them, and are labelled where they start; the SDK freezes none of them, and they are
settled when the first TV/Movies app starts and builds them. HEAD's Deluge client has no add method at all and its
`RemoveTorrent` has no caller (§2), so phase 1 puts neither on the required `Client`: a built package carries no
member whose only consumer is deferred (03 §3.4), and the API-stability gate would otherwise freeze the grab types
before `acquire`, their only consumer, exists to validate them. Phase 2 adds `Adder` and `Remover` beside
`FileLister`/`Relocator`, which is additive (SIMP3-4).

### Deferred-build members

> **Design sketch (non-normative; A-15).** The SDK freezes none of these members.

The phase-2 members of this built package (the §1 table's phase-2 row). None is in the R2 build. The first
column holds package-qualified Go identifiers; topics, task kinds and keys are in the second column (SIMP5-6). The
PR that builds a member deletes its row (SIMP4-1, COR4-19).

| Member | Also deferred (not checked) | Lands with | Contract |
|---|---|---|---|
| `download.Adder`, `download.Remover`, `download.Categorizer`, `download.Pauser`, `download.SeedLimiter` | — | `acquire` (phase 2) | §3 optional capabilities |
| `download.Caps.Add`, `download.Caps.ClientFetchesURL`, `download.Caps.CreateCategory`, `download.Caps.Tags`, `download.Caps.Pause`, `download.Caps.SeedGoals`, `download.Caps.RemoveWithData`, `download.Caps.PostImportCategory`, `download.Caps.Priority`, `download.SeedGoalCaps` | — | `acquire` | §3 `Caps` comment (appended fields, SIMP3-4) |
| `download.GrabRequest`, `download.ResolvedGrab`, `download.GrabID`, `download.AddResult`, `download.Source`, `download.Priority`, `download.SeedGoal`, `download.RemoveOptions`, `download.Selection`, `download.RecordFunc`, `download.ImportOutcome` | — | `acquire` | §3 grab types, §4.3 |
| `download.Pool.Grab`, `download.Pool.Remove`, `download.Pool.MarkImported`, `download.Pool.Refresh`, `download.Pool.Tracked`, `download.Tracked`, `download.TrackedFilter` | — | `acquire` | §3, §4.3, §4.5 |
| `download.Watch` | — (its registrar method `module.Registrar.DownloadWatch` is listed once, in 06 §6.4.9, SIMP6-8) | `acquire` | §3 `Watch` |
| `download.Event`, `download.EventKind` | topic `core.download.item.transitioned.v1`; task kind `core.download.poll`; keys `core.download.schedules.poll`, `core.download.removed_after`, `core.download.warning_to_failed`, `core.download.pending_recovery`, `core.download.snapshot_retention` | `acquire` | §3 topic table, §4.5, §7 |

Out of scope: release selection and the decision engine (10), indexers ([05-indexer](05-indexer.md)), file
import operations (hardlink/copy/move, [05-library](05-library.md)).

## 2. Current state at HEAD

| Fact | Evidence |
|---|---|
| One concrete client, Deluge Web JSON-RPC; no interface. Methods: `Login`, `ListTorrents`, `ListTorrentsByLabel`, `ListLabels`, `GetTorrent`, `MoveStorage`, `Connected`, `GetTorrentDetail(+Context)`, `ListTorrentDetails(+ByLabel)`, `RemoveTorrent` | `internal/deluge/client.go:38-49,259-506` |
| No **add** method; `RemoveTorrent` and `ListTorrentDetails` have no non-test caller | `internal/deluge/client.go:429,483`; grep of `internal/` non-test files |
| Most methods take no ctx and run under `context.Background()` with a 5-minute HTTP timeout | `client.go:145,193-195` |
| Re-login on RPC code 1 is generation-guarded so concurrent callers log in once | `client.go:42-47,198-221` |
| Client is a package-global singleton built on first use from `DelugeWebURL/Password` or `download_client.torrent.deluge`; never rebuilt when config changes; falls back to the Deluge factory-default password when none is set | `internal/deluge/integration.go:27-67` |
| Deluge settings are env-or-file authoritative; password **not** encrypted at rest | `internal/config/config.go:2236-2250,2546-2567`; raw/10 §7 |
| "Follow the book" after organize/version swap is a hard-wired Deluge call (`NotifyDelugeAfterOrganize`, `NotifyDelugeAfterVersionSwap`, `move_storage` gated by `deluge_move_enabled`), errors logged and dropped | `internal/deluge/integration.go:108-187` |
| Book ↔ torrent link is a bare info-hash with no client identity: `BookVersion.TorrentHash`, `BookFile.DelugeHash` | `internal/database/store.go:808`; `internal/database/bookfilecore.go:95` |
| Four Deluge ops (`deluge.centralize`, `deluge.link-backfill`, `deluge.path-update` triggered by `book.relocated`, `deluge.protected-paths-sync` whose cron is declarative only and never runs) + `maintenance.bulk-deluge-import` | `internal/plugins/deluge/centralization.go:30`, `link_backfill.go:130`, `path_update.go:25,49-54`, `protected_paths.go:18`; raw/06 §6 rows 57-60, 207 and note (d) |
| Protected-path cache: files inside any torrent's save path must not be modified | `internal/deluge/protected_paths.go:110-430` |
| Vestigial `plugin.DownloadClient` (`TestConnection`, `ListTorrents`, `MoveStorage`), zero implementors, wrong framework, no add/poll semantics | `internal/plugin/plugin.go:51-72`; raw/11 §4 |
| qBittorrent and SABnzbd settings existed but were never read and were removed; no Transmission/NZBGet code | `internal/config/removed_keys.go:30-35`; raw/11 §1 |
| *arr `IDownloadClient`: `Download(remote, indexer)`, `GetItems`, `GetFiles`, `GetImportItem`, `RemoveItem(deleteData)`, `GetStatus`, `MarkItemAsImported`; takes the app's `RemoteEpisode/RemoteMovie` — the coupling to avoid | raw/16 §4.6, §7.2 |

## 3. Go API

```go
package download // github.com/falkcorp/mediacore/integrations/download

type Protocol uint8

const (
	Torrent Protocol = iota + 1
	Usenet
)

// ItemID is the client's own identifier: torrent info-hash (lowercase hex v1,
// or v2 where the client reports it), SABnzbd nzo_id, NZBGet NZBID.
type ItemID string

// ItemRef is the stable, cross-instance identity of a download. Phase 1:
// books keeps its hash fields unchanged and builds
// ItemRef{Instance: download/deluge, ID: ItemID(hash)} at the call site
// (Pool.Files, Pool.Relocate). Storing ItemRef instead of a bare hash (which
// is ambiguous only once a second instance exists) is the phase-2 books
// migration (§1 table, SIMP2-7).
type ItemRef struct {
	Instance integrations.Ref
	ID       ItemID
}

type State uint8

const (
	StateUnknown State = iota
	StateQueued
	StatePaused
	StateDownloading
	StateCompleted // all data present (seeding or stopped); usenet: post-processing finished OK
	StateFailed
	StateWarning // stalled, missing files, encrypted, client-reported error that may clear
)

type Item struct {
	Ref           ItemRef
	Protocol      Protocol
	Name          string
	Category      string
	Tags          []string
	State         State
	Seeding       bool
	SeedGoalMet   bool
	Progress      float64       // 0..1
	TotalSize     int64
	RemainingSize int64
	ETA           time.Duration // 0 = unknown
	Ratio         float64       // torrent
	AddedAt       time.Time
	CompletedAt   time.Time
	RemotePath    string        // content path as the CLIENT reports it
	LocalPath     library.Path  // RemotePath mapped to this host; zero when unmapped (§4.4)
	Message       string        // client status text, redacted
	CanMoveFiles  bool          // torrent seeding must continue → hardlink/copy, not move
	CanBeRemoved  bool
	Raw           map[string]string // adapter-native fields for diagnostics (bounded)
}

type File struct {
	Path       string  // relative to Item.RemotePath
	Size       int64
	Progress   float64
	Priority   int
	LocalPath  library.Path // mapped; zero when unmapped
}

type ItemFilter struct {
	IDs        []ItemID
	Categories []string
	Tags       []string
	States     []State
}

type ClientStatus struct {
	Version         string
	DefaultSavePath string            // remote
	Categories      map[string]string // category → remote save path
	FreeSpace       int64             // -1 unknown
}

// Caps is static per adapter plus what Status discovered (version-gated
// features). Phase 1 builds only the fields phase-1 code reads; the phase-2
// fields are appended with the capabilities that use them (an additive change,
// SIMP3-4): Add, ClientFetchesURL, CreateCategory, Tags, Pause, SeedGoals
// (SeedGoalCaps{Ratio, Time, IdleTime}, the raw/16 §4.6 compatibility matrix),
// RemoveWithData, PostImportCategory, Priority.
type Caps struct {
	Protocol   Protocol
	Files      bool
	Relocate   bool
	Categories bool
}

// Client is what every download adapter instance implements (phase 1).
// Everything else is an optional capability below: Pool discovers it by type
// assertion and returns ClassUnsupported when it is absent.
type Client interface {
	integrations.Instance
	Caps(ctx context.Context) (Caps, error) // cached by Pool for core.download.caps_ttl
	Status(ctx context.Context) (ClientStatus, error)
	Items(ctx context.Context, f ItemFilter) ([]Item, error) // RemotePath only; Pool fills LocalPath
}

// Optional capabilities, phase 1 (ABO: details/files, move-after-organize).
type FileLister interface {
	Files(ctx context.Context, id ItemID) ([]File, error)
}
type Relocator interface {
	SetLocation(ctx context.Context, ids []ItemID, remoteDir string) error
}

// Adapter builds a descriptor for Registrar.Adapter / app.Options.Adapters.
func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Client, error),
) integrations.Descriptor

// ---- pool (per App; modules reach it as module.Deps.Downloads, 06 / COR-1) ----

type Pool struct{ /* unexported */ }

// Phase 1.
func (p *Pool) Instances(proto Protocol) []integrations.Ref
func (p *Pool) Lease(ctx context.Context, ref integrations.Ref) (Client, func(), error)
func (p *Pool) Items(ctx context.Context, ref integrations.Ref, f ItemFilter) ([]Item, error) // with LocalPath
func (p *Pool) Files(ctx context.Context, item ItemRef) ([]File, error)
func (p *Pool) Relocate(ctx context.Context, items []ItemRef, localDir library.Path) error // groups by instance; maps path

// ---- phase 2: grabs and monitor (deferred-build, built with acquire) ---------
// DESIGN SKETCH (non-normative, A-15): the SDK freezes nothing below, and
// nothing below is in the ABO extraction plan. Each optional interface is
// additive to the phase-1 Client, so landing phase 2 passes the API-stability
// gate (SIMP3-4).

// Source is exactly one of the four.
type Source struct {
	URL     string // http(s) link to a .torrent or .nzb; data, not the instance's configured URL: fetched by the ADAPTER via env.HTTP under EgressPublicOnly unless the client fetches it itself (Caps.ClientFetchesURL). Indexer links needing the indexer's credentials are fetched by indexer.Pool.Fetch first (05-indexer §4)
	Magnet  string
	Torrent []byte
	NZB     []byte
}

type Priority int8 // -1 low, 0 normal, 1 high, 2 top/force (mapped per client; unsupported → normal + Warning in AddResult)

type SeedGoal struct {
	Ratio    float64       // 0 = client default
	Time     time.Duration // 0 = client default
	IdleTime time.Duration // 0 = client default
}

// GrabRequest is media-neutral: no episode, movie or book fields (raw/16 §7.2).
type GrabRequest struct {
	Protocol       Protocol
	Source         Source
	Name           string          // display/job name; usenet clients use it as the job name
	InfoHash       string          // expected hash, torrent only; verified after Add when present
	Category       string          // client category/label; "" = instance default category
	SavePathHint   library.Path    // LOCAL path; mapped to the client's path (§4.4); zero = client default
	Priority       Priority
	Tags           []string        // in addition to the core tag "mc:<grabID>" when Caps.Tags
	Paused         bool
	SeedGoal       *SeedGoal       // torrent only
	Owner          string          // "core.acquire" or a registered module ID; routes the events (§4.5)
	Refs           []entity.Ref    // opaque entities the grab is for (a season pack: many); stored, never interpreted
	IdempotencyKey string          // required; same key = same grab (§4.3); acquire uses its tracked-download ID
}

type GrabID string // ULID

// ResolvedGrab is GrabRequest after core processing: SavePathHint mapped to
// the client path, URL fetched (unless ClientFetchesURL), core tag added.
type ResolvedGrab struct {
	GrabRequest
	RemoteSavePath string
	GrabID         GrabID
}

type AddResult struct {
	Ref       ItemRef
	GrabID    GrabID
	Duplicate bool     // the client already had it (same hash / same key)
	Warnings  []string // e.g. "priority not supported; added at normal"
}

type RemoveOptions struct {
	DeleteData bool
}

// Optional capabilities, phase 2 (each also flagged in Caps).
type Adder interface {
	Add(ctx context.Context, req ResolvedGrab) (ItemID, bool, error) // bool: duplicate
}
type Remover interface {
	Remove(ctx context.Context, id ItemID, o RemoveOptions) error // ClassNotFound if already gone
}
type Categorizer interface {
	SetCategory(ctx context.Context, id ItemID, category string) error
	EnsureCategory(ctx context.Context, category, remoteSavePath string) error
}
type Pauser interface {
	Pause(ctx context.Context, ids ...ItemID) error
	Resume(ctx context.Context, ids ...ItemID) error
}
type SeedLimiter interface {
	SetSeedGoal(ctx context.Context, id ItemID, g SeedGoal) error
}

type Selection struct {
	Pinned   *integrations.Ref // e.g. an indexer's pinned client (raw/16 §4.1 DownloadClientId)
	Tags     []string          // instance must carry all
	Fallback bool              // try the next instance on failure (default true)
}

// RecordFunc lets the grab's owner write its own rows into the SAME batch
// that persists the pending grab record (acquire: its tracked-download row,
// after Bind-ing c/acquire/, 05-storage §5S.3.2). The owner therefore never
// keeps a second intent row or a second recovery path (COR-3). nil = none.
type RecordFunc func(b *storage.Batch, id GrabID) error

// Grab selects an instance (pinned, else enabled instances for the protocol by
// Priority, round-robin among equal priority, next on failure — raw/16 §4.5),
// persists the grab record (with rec's rows), resolves the request and calls
// Add on the instance's Adder (ClassUnsupported when it has none).
func (p *Pool) Grab(ctx context.Context, req GrabRequest, sel Selection, rec RecordFunc) (AddResult, error)

func (p *Pool) Remove(ctx context.Context, item ItemRef, o RemoveOptions) error // through Remover

// MarkImported applies the instance's post-import policy: post-import
// category (Caps.PostImportCategory) and remove-when-done (only when
// Item.CanBeRemoved, and for torrents only once SeedGoalMet and the client
// has stopped seeding — raw/16 §4.7).
func (p *Pool) MarkImported(ctx context.Context, item ItemRef, o ImportOutcome) error

// Refresh asks for an immediate poll by starting a core.download.poll run
// unless one is already queued (§4.5); there is no debounce timer (SIMP-6).
func (p *Pool) Refresh(ctx context.Context, refs ...integrations.Ref) error

// Tracked iterates the monitor's persisted snapshots (basis of a queue view).
func (p *Pool) Tracked(ctx context.Context, f TrackedFilter) iter.Seq2[Tracked, error]

type Tracked struct {
	Item      Item
	GrabID    GrabID          // "" for foreign items (not grabbed through Pool)
	Owner     string          // GrabRequest.Owner; "" for foreign items
	Refs      []entity.Ref    // GrabRequest.Refs; nil for foreign items
	FirstSeen time.Time
	LastSeen  time.Time
	Imported  bool
}

// Watch declares foreign items (not grabbed through Pool) a module wants the
// monitor to report. Registered via module.Registrar.DownloadWatch. ABO does
// not need it: its discovery calls Pool.Items with a category filter (§10).
type Watch struct {
	Module     string
	Categories []string
	Tags       []string
}

// EventKind is the lifecycle step one Event reports. It marshals as its
// name: "grab_added", "grab_failed", "completed", "failed", "removed".
type EventKind uint8

const (
	EventGrabAdded  EventKind = iota + 1 // grab record reached added, synchronously or by recovery
	EventGrabFailed                      // grab record reached failed
	EventCompleted                       // client state reached Completed for a tracked item
	EventFailed                          // reached Failed (or Warning for longer than warning_to_failed)
	EventRemoved                         // a tracked, not-yet-imported item disappeared from the client
)

// Event is the payload of the ONE durable topic core.download.item.transitioned.v1
// (COR3-4). Message key: GrabID; a foreign item (no grab record) uses
// ItemRef.String(). One key is one partition and one cursor per consumer, so a
// consumer sees a download's events in the order they were staged (§4.5).
type Event struct {
	Kind           EventKind
	GrabID         GrabID             // "" for foreign items
	IdempotencyKey string             // GrabRequest.IdempotencyKey; "" for foreign items
	Owner          string             // GrabRequest.Owner; "" for foreign items matched by a Watch
	Refs           []entity.Ref       // nil for foreign items
	WatchedBy      []string           // modules whose Watch matched (foreign items)
	Ref            ItemRef            // zero on EventGrabFailed
	Item           *Item              // item kinds only
	Previous       State              // item kinds only
	Files          []File             // EventCompleted only, when Caps.Files; mapped
	Recovered      bool               // EventGrabAdded by recovery after an ambiguous Add (§4.3)
	Class          integrations.Class // EventGrabFailed only
	At             time.Time
}
```

Registrar slice and topics (06 owns the type; phase 2, design sketch, non-normative):
`func (r *Registrar) DownloadWatch(w ...download.Watch)`.
Owners consume the durable topic below as ordinary outbox consumers ([05-events](05-events.md)), each selecting
its own messages with the consumer filter `outbox.Where` (§4.5, COR2-10). The whole download lifecycle is **one**
durable topic, so per-key order is the outbox's ordinary per-partition order and no consumer reads several
cursors that could interleave (COR3-4). Both topics are declared once in the events catalog (SIMP-5), in its
**deferred-build** topic table: phase 1 declares neither, so no topic picker (notify rules, UI) offers a topic
without a producer (SIMP2-10). Only the bus topic `item.changed.v1` carries `Realtime` metadata; the durable topic
carries none, because a Durable topic never reaches the bus the realtime hub reads (05-events rejects Realtime on
a Durable topic; COR2-17).

| Topic (phase 2) | Kind | When |
|---|---|---|
| `core.download.item.transitioned.v1` (`Event`; key `GrabID`, foreign items `ItemRef`) | outbox | `grab_added`: a grab record reaches `added`, synchronously or by recovery; `grab_failed`: it reaches `failed` (invalid, every instance failed, or unrecovered after `core.download.pending_recovery`); `completed`: client state reaches `Completed` for a tracked item; `failed`: it reaches `Failed` (or `Warning` for longer than `core.download.warning_to_failed`); `removed`: a tracked, not-yet-imported item disappears from the client |
| `core.download.item.changed.v1` | events bus, notify-only | any transition; for UI/realtime |

## 4. Lifecycle

### 4.1 Instances and adapters

Instances are `download/<id>` records in the one instance registry (05-metadata §M1, SIMP-4), native rows only
(ABO's Deluge is migrated into `download/deluge` in R2, §10). Secrets are
`integrations.download.<id>.<field>` (05-config §2.3 grammar). Secret fields per adapter: Deluge `password` (the web UI's) and `daemon_password` (the daemon connection HEAD
configures under `download_client.torrent.deluge`);
qBittorrent `username`, `password`; Transmission `username`, `password`; SABnzbd `api_key`; NZBGet
`username`, `password`. Every adapter declares `Meta.Egress = EgressConfigured`: a client on the LAN (the
usual case) needs `Local=true`, which only an interactive `core:admin` holder can set (05-metadata M1.3,
SEC2-7), or its host listed in `core.integrations.egress.lan_allow`; a URL change clears the stored password
(SEC-15). The migrated `download/deluge` carries `Local` bound to its stored web URL's origin (05-metadata
M1.10), so a LAN Deluge keeps working; a later URL change follows the native rule. Adapters log in lazily on first
call and re-login once on an auth error with the generation guard of `internal/deluge/client.go:198-221`
(concurrent callers log in once).

| Adapter | Wire (raw/16 §4.6) | Health probe | Phase |
|---|---|---|---|
| `core.deluge` | Web JSON-RPC: `auth.login`, `web.connected`, `web.get_hosts`/`web.connect`, `core.get_torrents_status`, `core.move_storage`, `label.*`; phase 2 adds `core.add_torrent_magnet`/`_file`/`_url`, `core.remove_torrent`, `core.set_torrent_options` | `web.connected` (and connect to the first host when false) | **1** — phase 1: list, by label, details/files, move, status (ABO's needs). `Add`/`Remove` (`Adder`, `Remover`) land with phase 2 (`RemoveTorrent` has no caller at HEAD: `git grep '\.RemoveTorrent('` @748fd787a is empty; SIMP2-17, SIMP3-4) |
| `core.qbittorrent` | WebUI API v2: `auth/login`, `app/webapiVersion`, `torrents/info`, `torrents/files`, `torrents/add`, `torrents/delete`, `torrents/setLocation`, `torrents/setCategory`, `torrents/createCategory`, `torrents/categories`, `torrents/setShareLimits`, `torrents/addTags`, `torrents/topPrio` | `app/version` | **2** — first non-Deluge torrent adapter: richest surface in the evidence (categories, tags, share limits, post-import category, priority) |
| `core.sabnzbd` | HTTP `api?mode=`: `addurl`/`addfile`, `queue`, `history`, `fullstatus`, `get_config`, `version`, `delete` (+ `apikey` query, redacted) | `mode=version` then `fullstatus` | **2** — first usenet adapter; failed-download handling in *arr is compatible by default only with SABnzbd/NZBGet (raw/16 §4.8) |
| `core.transmission` | JSON-RPC: `torrent-add`, `torrent-get`, `torrent-set`, `torrent-set-location`, `torrent-remove`, `session-get`, `session-stats`; `X-Transmission-Session-Id` 409 handshake | `session-get` | **3** |
| `core.nzbget` | JSON-RPC: `append`, `listgroups`, `history`, `editqueue`, `config`, `status`, `version` | `version` | **3** |

Phase 1 = the ABO extraction waves ([09-abo-on-core](09-abo-on-core.md)); phase 2 = when the first TV/Movies
app starts (the `acquire` wave, 10; deferred-build, not in the ABO extraction plan); phase 3 = after phase 2
ships. The Pool features follow the same phases (§1 table). Order rationale: Deluge is the only
client with real users today; a TV/Movies app needs one torrent and one usenet client to prove the contract
on both protocols; Transmission and NZBGet add no new contract shape.

### 4.2 State normalization

Each adapter owns a mapping table and a golden test over **recorded fixtures**; the mappings below are the
specification and are confirmed against each client's API documentation and recorded responses when the
adapter is built (open item 1).

| Client value | → State | Seeding |
|---|---|---|
| Deluge `Queued` | Queued | – |
| Deluge `Checking`, `Allocating`, `Moving` | Downloading if progress < 1, else Completed | – |
| Deluge `Downloading` | Downloading | – |
| Deluge `Seeding` | Completed | yes |
| Deluge `Paused` | Paused if progress < 1, else Completed | no |
| Deluge `Error` | Warning (Failed after `warning_to_failed`) | – |
| qBittorrent `downloading`, `forcedDL`, `metaDL`, `forcedMetaDL`, `checkingDL`, `allocating`, `moving` (progress < 1) | Downloading | – |
| qBittorrent `queuedDL` | Queued | – |
| qBittorrent `pausedDL`/`stoppedDL` | Paused | – |
| qBittorrent `stalledDL` | Warning | – |
| qBittorrent `uploading`, `forcedUP`, `stalledUP`, `queuedUP`, `checkingUP` | Completed | yes |
| qBittorrent `pausedUP`/`stoppedUP` | Completed | no |
| qBittorrent `error`, `missingFiles` | Failed / Warning | – |
| Transmission status 0 (stopped) | Paused if incomplete, else Completed | no |
| Transmission 1/2 (check) , 3 (download wait) | Queued | – |
| Transmission 4 | Downloading | – |
| Transmission 5/6 (seed wait/seeding) | Completed | yes |
| Transmission `error` ≠ 0 | Warning (3 = local error → Failed) | – |
| SABnzbd queue `Queued`, `Grabbing`, `Fetching`, `Propagating` | Queued | – |
| SABnzbd queue `Downloading` | Downloading | – |
| SABnzbd queue `Paused` | Paused | – |
| SABnzbd history `Verifying`, `Repairing`, `Extracting`, `Moving`, `Running`, `QuickCheck` | Downloading (post-processing) | – |
| SABnzbd history `Completed` | Completed | – |
| SABnzbd history `Failed` | Failed | – |
| NZBGet group `QUEUED` / `PAUSED` / `DOWNLOADING`, `FETCHING` | Queued / Paused / Downloading | – |
| NZBGet `PP_QUEUED`, `LOADING_PARS`, `VERIFYING_*`, `REPAIRING`, `UNPACKING`, `MOVING`, `EXECUTING_SCRIPT` | Downloading (post-processing) | – |
| NZBGet history `SUCCESS/*` / `WARNING/*` / `FAILURE/*` / `DELETED/*` | Completed / Warning / Failed / removed | – |

Unknown client values map to `StateWarning` with the raw value in `Message` and increment
`mediacore_download_unknown_state_total`; they never map to `Completed` (a wrong Completed triggers an import).

### 4.3 Grab (phase 2, deferred-build)

> **Design sketch (non-normative; A-15).** Not built in the extraction; the SDK freezes none of it.

`Pool.Grab`: validate (exactly one `Source`; `IdempotencyKey` set; `Owner` is `"core.acquire"` or a
registered module ID; `Refs` non-empty) → look up `c/download/grabs/by-key/<key>`; if present return it
(`Duplicate`) → choose instance → write grab record `pending{Owner, Refs, key}` **plus the owner's
`RecordFunc` rows** in one batch → resolve (map `SavePathHint`; fetch a `Source.URL` via `env.HTTP` with
`EgressPublicOnly` unless the client fetches; parse torrent info-hash locally when bytes are present)
→ `Adder.Add` (no `Adder`: `ClassUnsupported`, the next instance is tried) → write grab record `added{Ref}`,
a tracked snapshot and the outbox message `core.download.item.transitioned.v1{Kind: grab_added}` in one batch. On a timeout or
ambiguous error after Add was sent, the record stays `pending` and the next monitor pass **recovers** it by core
tag `mc:<grabID>` (when `Caps.Tags`), else by info-hash, else by job name, and stages
`item.transitioned.v1{Kind: grab_added, Recovered: true}`; unrecovered after `core.download.pending_recovery` → `failed` with
`item.transitioned.v1{Kind: grab_failed}`. No blind re-add. This is the **only** grab-intent record and the only recovery
path in core: `acquire` keeps none (COR-3).

**Per-grab event order (COR3-4).** Every event of a grab is staged with message key `GrabID`, so all of them
land in one partition and each consumer reads them through one cursor in staging order (05-events
"Partitioning / order"). The two producers of a grab's events never race: `c/download/grabs/` is a
`Serialized` sub-prefix and the `pending → added | failed` step is a `Batch.GetForUpdate` compare-and-set
(05-storage §5S.3.2), so `Pool.Grab` and a recovery pass never both stage `grab_added`; and the poll stages item
events (`completed`, `failed`, `removed`) only for grabs whose `added` record has committed, so `grab_added`
always precedes them, also for a cached torrent or usenet job that is complete on its first poll. A recovery
pass that finds the item already complete stages `grab_added` and then `completed` in that order in one batch.
Every write to `c/download/grabs/` (a native keyspace) derives from a `GetForUpdate` read, and a writer whose commit
conflicts re-reads and decides again, so no later write can revert a `failed` or a recovery's `added` (R6-1).
Both intake rows of that batch commit together, so their relative order is their key order: it holds because
`Stage` takes message IDs from one monotonic generator per `Outbox`, so intake keys preserve `Stage` call order
within a process (05-events §5E.4, §5E.5.2, `TestStageOrderWithinBatch`; COR4-14). As defence in depth, acquire applies a
`completed` that reaches a TD still in `Grabbing` as `Grabbing → Downloading → ImportPending` (or `ImportBlocked`)
in one transition ([10](10-tv-movies-proof.md) §10.5.4), so a reorder can never lose a completion.

### 4.4 Path mapping

Mappings are `library.Mapping{Source: "download:<instance-id>", Remote, Local, Fold}` rows owned by
[05-library](05-library.md) (`core.library.mappings`); the instance form edits them through that API, and the
Pool applies them with `env.Paths.ToLocal`/`ToRemote("download:<instance-id>", …)`: longest-prefix,
separator-aware, `Fold` for Windows remotes (a client reporting `D:\dl\x` maps into a local root), never
string-replace mid-segment; no mapping and not under a root → `library.ErrUnmapped`. `Item.LocalPath`,
`File.LocalPath` are filled by the Pool; `SavePathHint` and `Relocate` map local → remote and fail with
`ClassInvalid` ("no mapping covers <path>") rather than sending a host path the client cannot see.
`Test` checks that the client's default save path and every category path map to a directory readable
through the library's `os.Root` and reports unmapped ones as `HealthDegraded`.

### 4.5 Monitor and handoff (phase 2, deferred-build)

> **Design sketch (non-normative; A-15).** Not built in the extraction; the SDK freezes none of it.

Task `core.download.poll` (05-tasks) is the only client poller in core. It is scheduled by
`tasks.ScheduleFromConfig("core.download.schedules.poll")` (the one schedule-key rule, R2-7) and started on
demand by `Pool.Refresh`
(`acquire` calls it after a grab, the facade's `RefreshMonitoredDownloads` command calls it). `Refresh` starts
a run only when none of the kind is queued, and the kind's `ConcurrencyKey` keeps runs from overlapping, so
on-demand bursts collapse without a debounce timer. Per enabled instance, with parallelism
`tasks.Fixed(min(instances, 4), "one poll per download client")`:

1. `Items(filter)` where filter = grabbed IDs ∪ all `Watch` categories/tags (never "all items" unless a
   Watch asks for it — large seedboxes have tens of thousands).
2. Diff against snapshots in `c/download/items/<instance>/<id>`.
3. For each transition: one storage **batch** containing the new snapshot and, for completed/failed/removed,
   the `core.download.item.transitioned.v1` message keyed by the item's `GrabID` (a foreign item: its `ItemRef`) (same-batch
   outbox, 03 §3.3 `outbox`). The bus event is published after commit.
4. An instance whose poll fails keeps its snapshots untouched (no "removed" storm on an outage); removal is
   declared only after `core.download.removed_after` consecutive successful polls without the item.

**Routing by owner (COR2-10).** Every outbox message carries `Owner` and `Refs` from the grab record. A
consumer selects its messages with the consumer filter `outbox.Where` (05-events); unmatched messages are acked
without effect, so no consumer starts a run or applies a write for another owner's download:

| Owner | Consumer |
|---|---|
| `"core.acquire"` | **one** consumer, `core.acquire.download-events` (`ConsumeTx` over `core.download.item.transitioned.v1`, `Where(Owner == "core.acquire")`): it applies the tracked-download transition in the relay batch and, for `ImportPending`, stages the module's import run in that same batch ([10](10-tv-movies-proof.md) §10.5.4), so an import never starts before its transition. A stale or illegal transition is acked, logged and counted, never returned as an error, so it cannot block the partition (10 §10.5.5, COR3-4). It is in the kernel's static core-consumer list, so outbox retention keeps each message until it has acked (05-events) |
| a module | that module's `ConsumeIntoTask[Raw]` consumer with `Where(Owner == "<module>")` |
| foreign item (no grab record) | each module in `WatchedBy`, with `Where(slices.Contains(WatchedBy, "<module>"))` |

The owner imports (its own code, in its own task kind: match entities, hardlink/copy/move via `library`,
rename) and calls `Pool.MarkImported` **after** the import commits. Delivery is at-least-once; consumers are
idempotent (keyed by `ItemRef` + `CompletedAt`).

### 4.6 Shutdown

Phase 1: close instances (05-metadata §M1.4). Phase 2: stop the poll task (05-tasks drain) and finish
in-flight batches first.

## 5. Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| Client unreachable / 5xx during poll | open for the App, closed for that instance's transitions (snapshots frozen) | An outage must not look like every item being removed. |
| Auth rejected (Deluge RPC code 1 after one re-login; qBittorrent 403 after re-login; SAB `API Key Incorrect`) | closed: `ClassAuth`, `HealthAuthFailed`, hold 6h | Needs a human; retrying looks like an attack (`internal/metadata/throttle.go:47-49` rationale). |
| Item not found on `Remove` (phase 2) / `Relocate` | `ClassNotFound` → treated as success for Remove, error for Relocate | Already gone is the goal of Remove (Deluge `ErrTorrentNotFound`, `client.go:97-101`). |
| `Add` rejected as invalid (bad torrent/nzb; phase 2) | closed: `ClassInvalid`, grab `failed` (+ `item.transitioned.v1{Kind: grab_failed}`), no fallback to another instance | Another client would reject the same file. |
| `Owner` neither `core.acquire` nor a registered module, or empty `Refs` | closed: `ClassInvalid`, nothing written | Every completion must have exactly one consumer. |
| `RecordFunc` returns an error | closed: nothing written, nothing sent to a client | The owner's row and the grab record are one fact. |
| `Add` transport failure before send (phase 2) | fall back to next instance (Selection.Fallback) | Capacity is per instance. |
| `Add` ambiguous (timeout after send; phase 2) | closed for re-add; recovery by tag/hash/name (§4.3) | Double grabs waste bandwidth and confuse imports. |
| Unmapped path on `SavePathHint`/`Relocate` | closed: `ClassInvalid` | Never send a path the client cannot see. |
| Unmapped `RemotePath` on completion | message still sent with a zero `LocalPath`; app marks ImportBlocked | Visible, actionable, not dropped. |
| Unknown client state value | `StateWarning`, counted | Never guess `Completed`. |
| Optional capability absent (`FileLister`, `Relocator`; phase 2: `Adder`, `Remover`, …) | `ClassUnsupported` | Caller decides (e.g. ABO skips move_storage, as today when disabled; `Pool.Grab` tries the next instance). |
| Snapshot/outbox batch write fails | closed: transition retried next poll | Same-batch rule: no event without state, no state without event. |
| `core.integrations.disabled` true (a restored copy, R6-7; 05-metadata M1.4) | closed: `Lease` gives `ErrDisabled`, so `Items`, `Files` and `Relocate` fail and the poll leaves every snapshot untouched (as for an outage); phase 2: `Pool.Grab` stores nothing. A schedule that fires against a disabled integration fails closed, but its run is enqueued and its clock advances (the phase-2 `core.download.schedules.poll` run included). The optional owner rehearsal on a restored archive sets the switch and runs with `--network none` (09) | A copy of a production store must never move, add or remove items in a production client (OPS6-4, SEC7-6); schedules must still fire there (DATA5-8). |
| Relocate after organize fails (ABO "follow the book") | open for the organize (already succeeded), error recorded on the item and surfaced in health | Today it is a log line only (`integration.go:131-135`); the design keeps "best effort" but makes it visible. |

## 6. Concurrency contract

Adapters are goroutine-safe (login guarded; RPC ids atomic as `client.go:48`). Every method takes ctx; there
is no `context.Background()` path in core (HEAD's no-ctx methods, `client.go:193-195`, are not carried).
Phase 2: poll fan-out is bounded (§4.5). Grab is serialized per `IdempotencyKey` with core's own internal
keyed mutex, `core/internal/keyedmutex` (a core-internal copy, added by 04 §9; ABO's
`internal/keyedmutex` stays in ABO, 03 §3.2.1, and core cannot import it, R1; COR2-16). Serializing grabs
**across** keys for the same entity is the owner's job: `acquire`'s per-entity grab lock is the executor's
ref-lock table (10 §10.5.6, 05-tasks §5T.3.4). Monitor writes per instance are sequential
(one poller per instance at a time; the kind's `ConcurrencyKey` keeps runs from overlapping). Library-scale app work over download items (ABO link-backfill,
centralize) runs as module tasks through `RunItems` with declared parallelism; the providerhttp bucket of the
instance bounds the request rate regardless of worker count.

## 7. Config keys

Instance records are data in the one instance store (05-metadata M1), not config keys (SIMP-4); the rows
marked "instance setting" are fields of a `download/<id>` record.

Protection classes per 05-config §1.3.7 (SEC2-8). Instance fields follow the instance rules of 05-metadata M1.3
(`core:integrations.manage`; `Local` only with the ProtectAdmin check).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| instance setting `category` | string | `""` | yes | instance rules |
| instance setting `post_import_category` (phase 2) | string | `""` | yes | instance rules |
| instance setting `remove_completed` / `remove_failed` (phase 2) | bool | `true` / `true` (raw/16 §4.6 definition defaults) | yes | instance rules |
| instance setting `limits.*` | providerhttp limits | adapter default: RPS 10, burst 10, timeout 30s (Add 120s), no daily quota | yes | instance rules |
| `core.library.mappings` (Source `download:<id>`) | list of `library.Mapping` (05-library) | `[]` | yes | as 05-library declares it |
| `core.download.caps_ttl` | duration | `1h` | yes | none |
| `core.download.schedules.poll` (phase 2) | `tasks.ScheduleConfig`, declared by `download`, registered through `tasks.ScheduleFromConfig` (R2-7) | `{enabled: true, every: 1m}` | yes (re-read every scheduler poll) | none |
| `core.download.removed_after` (phase 2) | int (polls) | `3` | yes | none |
| `core.download.warning_to_failed` (phase 2) | duration | `24h` (0 = never) | yes | none |
| `core.download.pending_recovery` (phase 2) | duration | `10m` | yes | none |
| `core.download.snapshot_retention` (phase 2) | duration | `30d` after imported/removed | yes | none |

## 8. Metrics

Phase 2 metrics are marked (design sketch, A-15); phase 1 ships only the unmarked ones. Names use core's default
namespace `mediacore`; an app's `app.Options.MetricNamespace` replaces it (03 §3.6, A-5). All are new with core.

| Name | Type | Labels |
|---|---|---|
| `mediacore_download_items` (phase 2) | gauge | `instance`, `state` |
| `mediacore_download_transitions_total` (phase 2) | counter | `instance`, `from`, `to` |
| `mediacore_download_grabs_total` (phase 2) | counter | `instance`, `protocol`, `result` (`added`, `duplicate`, `failed`, `recovered`) |
| `mediacore_download_poll_seconds` (phase 2) | histogram | `adapter` |
| `mediacore_download_poll_failures_total` (phase 2) | counter | `instance`, `class` |
| `mediacore_download_unknown_state_total` | counter | `adapter` |
| `mediacore_download_unmapped_paths_total` | counter | `instance` |

## 9. Tests

Core, phase 1: `TestPool_ItemsFillsLocalPath`, `TestPool_RelocateGroupsByInstance`,
`TestPaths_UnmappedRelocateRefused`, `TestPaths_WindowsRemote`, `TestDeluge_PrivateHostNeedsLocal`,
`TestPool_IntegrationsDisabledNoClientCall` (`core.integrations.disabled`: a fake client sees no request and no
snapshot changes; R6-7),
`TestDelugeMigration_InstanceAndPassword` (books' R2 step M6 over a HEAD-written fixture: one native
`download/deluge` row with HEAD's URL and settings, its passwords refs to `integrations.download.deluge.password` and
`integrations.download.deluge.daemon_password` holding HEAD's values, `Local` bound to the stored URL's origin, and no Deluge field left in the blob; 05-metadata
M1.9, M1.10).
Core, phase 2 (with `acquire`; a sketch until then): `TestGrab_IdempotencyKeyDedupes`, `TestGrab_AmbiguousAddRecoveredByTag`,
`TestGrab_FallbackOnTransportOnly`, `TestGrab_InvalidNoFallback`, `TestGrab_OwnerAcquireAccepted`,
`TestGrab_RecordFuncInSameBatch`, `TestGrab_AddedEventStaged`, `TestPool_SelectionPriorityRoundRobin`,
`TestPool_PinnedInstance`, `TestMonitor_TransitionWritesSnapshotAndOutboxInOneBatch`,
`TestMonitor_OutageFreezesSnapshots`, `TestMonitor_RemovedAfterNPolls` (synctest),
`TestMonitor_WatchFiltersForeignItems`, `TestMonitor_RoutesByOwner` (each owner's `Where` filter acks other
owners' messages without a run or a write; COR2-10), `TestMonitor_GrabEventsShareKey` (every event of one grab
carries key `GrabID`; a cached item complete on its first poll is staged `grab_added` then `completed`; COR3-4),
`TestGrab_RecoveryAndAddNeverBothStage` (`-race`: a late `Add` success and a recovery pass on one pending
record → exactly one `grab_added`), `TestGrab_EveryRecordWriteChecked` (`-race`: `MarkImported` against a
concurrent recovery or failure on one grab record → exactly one commits and the loser re-reads; R6-1), `TestPool_RefreshDoesNotStackRuns`, `TestMarkImported_RemovesOnlyWhenSeedGoalMet`.
Adapters: `coretest.DownloadConformance(t, factory, fixtures)` — one suite every adapter passes, covering what
is built: phase 1 items/state mapping golden table, status, files, relocate, auth failure and re-login, unknown
state; phase 2 adds add (magnet, file, URL), duplicate add, category and remove ± data for adapters implementing
`Adder`/`Remover`/`Categorizer` (SIMP3-4); fixtures recorded from real clients; live mode behind
`MEDIACORE_LIVE_DOWNLOAD=1` against containerized clients. Fake: `coretest.FakeDownloadClient` — in-memory
client with a scriptable clock-driven state machine (`Advance(id, State)`), capability toggles and fault injection.

## 10. Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/deluge/client.go` | `core/integrations/download/deluge` adapter (`core.deluge`); RPC/auth code reused; every method gains ctx; `http.Client` from `env.HTTP` (5-minute timeout becomes per-op limits) |
| `deluge.GetClient()` global, `ResetGlobalClientForTest`, `SetGlobalClientForTest` | deleted; `Pool.Lease` |
| `DelugeWebURL/Password`, `download_client.torrent.deluge` | the **native** instance `download/deluge`, written in R2 by books' cutover step M6 (05-metadata M1.10): its URL and settings into the row, its passwords moved by the M2 secrets step into `c/secrets/` as `integrations.download.deluge.password` (the web password) and `integrations.download.deluge.daemon_password` (`download_client.torrent.deluge.password`; 05-config §1.12.3), encrypted under the keyring (both plaintext in the blob at HEAD) and referenced from the row, `Local` bound to the stored web URL's origin and `OperatorSecretOrigin` set to it (05-metadata M1.10 "Trust carried over"); the step deletes the Deluge fields from the blob. From then on the native rules apply: a URL change by anyone without the ProtectAdmin check clears the stored password and `Local` (05-metadata M1.3). The env-authoritative behaviour is an **in-memory operator overlay** on the effective instance while an operator value is set, never written to the row or `c/secrets/`, so removing the env value restores the stored password as HEAD does (05-metadata M1.4 step 2, SIMP2-5); every operator value overlays and locks its field (05-config §1.3.5). An env-given Deluge password is served beside the migrated web URL, and is withheld beside one a non-admin sets later (05-metadata M1.4 step 2) |
| `deluge_move_enabled`, `deluge_discovery_label`, `deluge_discovery_enabled` | `modules.books.download.*` settings (they are ABO behaviour, not client config) |
| `BookVersion.TorrentHash`, `BookFile.DelugeHash` | **phase 1: unchanged** — no books row rewrite; books builds `download.ItemRef{Instance: download/deluge, ID: ItemID(hash)}` at the call site. Storing `ItemRef` is the phase-2 books migration (§1 table), with its own release entry when it ships (SIMP2-7) |
| `NotifyDelugeAfterOrganize/AfterVersionSwap/MoveStorage(ToDir)` | books code calling `Pool.Relocate` (reached as `module.Deps.Downloads`, COR-1) for every `ItemRef` of the book's active versions, behind `Caps.Relocate` |
| `deluge.path-update` (triggered by `book.relocated`), `deluge.centralize`, `deluge.link-backfill`, `deluge.protected-paths-sync`, `maintenance.bulk-deluge-import` | books tasks `books.download.*` calling `Pool`; `protected-paths-sync` gets a real schedule (fixes raw/06 note (d)) |
| `ProtectedPathCache`, discovery, import, mark-in-place (`internal/deluge/*.go` minus client) | stay in ABO (audiobook behaviour) using `Pool.Items` (discovery: `ItemFilter{Categories: [<discovery label>]}`) and `Pool.Files` across **all** download instances; no `Watch`, monitor or grab is needed (phase 1 only) |
| `/api/v1/deluge/*` routes (`internal/server/deluge_integration.go:139`) | books behaviour routes stay on the books gin mount (cross-cutting 2; no reason of their own to rewrite them); generic client routes (test connection, status) become core instance routes in R2 and the old paths are deleted, with the SPA switched in the same release (09; SIMP2-17) |
| `internal/plugin` `DownloadClient`, `TorrentInfo` | deleted in R2's integrations flip with `internal/plugin` (N4; 04 §8.3) |

## 11. Open items

1. Client state strings for qBittorrent (v4 vs v5 `paused*`/`stopped*` naming), SABnzbd and NZBGet are to be
   confirmed against recorded responses when each adapter is built; the table in §4.2 is the intent.
2. Torrent v2 / hybrid hashes: `ItemID` is whatever the client reports; matching against an indexer's v1
   info-hash needs both forms where the client exposes them.
3. Seedbox setups where the client's files are reachable only over a remote protocol are not covered; 05-library's
   `RootFS` leaves room for a remote driver.

## Objections to 03

None. Note: 03 §3.3 lists adapters as subpackages `download/deluge` etc.; this file keeps that layout
(`integrations/download/deluge`), with adapter IDs `core.<name>`.

## Out-of-scope defects found

- `deluge.Client.RemoveTorrent` and `ListTorrentDetails` have no non-test callers at HEAD
  (`internal/deluge/client.go:429,483`): dead code until the core adapter uses them (`ListTorrentDetails` behind
  phase-1 `Items`; `RemoveTorrent` behind the phase-2 `Remover`).

## Review history

Round 1 (`docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- Cross-cutting resolution 1 / SIMP-12: Pool feature phase table; phase 1 = contract + Deluge adapter +
  `Instances/Lease/Items/Files/Relocate` + path mapping; grab, monitor, recovery, `Watch`, `MarkImported`,
  `Refresh`, seed goals and poll keys marked phase 2, deferred-build (not in the ABO extraction plan).
- COR-3: single owner stated; `GrabRequest.Owner` + `Refs`; `RecordFunc` puts the owner's row in the grab
  record's batch; `grab.added/failed` topics (superseded in round 3 by `core.download.event.v1`, COR3-4); §4.5
  routes by owner; MarkImported after the owner's commit.
- COR-17: `events.EntityRef` replaced by `entity.Ref` / `[]entity.Ref` everywhere.
- COR-1: modules reach the Pool as `module.Deps.Downloads`.
- SIMP-4: instance records live in the 05-metadata M1 store; config rows removed; secret names
  `integrations/download/<id>/<field>` (superseded by the dotted form `integrations.download.<id>.<field>` in the
  integration pass); env-pinned Deluge settings via `Origin=pinned` (superseded in round 2 by the in-memory
  overlay, SIMP2-5).
- SIMP-5: download topics declared once in the events catalog; `state_changed` renamed `changed` (03 §3.6 grammar).
- SIMP-6: poll scheduled by `tasks.ScheduleFromConfig` (key superseded in round 2 by `core.download.schedules.poll`,
  R2-7); `refresh_debounce` timer key removed.
- SIMP-17: `tasks.Fixed(n, reason)`.
- SEC-15: adapters declare `EgressConfigured` (LAN clients need `Local`); `Source.URL` fetched `EgressPublicOnly`.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-E request (SIMP-4) — secret names use 05-config's dotted `integrations.download.<id>.<field>` grammar; `download/deluge` cites 05-metadata M1.10's import table; pinned rows follow the one permanent-env rule.

Round 2 (`docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-7 / COR2-6 / SIMP2-1 — `core.download.poll_interval` (duration) replaced by the declared schedule key
  `core.download.schedules.poll` (`tasks.ScheduleConfig`, phase 2) in §1, §4.5 and §7.
- COR2-10 — §4.5 routing by owner via `outbox.Where`: one acquire consumer (`core.acquire.download-events`,
  `ConsumeTx`) that stages the import run in the transition batch; module and `WatchedBy` filters; test wording.
  (Its five-topic input superseded in round 3 by the one topic `core.download.event.v1`, COR3-4.)
- COR2-16 — the grab mutex is `core/internal/keyedmutex` (04 §9 core-internal copy), not ABO's package; acquire's
  cross-key lock is the executor ref-lock table.
- COR2-17 / SIMP2-10 — the six topics live in the events catalog's deferred-build table; only the bus topic
  `item.changed.v1` carries Realtime metadata. (Six topics superseded in round 3 by two, COR3-4.)
- SIMP2-7 — the `TorrentHash`/`DelugeHash` → `ItemRef` books migration moved to phase 2 (§1, §3, §10); phase 1
  builds `ItemRef` at the call site.
- SIMP2-17 — "remove" dropped from ABO's Deluge needs; books Deluge behaviour routes stay on the legacy mount.
- SIMP2-5 — Deluge env values are an in-memory overlay, never persisted (§10).
- DATA2-10 — `download/deluge` edits mirror back to the legacy keys until 09 C7; previous-build edits re-import
  URL and password together (§10). (Superseded in round 3 by R3-1: the instance is read and written in place, no
  import and no mirror.)
- SEC2-7 — `Local` is set only by an interactive `core:admin` holder; `lan_allow` named.
- SEC2-8 — §7 gains a Protection column.
- SIMP2-16 — the slash-form secret-name history line marked superseded by the dotted form.

Round 3 (`docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 — §10 Deluge row: `download/deluge` is legacy-backed, read and written in place in the config blob through
  05-config's adapter until cutover; password moved to `secrets` on this build's first write and blanked in the
  blob (rollback = re-enter it); import, re-import and C7 mirror text removed; test `TestDelugeInstance_LegacyBackedReadInPlace`;
  §1 phase-2 `ItemRef` books migration is a one-way level-guarded format change, not a mirror.
- DATA3-3 / OPS3-8 — resolved by R3-1 here: nothing is deleted or stops being mirrored, because nothing is mirrored.
- SEC3-5 — `Local` for `download/deluge` granted once, IP-literal private hosts only, bound to the host (§10).
- SIMP3-4 — phase-1 `Client` is `Caps`/`Status`/`Items`; `Add`/`Remove` become the phase-2 optional `Adder`/`Remover`;
  grab types, phase-2 optional interfaces and `Caps` fields moved to phase 2 (§1 table, §3, §4.1 row, §4.3, §5, §9
  conformance scope).
- COR3-4 — the download lifecycle is one durable topic `core.download.event.v1` (`Event`, `EventKind`) keyed by
  `GrabID` (foreign items: `ItemRef`); `grab_added` provably precedes a grab's item events (Serialized grab
  records, CAS, poll after `added`); acquire acks refused transitions; §3 topic table, §4.3, §4.5, two tests.
- SIMP3-11 — `Selection.Tags` and `GrabRequest.Tags` confirmed phase-2 only (with the grab types).
- SIMP3-12 — COR2-10, COR2-17/SIMP2-10 and DATA2-10 history lines marked superseded.

Integration pass (round 3, 2026-11-04):

- integration pass (round 3): `internal/plugin` `DownloadClient` deleted at 09 S7.

Round 4 (`docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- R4-1 — §4.1, §9 and the §10 Deluge row: `download/deluge` is served by the app's legacy instance store (books
  code), not a core adapter under a grant; `Local` in books' extension family, suspended after an older build's change.
- R4-2 — §10 Deluge row: the blob password and every plaintext copy get the never-matching sentinel, not a blank.
- COR4-14 — §4.3: intra-batch order rests on `Stage`'s monotonic per-`Outbox` ID generator (05-events §5E.4);
  defence in depth in acquire (10).
- COR4-15 — the durable topic is `core.download.item.transitioned.v1` (03 topic grammar).
- SIMP4-1 / COR4-19 — `Deferred-build members` heading in §1 listing the phase-2 members package-qualified.

Round 5 (`docs/plans/platform-core/reviews/spec-round-5-dispositions.md`):

- SIMP5-6 — §1 `Deferred-build members` table: every `Caps` field package-qualified; topic, task kind and keys moved
  to an unchecked column.
- SIMP5-1 — §4.1: instances routed per record (legacy store covers only `download/deluge`; the rest native).
- DATA5-9 — §10 Deluge row: the upgrade grants no `Local`; a pending candidate an admin accepts.
- R5-1(b) — §10 Deluge row: the plaintext Deluge password still moves; the HEAD-encrypted secrets stay in place.
- OPS5-12 — §4.1: a natively stored download client does nothing after a rollback.

Round 6 (`docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-1 — §4.3: every write to `c/download/grabs/` (new record, step, `MarkImported`/`Remove` outcome, pruning)
  stages `GetForUpdate`; the sub-prefix is `CheckedWrites`; a failed write is dropped and re-read;
  `TestGrab_EveryRecordWriteChecked`.
- R6-7 / OPS6-4 — §5 row, §9 `TestPool_IntegrationsDisabledNoClientCall`: with `core.integrations.disabled`
  (05-metadata M1.4) no client call is made and snapshots stay untouched.
- SEC6-5 — §10 Deluge row: env precedence per name under the one rule; private-network trust only for the
  effective value.
- SIMP6-8 — §1 deferred table: `module.Registrar.DownloadWatch` removed from the first column; 06 §6.4.9 lists it.

Round 7 (`docs/plans/platform-core/reviews/spec-round-7-dispositions.md`):

- SEC7-3 — §10 Deluge row: an env-given Deluge password is withheld while the effective web URL is not operator-sourced or has a different origin (05-metadata M1.4 step 2).
- R7-6 / SEC7-6 / OPS7-3 — §5 `core.integrations.disabled` row: a schedule against a disabled integration fails closed but is enqueued and advances its clock; staging clears every legacy and native integration URL at every stage.

Round 8 (`docs/plans/platform-core/reviews/spec-round-8-dispositions.md`):

- R8-5 / SEC8-2 — §10 Deluge row: an env Deluge password is served while the effective web URL's origin equals the env URL's, the default, or the instance's `OperatorSecretOrigin` (recorded at the first S7 boot from the admin-set URL; 05-metadata M1.4 step 2).
- R8-5 / SEC8-3 — §10 Deluge row: until the integrations cutover a non-ProtectAdmin `Put` that moves the web URL while the password is operator-sourced gets 403 `ErrOperatorSecretBound`.
- R8-3 / SIMP8-2 — §5 `core.integrations.disabled` row: staging clears legacy URLs only; native client rows are covered by the switch.

Advisor (`docs/plans/platform-core/reviews/spec-advisor-dispositions.md`):

- A-2 / ADV-S1 — §4.1, §9, §10 Deluge row: password stays plaintext in the blob, read and written in place; no sentinel, re-import, extension record, pending `Local` candidate, `Registry.Confirm`, `OperatorSecretOrigin` or `ErrOperatorSecretBound`; the web URL keeps HEAD's reach and HEAD's (ProtectAdmin) writers; env password served as at HEAD.
- A-4 / ADV-S3 — §1 "Deferred-build members": `archtest.DeferredMembersAbsent` and the S7 "absent" done-check reference deleted.
- A-5 / ADV-S4 — §8: names under `app.Options.MetricNamespace`.
- A-12 / ADV-S6 — §5 `core.integrations.disabled` row: `staging-scrub` named.
- A-15 / ADV-S9 — §1, §1 "Deferred-build members", §3 phase-2 block, registrar/topic paragraph, §4.3, §4.5, §8, §9: phase 2 labelled design sketch (non-normative), not frozen by the SDK; §4.3's commit-check paragraph and the phase-2 `ErrBlindWrite` test clause trimmed.
- integration pass (advisor): `acquire` cited as 10's design sketch (non-normative).
- Round 10:
  - R10-10 / SEC10-2 / SEC10-3 — instance paragraph and §10 Deluge row: a `Test` that changes the web URL needs ProtectAdmin as `Put` does; with auth off, the legacy-backed Deluge instance stays editable as at HEAD (05-metadata M1.3).

- D10–D12 revision (2026-11-04): §4.1 and §10: ABO's Deluge is no longer a legacy-backed instance served from
  the blob; books' R2 cutover step migrates it to a native `download/deluge` row (password moved to `c/secrets/`
  by the secrets step, `Local` and `OperatorSecretOrigin` bound to the stored URL, 05-metadata M1.10), and the
  native rules apply from then on; `BelowStored`, HEAD-reach, auth-off and rollback text removed. The generic
  Deluge routes are deleted in R2, not aliased. `staging-scrub` row text removed; the legacy-store test replaced by
  `TestDelugeMigration_InstanceAndPassword`. Stage references re-keyed to R2. Consistency pass: the Deluge daemon password (`download_client.torrent.deluge.password`) is adopted as its own secret field `daemon_password` at `integrations.download.deluge.daemon_password` (§4.1, §9, §10), the name 05-config's rename table coined.

