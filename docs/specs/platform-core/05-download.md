<!-- file: docs/specs/platform-core/05-download.md -->
<!-- version: 1.2.0 -->
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
completion events. `acquire` ([10](10-tv-movies-proof.md) §10.5, deferred-build) owns grab *decisions*,
tracked-download records and the import hand-off; it calls `Pool.Grab`, consumes this package's events,
and runs no poller and no recovery of its own.

**Build scope (SIMP-12, cross-cutting resolution 1).** Phase 1 is what ABO uses today and is the only part
the ABO extraction plan ([09](09-abo-on-core.md) S7) builds:

| Pool feature | Phase | Consumer |
|---|---|---|
| `Client` interface (all methods, incl. `Add` and `Remove`, so adapter conformance covers them), `Adapter`, instances (05-metadata M1), path mapping (§4.4) | **1** | ABO (Deluge) |
| `Pool.Instances`, `Lease`, `Items`, `Files`, `Relocate` | **1** | ABO: discovery by label, details/files, move-after-organize (§10) |
| `Pool.Grab`, grab records and recovery (§4.3), monitor/poll task and outbox topics (§4.5), `Watch`, `Tracked`, `Refresh`, `MarkImported`, `Pool.Remove`, seed goals, the `core.download.*` poll keys | **2 — deferred-build: not in the ABO extraction plan** | `acquire` (TV/Movies) |

Phase-2 contracts below are kept at proof depth so `acquire` can be checked against them; nothing in
phase 2 is built until the first TV/Movies app starts.

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

// ItemRef is the stable, cross-instance identity of a download. Apps store
// this, never a bare hash (fixes BookVersion.TorrentHash ambiguity).
type ItemRef struct {
	Instance integrations.Ref
	ID       ItemID
}

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

type AddResult struct {
	Ref       ItemRef
	GrabID    GrabID
	Duplicate bool     // the client already had it (same hash / same key)
	Warnings  []string // e.g. "priority not supported; added at normal"
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

type RemoveOptions struct {
	DeleteData bool
}

type ClientStatus struct {
	Version         string
	DefaultSavePath string            // remote
	Categories      map[string]string // category → remote save path
	FreeSpace       int64             // -1 unknown
}

// Caps is static per adapter plus what Status discovered (version-gated features).
type Caps struct {
	Protocol         Protocol
	Add              bool
	ClientFetchesURL bool
	Files            bool
	Relocate         bool
	Categories       bool
	CreateCategory   bool
	Tags             bool
	Pause            bool
	SeedGoals        SeedGoalCaps // which of Ratio/Time/IdleTime the client enforces (raw/16 §4.6 compatibility matrix)
	RemoveWithData   bool
	PostImportCategory bool
	Priority         bool
}

type SeedGoalCaps struct{ Ratio, Time, IdleTime bool }

// Client is what every download adapter instance implements.
type Client interface {
	integrations.Instance
	Caps(ctx context.Context) (Caps, error) // cached by Pool for core.download.caps_ttl
	Status(ctx context.Context) (ClientStatus, error)
	Add(ctx context.Context, req ResolvedGrab) (ItemID, bool, error) // bool: duplicate
	Items(ctx context.Context, f ItemFilter) ([]Item, error)        // RemotePath only; Pool fills LocalPath
	Remove(ctx context.Context, id ItemID, o RemoveOptions) error   // ClassNotFound if already gone
}

// ResolvedGrab is GrabRequest after core processing: SavePathHint mapped to
// the client path, URL fetched (unless ClientFetchesURL), core tag added.
type ResolvedGrab struct {
	GrabRequest
	RemoteSavePath string
	GrabID         GrabID
}

// Optional capabilities (each also flagged in Caps; Pool returns
// ClassUnsupported when absent).
type FileLister interface {
	Files(ctx context.Context, id ItemID) ([]File, error)
}
type Relocator interface {
	SetLocation(ctx context.Context, ids []ItemID, remoteDir string) error
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
// persists the grab record (with rec's rows), resolves the request and calls Add.
func (p *Pool) Grab(ctx context.Context, req GrabRequest, sel Selection, rec RecordFunc) (AddResult, error)

func (p *Pool) Remove(ctx context.Context, item ItemRef, o RemoveOptions) error

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

type GrabID string // ULID

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

// Outbox payload for core.download.item.{completed,failed,removed}.v1.
type ItemEvent struct {
	Item      Item
	Previous  State
	GrabID    GrabID
	Owner     string            // "" for foreign items matched by a Watch
	Refs      []entity.Ref      // nil for foreign items
	WatchedBy []string          // modules whose Watch matched
	Files     []File            // completed only, when Caps.Files; mapped
	At        time.Time
}

// Outbox payload for core.download.grab.{added,failed}.v1.
type GrabEvent struct {
	GrabID         GrabID
	IdempotencyKey string
	Owner          string
	Refs           []entity.Ref
	Ref            ItemRef // zero on failed
	Recovered      bool    // added by recovery after an ambiguous Add (§4.3)
	Class          integrations.Class // failed only
	At             time.Time
}
```

Registrar slice (06 owns the type; phase 2): `func (r *Registrar) DownloadWatch(w ...download.Watch)`.
Owners consume the topics below as ordinary outbox consumers ([05-events](05-events.md)); every topic is
declared once in the events catalog (SIMP-5).

| Topic (phase 2) | Kind | When |
|---|---|---|
| `core.download.grab.added.v1` | outbox | a grab record reaches `added`, synchronously or by recovery |
| `core.download.grab.failed.v1` | outbox | a grab record reaches `failed` (invalid, every instance failed, or unrecovered after `core.download.pending_recovery`) |
| `core.download.item.completed.v1` | outbox | client state reaches `Completed` for a tracked item |
| `core.download.item.failed.v1` | outbox | reaches `Failed` (or `Warning` for longer than `core.download.warning_to_failed`) |
| `core.download.item.removed.v1` | outbox | a tracked, not-yet-imported item disappears from the client |
| `core.download.item.changed.v1` | events bus, notify-only | any transition; for UI/realtime |

## 4. Lifecycle

### 4.1 Instances and adapters

Instances are `download/<id>` records in the one instance store (05-metadata §M1, SIMP-4); secrets are
`integrations.download.<id>.<field>` (05-config §2.3 grammar). Secret fields per adapter: Deluge `password`;
qBittorrent `username`, `password`; Transmission `username`, `password`; SABnzbd `api_key`; NZBGet
`username`, `password`. Every adapter declares `Meta.Egress = EgressConfigured`: a client on the LAN (the
usual case) needs `Local=true`, which only an interactive admin can set, and a URL change clears the stored
password (05-metadata M1.3, SEC-15). Adapters log in lazily on first call and re-login once on an auth error with the
generation guard of `internal/deluge/client.go:198-221` (concurrent callers log in once).

| Adapter | Wire (raw/16 §4.6) | Health probe | Phase |
|---|---|---|---|
| `core.deluge` | Web JSON-RPC: `auth.login`, `web.connected`, `web.get_hosts`/`web.connect`, `core.add_torrent_magnet`/`_file`/`_url`, `core.get_torrents_status`, `core.move_storage`, `core.remove_torrent`, `core.set_torrent_options`, `label.*` | `web.connected` (and connect to the first host when false) | **1** — extracted from ABO with ABO's needs first (list, by label, details/files, move, remove), Add/Status added |
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

`Pool.Grab`: validate (exactly one `Source`; `IdempotencyKey` set; `Owner` is `"core.acquire"` or a
registered module ID; `Refs` non-empty) → look up `c/download/grabs/by-key/<key>`; if present return it
(`Duplicate`) → choose instance → write grab record `pending{Owner, Refs, key}` **plus the owner's
`RecordFunc` rows** in one batch → resolve (map `SavePathHint`; fetch a `Source.URL` via `env.HTTP` with
`EgressPublicOnly` unless the client fetches; parse torrent info-hash locally when bytes are present)
→ `Add` → write grab record `added{Ref}`, a tracked snapshot and the outbox message
`core.download.grab.added.v1` in one batch. On a timeout or ambiguous error after Add was sent, the record
stays `pending` and the next monitor pass **recovers** it by core tag `mc:<grabID>` (when `Caps.Tags`), else
by info-hash, else by job name, and stages `grab.added.v1{Recovered: true}`; unrecovered after
`core.download.pending_recovery` → `failed` with `core.download.grab.failed.v1`. No blind re-add. This is the
**only** grab-intent record and the only recovery path in core: `acquire` keeps none (COR-3).

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

Task `core.download.poll` (05-tasks) is the only client poller in core. It is scheduled by
`tasks.ScheduleFromConfig("core.download.poll_interval")` (SIMP-6) and started on demand by `Pool.Refresh`
(`acquire` calls it after a grab, the facade's `RefreshMonitoredDownloads` command calls it). `Refresh` starts
a run only when none of the kind is queued, and the kind's `ConcurrencyKey` keeps runs from overlapping, so
on-demand bursts collapse without a debounce timer. Per enabled instance, with parallelism
`tasks.Fixed(min(instances, 4), "one poll per download client")`:

1. `Items(filter)` where filter = grabbed IDs ∪ all `Watch` categories/tags (never "all items" unless a
   Watch asks for it — large seedboxes have tens of thousands).
2. Diff against snapshots in `c/download/items/<instance>/<id>`.
3. For each transition: one storage **batch** containing the new snapshot and, for completed/failed/removed,
   the outbox message (same-batch outbox, 03 §3.3 `outbox`). The bus event is published after commit.
4. An instance whose poll fails keeps its snapshots untouched (no "removed" storm on an outage); removal is
   declared only after `core.download.removed_after` consecutive successful polls without the item.

**Routing by owner.** Every outbox message carries `Owner` and `Refs` from the grab record. Items whose
`Owner` is a module go to that module's consumer; items owned by `"core.acquire"` go to `acquire`, which
applies the tracked-download transition in the relay batch (`ConsumeTx`) and starts its import task
(`ConsumeIntoTask`, 05-events); foreign items go to the modules in `WatchedBy`. A consumer ignores messages
it does not own. The owner imports (its own code: match entities, hardlink/copy/move via `library`, rename)
and calls `Pool.MarkImported` **after** the import commits. Delivery is at-least-once; consumers are
idempotent (keyed by `ItemRef` + `CompletedAt`).

### 4.6 Shutdown

Phase 1: close instances (05-metadata §M1.4). Phase 2: stop the poll task (05-tasks drain) and finish
in-flight batches first.

## 5. Error semantics

| Error path | Fail-open / closed | Why |
|---|---|---|
| Client unreachable / 5xx during poll | open for the App, closed for that instance's transitions (snapshots frozen) | An outage must not look like every item being removed. |
| Auth rejected (Deluge RPC code 1 after one re-login; qBittorrent 403 after re-login; SAB `API Key Incorrect`) | closed: `ClassAuth`, `HealthAuthFailed`, hold 6h | Needs a human; retrying looks like an attack (`internal/metadata/throttle.go:47-49` rationale). |
| Item not found on `Remove`/`Relocate` | `ClassNotFound` → treated as success for Remove, error for Relocate | Already gone is the goal of Remove (Deluge `ErrTorrentNotFound`, `client.go:97-101`). |
| `Add` rejected as invalid (bad torrent/nzb) | closed: `ClassInvalid`, grab `failed` (+ `grab.failed.v1`), no fallback to another instance | Another client would reject the same file. |
| `Owner` neither `core.acquire` nor a registered module, or empty `Refs` | closed: `ClassInvalid`, nothing written | Every completion must have exactly one consumer. |
| `RecordFunc` returns an error | closed: nothing written, nothing sent to a client | The owner's row and the grab record are one fact. |
| `Add` transport failure before send | fall back to next instance (Selection.Fallback) | Capacity is per instance. |
| `Add` ambiguous (timeout after send) | closed for re-add; recovery by tag/hash/name (§4.3) | Double grabs waste bandwidth and confuse imports. |
| Unmapped path on `SavePathHint`/`Relocate` | closed: `ClassInvalid` | Never send a path the client cannot see. |
| Unmapped `RemotePath` on completion | message still sent with a zero `LocalPath`; app marks ImportBlocked | Visible, actionable, not dropped. |
| Unknown client state value | `StateWarning`, counted | Never guess `Completed`. |
| Optional capability absent | `ClassUnsupported` | Caller decides (e.g. ABO skips move_storage, as today when disabled). |
| Snapshot/outbox batch write fails | closed: transition retried next poll | Same-batch rule: no event without state, no state without event. |
| Relocate after organize fails (ABO "follow the book") | open for the organize (already succeeded), error recorded on the item and surfaced in health | Today it is a log line only (`integration.go:131-135`); the design keeps "best effort" but makes it visible. |

## 6. Concurrency contract

Adapters are goroutine-safe (login guarded; RPC ids atomic as `client.go:48`). Every method takes ctx; there
is no `context.Background()` path in core (HEAD's no-ctx methods, `client.go:193-195`, are not carried).
Phase 2: poll fan-out is bounded (§4.5). Grab is serialized per `IdempotencyKey` (keyed mutex,
`internal/keyedmutex` moves to core per 03 §3.2.1 E2); serializing grabs **across** keys for the same entity
is the owner's job (`acquire`'s per-entity grab lock, 10 §10.5.6). Monitor writes per instance are sequential
(one poller per instance at a time; the kind's `ConcurrencyKey` keeps runs from overlapping). Library-scale app work over download items (ABO link-backfill,
centralize) runs as module tasks through `RunItems` with declared parallelism; the providerhttp bucket of the
instance bounds the request rate regardless of worker count.

## 7. Config keys

Instance records are data in the one instance store (05-metadata M1), not config keys (SIMP-4); the rows
marked "instance setting" are fields of a `download/<id>` record.

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| instance setting `category` | string | `""` | yes |
| instance setting `post_import_category` (phase 2) | string | `""` | yes |
| instance setting `remove_completed` / `remove_failed` (phase 2) | bool | `true` / `true` (raw/16 §4.6 definition defaults) | yes |
| instance setting `limits.*` | providerhttp limits | adapter default: RPS 10, burst 10, timeout 30s (Add 120s), no daily quota | yes |
| `core.library.mappings` (Source `download:<id>`) | list of `library.Mapping` (05-library) | `[]` | yes |
| `core.download.caps_ttl` | duration | `1h` | yes |
| `core.download.poll_interval` (phase 2; bound by `tasks.ScheduleFromConfig`) | duration | `60s` | yes |
| `core.download.removed_after` (phase 2) | int (polls) | `3` | yes |
| `core.download.warning_to_failed` (phase 2) | duration | `24h` (0 = never) | yes |
| `core.download.pending_recovery` (phase 2) | duration | `10m` | yes |
| `core.download.snapshot_retention` (phase 2) | duration | `30d` after imported/removed | yes |

## 8. Metrics

Phase 2 metrics are marked; phase 1 ships only the unmarked ones.

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
`TestPaths_UnmappedRelocateRefused`, `TestPaths_WindowsRemote`, `TestDeluge_PrivateHostNeedsLocal`.
Core, phase 2 (with `acquire`): `TestGrab_IdempotencyKeyDedupes`, `TestGrab_AmbiguousAddRecoveredByTag`,
`TestGrab_FallbackOnTransportOnly`, `TestGrab_InvalidNoFallback`, `TestGrab_OwnerAcquireAccepted`,
`TestGrab_RecordFuncInSameBatch`, `TestGrab_AddedEventStaged`, `TestPool_SelectionPriorityRoundRobin`,
`TestPool_PinnedInstance`, `TestMonitor_TransitionWritesSnapshotAndOutboxInOneBatch`,
`TestMonitor_OutageFreezesSnapshots`, `TestMonitor_RemovedAfterNPolls` (synctest),
`TestMonitor_WatchFiltersForeignItems`, `TestMonitor_RoutesByOwner`, `TestPool_RefreshDoesNotStackRuns`,
`TestMarkImported_RemovesOnlyWhenSeedGoalMet`.
Adapters: `coretest.DownloadConformance(t, factory, fixtures)` — one suite every adapter passes: add (magnet,
file, URL), duplicate add, items/state mapping golden table, files, relocate, category, remove ± data, auth
failure and re-login, unknown state; fixtures recorded from real clients; live mode behind
`MEDIACORE_LIVE_DOWNLOAD=1` against containerized clients. Fake: `coretest.FakeDownloadClient` — in-memory
client with a scriptable clock-driven state machine (`Advance(id, State)`), capability toggles and fault injection.

## 10. Migration from ABO

| ABO | Becomes |
|---|---|
| `internal/deluge/client.go` | `core/integrations/download/deluge` adapter (`core.deluge`); RPC/auth code reused; every method gains ctx; `http.Client` from `env.HTTP` (5-minute timeout becomes per-op limits) |
| `deluge.GetClient()` global, `ResetGlobalClientForTest`, `SetGlobalClientForTest` | deleted; `Pool.Lease` |
| `DelugeWebURL/Password`, `download_client.torrent.deluge` | one `download/deluge` record in the instance store created at first boot, `Local=true` when the host is loopback/private (05-metadata M1.10); password into `secrets` as `integrations.download.deluge.password` (instance ID fixed by 05-metadata M1.10's import table); env-authoritative behaviour preserved as an `Origin=pinned` row while an operator value is set (env names permanent, 05-metadata M1.4 step 2), not by a config key |
| `deluge_move_enabled`, `deluge_discovery_label`, `deluge_discovery_enabled` | `modules.books.download.*` settings (they are ABO behaviour, not client config) |
| `BookVersion.TorrentHash`, `BookFile.DelugeHash` | books stores `download.ItemRef`; legacy hashes resolved to the migrated Deluge instance (`Instance=download/deluge`) by a books migration |
| `NotifyDelugeAfterOrganize/AfterVersionSwap/MoveStorage(ToDir)` | books code calling `Pool.Relocate` (reached as `module.Deps.Downloads`, COR-1) for every `ItemRef` of the book's active versions, behind `Caps.Relocate` |
| `deluge.path-update` (triggered by `book.relocated`), `deluge.centralize`, `deluge.link-backfill`, `deluge.protected-paths-sync`, `maintenance.bulk-deluge-import` | books tasks `books.download.*` calling `Pool`; `protected-paths-sync` gets a real schedule (fixes raw/06 note (d)) |
| `ProtectedPathCache`, discovery, import, mark-in-place (`internal/deluge/*.go` minus client) | stay in ABO (audiobook behaviour) using `Pool.Items` (discovery: `ItemFilter{Categories: [<discovery label>]}`) and `Pool.Files` across **all** download instances; no `Watch`, monitor or grab is needed (phase 1 only) |
| `/api/v1/deluge/*` routes (`internal/server/deluge_integration.go:139`) | `/api/books/v1/downloads/*` for ABO behaviour + core instance routes; legacy paths aliased (09) |
| `internal/plugin` `DownloadClient`, `TorrentInfo` | deleted (N4) |

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
  (`internal/deluge/client.go:429,483`): dead code until the core adapter uses them.

## Review history

Round 1 (`docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- Cross-cutting resolution 1 / SIMP-12: Pool feature phase table; phase 1 = contract + Deluge adapter +
  `Instances/Lease/Items/Files/Relocate` + path mapping; grab, monitor, recovery, `Watch`, `MarkImported`,
  `Refresh`, seed goals and poll keys marked phase 2, deferred-build (not in the ABO extraction plan).
- COR-3: single owner stated; `GrabRequest.Owner` + `Refs`; `RecordFunc` puts the owner's row in the grab
  record's batch; `grab.added/failed` topics; §4.5 routes by owner; MarkImported after the owner's commit.
- COR-17: `events.EntityRef` replaced by `entity.Ref` / `[]entity.Ref` everywhere.
- COR-1: modules reach the Pool as `module.Deps.Downloads`.
- SIMP-4: instance records live in the 05-metadata M1 store; config rows removed; secret names
  `integrations/download/<id>/<field>`; env-pinned Deluge settings via `Origin=pinned`.
- SIMP-5: download topics declared once in the events catalog; `state_changed` renamed `changed` (03 §3.6 grammar).
- SIMP-6: poll scheduled by `tasks.ScheduleFromConfig`; `refresh_debounce` timer key removed.
- SIMP-17: `tasks.Fixed(n, reason)`.
- SEC-15: adapters declare `EgressConfigured` (LAN clients need `Local`); `Source.URL` fetched `EgressPublicOnly`.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-E request (SIMP-4) — secret names use 05-config's dotted `integrations.download.<id>.<field>` grammar; `download/deluge` cites 05-metadata M1.10's import table; pinned rows follow the one permanent-env rule.
