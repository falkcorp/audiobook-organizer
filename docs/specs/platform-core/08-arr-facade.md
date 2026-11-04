<!-- file: docs/specs/platform-core/08-arr-facade.md -->
<!-- version: 1.4.0 -->
<!-- guid: 6d9b089a-fb08-4087-9ccd-43b9aa4ec555 -->
<!-- last-edited: 2026-11-04 -->

# 08 — *arr-v3 facade: `core/arrfacade`

Evidence commit: `748fd787a`. Raw data: [`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md)
§5 (conventions and endpoint table, public sources fetched 2026-11-03),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §2.3, §3.1, §8,
[`raw/10-auth-rbac.md`](../../plans/platform-core/raw/10-auth-rbac.md) §2, §8.
Decision D2: core provides an **extension point and a contract** for an *arr-v3-compatible API;
apps implement it per resource; there is **no** commitment to Sonarr/Radarr parity, to their
database, or to endpoints no named client uses (N3).

**Build status: deferred-build** (03 §3.3, cross-cutting resolution 1, SIMP-1). This file is the
contract at proof depth (D5). No S0–S10 stage (09 §9.3) builds `arrfacade`; it is built with the first TV/Movies
app. During the extraction only one thing exists: core's reservation of `/api/v3/` (05-http-api
§4.1). ABO's conformance engine moves to core (`httpapi/conformance`) **when `arrfacade` is built**;
until then it stays `internal/syncapi/conformance` in ABO, as 04 records, because during S0–S10 it has
one consumer (SIMP2-9). The `module` hooks the facade needs (`Registrar.ArrFacade`, the facade problem
kinds) are deferred-build members of 06 §6.4.9.

**Deferred-build members of built packages (SIMP2-6).** Deferral applies to features inside built
packages as well as to packages (03 §3.4). Everything below exists only for this facade, has no ABO
consumer, and is therefore **not** built in S2/S4; each owning file keeps its contract text under a
heading named exactly `Deferred-build members` (03 §3.4), and it lands additively in the build that
creates `arrfacade`. The table below is a reader's index of those entries, not a source of the check:

| Member | Owning file | Notes |
|---|---|---|
| `KeySpec.Purpose`, `KeySpec.Module`, `PurposeFacade`, and the stored key record's purpose field (defaults to general) | 05-authn §3.2, §4.4 | additive; until then no facade key can be minted, so SEC-5's guarantee holds trivially |
| query-string key extraction (`apikey`, `X-Api-Key`), enabled only on facade groups | 05-authn §4.2 | HEAD reads neither (`internal/server/middleware/auth.go:83-98`) |
| `Router.FacadeGroup`, `RouteKind Facade`, the stage-13 facade refusal clause | 05-http-api §3, §4.2, §4.9 | the `/api/v3/` reservation itself is built |
| `TestAPIKey_FacadePurposeBoundToModule`, `TestConfining_FacadeKeyRefusedOnNative` | 05-authn, 05-rbac | `Confining` itself is built (the fingerprint worker needs it, SEC-3) |

Absence is checked by one test, not a prose grep: the deferred-members archtest
`archtest.DeferredMembersAbsent` runs in the root module's `make ci` from S2 (06 §6.4.9, 09 S2) and fails
when any listed identifier appears in non-test code under `core/`, because `test ! -e core/arrfacade`
cannot see a feature inside a built package. Its list is generated from every `Deferred-build members`
heading under `docs/specs/platform-core/` (03 §3.4), with package-qualified entries
(`module.Registrar.ArrFacade`, `auth.PurposeFacade`, `httpapi.Router.FacadeGroup`,
`httpapi.RouteKindFacade`, `module.ProblemFacadeIncomplete`, `module.ProblemFacadeURLBaseCollision`, …).
The two facade problem kinds are appended to the end of 06's `ProblemKind` block when this package
lands, so adding them renumbers nothing (SIMP3-2, SIMP3-11). The PR that adds `arrfacade` deletes the
facade members' entries only; other deferred members (the download grab pipeline, `acquire`, the AI
usage ledger, notify's inbox) keep their guard, and the test is deleted when the list is empty
(SIMP4-1).

## 8.1 Purpose & scope

Let a TV or Movies app be driven by the existing ecosystem clients that speak the Sonarr/Radarr v3
API:

| Client | What it needs the facade for |
|---|---|
| Overseerr / Jellyseerr / Seerr ("Seerr") | add, update, delete, search series/movies on user request; profile/folder/tag pickers; download progress |
| Prowlarr | push, update, test and delete Newznab/Torznab indexers in the app |
| Bazarr | read series/episodes/movies and their files to search subtitles |

In scope: the `arrfacade.Provider` contract, core scaffolding (key auth, permissions, ID mapping,
v3 wire conventions, paging, errors, mount point, metrics, conformance harness), the endpoint table
with per-client requirements, and the SignalR decision. Out of scope: any endpoint not in §8.7, UI
for the facade beyond key management (07), Sonarr/Radarr data import.

## 8.2 Current state at HEAD

| Fact | Anchor |
|---|---|
| No *arr-compatible endpoint, no indexer abstraction, no Prowlarr/Torznab code exists | raw/11 §1, §4 (`internal/config/removed_keys.go:30-35` removed qBittorrent/SABnzbd settings) |
| `/api/v3` is already used by ABO: `/api/v3/ops/*`, 12 routes (operations v3), same middleware chain as `/api/v1` | `internal/server/server_lifecycle.go:2125-2135`; raw/09 §2.3, §3.1 rows `/api/v3/ops/*` |
| The `/api/*` → `/api/v1/*` 301 redirect excludes `/api/v3/` because of it | `internal/server/server_lifecycle.go:1801-1803` |
| No non-test web code calls `/api/v3` (grep of `web/src`, `cmd`, `scripts` at HEAD returns nothing) | grep |
| Precedent for an external-protocol compatibility surface: the Audiobookshelf API, feature-flagged, its own auth stack resolving to core users | `internal/server/wire_abs_routes.go:628-653`; raw/10 G15 (`internal/server/middleware/absauth.go:783`) |
| Reusable conformance engine: JSON-shape diff of our responses against captured fixtures (missing/extra field, type and length mismatch) | `internal/syncapi/conformance/diff.go` (`FindingKind` consts, `Compare`) |
| API keys: `abk_` + base64url(32 bytes), SHA-256 stored, mandatory expiry (default 30 d, max 365 d), scopes intersect role permissions | raw/10 §2 (`internal/database/apikey_token.go:16-35`, `internal/server/handlers/apikeys.go:27-33`) |
| Access-log/trace redaction masks `?token=` and `api_key=` but not `apikey` | raw/10 §7 (`internal/server/server.go:695-704`) |

## 8.3 Design decisions

| # | Decision | Why |
|---|---|---|
| F1 | The facade is a **per-module** HTTP surface served by core, backed by an app-implemented `Provider` whose resources are **optional interface fields**; a nil field means the endpoints are not served. Required fields are derived from the clients the provider declares, and checked at boot. | D2 "extension point, not parity": the contract grows only with real client needs; incomplete client support fails at boot, not in front of a user. |
| F2 | Core owns the **wire DTOs** (camelCase, the foreign API's convention), written from the conformance fixtures when the facade is built (§8.6.2). Apps map domain → DTO. | One place to fix a field name for every app; conformance tests run against core types. |
| F3 | Core provides **default providers** for resources that are core data: system status, root folders (from `library`), indexers (instances in the one integration-instance store, written through `integrations.FacadeWriter`), commands (mapped to `tasks`). | Prowlarr push and most pickers then need zero app code. |
| F4 | Integer IDs: core provides a persistent **ID map** from `entity.Ref{Module, Kind, ID}` (03 §3.3) to stable `int32` (keyspace `c/arrfacade/<module>/ids/`). | v3 clients require numeric ids and store them; app IDs are opaque strings. |
| F5 | Auth is **API key only**, through the core API key store, with `PurposeFacade` keys bound to one module (05-authn §4.4, deferred-build member, §8.0) whose scopes are that module's `Confining` facade scopes (05-rbac). | Clients store one key forever; it must not be a general admin key. |
| F6 | Mount at `<url_base>/api/v3` per module. Core **reserves** `/api/v3/` for facades; ABO's `/api/v3/ops` becomes a legacy alias of the core tasks API for the compatibility window and cannot coexist with a facade on the same base. | §8.4 |
| F7 | **No SignalR.** `/signalr/*` is not served. | §8.8.4 |

## 8.4 Mount point and the `/api/v3/ops` collision

* Each module that registers a provider gets one facade at `<url_base>/api/v3/`, where `url_base` is
  `modules.<module>.arrfacade.url_base` (default `""`, the key 05-http-api §4.9 uses). Sonarr and Radarr have the same "URL Base"
  setting and the named clients accept a base URL with a path, so a non-empty base is a normal
  client configuration.
* The kernel (06 P7, after config has loaded) fails boot with `ProblemFacadeURLBaseCollision` when two enabled facades
  resolve to the same base, or when a facade prefix overlaps any module's declared legacy prefix or a
  core route. This is the multi-app rule too (11): `/tv/api/v3` and `/movies/api/v3`.
* **ABO**: ABO registers no provider (Readarr, the only book-shaped *arr, is retired — raw/16 §6). Its
  `/api/v3/ops/*` routes are re-pointed at the core tasks API (`/api/core/v1/tasks/...`, 05-tasks) and
  kept as a declared legacy prefix of the `books` module for the two-minor-release window (02 §2.5 Q5,
  09), then answer 410 and are deleted. No in-repo client uses them (§8.2). If ABO ever registered a
  provider with an empty base during the window, boot fails — the collision is detected, never
  resolved by path precedence.
* Rejected: letting `/api/v3/ops` and a facade share the prefix because no *arr resource is named
  `ops`. Two APIs with different auth, error shapes and permission models under one prefix is a
  standing trap for routing, logging and rate limiting.
* Middleware: the facade is mounted on `httpapi.Router.FacadeGroup(owner, base)` (05-http-api §4.9, deferred-build member, §8.0) with the **same core chain** as native
  routes (peer allow-list, recovery, security headers, rate limit, body limit, authenticate,
  authorize) but with facade key extraction (§8.5) instead of session cookies, and v3 error encoding.
* Path matching: ASP.NET routing in the originals is case-insensitive and clients rely on it (Seerr
  calls `/qualityProfile`, Bazarr `/episodefile`; raw/16 §5.2). Go's `ServeMux` is case-sensitive, so
  the facade router lower-cases the **resource segments** before matching (numeric ids untouched) and
  ignores a trailing slash. Query parameter names are matched case-insensitively.

## 8.5 Authentication and permissions

* Key sources, in order: header `X-Api-Key`, query `apikey`, header `Authorization: Bearer <key>`
  (raw/16 §5.1: Seerr sends `apikey`, Prowlarr sends `X-Api-Key`, Bazarr sends `?apikey=`). Session
  cookies are ignored on facade paths. Query-string key extraction is enabled only on facade groups
  (05-authn §4.2); like the purpose fields below, it is built with this package (§8.0).
* **Facade keys are purpose-bound (SEC-5).** The key must be a core API key with
  `Purpose = PurposeFacade` and `Module = m` (05-authn §4.4, `KeySpec.Purpose`/`Module`, normative
  there), and every scope it holds must be one of `m:arr.read`, `m:arr.request`, `m:arr.indexers`. The
  facade refuses any other key, including a full-scope admin key and a general key that holds
  `m:arr.*` scopes plus anything else (the scope rule is a second check).
* **Confinement is enforced by the core chain, not by this file.** Every `<m>:arr.*` scope is
  declared `Confining` (05-rbac §3.2, §4.10). A confined principal satisfies only requirements that
  name one of its scopes literally, never `rbac.AnyAuthenticated`, so a facade key is refused on every
  core and module route by 05-http-api §4.2 stage 12, and a `PurposeFacade` key is refused off its
  module's facade group by 05-authn. One leaked Seerr key can therefore only do what the facade allows
  for one module. Tests for the refusal live with the chain (`TestConfining_FacadeKeyRefusedOnNative`,
  05-rbac; `TestAPIKey_FacadePurposeBoundToModule`, 05-authn); §8.14 keeps the facade-side cases.
* Permissions are declared by the module in its manifest with `arrfacade.Permissions(id)`, which
  returns `[]rbac.PermissionSpec` (05-rbac §3.2):

| Permission | Grants | `PermissionSpec` fields |
|---|---|---|
| `<module>:arr.read` | every GET in §8.7 | `Confining: true`; no `DefaultGrants` (05-rbac refuses them on a `Confining` scope, `ErrRestrictedGrant`) |
| `<module>:arr.request` | add/update/delete series or movies, tags, episode monitor, commands | `Confining: true`; `Warn: true` (UI-only warning when granting, SEC-23); no `DefaultGrants` |
| `<module>:arr.indexers` | indexer create/update/delete/test | `Confining: true`; no `DefaultGrants` |

  None is `Sensitive`: a facade key is a non-interactive principal, and 05-rbac evaluates `Sensitive`
  before the admin flag, so a `Sensitive` scope is never satisfied by any API key, `core:admin`
  included (SEC-23). Marking a facade scope `Sensitive` would make the facade unusable.
* Key creation presets in the settings UI: **Seerr** = read + request, **Prowlarr** = read + indexers,
  **Bazarr** = read; each creates a `PurposeFacade` key for the chosen module. A key's effective
  permission is `scopes ∩ owner's permissions` as today (raw/10 G6); the creating user should be a
  dedicated service user, which the UI suggests. Because no role receives `<m>:arr.*` by default, the
  service user's role must be given them explicitly (an admin edit or a module `RoleSeed`).
* Expiry: facade keys follow the core maximum (365 d). Because clients fail silently when a key
  expires, core health component `arrfacade.<module>.keys` goes **degraded 30 days before** the
  earliest facade key expires and core notify (05-notify) sends one notification; rotation is a
  one-click "regenerate" that shows the new key once. Allowing non-expiring facade keys is open item O1.
* Audit: every facade write is an audit event with actor = key owner, method `api_key:arrfacade`,
  module, resource, outcome (05-rbac §3.5, `audit.Logger`).
* Redaction: `apikey` and `access_token` are added to core's query redaction list (05-logging), so the
  key never reaches access logs or traces.

## 8.6 Go API

```go
package arrfacade

// Flavor selects the resource vocabulary.
type Flavor int

const (
	Sonarr Flavor = iota + 1 // series/episode/episodeFile/languageProfile
	Radarr                   // movie
)

// Client is an ecosystem client whose requirements the provider claims to meet.
type Client string

const (
	ClientSeerr    Client = "seerr"    // Overseerr, Jellyseerr, Seerr
	ClientProwlarr Client = "prowlarr"
	ClientBazarr   Client = "bazarr"
)

// Provider is what a module registers with module.Registrar.ArrFacade
// (a deferred-build member, 06 §6.4.9).
// Nil resource fields are not served (404 in v3 error shape). The kernel
// checks that every resource required by each declared Client (table 8.7)
// is non-nil, else boot fails with ProblemFacadeIncomplete.
type Provider struct {
	Flavor Flavor
	// CompatVersion is reported as system/status "version" and as the
	// X-Application-Version header. Its major selects v3 vs v4 shapes
	// (language profiles exist only for Sonarr major 3). Must be >= 3.0.5
	// (Prowlarr's minimum, raw/16 §5.2 /indexer/test row).
	CompatVersion string
	Clients       []Client

	System           SystemProvider          // required; DefaultSystem() is the usual value
	QualityProfiles  QualityProfileProvider
	LanguageProfiles LanguageProfileProvider // Sonarr, CompatVersion major 3 only
	RootFolders      RootFolderProvider      // LibraryRootFolders(...) usually
	Tags             TagProvider
	Series           SeriesProvider          // Sonarr
	Episodes         EpisodeProvider         // Sonarr
	EpisodeFiles     EpisodeFileProvider     // Sonarr
	Movies           MovieProvider           // Radarr
	Queue            QueueProvider
	Commands         CommandProvider         // TaskCommands(...) usually
	History          HistoryProvider
	Indexers         IndexerProvider         // CoreIndexers(pool, facadeWriter, ids) usually
}

type SystemProvider interface {
	Status(ctx context.Context) (SystemStatus, error)
}

type QualityProfileProvider interface {
	ListQualityProfiles(ctx context.Context) ([]QualityProfile, error)
}

type LanguageProfileProvider interface {
	ListLanguageProfiles(ctx context.Context) ([]LanguageProfile, error)
}

type RootFolderProvider interface {
	ListRootFolders(ctx context.Context) ([]RootFolder, error)
}

type TagProvider interface {
	ListTags(ctx context.Context) ([]Tag, error)
	CreateTag(ctx context.Context, label string) (Tag, error)
	UpdateTag(ctx context.Context, t Tag) (Tag, error)
}

type SeriesProvider interface {
	ListSeries(ctx context.Context, f SeriesFilter) ([]Series, error) // f.TvdbID optional
	GetSeries(ctx context.Context, id int32) (Series, error)
	LookupSeries(ctx context.Context, term string) ([]Series, error)  // "tvdb:<id>" or free text
	AddSeries(ctx context.Context, req AddSeries) (Series, error)
	UpdateSeries(ctx context.Context, s Series) (Series, error)
	DeleteSeries(ctx context.Context, id int32, opt DeleteOptions) error
}

type EpisodeProvider interface {
	ListEpisodes(ctx context.Context, f EpisodeFilter) ([]Episode, error) // SeriesID required
	GetEpisode(ctx context.Context, id int32) (Episode, error)
	SetEpisodesMonitored(ctx context.Context, ids []int32, monitored bool) error
}

type EpisodeFileProvider interface {
	ListEpisodeFiles(ctx context.Context, seriesID int32) ([]EpisodeFile, error)
	GetEpisodeFile(ctx context.Context, id int32) (EpisodeFile, error)
}

type MovieProvider interface {
	ListMovies(ctx context.Context, f MovieFilter) ([]Movie, error) // f.TmdbID optional
	GetMovie(ctx context.Context, id int32) (Movie, error)
	LookupMovie(ctx context.Context, term string) ([]Movie, error)  // "tmdb:<id>"
	AddMovie(ctx context.Context, req AddMovie) (Movie, error)
	UpdateMovie(ctx context.Context, m Movie) (Movie, error)
	DeleteMovie(ctx context.Context, id int32, opt DeleteOptions) error
}

type QueueProvider interface {
	Queue(ctx context.Context, q QueueQuery) (Page[QueueItem], error)
}

type HistoryProvider interface {
	History(ctx context.Context, q HistoryQuery) (Page[HistoryRecord], error)
}

type IndexerProvider interface {
	ListIndexers(ctx context.Context) ([]Indexer, error)
	GetIndexer(ctx context.Context, id int32) (Indexer, error)
	CreateIndexer(ctx context.Context, ix Indexer, forceSave bool) (Indexer, error)
	UpdateIndexer(ctx context.Context, ix Indexer, forceSave bool) (Indexer, error)
	DeleteIndexer(ctx context.Context, id int32) error
	IndexerSchema(ctx context.Context) ([]Indexer, error) // Newznab and Torznab templates
	TestIndexer(ctx context.Context, ix Indexer) error     // *ValidationError on failure
}

// CommandProvider maps v3 command names to work. POST /command answers with
// a Command resource; GET /command/{id} reports its state.
type CommandProvider interface {
	Start(ctx context.Context, name string, body json.RawMessage) (Command, error)
	Get(ctx context.Context, id int32) (Command, error)
}
```

### 8.6.1 Core-provided scaffolding

```go
// Permissions returns the three facade permission specs for a module's
// manifest (§8.5): all Confining, arr.request with Warn, none Sensitive.
func Permissions(module string) []rbac.PermissionSpec

// DefaultSystem builds system/status from the App identity, CompatVersion,
// url base and start time. Path-revealing fields are returned empty.
func DefaultSystem(info AppInfo) SystemProvider

// LibraryRootFolders exposes the module's library roots (05-library
// Manager.Roots through the module's library.Scope) with free/total space;
// unmappedFolders is always empty.
func LibraryRootFolders(lib library.Scope, filter library.RootFilter) RootFolderProvider

// CoreIndexers stores pushed indexers as core indexer instances (05-indexer,
// Newznab/Torznab adapter) through w, the facade's only write path
// (integrations.FacadeWriter: accepts <m>:arr.indexers, forces
// Origin=arrfacade and the SEC-15 egress rules, 05-metadata M1, SEC-5). Test
// runs w.Test (unsaved; class and status only); Caps reads go through pool;
// forceSave=false refuses to save a failing one.
func CoreIndexers(pool *indexer.Pool, w *integrations.FacadeWriter, ids *IDMap) IndexerProvider

// TaskCommands maps command names to task kinds; the task is started through
// the module's tasks.Client, which stamps the key's principal as the actor
// (06 §6.4.4), so task permissions apply; the run id is mapped to a command
// id. Unknown names answer 400 with a validation failure.
func TaskCommands(c tasks.Client, ids *IDMap, specs map[string]CommandSpec) CommandProvider

type CommandSpec struct {
	Kind   tasks.Kind                              // e.g. "tv.search.series"
	Params func(body json.RawMessage) (any, error) // decode {seriesId} etc. into task params
}

// IDMap assigns stable int32 ids to entity references (03 §3.3 entity.Ref),
// persisted under c/arrfacade/<module>/ids/. Assign is idempotent; ids are
// never reused.
type IDMap struct{ /* unexported */ }

func (m *IDMap) Assign(ctx context.Context, ref entity.Ref) (int32, error)
func (m *IDMap) Resolve(ctx context.Context, id int32) (entity.Ref, error) // ErrNotFound if unknown

// Errors a provider returns; the facade encodes them in v3 shapes (§8.8.2).
var ErrNotFound = errors.New("arrfacade: not found")

type ValidationError struct{ Failures []ValidationFailure }

func (e *ValidationError) Error() string

type ValidationFailure struct {
	PropertyName   string `json:"propertyName"`
	ErrorMessage   string `json:"errorMessage"`
	AttemptedValue any    `json:"attemptedValue,omitempty"`
	Severity       string `json:"severity"` // "error" | "warning"
	ErrorCode      string `json:"errorCode,omitempty"`
}

// Page is the paged envelope used by /queue (and /history, see O3).
type Page[T any] struct {
	Page          int    `json:"page"`
	PageSize      int    `json:"pageSize"`
	SortKey       string `json:"sortKey"`
	SortDirection string `json:"sortDirection"` // "ascending" | "descending"
	TotalRecords  int    `json:"totalRecords"`
	Records       []T    `json:"records"`
}
```

`IDMap` is the only state the facade itself owns. It maps `entity.Ref{Module, Kind, ID}`, the one
polymorphic reference type of 03 §3.3 (COR-17), never a facade-specific struct. Indexer instances
pushed by Prowlarr live in the one integration-instance store (`integrations`, SIMP-4); the facade
keeps only their `int32` ↔ `integrations.Ref` mapping in `IDMap`. `AppInfo` carries `Name`,
`InstanceName`, `URLBase`, `StartTime`.

### 8.6.2 Wire resources (proof depth)

D5 limits TV/Movies to proof depth and the facade is deferred-build, so this file does not carry Go
wire DTOs (SIMP-24). **The contract is the conformance fixtures of §8.14**, which hold every field
each named client reads, transcribed with provenance; raw/16 §5.2 is the field-level source. Core
still owns the DTOs when the facade is built (F2): they are written then, from the fixtures, in
package `arrfacade`. The resources and the fields that matter for behaviour:

| Resource | Required fields (raw/16 §5.2 "Fields required") | Notes |
|---|---|---|
| `SystemStatus` | `appName`, `instanceName`, `version` (= `CompatVersion`), `urlBase`, `startTime`, `isProduction`, `branch`, `authentication` (`"external"`) | `X-Application-Version` header carries the same version |
| `QualityProfile`, `LanguageProfile`, `Tag` | `id`, `name` / `label` | language profiles only for Sonarr major 3 |
| `RootFolder` | `id`, `path`, `accessible`, `freeSpace`, `totalSpace`, `unmappedFolders` (always empty) | from `LibraryRootFolders` |
| `Series` (+ `AddSeries.addOptions`) | `id`, `title`, `sortTitle`, `year`, `path`, `tvdbId`, `seriesType`, `qualityProfileId`, `monitored`, `seasonFolder`, `seasons[]` (number, monitored, statistics), `tags`, `images`, `ended`, `rootFolderPath` | Seerr PUTs back the whole object it read, so unknown fields must round-trip or be ignored |
| `Episode`, `EpisodeFile` | episode: `id`, `seriesId`, `seasonNumber`, `episodeNumber`, `title`, `monitored`, `hasFile`, `episodeFileId`, optional embedded file; file: `id`, `path`, `size`, `quality`, `mediaInfo`, `languages` | Bazarr reads files and media info for subtitle search |
| `Movie` (+ `AddMovie`, legacy `profileId`) | `id`, `title`, `sortTitle`, `titleSlug`, `year`, `path`, `tmdbId`, `qualityProfileId`, `minimumAvailability`, `monitored`, `hasFile`, `movieFile`, `tags`, `images`, `rootFolderPath` | |
| `QueueItem` (in `Page`) | `id`, `title`, `size`, `sizeleft`, `status`, `trackedDownloadStatus`, `trackedDownloadState`, `downloadId`, `protocol`, `downloadClient`, `indexer`, `episodeId`/`seriesId`/`movieId` | backed by `acquire` tracked downloads when built (O8) |
| `Command` | `id`, `name`, `status` (`queued`/`started`/`completed`/`failed`/`aborted`), `queued`, `started`, `ended` | from `TaskCommands` |
| `HistoryRecord` | `id`, `eventType`, `sourceTitle`, `episodeId`/`movieId`, `date`, `data` | shape vs `Page` is O3 |
| `Indexer` (+ `Field`) | `id`, `name`, `implementation`, `implementationName`, `configContract`, `enableRss`, `enableAutomaticSearch`, `enableInteractiveSearch`, `priority`, `protocol`, `tags`, `fields[]` (`baseUrl`, `apiPath`, `apiKey`, `categories`, `animeCategories`, `minimumSeeders`, `seedCriteria.*`; schema metadata on `/indexer/schema`) | secret fields are written to core `secrets` through `FacadeWriter` and read back masked (`"********"`), as the originals do |

## 8.7 Endpoint table

Paths are relative to `<url_base>/api/v3`. **S** = required when `ClientSeerr` is declared, **P** =
`ClientProwlarr`, **B** = `ClientBazarr`; "opt" = served when the field is non-nil but required by
no client. Sources: raw/16 §5.2 (each row there cites the client source file and line).

### Shared (both flavours)

| Method | Path | Provider method | Permission | S | P | B |
|---|---|---|---|---|---|---|
| GET | `/system/status` | `System.Status` | `arr.read` | ✔ | ✔ | ✔ |
| GET | `/qualityprofile` | `QualityProfiles.ListQualityProfiles` | `arr.read` | ✔ | | ✔ |
| GET | `/rootfolder` | `RootFolders.ListRootFolders` | `arr.read` | ✔ | | ✔ |
| GET | `/tag` | `Tags.ListTags` | `arr.read` | ✔ | | ✔ |
| POST | `/tag` | `Tags.CreateTag` | `arr.request` | ✔ | | |
| PUT | `/tag/{id}` | `Tags.UpdateTag` | `arr.request` | ✔ | | |
| GET | `/queue` (`page`, `pageSize`, `includeEpisode`/`includeMovie`) | `Queue.Queue` | `arr.read` | ✔ | | |
| POST | `/command` | `Commands.Start` | `arr.request` | ✔ | | |
| GET | `/command/{id}` | `Commands.Get` | `arr.read` | opt | | |
| GET | `/history` (`eventType`, `episodeId` / `movieIds`) | `History.History` | `arr.read` | | | ✔ |
| GET | `/indexer` | `Indexers.ListIndexers` | `arr.read` | | ✔ | |
| GET | `/indexer/{id}` | `Indexers.GetIndexer` | `arr.read` | | ✔ | |
| POST | `/indexer` (`forceSave`) | `Indexers.CreateIndexer` | `arr.indexers` | | ✔ | |
| PUT | `/indexer/{id}` (`forceSave`) | `Indexers.UpdateIndexer` | `arr.indexers` | | ✔ | |
| DELETE | `/indexer/{id}` | `Indexers.DeleteIndexer` | `arr.indexers` | | ✔ | |
| GET | `/indexer/schema` | `Indexers.IndexerSchema` | `arr.read` | | ✔ | |
| POST | `/indexer/test` | `Indexers.TestIndexer` | `arr.indexers` | | ✔ | |

Commands Seerr sends: `RefreshMonitoredDownloads` (both), `MissingEpisodeSearch {seriesId}` (Sonarr),
`MoviesSearch {movieIds}` (Radarr). Declaring `ClientSeerr` requires `TaskCommands` specs for exactly
these names for the provider's flavour (checked at boot).

### Sonarr flavour

| Method | Path | Provider method | Permission | S | P | B |
|---|---|---|---|---|---|---|
| GET | `/languageprofile` | `LanguageProfiles.ListLanguageProfiles` | `arr.read` | ✔ if major 3 | | ✔ if major 3 |
| GET | `/series` (`tvdbId`) | `Series.ListSeries` | `arr.read` | ✔ | | ✔ |
| GET | `/series/{id}` | `Series.GetSeries` | `arr.read` | ✔ | | ✔ |
| GET | `/series/lookup` (`term`) | `Series.LookupSeries` | `arr.read` | ✔ | | |
| POST | `/series` | `Series.AddSeries` | `arr.request` | ✔ | | |
| PUT | `/series` and `/series/{id}` | `Series.UpdateSeries` | `arr.request` | ✔ | | |
| DELETE | `/series/{id}` (`deleteFiles`, `addImportExclusion`) | `Series.DeleteSeries` | `arr.request` | ✔ | | |
| GET | `/episode` (`seriesId`, `includeEpisodeFile`) | `Episodes.ListEpisodes` | `arr.read` | ✔ | | ✔ |
| GET | `/episode/{id}` | `Episodes.GetEpisode` | `arr.read` | ✔ | | ✔ |
| PUT | `/episode/monitor` | `Episodes.SetEpisodesMonitored` | `arr.request` | ✔ | | |
| GET | `/episodefile` (`seriesId`), `/episodefile/{id}` | `EpisodeFiles.*` | `arr.read` | | | ✔ |

### Radarr flavour

| Method | Path | Provider method | Permission | S | P | B |
|---|---|---|---|---|---|---|
| GET | `/movie` (`tmdbId`) | `Movies.ListMovies` | `arr.read` | ✔ | | ✔ |
| GET | `/movie/{id}` | `Movies.GetMovie` | `arr.read` | ✔ | | ✔ |
| GET | `/movie/lookup` (`term`) | `Movies.LookupMovie` | `arr.read` | ✔ | | |
| POST | `/movie` | `Movies.AddMovie` | `arr.request` | ✔ | | |
| PUT | `/movie` and `/movie/{id}` | `Movies.UpdateMovie` | `arr.request` | ✔ | | |
| DELETE | `/movie/{id}` (`deleteFiles`, `addImportExclusion`) | `Movies.DeleteMovie` | `arr.request` | ✔ | | |

### Not served

| Path | Client | Decision |
|---|---|---|
| `/signalr/messages` (WebSocket, `access_token`) | Bazarr | not served (§8.8.4) |
| unversioned `/api/system/status` | Bazarr legacy-v2 fallback | not served: only v3 exists |
| anything else in Sonarr/Radarr v3 | — | not served; counted in `mediacore_arrfacade_unserved_total` so real demand is visible |

## 8.8 Wire conventions

### 8.8.1 Encoding

* JSON, camelCase output (`encoding/json` with explicit tags). Input decoding is case-insensitive and
  ignores unknown fields — both are `encoding/json` defaults and match the originals, which matters
  because Seerr PUTs back the full object it read (raw/16 §5.2 PUT rows).
* Lists are bare JSON arrays, except `/queue` (and possibly `/history`, O3) which use `Page[T]`.
* Times RFC 3339 UTC; ids `int32`; enums lower-camel strings.
* Every facade response carries `X-Application-Version: <CompatVersion>` (Prowlarr reads it on
  `/indexer/test`, minimum 3.0.5) and `Content-Type: application/json; charset=utf-8`.

### 8.8.2 Errors

| Situation | Status | Body |
|---|---|---|
| missing/invalid/wrong-purpose/wrong-module key | 401 | empty |
| key lacks the endpoint permission | 403 | `{"message":"Forbidden"}` |
| `ErrNotFound` or nil provider field | 404 | `{"message":"NotFound"}` |
| `*ValidationError` | 400 | JSON array of `ValidationFailure` |
| warning-only validation without `forceSave=true` | 400 | array with `severity: "warning"` (Prowlarr then retries with `forceSave=true`, raw/16 §5.2 POST `/indexer` row) |
| other provider error | 500 | `{"message":"<safe summary>","description":""}` — never a stack trace or a path |
| facade disabled by config | 404 | `{"message":"NotFound"}` |

The exact v3 error JSON is confirmed against the conformance fixtures (§8.14); if a fixture shows a
different shape, the fixture wins and this table is amended.

### 8.8.3 Paging

`QueueQuery`/`HistoryQuery` carry `Page` (1-based, default 1), `PageSize` (default 10, max 1000),
`SortKey`, `SortDirection`; core parses and clamps, the provider pages.

### 8.8.4 SignalR decision

Not served. Reasons: core has no WebSocket server (raw/09 §6 — SSE only, `coder/websocket` is only an
indirect dependency); SignalR's negotiate + hub framing is a second realtime protocol to maintain;
Bazarr reads SignalR only to *trigger partial syncs* (raw/16 §5.2 last row) and its REST sync works
without it. The reserved path answers 404. Revisit only if the Bazarr conformance scenario shows
Bazarr misbehaving (not merely logging) without it (O2); the implementation would then be an adapter
from core `events` topics to SignalR messages inside `arrfacade`, not a core realtime change.

## 8.9 Lifecycle

1. Module manifest includes `arrfacade.Permissions(id)` in `Manifest.Permissions`; `Register` calls
   `r.ArrFacade(p)`, building `CoreIndexers` from `Deps.Indexers` and
   `Deps.Integrations.FacadeWriter(id)`, and `TaskCommands` from `Deps.Tasks`.
2. Kernel P7: validates `Flavor`, `CompatVersion` (semver-ish 3- or 4-part, ≥ 3.0.5), required
   resources per declared client, command specs for Seerr, URL-base collisions (06 problem kinds
   `ProblemFacadeIncomplete`, `ProblemFacadeURLBaseCollision`).
3. Kernel P8: mounts the facade if `modules.<module>.arrfacade.enabled` is true at boot; when false the
   handler is still mounted but answers 404, so enabling later is a hot change (§8.11).
4. Shutdown: in-flight facade requests drain with the listener; no facade-owned goroutines.

## 8.10 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Provider incomplete for a declared client | fail-closed at boot | a half-working Seerr integration fails in front of users |
| URL-base collision (with another facade, legacy prefix, core route) | fail-closed at boot | §8.4 |
| Key missing/invalid/expired/wrong purpose/wrong module | 401, audited as denial | facade keys are single-purpose |
| Permission missing | 403, audited | R6 |
| Nil provider field / unknown path | 404, counted as unserved | contract grows on evidence |
| Provider returns `ErrNotFound` | 404 | v3 semantics |
| Provider returns `*ValidationError` | 400 with failures | clients show the messages |
| Provider returns other error | 500 safe summary; full error logged with request id | never leak internals |
| `IDMap` store error | 500, fail-closed (no id invented) | an id must be stable forever |
| Command name unknown | 400 validation failure | clients surface it |
| Task enqueue refused (permission) | 403 | task permissions apply to facade callers |
| Request body over limit | 413 via core body limit | shared chain |
| Facade disabled | 404 | indistinguishable from not configured |

## 8.11 Concurrency contract

* Providers must be goroutine-safe; the facade calls them from request goroutines with the request
  ctx (cancelled on client disconnect). No provider call may outlive its ctx.
* `IDMap.Assign` is idempotent under concurrency (compare-and-set on the ref key inside one storage
  batch); two concurrent first requests for the same ref get the same id.
* List endpoints over library-scale collections (`/series` with Bazarr's full sync) must stream or
  page inside the provider; the facade adds no unbounded buffering beyond encoding the result.
* The enabled flag is read per request from `config.Value` (lock-free).

## 8.12 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `modules.<module>.arrfacade.enabled` | bool | `false` (opt-in: it exposes a key-authenticated API) | yes (404 when off) |
| `modules.<module>.arrfacade.url_base` | string (`""` or `/segment[/segment]`) | `""` | no |
| `modules.<module>.arrfacade.app_name` | string | app `DisplayName` | yes |
| `modules.<module>.arrfacade.instance_name` | string | app `DisplayName` | yes |
| `modules.<module>.arrfacade.key_expiry_warning` | duration | `720h` | yes |
| `modules.<module>.arrfacade.page_size_max` | int | `1000` | yes |

## 8.13 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_arrfacade_requests_total` | counter | `module`, `resource`, `method`, `status` |
| `mediacore_arrfacade_request_seconds` | histogram | `module`, `resource`, `method` |
| `mediacore_arrfacade_auth_failures_total` | counter | `module`, `reason` (`missing`/`invalid`/`expired`/`purpose`/`module`/`permission`) |
| `mediacore_arrfacade_unserved_total` | counter | `module`, `method`, `path_template` (normalised, ids replaced by `{id}`) |
| `mediacore_arrfacade_key_expiry_seconds` | gauge | `module` (earliest facade key) |
| `mediacore_arrfacade_idmap_entries` | gauge | `module` |

## 8.14 Tests and conformance

**Fixtures are transcribed from public client source, never captured from live services** (none are
reachable or needed). Layout:

```
core/arrfacade/conformance/
  testdata/seerr/{sonarr,radarr}/<step>.json
  testdata/prowlarr/{sonarr,radarr}/<step>.json
  testdata/bazarr/{sonarr,radarr}/<step>.json
  PROVENANCE.md   per file: client repo, file path, commit SHA, fetch date, the fields read
```

Each fixture is `{ "request": {method, path, query, headers, body}, "expect": {status, headers, body} }`
where `expect.body` is a representative JSON document containing **every field the client reads**
(raw/16 §5.2 "Fields required" column) with type-correct values. The engine is ABO's
`internal/syncapi/conformance` diff (`Compare`, `KindMissingField` fatal, `KindExtraField` ignored).
It is **promoted to core** as `httpapi/conformance` in the build that creates `arrfacade`, because only
then does it have two consumers (ABS compatibility in ABO and this facade), as the 03 §3.4 promotion
rule requires; until then it stays in ABO (04, SIMP2-9).

| Test | Proves |
|---|---|
| `TestSeerrAddSeriesFlow` / `TestSeerrAddMovieFlow` | status → profiles → root folders → tags → lookup → POST → queue (+`RefreshMonitoredDownloads`) → search command, against a fake provider |
| `TestProwlarrIndexerSync` | status (+`X-Application-Version`) → schema → list → test → POST 400 warning → POST `forceSave=true` → PUT → DELETE, using `CoreIndexers` over a `coretest/fakes` indexer pool and a real `FacadeWriter` (instance written with `Origin=arrfacade`; a LAN URL refused unless allowed) |
| `TestBazarrFullSync` | status → (language profiles if major 3) → series → episodes with files → episode files → history |
| `TestRequestDecoding_*` | Seerr's POST/PUT bodies decode into `AddSeries`/`Series`/`AddMovie` without dropping required fields; unknown fields ignored |
| `TestCaseInsensitivePaths` | `/qualityProfile`, `/episodeFile`, trailing slash, `ApiKey` query name |
| `TestAuth_*` | each key source; admin key refused; general key with extra scopes refused; wrong module refused (the refusal off the facade group is tested by the chain's owners, §8.5) |
| `TestProviderValidation_*` | each missing-resource combination fails boot with `ProblemFacadeIncomplete` |
| `TestURLBaseCollision` | with a legacy `/api/v3/ops` prefix and with two facades |
| `TestIDMapStableConcurrent` | `synctest` + parallel `Assign` |
| `TestErrorShapes` | §8.8.2 table |

Apps run the same scenarios against their real provider: `arrfacadetest.Conformance(t, h *coretest.Harness, module, arrfacade.ClientSeerr)`.

## 8.15 Migration from ABO

| Today | Becomes |
|---|---|
| `/api/v3/ops/*` (12 routes, `internal/server/wire_ops_v3_routes.go`, `internal/server/handlers/opsv3/`) | core tasks API `/api/core/v1/tasks/...` (05-tasks); `/api/v3/ops/` stays as a declared `books` legacy prefix for the compatibility window (09), then 410, then removed |
| `/api/*` redirect exclusion for `/api/v3/` (`server_lifecycle.go:1801-1803`) | removed with the legacy prefix; core reserves `/api/v3/` for facades |
| `internal/syncapi/conformance` | unchanged during S0–S10 (04); moves to core `httpapi/conformance` when `arrfacade` is built, then shared by ABO's ABS surface and the facade (SIMP2-9) |
| query redaction list (`internal/server/server.go:695-704`) | core redaction (05-logging) with `apikey`, `access_token` added |
| ABS compatibility surface | stays in ABO (an app's compatibility surface, raw/10 G15); its identity already resolves to core users — the same principle the facade follows |

## 8.16 Open items

| # | Item | Owner |
|---|---|---|
| O1 | Non-expiring facade keys (owner decision); default stays 365 d with warning | owner / 05-authn |
| O2 | Bazarr behaviour without SignalR: confirm from Bazarr source that REST scheduled sync continues and failures are only logged | 08 (fixture work) |
| O3 | `/history` response shape (bare array vs `Page`) as Bazarr reads it — raw/16 §5.1 names only `/queue` as paged | 08 (fixture work) |
| O4 | Whether any client checks `appName` equals "Sonarr"/"Radarr"; default is the app's own name | 08 (fixture work) |
| O5 | Radarr-side minimum `X-Application-Version` Prowlarr accepts (raw/16 cites the Sonarr proxy) | 08 |
| O6 | Commit SHAs of the client sources: raw/16 records branch + date only; PROVENANCE.md must pin SHAs when fixtures are written | plan |
| O7 | Licensing note: fixtures are authored by us from interface facts (field names, paths); no client source is copied into the repo | 08 |
| O8 | Default `Tags` and `Queue` providers once core has a tag store / `acquire` tracked downloads (raw/16 §7.1 #10, #15) | 10-tv-movies-proof |

Closed in 1.1.0: O9 (explicit facade purpose on keys): `KeySpec.Purpose = PurposeFacade` and
`Module` are normative in 05-authn §4.4, as deferred-build members that land with this package
(§8.0, SIMP2-6), and the refusal of facade keys off their group is owned by 05-rbac §4.10 and
05-http-api §4.2 stage 12 (SEC-5).

## Review history

Round 1 (2026-11-04), findings applied to this file:

- SEC-5 — facade keys are `PurposeFacade` + `Module` (05-authn), scopes `Confining` (05-rbac); refusal on non-facade routes owned by the chain; indexer writes only through `integrations.FacadeWriter`; O9 closed.
- SEC-23 — `rbac.PermissionSpec` with `Warn` replaces `PermissionDef`/`Dangerous`; no `DefaultGrants` on the `Confining` facade scopes; facade scopes never `Sensitive`, with the reason.
- SIMP-24 — Go wire DTOs replaced by a field table; fixtures (§8.14) are the contract.
- SIMP-1 / cross-cutting 1 — file marked deferred-build; only the `/api/v3/` reservation and the conformance-engine move happen during the extraction. (Superseded in round 2 by SIMP2-9: the conformance engine stays in ABO until `arrfacade` is built.)
- SIMP-4 — pushed indexers live in the one integration-instance store; the facade keeps only the id map.
- COR-17 — `IDMap` over `entity.Ref`.
- COR-27 — `LibraryRootFolders(library.Scope, library.RootFilter)`, `CoreIndexers(*indexer.Pool, *integrations.FacadeWriter, *IDMap)`, `CommandSpec.Kind tasks.Kind`.
- COR-14 — URL-base collision checked in P7, after config load.
- COR-1 — `Registrar.ArrFacade`, `Deps.Indexers` referenced as 06 §6.4.9 deferred-build members.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-H request — stage range S0–S10.
- integration pass: Verified already present (R-F/R-G request): `integrations.FacadeWriter`, `*indexer.Pool`, `IDMap` over `entity.Ref`.

Round 2 (2026-11-04), findings applied to this file:

- SIMP2-6 — facade-only key machinery (`KeySpec.Purpose`/`Module`, `PurposeFacade`, query-key extraction, `FacadeGroup`, `RouteKind Facade`, the stage-13 facade clause, the two facade tests) listed in §8.0 as deferred-build members of `auth`/`rbac`/`httpapi` that land with `arrfacade`; F5, §8.4, §8.5 and O9 point at that table; S7 grep done-check. (Superseded in round 3 by SIMP3-2: the deferred-members archtest from S2.)
- SIMP2-9 — the conformance engine stays `internal/syncapi/conformance` in ABO until `arrfacade` is built (§8.0, §8.14, §8.15); the extraction-time list is the `/api/v3/` reservation only.

Round 3 (2026-11-04), findings applied to this file:

- SIMP3-2 — §8.0: the S7 prose grep replaced by the deferred-members archtest (`archtest.DeferredMembersAbsent`, root `make ci` from S2, 06 §6.4.9).
- SIMP3-11 — §8.0: the facade problem kinds are appended at the end of 06's `ProblemKind` block when `arrfacade` lands, and are in the archtest list.
- SIMP3-12 — round-1 SIMP-1 history line marked superseded by SIMP2-9.
- R3-1 — checked: no dual-format machinery here; facade keys keep explicit, non-empty `Confining` scopes, consistent with R3-1's never-empty-scope rule.

Round 4 (2026-11-04), findings applied to this file:

- SIMP4-1 — §8.0: owning files keep facade members under a `Deferred-build members` heading; the archtest list is generated from every such heading (03 §3.4) with package-qualified entries; the `arrfacade` PR deletes only the facade entries, and the test goes when the list is empty.
- R4-1 — checked: the facade touches no legacy store or grant; nothing else to change.
