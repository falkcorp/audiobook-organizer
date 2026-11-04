<!-- file: docs/specs/platform-core/08-arr-facade.md -->
<!-- version: 1.0.0 -->
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
| F2 | Core owns the **wire DTOs** (camelCase, field sets from raw/16 §5.2). Apps map domain → DTO. | One place to fix a field name for every app; conformance tests run against core types. |
| F3 | Core provides **default providers** for resources that are core data: system status, root folders (from `library`), indexers (from `integrations/indexer`), commands (mapped to `tasks`). | Prowlarr push and most pickers then need zero app code. |
| F4 | Integer IDs: core provides a persistent **ID map** from `EntityRef{Module, Kind, ID}` to stable `int32` (keyspace `c/arrfacade/<module>/ids/`). | v3 clients require numeric ids and store them; app IDs are opaque strings. |
| F5 | Auth is **API key only**, through the core API key store, with keys bound to one module by holding only that module's facade scopes. | Clients store one key forever; it must not be a general admin key. |
| F6 | Mount at `<url_base>/api/v3` per module. Core **reserves** `/api/v3/` for facades; ABO's `/api/v3/ops` becomes a legacy alias of the core tasks API for the compatibility window and cannot coexist with a facade on the same base. | §8.4 |
| F7 | **No SignalR.** `/signalr/*` is not served. | §8.8.4 |

## 8.4 Mount point and the `/api/v3/ops` collision

* Each module that registers a provider gets one facade at `<url_base>/api/v3/`, where `url_base` is
  `modules.<module>.arrfacade.url_base` (default `""`, the key 05-http-api §4.9 uses). Sonarr and Radarr have the same "URL Base"
  setting and the named clients accept a base URL with a path, so a non-empty base is a normal
  client configuration.
* The kernel (06 P1/P7) fails boot with `ProblemFacadeURLBaseCollision` when two enabled facades
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
* Middleware: the facade is mounted on `httpapi.Router.FacadeGroup(owner, base)` (05-http-api §4.9) with the **same core chain** as native
  routes (peer allow-list, recovery, security headers, rate limit, body limit) but with facade
  authentication (§8.5) instead of session/key auth, and v3 error encoding.
* Path matching: ASP.NET routing in the originals is case-insensitive and clients rely on it (Seerr
  calls `/qualityProfile`, Bazarr `/episodefile`; raw/16 §5.2). Go's `ServeMux` is case-sensitive, so
  the facade router lower-cases the **resource segments** before matching (numeric ids untouched) and
  ignores a trailing slash. Query parameter names are matched case-insensitively.

## 8.5 Authentication and permissions

* Key sources, in order: header `X-Api-Key`, query `apikey`, header `Authorization: Bearer <key>`
  (raw/16 §5.1: Seerr sends `apikey`, Prowlarr sends `X-Api-Key`, Bazarr sends `?apikey=`). Session
  cookies are ignored on facade paths.
* The key must be a core API key (05-authn §4.2 already enables `X-Api-Key`/`apikey` extraction only
  on facade groups) **bound to the facade of one module**. Binding rule: a key is a facade key for
  module `m` iff **every** scope it holds is one of `m:arr.read`, `m:arr.request`, `m:arr.indexers`.
  The facade refuses any other key — including a full-scope admin key — and `httpapi` refuses facade
  keys on every non-facade group (including `rbac.AnyAuthenticated` routes, which a `tv:arr.*` scope
  would otherwise satisfy, 05-rbac §4.10). This needs no new key field; if 05-authn adds an explicit
  `KeySpec.Purpose`, the rule becomes "purpose = arrfacade and module = m" and the scope rule stays as
  a second check (open item O9). One leaked Seerr key can therefore only do what the facade allows
  for one module.
* Permissions are declared by the module in its manifest with `arrfacade.Permissions(id)`:

| Permission | Grants | Default grants (06 `Manifest.Grants`) |
|---|---|---|
| `<module>:arr.read` | every GET in §8.7 | none (admin only) |
| `<module>:arr.request` | add/update/delete series or movies, tags, episode monitor, commands | none; `Dangerous: true` (05-rbac `PermissionDef`: UI warns on grant) |
| `<module>:arr.indexers` | indexer create/update/delete/test | none |

* Key creation presets in the settings UI: **Seerr** = read + request, **Prowlarr** = read + indexers,
  **Bazarr** = read. A key's effective permission is `scopes ∩ owner's permissions` as today
  (raw/10 G6); the creating user should be a dedicated service user, which the UI suggests.
* Expiry: facade keys follow the core maximum (365 d). Because clients fail silently when a key
  expires, core health component `arrfacade.<module>.keys` goes **degraded 30 days before** the
  earliest facade key expires and core notify (05-notify) sends one notification; rotation is a
  one-click "regenerate" that shows the new key once. Allowing non-expiring facade keys is open item O1.
* Audit: every facade write is an audit event with actor = key owner, method `api_key:arrfacade`,
  module, resource, outcome (05-rbac).
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

// Provider is what a module registers with module.Registrar.ArrFacade.
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
	Indexers         IndexerProvider         // CoreIndexers(...) usually
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
// Permissions returns the three facade permission specs for a module's manifest.
func Permissions(module string) []rbac.PermissionDef

// DefaultSystem builds system/status from the App identity, CompatVersion,
// url base and start time. Path-revealing fields are returned empty.
func DefaultSystem(info AppInfo) SystemProvider

// LibraryRootFolders exposes the module's library roots (05-library) with
// free/total space; unmappedFolders is always empty.
func LibraryRootFolders(roots library.Roots, filter func(library.Root) bool) RootFolderProvider

// CoreIndexers stores pushed indexers as core indexer instances (05-indexer,
// Newznab/Torznab adapter), tagged source=arrfacade:<client>; Test runs a caps
// request through the adapter; forceSave=false refuses to save a failing one.
func CoreIndexers(set indexer.Set, ids *IDMap) IndexerProvider

// TaskCommands maps command names to task kinds; the task is enqueued as the
// key's principal (so task permissions apply) and the run id is mapped to a
// command id. Unknown names answer 400 with a validation failure.
func TaskCommands(c tasks.Client, ids *IDMap, specs map[string]CommandSpec) CommandProvider

type CommandSpec struct {
	Kind   string                                 // e.g. "tv.search.series"
	Params func(body json.RawMessage) (any, error) // decode {seriesId} etc. into task params
}

// IDMap assigns stable int32 ids to opaque entity refs, persisted under
// c/arrfacade/<module>/ids/. Assign is idempotent; ids are never reused.
type IDMap struct{ /* unexported */ }

func (m *IDMap) Assign(ctx context.Context, ref events.EntityRef) (int32, error)
func (m *IDMap) Resolve(ctx context.Context, id int32) (events.EntityRef, error) // ErrNotFound if unknown

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

`IDMap` is the only state the facade itself owns. `EntityRef{Module, Kind, ID}` is core's polymorphic
reference (02 §2.2 decision 6); its home package is fixed by 05-events/05-storage and is shown here as
`events.EntityRef`. `AppInfo`
carries `Name`, `InstanceName`, `URLBase`, `StartTime`.

### 8.6.2 Wire DTOs (field sets from raw/16 §5.2; JSON names are the contract)

```go
type SystemStatus struct {
	AppName      string    `json:"appName"`
	InstanceName string    `json:"instanceName"`
	Version      string    `json:"version"` // = CompatVersion
	URLBase      string    `json:"urlBase"`
	StartTime    time.Time `json:"startTime"`
	IsProduction bool      `json:"isProduction"`
	Branch       string    `json:"branch"`
	Authentication string  `json:"authentication"` // "external"
}

type QualityProfile struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type LanguageProfile struct {
	ID   int32  `json:"id"`
	Name string `json:"name"`
}

type RootFolder struct {
	ID              int32    `json:"id"`
	Path            string   `json:"path"`
	Accessible      bool     `json:"accessible"`
	FreeSpace       int64    `json:"freeSpace"`
	TotalSpace      int64    `json:"totalSpace"`
	UnmappedFolders []string `json:"unmappedFolders"`
}

type Tag struct {
	ID    int32  `json:"id"`
	Label string `json:"label"`
}

type Image struct {
	CoverType string `json:"coverType"` // "poster", "banner", "fanart"
	URL       string `json:"url"`
	RemoteURL string `json:"remoteUrl,omitempty"`
}

type SeasonStatistics struct {
	EpisodeFileCount  int `json:"episodeFileCount"`
	TotalEpisodeCount int `json:"totalEpisodeCount"`
}

type Season struct {
	SeasonNumber int               `json:"seasonNumber"`
	Monitored    bool              `json:"monitored"`
	Statistics   *SeasonStatistics `json:"statistics,omitempty"`
}

type Series struct {
	ID                int32    `json:"id,omitempty"`
	Title             string   `json:"title"`
	SortTitle         string   `json:"sortTitle"`
	Year              int      `json:"year"`
	Path              string   `json:"path"`
	TvdbID            int      `json:"tvdbId"`
	ImdbID            string   `json:"imdbId,omitempty"`
	Overview          string   `json:"overview,omitempty"`
	SeriesType        string   `json:"seriesType"` // "standard" | "daily" | "anime"
	QualityProfileID  int32    `json:"qualityProfileId"`
	LanguageProfileID int32    `json:"languageProfileId,omitempty"`
	Monitored         bool     `json:"monitored"`
	MonitorNewItems   string   `json:"monitorNewItems,omitempty"`
	SeasonFolder      bool     `json:"seasonFolder"`
	Seasons           []Season `json:"seasons"`
	Tags              []int32  `json:"tags"`
	Images            []Image  `json:"images"`
	RemotePoster      string   `json:"remotePoster,omitempty"`
	AlternateTitles   []AlternateTitle `json:"alternateTitles,omitempty"`
	Ended             bool     `json:"ended"`
	LastAired         *time.Time `json:"lastAired,omitempty"`
	OriginalLanguage  *Language `json:"originalLanguage,omitempty"`
	RootFolderPath    string   `json:"rootFolderPath,omitempty"`
}

type AddSeries struct {
	Series
	AddOptions struct {
		IgnoreEpisodesWithFiles  bool `json:"ignoreEpisodesWithFiles"`
		SearchForMissingEpisodes bool `json:"searchForMissingEpisodes"`
	} `json:"addOptions"`
}

type Episode struct {
	ID                    int32        `json:"id"`
	SeriesID              int32        `json:"seriesId"`
	SeasonNumber          int          `json:"seasonNumber"`
	EpisodeNumber         int          `json:"episodeNumber"`
	AbsoluteEpisodeNumber *int         `json:"absoluteEpisodeNumber,omitempty"`
	Title                 string       `json:"title"`
	Monitored             bool         `json:"monitored"`
	HasFile               bool         `json:"hasFile"`
	EpisodeFileID         int32        `json:"episodeFileId"`
	EpisodeFile           *EpisodeFile `json:"episodeFile,omitempty"` // includeEpisodeFile=true
}

type MediaFile struct { // shared by EpisodeFile and Movie.MovieFile
	ID        int32      `json:"id"`
	Path      string     `json:"path"`
	Size      int64      `json:"size"`
	Quality   QualityRef `json:"quality"`
	MediaInfo MediaInfo  `json:"mediaInfo"`
	SceneName string     `json:"sceneName,omitempty"`
	Languages []Language `json:"languages"`
}

type EpisodeFile struct{ MediaFile }

type Movie struct {
	ID                 int32      `json:"id,omitempty"`
	Title              string     `json:"title"`
	SortTitle          string     `json:"sortTitle"`
	TitleSlug          string     `json:"titleSlug"`
	Year               int        `json:"year"`
	Path               string     `json:"path"`
	TmdbID             int        `json:"tmdbId"`
	ImdbID             string     `json:"imdbId,omitempty"`
	Overview           string     `json:"overview,omitempty"`
	QualityProfileID   int32      `json:"qualityProfileId"`
	MinimumAvailability string    `json:"minimumAvailability"` // "announced" | "inCinemas" | "released"
	Monitored          bool       `json:"monitored"`
	HasFile            bool       `json:"hasFile"`
	MovieFile          *MediaFile `json:"movieFile,omitempty"`
	Tags               []int32    `json:"tags"`
	Images             []Image    `json:"images"`
	AlternativeTitles  []AlternateTitle `json:"alternativeTitles,omitempty"`
	OriginalLanguage   *Language  `json:"originalLanguage,omitempty"`
	RootFolderPath     string     `json:"rootFolderPath,omitempty"`
}

type AddMovie struct {
	Movie
	ProfileID  int32 `json:"profileId,omitempty"` // legacy alias Seerr still sends
	AddOptions struct {
		SearchForMovie bool `json:"searchForMovie"`
	} `json:"addOptions"`
}

type QueueItem struct {
	ID                      int32      `json:"id"`
	Title                   string     `json:"title"`
	Size                    float64    `json:"size"`
	Sizeleft                float64    `json:"sizeleft"`
	Timeleft                string     `json:"timeleft,omitempty"` // "hh:mm:ss"
	EstimatedCompletionTime *time.Time `json:"estimatedCompletionTime,omitempty"`
	Status                  string     `json:"status"`
	TrackedDownloadStatus   string     `json:"trackedDownloadStatus"`
	TrackedDownloadState    string     `json:"trackedDownloadState"`
	DownloadID              string     `json:"downloadId"`
	Protocol                string     `json:"protocol"` // "torrent" | "usenet"
	DownloadClient          string     `json:"downloadClient"`
	Indexer                 string     `json:"indexer"`
	EpisodeID               int32      `json:"episodeId,omitempty"`
	Episode                 *Episode   `json:"episode,omitempty"` // includeEpisode=true
	SeriesID                int32      `json:"seriesId,omitempty"`
	MovieID                 int32      `json:"movieId,omitempty"`
}

type Command struct {
	ID      int32     `json:"id"`
	Name    string    `json:"name"`
	Status  string    `json:"status"` // "queued" | "started" | "completed" | "failed" | "aborted"
	Queued  time.Time `json:"queued"`
	Started *time.Time `json:"started,omitempty"`
	Ended   *time.Time `json:"ended,omitempty"`
}

type HistoryRecord struct {
	ID          int32             `json:"id"`
	EventType   string            `json:"eventType"` // "grabbed" | ...
	SourceTitle string            `json:"sourceTitle"`
	EpisodeID   int32             `json:"episodeId,omitempty"`
	MovieID     int32             `json:"movieId,omitempty"`
	Date        time.Time         `json:"date"`
	Data        map[string]string `json:"data"`
}

// Indexer is the provider-style resource shared by every *arr provider type.
type Indexer struct {
	ID                      int32   `json:"id,omitempty"`
	Name                    string  `json:"name"`
	Implementation          string  `json:"implementation"`     // "Newznab" | "Torznab"
	ImplementationName      string  `json:"implementationName"`
	ConfigContract          string  `json:"configContract"`     // "NewznabSettings" | "TorznabSettings"
	InfoLink                string  `json:"infoLink,omitempty"`
	EnableRss               bool    `json:"enableRss"`
	EnableAutomaticSearch   bool    `json:"enableAutomaticSearch"`
	EnableInteractiveSearch bool    `json:"enableInteractiveSearch"`
	Priority                int     `json:"priority"`
	Protocol                string  `json:"protocol"`
	Tags                    []int32 `json:"tags"`
	Fields                  []Field `json:"fields"` // baseUrl, apiPath, apiKey, categories, animeCategories, minimumSeeders, seedCriteria.*
}

type Field struct {
	Name  string `json:"name"`
	Value any    `json:"value,omitempty"`
	// Schema-only metadata (GET /indexer/schema): label, type, advanced, ...
	Label    string `json:"label,omitempty"`
	Type     string `json:"type,omitempty"`
	Advanced bool   `json:"advanced,omitempty"`
}
```

`QualityRef`, `MediaInfo` (audio/video codec, channels, resolution, run time, subtitles),
`Language {id, name}` and `AlternateTitle {title, seasonNumber?}` complete the set. Secret fields
(`apiKey` in `Fields`) are written to core `secrets` by `CoreIndexers` and read back masked
(`"********"`), matching how the originals mask them.

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

The exact v3 error JSON is confirmed against the conformance fixtures (§8.12); if a fixture shows a
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

1. Module manifest includes `arrfacade.Permissions(id)`; `Register` calls `r.ArrFacade(p)`.
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
`internal/syncapi/conformance` diff (`Compare`, `KindMissingField` fatal, `KindExtraField` ignored),
**promoted to core** as `httpapi/conformance` — it then has two consumers (ABS compatibility in ABO and
this facade), satisfying the 03 §3.4 promotion rule; 04 records the move.

| Test | Proves |
|---|---|
| `TestSeerrAddSeriesFlow` / `TestSeerrAddMovieFlow` | status → profiles → root folders → tags → lookup → POST → queue (+`RefreshMonitoredDownloads`) → search command, against a fake provider |
| `TestProwlarrIndexerSync` | status (+`X-Application-Version`) → schema → list → test → POST 400 warning → POST `forceSave=true` → PUT → DELETE, using `CoreIndexers` over `coretest/fakes` indexer set |
| `TestBazarrFullSync` | status → (language profiles if major 3) → series → episodes with files → episode files → history |
| `TestRequestDecoding_*` | Seerr's POST/PUT bodies decode into `AddSeries`/`Series`/`AddMovie` without dropping required fields; unknown fields ignored |
| `TestCaseInsensitivePaths` | `/qualityProfile`, `/episodeFile`, trailing slash, `ApiKey` query name |
| `TestAuth_*` | each key source; admin key refused; facade key refused on `/api/core/v1/*`; wrong module refused |
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
| `internal/syncapi/conformance` | core `httpapi/conformance` (shared by ABO's ABS surface and the facade) |
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
| O9 | Explicit facade purpose on API keys (`KeySpec.Purpose`, `Module`) vs the scope-derived binding of §8.5; plus the httpapi rule refusing facade keys on non-facade groups | 05-authn, 05-http-api |
| O8 | Default `Tags` and `Queue` providers once core has a tag store / `acquire` tracked downloads (raw/16 §7.1 #10, #15) | 10-tv-movies-proof |
