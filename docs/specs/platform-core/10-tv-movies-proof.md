<!-- file: docs/specs/platform-core/10-tv-movies-proof.md -->
<!-- version: 1.7.0 -->
<!-- guid: e9ed438b-6097-4a4d-8b51-d2eacaca8221 -->
<!-- last-edited: 2026-11-04 -->

# 10 — TV and Movies: proof that the core contracts fit

Evidence commit: `748fd787a`. Domain evidence: [`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md)
(cited below as `raw/16 §n`; it cites Sonarr/Radarr source and the Servarr wiki by URL).
Normative names come from [03-architecture](03-architecture.md); decisions from [02-decision-record](02-decision-record.md).

## 10.0 Purpose, scope, and how to read this file

D5 says: specify ABO-on-core fully, and TV/Movies **only deep enough to prove the core contracts
fit**. This file therefore does three things and no more:

1. Sketches a Sonarr-like `tv` module and a Radarr-like `movies` module (§10.1, §10.2) and, for each,
   lists exactly which core capability it consumes and how. A row that cannot be filled from an
   existing core contract is a **gap**.
2. Collects the gaps (§10.4) and says how each is closed — in an existing contract spec, or in the one
   new core package this file owns: **`acquire`** (§10.5, listed in 03 §3.3 as "public, later").
3. Justifies that `acquire` is media-neutral rather than a TV engine with a movie bolted on (§10.6),
   and closes the old ebook question Q2 (§10.7).

Non-goals: Sonarr/Radarr parity (D2: native API plus an optional facade, no parity); release-title
parsers, scene numbering/XEM, import lists, naming-token catalogues (all app-level per `raw/16 §7.1`
rows 7, 11, 17–20); any UI beyond the module contribution points of [07-ui-shell](07-ui-shell.md).

**Where the code would live.** `tv` and `movies` are *modules* (03 §3.1) in their own future app
repos (`falkcorp/tv-*`, `falkcorp/movies-*`). Nothing in this file is built in the ABO repo.

> **Design sketch (non-normative; A-15).** This whole file is a design sketch. The SDK freezes none of it: the
> `tv` and `movies` modules, `acquire` (§10.5), and what it relies on that is also deferred — `integrations/indexer`,
> the `arrfacade` and phase 2 of `integrations/download` (grab, monitor, recovery) — are designed for real and built
> when the first TV/Movies app starts (03 §3.3, §3.4; cross-cutting resolution 1, SIMP-1, SIMP-24). What the file
> still proves (D5) is that the contracts the SDK **does** freeze fit TV and Movies: every row of §10.1.2 and
> §10.2.2 that names a built package (`storage`, `tasks`, `library`, `integrations`, `providerhttp`,
> `integrations/metadata`, `integrations/notify`, `events`, `outbox`, `rbac`, `httpapi`, `realtime`, the UI shell)
> is filled from that package's frozen contract, and §10.4 closes each gap either in such a contract or in the
> sketched `acquire`. §10.5 is kept at **proof depth**: interfaces, key types and invariants, not an
> implementation-ready API.

## 10.1 TV module (`tv`) — Sonarr-like

### 10.1.1 Data model sketch

Shapes follow Sonarr's entities (`raw/16 §1.1`) with three deliberate differences: Season stays an
**embedded value** in Series exactly as in Sonarr (`SON Tv/Season.cs` L6, `raw/16 §1.1`); quality is
on the **file**, the profile on the **series** (`raw/16 §1.1` notes, §7.2); IDs are `uint64` from a
storage sequence so the optional *arr facade can expose the integer IDs Seerr/Bazarr send
(`seriesId`, `episodeIds[]`: `raw/16 §5.2`) without a mapping table.

```go
package tv // module "tv", in the TV app repo

import (
	"time"

	"github.com/falkcorp/mediacore/acquire"
	"github.com/falkcorp/mediacore/library"
)

type SeriesID uint64
type EpisodeID uint64
type EpisodeFileID uint64

// SeriesType selects numbering, search criteria and naming template (raw/16 §1.2).
type SeriesType uint8

const (
	SeriesStandard SeriesType = iota // SxxEyy
	SeriesDaily                      // yyyy-MM-dd; Newznab season=<yyyy>&ep=<MM>/<dd>
	SeriesAnime                      // absolute numbering; anime categories; no season packs
)

type ExternalIDs struct {
	TVDB, TMDB, TVMaze int64  // zero = unknown; all three are cross-references, see §10.3
	IMDb               string // "tt..."
}

// Season is embedded in Series, never stored on its own (Sonarr: IEmbeddedDocument).
type Season struct {
	Number    int
	Monitored bool
	Title     string
}

type Series struct {
	ID              SeriesID
	IDs             ExternalIDs
	Title, SortTitle string
	CleanTitle      string // normalised for matching; normaliser from core shared/ once ≥2 apps use it (03 §3.4)
	Status          string // continuing | ended | upcoming
	Type            SeriesType
	UseSceneNumbering bool
	Monitored       bool
	MonitorNewItems bool // Sonarr NewItemMonitorTypes All/None
	SeasonFolder    bool
	Profile         acquire.ProfileID // quality profile lives on the library entity
	Tags            []acquire.TagID   // bind delay profiles, indexers, clients (raw/16 §3.3, §4.1)
	Path            library.Path      // root + relative path (05-library); never absolute
	Language        string            // BCP 47
	Seasons         []Season          // embedded
	Added           time.Time
	Runtime         time.Duration     // nominal episode runtime: fallback for acquire.Target.Runtime
}

type Episode struct {
	ID             EpisodeID
	Series         SeriesID
	Season, Number int
	Absolute       int // 0 = none; anime
	SceneSeason, SceneNumber, SceneAbsolute int // 0 = none
	Title          string
	AirDate        string    // "2006-01-02", local to the network, as Sonarr stores it
	AirDateUTC     time.Time // zero = unknown
	Runtime        time.Duration
	Monitored      bool
	File           EpisodeFileID // 0 = no file
	LastSearch     time.Time
}

// EpisodeFile: one file may carry several episodes (multi-episode files).
type EpisodeFile struct {
	ID           EpisodeFileID
	Series       SeriesID
	Season       int
	Episodes     []EpisodeID
	Path         library.Path
	Size         int64
	Added        time.Time
	SceneName    string
	ReleaseGroup string
	Facts        acquire.Facts // parsed release attributes, persisted at import (COR-5)
	Grade        acquire.Grade // quality + revision; FormatScore is NOT stored: recomputed from Facts by Engine.Grade (raw/16 §3.3)
	Languages    []string
	MediaInfo    MediaInfo     // module type (codec/resolution/duration probe); promoted to core/shared once both apps use it (03 §3.4)
}
```

**Keyspace** (03 §3.6 `m/<moduleID>/`; mechanics in [05-storage](05-storage.md)):

| Key | Value | Written with |
|---|---|---|
| `m/tv/series/<SeriesID:be64>` | `Series` (Seasons embedded) | — |
| `m/tv/episode/<EpisodeID:be64>` | `Episode` | — |
| `m/tv/file/<EpisodeFileID:be64>` | `EpisodeFile` | — |
| `m/tv/ix/series-ep/<SeriesID>/<season:be32>/<number:be32>` | `EpisodeID` | same batch as the episode |
| `m/tv/ix/ext/tvdb/<id>`, `…/tmdb/<id>`, `…/tvmaze/<id>`, `…/imdb/<id>` | `SeriesID` | same batch as the series |
| `m/tv/ix/air/<AirDateUTC:be64-unix>/<EpisodeID>` | empty | same batch; drives calendar and "recently aired" search |
| `m/tv/ix/wanted/<AirDateUTC>/<EpisodeID>` | empty | same batch; maintained when monitored ∧ no file (no time condition); "aired" is evaluated at **read time** as the key range up to now (COR-6) |

The two `ix/` time indexes are why calendar and Wanted need no core feature (§10.4 G9). Neither index stores a
predicate that depends on the clock or on hot-reloadable config.

### 10.1.2 Core capabilities the `tv` module consumes

| Capability | Core package (spec) | How `tv` uses it |
|---|---|---|
| Persisted core state | `auth`, `rbac`, `tasks`, `integrations`, `providerhttp` (and `ai`, unused) | No module of the TV app sets `Manifest.LegacyStores` (06 §6.4.2), and the app imports nothing from core's `legacy` package, which holds every legacy-only identifier outside the API promise (03 §3.3, R6-3): it has no pre-core data, so every core service of 03 §3.3's "Store interfaces" table uses its **native store** under `c/<subsystem>/`, alone — for an exclusive store because no legacy one replaces it, and for a routed store (instances, gates, rules; 05-metadata M1.1, SIMP5-1) because no legacy store covers any record, so the native store serves every one and no legacy capability (`legacy.Covering`, 05-storage §5S.3.11) is ever consulted: users, keys and identities ([05-authn](05-authn.md)), roles ([05-rbac](05-rbac.md)), runs, queues, clocks and stamps ([05-tasks](05-tasks.md)), integration instances (download clients, indexers, notification channels: `NativeInstanceStore`, [05-metadata](05-metadata.md) M1.4), provider holds and daily quotas (`NativeGateStore`, M2.4), notification rules (`NativeRuleStore`, [05-notify](05-notify.md) §3) and AI batch jobs (`NativeJobStore`, [05-ai](05-ai.md) §4; TV/Movies consume no AI). No legacy key name, row format or storage grant exists in this app, so every instance has the full native design: `Local`, origin binding of operator secrets, encrypted `secrets` records (R4-1, SIMP4-3, A-2). |
| Storage keyspace | `storage` ([05-storage](05-storage.md)) | `Deps.Storage` hands the module `Keyspace("m/tv/")`; writes outside it are refused (R8). Scans are `codec.Typed[Series].All` (`iter.Seq2[Series, error]`). Integer IDs from `Keyspace.Sequence("series").Next` (§10.4 G10). |
| Tasks | `tasks` ([05-tasks](05-tasks.md)) | Module task kinds: `tv.metadata.refresh` (every 12 h like Sonarr, `raw/16 §1.1` notes, on `tasks.ScheduleFromConfig("modules.tv.metadata.schedules.refresh")`, default `{enabled: true, every: 12h}`, §10.5.7; `tasks.Fixed(4, "metadata provider rate limits")`), `tv.search.missing`, `tv.search.series`, `tv.search.episodes`, `tv.disk.rescan` (`tasks.CPU()`; per-series partition), `tv.recent.search` (aired in last 14 days or 1 day ahead, `raw/16 §1.3`). RSS sync is a **core** `acquire` task and download polling a **core** `integrations/download` task (§10.5.4); neither is the module's. The import of a completed download **is** the module's: kind `tv.acquire.import`, built by `acquire.ImportTask` and registered by `tv` (§10.5.4, COR2-11), so its writes run under `tv`'s own `Effects`. Every `RunItems` declares parallelism (R7). Any cadence a `tv.*` or `movies.*` kind gets is a declared module schedule key of this shape (R2-7); in this proof only `tv.metadata.refresh` has one (SIMP3-9). |
| RSS sync & search | `acquire` + `integrations/indexer` | The module registers an `acquire.MediaType[tv.Target]` (§10.5.3), reaching the service as `module.Deps.Acquire` (06, COR-1): Newznab categories (TV 5000 family + anime 5070, `raw/16 §4.2`) and RSS type `TypeTV`, a release parser, a mapper release → series/episodes, its search-criteria builder (`t=tvsearch` + `tvdbid/tmdbid/imdbid`, `season`/`ep`; daily `season=<yyyy>&ep=<MM>/<dd>`; anime `q=<title>+<abs>`, `raw/16 §1.2, §4.2`), and its own specs (§10.5.3 table). |
| Library roots, path mapping, import | `library` ([05-library](05-library.md)) | Root folders = `library.Manager.Roots(filter)`; free space from `Root.Usage`; "unmapped folders" from a `RootFS.Walk`; remote-path mapping of download-client paths (`raw/16 §4.6` last bullet) = `library.PathMapper` over `library.Mapping` rows (already applied in `download.Item.LocalPath`); hardlink-or-copy for torrents, move for usenet (`raw/16 §4.7`) = `d.Library.TransferIn(tx.Batch(), …)` on the module's `library.Scope` (06) with `TransferOptions` mode `library.Auto` or `Move`. The module supplies naming (Standard/Daily/Anime episode formats, series/season folder formats, `raw/16 §1.4`). |
| Download clients | `integrations/download` ([05-download](05-download.md)) | Never called directly by the module: `acquire` grabs with a media-neutral `download.GrabRequest{Owner: "core.acquire", Refs}` and consumes download's one durable event topic `core.download.item.transitioned.v1`; it never polls (§10.4 G2, G5; COR-3). |
| Indexers | `integrations/indexer` ([05-indexer](05-indexer.md)) | Newznab/Torznab transport, caps, paging, attribute parsing are core; the module supplies only the query mapping above. |
| Metadata | `integrations/metadata` ([05-metadata](05-metadata.md)) | Two instances of the generic contract: `metadata.Provider[tv.SeriesQuery, tv.SeriesRecord]` and `metadata.Provider[tv.EpisodeListQuery, []tv.EpisodeRecord]`, adapters TMDB and TVmaze (§10.3), each behind its own `providerhttp` instance (throttle, daily quota, breaker). |
| Notify | `integrations/notify` ([05-notify](05-notify.md)) | Module event topics `tv.episode.grabbed.v1`, `tv.episode.imported.v1`, `tv.episode.upgraded.v1`, `tv.download.failed.v1`, `tv.series.added.v1`, `tv.series.deleted.v1` (03 §3.6 topic convention) are declared once in the events catalog with notify metadata and so are offered to notification rules (SIMP-5); payloads are module DTOs. The module produces each notification with `module.Deps.Notify.Publish(ctx, b, n)` in the batch of the change it reports (05-notify §1, COR2-3); the catalog metadata alone sends nothing. |
| Events / outbox | `events`, `outbox` ([05-events](05-events.md)) | "Episode imported → refresh media server" is notify-only (bus). "Import committed → mark tracked download Imported" is correctness-critical: the module's `EpisodeFile` rows, the journaled file transfer, acquire's history row and `StateImported` commit in **one** storage batch (`acquire.ImportTx`, §10.5.3; G4 of 02 §2.4; COR-4). |
| RBAC | `rbac` ([05-rbac](05-rbac.md)) | Module-declared permissions: `tv:view` (read library, calendar, queue entries owned by `tv`), `tv:request` (add a series with the **default** profile/root and a restricted set of monitor presets — the Seerr-style "request" path), `tv:search` (trigger automatic/interactive search, push a release), `tv:manage` (edit series, profiles, custom formats, delay profiles scoped to `tv`), `tv:files.delete` (delete files/series with files). Core permissions it relies on: `core:integrations.manage` (clients, indexers — shared across modules, 05-metadata M1), `core:admin`. |
| HTTP API | `httpapi` ([05-http-api](05-http-api.md)) | Native routes under `/api/tv/v1/…` (`series`, `episodes`, `files`, `calendar`, `calendar.ics`, `wanted`, `search`, `releases`), each with a permission (R6). Queue/history/blocklist are **core** routes under `/api/core/v1/acquire/…` filtered by the caller's module permissions (§10.5). |
| *arr facade | `arrfacade` ([08-arr-facade](08-arr-facade.md)) | The TV app may mount a Sonarr-v3 facade: `/api/v3/series`, `/series/lookup`, `/episode`, `/episodeFile`, `/qualityProfile`, `/rootfolder`, `/tag`, `/queue`, `/history`, `/command`, `/system/status` with the field names Seerr/Bazarr read (`raw/16 §5.2`). It is a translation layer over the native services; it owns no data. |
| Realtime | `realtime` ([05-realtime](05-realtime.md)) | Series/episode/queue change streams for the UI; the facade's SignalR feed (Bazarr, `raw/16 §5.2` last row) is an adapter over the same stream, and is optional. |
| UI | `@mediacore/shell` ([07-ui-shell](07-ui-shell.md)) | Module pages `/tv/series`, `/tv/calendar`, `/tv/wanted`, `/tv/activity`; settings panels for profiles/formats contributed by `acquire`'s shared UI components (in `@mediacore/ui-kit`). |

### 10.1.3 What stays in the `tv` module (not core)

Release-title parser (`SxxEyy`/daily/absolute/specials, `raw/16 §4.4` step 1); scene numbering
and XEM mapping (`raw/16 §1.2`); monitoring presets and `MonitorNewItems` (`raw/16 §1.3`); season-pack
rules; anime version upgrade; naming tokens; metadata refresh policy. These are the specs and
callbacks the module hands to `acquire` (§10.5.3).

## 10.2 Movies module (`movies`) — Radarr-like

### 10.2.1 Data model sketch

Radarr splits the user's decision (`Movie`) from provider data (`MovieMetadata`) so that a collection
can list movies the user has not added (`RAD Movies/Movie.cs`, `MovieMetadata.cs`,
`MovieCollection.cs`; `raw/16 §2`). The sketch keeps that split.

```go
package movies // module "movies", in the Movies app repo

import (
	"time"

	"github.com/falkcorp/mediacore/acquire"
	"github.com/falkcorp/mediacore/library"
)

type MovieID uint64
type MetadataID uint64
type MovieFileID uint64
type CollectionID uint64

// Availability is Radarr's MovieStatusType used as a threshold (raw/16 §2).
type Availability int8

const (
	AvailDeleted Availability = iota - 1
	AvailTBA
	AvailAnnounced
	AvailInCinemas
	AvailReleased
)

// Metadata is provider data; one row per TMDB id whether or not the user added the movie.
type Metadata struct {
	ID                MetadataID
	TMDB              int64
	IMDb              string
	Title, OriginalTitle string
	Year              int
	Status            Availability
	InCinemas, PhysicalRelease, DigitalRelease time.Time // zero = unknown
	Collection        int64 // TMDB collection id, 0 = none
	Runtime           time.Duration
	Genres            []string
	AlternativeTitles []string
	OriginalLanguage  string
}

type Movie struct {
	ID                  MovieID
	Metadata            MetadataID
	Monitored           bool // single flag; no season/episode tree (raw/16 §2)
	MinimumAvailability Availability
	Profile             acquire.ProfileID
	Tags                []acquire.TagID
	Path                library.Path
	File                MovieFileID // 0 = none
	Added, LastSearch   time.Time
}

type MovieFile struct {
	ID           MovieFileID
	Movie        MovieID
	Path         library.Path
	Size         int64
	Added        time.Time
	SceneName    string
	ReleaseGroup string
	Edition      string
	Facts        acquire.Facts // persisted at import (COR-5)
	Grade        acquire.Grade // FormatScore recomputed, never stored
	Languages    []string
	MediaInfo    MediaInfo     // module type, as in tv
}

type Collection struct {
	ID                  CollectionID
	TMDB                int64
	Title               string
	Monitored, SearchOnAdd bool
	Profile             acquire.ProfileID
	Root                library.RootID // root folder new members are added under
	MinimumAvailability Availability
	Tags                []acquire.TagID
	Members             []MetadataID // includes movies not added
}

// AvailableAt mirrors Radarr Movie.IsAvailable (RAD Movies/Movie.cs L77-L110, raw/16 §2) as a
// date computed from dates only: TBA/Announced → 0 (always); InCinemas → theatrical date;
// Released → earlier of physical/digital, else theatrical + 90 days. No delay, no clock.
func (m *Movie) AvailableAt(md *Metadata) time.Time

// IsAvailable evaluates at QUERY time: AvailableAt + delay <= now, where delay is the
// hot-reloadable core.acquire.availability_delay (may be negative).
func (m *Movie) IsAvailable(md *Metadata, delay time.Duration, now time.Time) bool
```

Keyspace: `m/movies/movie/<id>`, `m/movies/meta/<id>`, `m/movies/file/<id>`,
`m/movies/collection/<id>`, indexes `m/movies/ix/tmdb/<tmdb>` → MetadataID,
`m/movies/ix/meta-movie/<MetadataID>` → MovieID, `m/movies/ix/release/<date>/<MovieID>` (calendar),
`m/movies/ix/avail/<availableAt:be64>/<MovieID>` for monitored movies, maintained in the same batch as the
movie or metadata write (COR-6). The Wanted view scans the key range up to `now - delay` and checks "no file"
at read time, so a release date passing overnight or a change of `availability_delay` needs no rewrite or
rebuild.

### 10.2.2 Core capabilities the `movies` module consumes

Identical rows to §10.1.2 except where listed:

| Capability | Difference from `tv` |
|---|---|
| Tasks | `movies.metadata.refresh`, `movies.collections.refresh` (adds members as `Metadata` rows, optionally `Movie` rows when the collection is monitored), `movies.search.missing`, `movies.search.cutoff-unmet` (uses `Engine.CutoffUnmet` over each file's persisted `Facts`, COR-5), `movies.disk.rescan`. |
| RSS sync & search | `acquire.MediaType[movies.Target]`: categories Movies 2000 family and RSS type `TypeMovie` (`raw/16 §4.2`); search `t=movie&tmdbid=<id>` and/or `imdbid=<digits>`, fallback `t=search` (`raw/16 §4.2`). Module specs: `MinimumAvailability` (**Permanent** reject until `IsAvailable`, skipped for `SourceUserSearch`/`SourceInteractive` as Radarr's `AvailabilitySpecification` is; COR-6), `MonitoredMovie`, `Edition` custom-format condition (`raw/16 §3.3`, Radarr adds Edition). |
| Library/import | One file per movie; no multi-entity files. Naming: Movie Folder Format + Standard Movie Format with `{Movie Collection}` token (`raw/16 §2`). |
| Metadata | `metadata.Provider[movies.Query, movies.MetadataRecord]` and `metadata.Provider[movies.CollectionQuery, movies.CollectionRecord]`, adapter TMDB (§10.3). |
| RBAC | `movies:view`, `movies:request`, `movies:search`, `movies:manage`, `movies:files.delete`. |
| *arr facade | Radarr-v3 subset: `/movie`, `/movie/lookup`, `/qualityProfile`, `/rootfolder`, `/tag`, `/queue`, `/history`, `/command` (`MoviesSearch`, `RefreshMonitoredDownloads`), `/system/status` (`raw/16 §5.2`). Radarr's profile-level `Language` (`RAD Profiles/Qualities/QualityProfile.cs` L24, `raw/16 §3.4`) is rendered by the facade from a language custom format, not stored as a profile field (§10.5.3). |
| Notify topics | `movies.movie.grabbed.v1`, `movies.movie.imported.v1`, `movies.movie.upgraded.v1`, `movies.download.failed.v1`, `movies.movie.added.v1`, published through `Deps.Notify` as for `tv`. |
| Import kind | `movies.acquire.import` (`acquire.ImportTask`), as for `tv`. |

## 10.3 Metadata sources (D9: nothing paid)

**Provenance warning.** The provider sites (`www.tvmaze.com`, `developer.themoviedb.org`,
`thetvdb.com`) did not resolve from the authoring session (network policy), so the terms below are
from prior knowledge and are **unverified at 2026-11-04**. Re-reading each provider's current terms is
an implementation-plan precondition and is listed for [14-risks-open-questions](14-risks-open-questions.md).

| Source | Covers | Key / cost (unverified) | Use in design |
|---|---|---|---|
| TMDB API | Movies, collections, TV series/seasons/episodes, external IDs (TVDB/IMDb/TVmaze cross-refs) | Free API key for non-commercial use; attribution notice required; commercial use needs a separate licence | **Primary** for `movies`; **primary series/season data** for `tv`. The key is a user-supplied secret (`core/secrets`, N10), never shipped in the binary. |
| TVmaze public API | TV shows, episodes, air dates/times, network schedules | No key for the public read API; data licensed CC BY-SA (attribution); per-IP rate limit | **Primary episode list and air times** for `tv` (closest analogue to Sonarr's 12-hour TVDB refresh). |
| Wikidata | Cross-IDs (TMDB/TVDB/IMDb/TVmaze), titles in many languages | CC0, no key | Fallback ID resolution; alternative titles. |
| TheTVDB v4 | TV (the source Sonarr uses via its "Skyhook" proxy, `raw/16 §1.1` notes) | Licensing has paid components (subscriber or licensed keys) | **Not a dependency.** TVDB IDs are still stored (indexers search by `tvdbid`, `raw/16 §4.2`) but obtained from TMDB/TVmaze external-ID fields, so no TVDB call is made. |
| Servarr Skyhook | TVDB proxy | Operated for Sonarr itself | **Not used**: not a public service for third-party apps. |
| IMDb non-commercial datasets | IDs, ratings (bulk TSV) | Free for personal, non-commercial use | Optional offline enrichment, same pattern as ABO's Open Library dumps (`internal/openlibrary`, `raw/11-integrations.md` §1). |
| XEM (scene mapping) | Scene vs TVDB numbering | Free, community-run | Optional `tv` adapter for `UseSceneNumbering`; failure is fail-open (use provider numbering). |

Readarr's retirement was a metadata-source failure, not a pipeline failure (`raw/16 §6`). The design
consequence: every module has **at least two** independent metadata adapters behind
`metadata.Provider[Q, R]`, and the module's record type carries cross-IDs so it can re-key to a
different provider without re-adding the library.

## 10.4 Gap list: what core lacks today and how each gap closes

"Today" means at HEAD (`748fd787a`) *and* in the contracts already named by 03 §3.3. Each row names
the spec that closes it; rows closed here are specified in §10.5.

| # | Gap | Evidence | Closed by |
|---|---|---|---|
| G1 | **Polymorphic entity reference.** Sonarr's `EpisodeHistory`, `Blocklist`, `Queue` embed `EpisodeId`/`EpisodeIds`/`SeriesId` columns (`raw/16 §4.8`, §7.2). A shared history/blocklist/queue needs `(module, kind, id)`. | 02 §2.2 row 6 ("Core gets a polymorphic `EntityRef{Module, Kind, ID}`"); 02 §2.4 G8 (`Resource = {Module, Kind, ID}`). | `entity.Ref` (03 v1.2.0 §3.3, leaf package at L0; `tasks.Resource` is the same type, SIMP-16). `acquire` keys history/blocklist/queue/tracked downloads by `entity.Ref`, multi-entity grabs by `[]entity.Ref` (COR-17). |
| G2 | **Media-neutral grab request.** Sonarr/Radarr `IDownloadClient.Download` takes `RemoteEpisode`/`RemoteMovie` (`raw/16 §4.6`, §7.2). ABO has only the concrete `deluge.Client` (`internal/deluge/client.go:39`, constructor `:136`) and a vestigial `plugin.DownloadClient` with zero implementors and no add-torrent (`internal/plugin/plugin.go:51-63`). | `raw/11-integrations.md` §4 | [05-download](05-download.md) `download.GrabRequest`. Requirements from this proof: protocol (usenet/torrent), title, download URL or magnet, info-hash, category, priority, seed goals (ratio, time, season-pack time), post-import category, `Owner` (`"core.acquire"`), `Refs []entity.Ref` stored as opaque routing data, `IdempotencyKey` = the tracked-download ID. No media type. |
| G3 | **Per-item duration for size limits.** Quality definitions are MB per minute of runtime; no runtime → rejection `UnknownRuntime` (`raw/16 §3.1`, `AcceptableSizeSpecification.cs` L74/L95/L116). | `raw/16 §7.1` row 3 | `acquire.Target.Runtime()` hook (§10.5.3): total runtime of everything the release covers (sum over episodes for a pack; movie runtime). |
| G4 | **Quality vocabulary, profiles, custom formats, delay profiles, decision engine, comparer.** None exist in ABO (adversarial review attack 8: "no release-candidate model, quality/custom-format engine", `raw/08-prior-art.md` §2). | `raw/16 §3`, §4.4 | `acquire` (§10.5). |
| G5 | **Tracked-download state machine, poll loop, completed/failed handling.** ABO's Deluge integration polls by label and moves storage after organize (`internal/server/deluge_integration.go:39-51`, `raw/11-integrations.md` §4); there is no queue/history/failed model. | `raw/16 §4.7–4.8` | Split by owner (COR-3): polling, item state, grab records/recovery and completion events in [05-download](05-download.md) phase 2; the tracked-download state machine, completed/failed handling and import hand-off in `acquire` (§10.5), driven by download's events. |
| G6 | **Pending-release store** (temporary rejections retried on next RSS sync). | `raw/16 §3.3` delay profiles (`ProcessDownloadDecisions.cs` L61-L63, `RssSyncService.cs` L41-L46) | `acquire` pending store (§10.5). |
| G7 | **Tags as cross-cutting scope** (series ↔ delay profile, indexer, release profile, client). | `raw/16 §7.1` row 15 | `acquire.TagID` and tag store under `c/acquire/tag/`; modules attach tag IDs to their entities. Kept inside `acquire` because only acquisition scoping uses tags; promotion to its own package follows the 03 §3.4 rule if a second consumer appears. |
| G8 | **Provider registry + settings schema** (`Implementation`, `ConfigContract`, `Fields[]`, `/schema`) shared by indexers, clients, notifications (`raw/16 §5.1`, §7.1 row 1). | ABO builds providers in a `switch src.ID` (`raw/11-integrations.md` §3) | [05-metadata](05-metadata.md) M1: the one integration-instance store (adapter `Meta.Settings` as a `config.Schema`, instance records, `secrets.Ref`s; SIMP-4), used by [05-download](05-download.md)/[05-indexer](05-indexer.md)/[05-notify](05-notify.md). The facade renders the *arr `Fields[]` shape from that schema ([08-arr-facade](08-arr-facade.md)). |
| G9 | **Calendar** (air dates, release dates, iCal feed). | `raw/16 §7.1` row 18 (app-level); no calendar code in ABO (grep `calendar` over `internal/*.go`: test strings only) | **App-level**, by the time indexes in §10.1.1/§10.2.1. Core requirement only: an iCal client cannot send headers, so [05-authn](05-authn.md) must support a per-route opt-in **feed token** in the query string (scoped, revocable, read-only), which is also what the *arr facade's `?apikey=` needs (`raw/16 §5.1`). Both are **deferred-build members** of `auth` (SIMP2-6): the feed token lands with the first TV/Movies app, query-key extraction with `arrfacade`; the S4 build of `auth` has neither. A shared iCal writer moves to `core/shared/ical` only once both apps use it (03 §3.4). |
| G10 | **Integer IDs.** Seerr/Bazarr send integer `seriesId`/`movieIds` (`raw/16 §5.2`). | — | [05-storage](05-storage.md) §5S.3.2 `Keyspace.Sequence(name).Next(ctx)` (leased ranges; `NextIn(b)` inside a batch). Closed. |
| G11 | **Release profiles** (Required/Ignored term filters, indexer and tag scope). | `raw/16 §3.3` (`ReleaseProfile.cs` L10-L17) | `acquire.ReleaseRestriction` + generic spec (§10.5.3). Sonarr-v5-only air-date fields are a `tv` spec. |
| G12 | **Remote path mapping** (client path → local path). | `raw/16 §4.6`; ABO has only iTunes path mappings (`internal/server/handlers/itunes.go:30`) | [05-library](05-library.md) `library.PathMapper` over `library.Mapping` rows (source `download:<instance>`), applied by `download.Pool` into `Item.LocalPath`. |
| G13 | **Hardlink-or-copy import with free-space check**. | `raw/16 §4.7`, §1.4 | [05-library](05-library.md) `Manager.Transfer` / `TransferIn` (mode `Auto` or `Move`) + `Root.Usage`; `acquire`'s `FreeSpace` spec reads `Root.Usage`. |
| G14 | **"Re-search on failure" callback** (Redownload Failed). | `raw/16 §4.8` (`RedownloadFailedDownloadService.cs`) | `acquire.MediaType[T].Research` callback (§10.5.3). |
| G15 | **Activity count / queue view across modules** in one UI. | `raw/16 §4.8` last bullets | Core route `/api/core/v1/acquire/queue`, permission-filtered (§10.5). |

No gap requires a universal media row (02 §2.2 row 6 holds).

## 10.5 Capability contract: `acquire`

> **Design sketch (non-normative; A-15).** Deferred-build, not in the ABO extraction plan, and frozen by nothing.
> Proof depth (SIMP-24): the Go block names the interfaces and key types `acquire` would offer and the invariants
> that make the flows correct; exhaustive field lists come from `raw/16` when the package is built.

### 10.5.1 Purpose & scope

`acquire` is the **media-neutral acquisition engine**: given releases from indexers (RSS or search)
and an app's mapping of a release to the library entities it would satisfy, decide accept/reject with
reasons, rank, grab through `download.Pool`, follow the download through download's events, hand
completed downloads to the app's importer, and record history, blocklist and queue — all keyed by
`entity.Ref`, never by a TV or movie column.

In scope: quality vocabulary registry, quality definitions (size per unit of runtime), quality
profiles (ordered items, groups, cutoff, upgrades, format-score thresholds), custom formats
(conditions, Negate/Required, per-profile scores), delay profiles, release restrictions, tags, the
decision engine (`Spec` interface, staged evaluation, Permanent vs Temporary rejection, comparer
chain), pending releases, the tracked-download state machine, completed- and failed-download
handling, history, blocklist, queue view, and core task kinds `core.acquire.*`.

Out of scope: release-title parsing, entity mapping, search-criteria construction, import file
matching and naming, monitoring semantics (all supplied by the module through `MediaType[T]`);
indexer transport ([05-indexer](05-indexer.md)); **client polling, client-item state, grab records and
their crash recovery** ([05-download](05-download.md) phase 2 is their single owner, COR-3); client wire
protocols; file operations ([05-library](05-library.md)).

### 10.5.2 Current state at HEAD

Nothing in ABO performs acquisition. Evidence: no indexer, Newznab/Torznab or usenet code anywhere
(`raw/11-integrations.md` §4, case-insensitive grep over `internal`, `pkg`, `cmd`); qBittorrent and
SABnzbd settings were removed because "nothing ever read them" (`internal/config/removed_keys.go:30-35`);
the only client is concrete `deluge.Client` (`internal/deluge/client.go:39`) used for label discovery
and post-organize `MoveStorage` (`raw/11-integrations.md` §4); `plugin.DownloadClient` is documented
as vestigial with zero implementors (`internal/plugin/plugin.go:51-58`). The design source is
therefore the public Sonarr/Radarr model (`raw/16 §3–§4`), not ABO code.

### 10.5.3 Go API (proof depth)

Package `github.com/falkcorp/mediacore/acquire`, R4 L5. Imports: `entity`, `log`, `metrics`, `config`,
`storage`, `events`, `outbox`, `tasks`, `library`, `integrations`, `integrations/download`,
`integrations/indexer`. acquire runs module code (the `Importer`) only inside a **module** task kind
(`ImportTask`): no core run invokes a module callback that writes module entities (COR2-11).

```go
package acquire

// ---------- Vocabulary, facts, grades ----------

type QualityID int32 // stable on disk; Sonarr's numbers when facade compatibility is wanted (raw/16 §3.1)

type Quality struct {
	ID         QualityID
	Name       string // "Bluray-1080p"
	Source     string // vocabulary-defined ("bluray", "webdl"; "m4b" for audio)
	Resolution int    // 0 when not applicable
}

// Vocabulary is supplied by the app (video apps share one, owned by core/shared/videoquality
// once two modules use it — 03 §3.4). acquire never interprets Source/Resolution itself.
type Vocabulary struct {
	Name      string // "video"
	Qualities []Quality
}

// RegisterVocabulary is idempotent for equal content; a different vocabulary under a name
// already registered fails boot (COR-25). Called from Register before acquire.Register[T].
func RegisterVocabulary(svc *Service, v Vocabulary) error

type ProfileID uint64
type FormatID uint64
type TagID uint32

type Revision struct {
	Version  int // 1 original, 2 proper/repack, ...
	Real     int
	IsRepack bool
}

// Facts are the parsed release attributes custom-format conditions see. The module persists
// them on its file row at import (from the grabbed release, else parsed from the on-disk name),
// so a held file can always be re-scored (COR-5).
type Facts struct {
	Title        string // release title or file name
	Quality      QualityID
	Revision     Revision
	Languages    []string
	ReleaseGroup string
	Size         int64
	IndexerFlags uint32
	Extra        map[string]string // module facts: "tv.release_type", "movies.edition"
}

// Grade is what a held file, a queued download or a candidate has. FormatScore is NEVER
// persisted: it is recomputed from Facts against the current snapshot (Engine.Grade).
type Grade struct {
	Quality     QualityID
	Revision    Revision
	FormatScore int
}

// Propers is core.acquire.prefer_propers (three values, not a bool: COR-5).
type Propers uint8

const (
	PreferPropers Propers = iota // a higher revision of the same quality is an upgrade
	DoNotPrefer                  // revision ignored when ranking; never an upgrade by itself
	DoNotUpgrade                 // revision ranks candidates, but never triggers a grab over a held file
)

// ---------- Targets and candidates ----------

// Target is the module's view of the library entities one release would fill.
type Target interface {
	Refs() []entity.Ref // every entity covered (a season pack: many)
	Profile() ProfileID
	Tags() []TagID
	Runtime() (d time.Duration, ok bool) // gap G3; ok=false → reject core.size.unknown_runtime
	HeldFacts() []Facts                  // facts of every file the release would replace; empty = nothing held
	Monitored() bool
}

type Source uint8

const (
	SourceRSS Source = iota
	SourceSearch      // automatic search
	SourceUserSearch  // user-invoked automatic search
	SourceInteractive // rejected results shown; delay ignored
	SourcePush        // release pushed via API
)

type Candidate[T Target] struct {
	Release indexer.Release
	Facts   Facts
	Target  T
	Source  Source
	Grade   Grade // FormatScore filled by the engine
	Search  any   // module search criteria; nil for RSS
}

// ---------- Decision engine ----------

type Stage uint8 // StageDefault (pure) < StageDatabase (blocklist/history/queue) < StageDisk

type Reason string // namespaced: "core.size.below_minimum", "tv.season_pack.not_all_aired"

type Result struct {
	Reason    Reason // "" = accept
	Message   string
	Temporary bool   // park as pending, retry next RSS sync
}

type Spec[T Target] interface {
	Name() string
	Stage() Stage
	Evaluate(ctx context.Context, c *Candidate[T]) Result
}

type Decision[T Target] struct {
	Candidate  *Candidate[T]
	Rejections []Result // empty = approved
}

type Comparator[T Target] func(a, b *Decision[T]) int

// Condition is one custom-format condition kind ("core.release_title", "movies.edition").
type Condition interface {
	Kind() string
	Match(f *Facts) bool
}
type ConditionFactory func(params jsontext.Value) (Condition, error)

// Engine is per module; its profile/format/delay/restriction snapshot is swapped atomically.
type Engine[T Target] struct{ /* unexported */ }

// Decide evaluates cands with tasks.ForEach(ctx, par, ...), so it joins the
// calling run's CPU budget and pause gate (COR-23, COR2-16). Approved best-first, then rejected.
func (e *Engine[T]) Decide(ctx context.Context, par tasks.Parallelism, cands []*Candidate[T]) iter.Seq[*Decision[T]]
func (e *Engine[T]) Grade(ctx context.Context, p ProfileID, f *Facts) (Grade, error)          // current snapshot (COR-5)
func (e *Engine[T]) CutoffUnmet(ctx context.Context, t T) (bool, error)                        // worst held grade vs cutoff
// Grab takes the per-entity grab lock through l (the calling run's ref-lock
// table), re-checks, and grabs (§10.5.4 step 5, COR-7, COR2-16).
func (e *Engine[T]) Grab(ctx context.Context, l RefLocker, d *Decision[T]) (*TrackedDownload, error)

// RefLocker is satisfied by *tasks.RC and *tasks.ItemRC: the executor's one
// ref-lock table (05-tasks §5T.3.4), acquired in sorted tasks.FormatRef order.
// Every grab runs inside a run (rss-sync, a module search task), so the grab
// lock and module ExclusiveRefs/Subjects interlock.
type RefLocker interface {
	LockRefs(refs ...entity.Ref) (release func(), err error)
}

// ---------- Module registration ----------

type MediaType[T Target] struct {
	Module      string
	Vocabulary  string
	Categories  []int              // Newznab categories routed to this module (exclusive)
	RSSType     indexer.SearchType // this module's RSS feed type (COR-25)
	Parse       func(title string) (Facts, bool)
	Map         func(ctx context.Context, r indexer.Release, f Facts) ([]T, error)
	Specs       []Spec[T]
	Comparators []Comparator[T]
	Conditions  map[string]ConditionFactory
	Importer    Importer[T]
	Research    func(ctx context.Context, refs []entity.Ref) error // G14
	Search      SearchBuilder[T]
	// DefaultProfiles are this module's seed profiles, written once by acquire
	// at its start (§10.5.4 step 3; COR5-8), never by a module migration.
	DefaultProfiles []QualityProfile
}

type SearchBuilder[T Target] interface {
	Queries(ctx context.Context, req any, caps indexer.Caps) iter.Seq[indexer.Query]
}

func Register[T Target](svc *Service, mt MediaType[T]) (*Engine[T], error)

// ---------- Import (COR-4) ----------

type Importer[T Target] interface {
	// Import writes the module's rows into tx.Batch() (after Bind-ing its own keyspace),
	// moves files with d.Library.TransferIn(tx.Batch(), ...) on the module's library.Scope
	// (06), and calls tx.Imported(refs...). For a multi-file pack it fans out with
	// tasks.ForEach(ctx, tasks.Fixed(2, "disk-bound moves/hardlinks"), ...).
	// It must be idempotent by (td.ID, destination digest).
	Import(ctx context.Context, td *TrackedDownload, files iter.Seq[library.Entry], tx *ImportTx) (ImportResult, error)
}

// ImportTask returns the module kind "<module>.acquire.import" (COR2-11). Params
// carry the TD ID; Subjects returns TD.Refs, so the dispatch conflict rule
// serialises an import against any module run writing an overlapping entity
// (tv.disk.rescan, another import of a pack that shares an episode). effects
// names the module's own resources. The run moves the TD ImportPending ->
// Importing, calls the module Importer, and commits once through rc.Writer().
// The module registers it with r.Tasks(...); Register fails boot for a
// MediaType whose module registered none.
func ImportTask[T Target](e *Engine[T], effects tasks.Effects) tasks.Definition

// ImportParams is the JSON body of an import run's StartRequest.Params.
type ImportParams struct {
	TD string // TrackedDownload.ID
}

// ImportTx carries ONE batch: the import run's fenced write (rc.Writer().Do),
// with c/acquire/ Bound to it by acquire as a core participant. acquire adds the
// history rows and StateImported to it and commits once; the module never commits it.
type ImportTx struct{ /* unexported */ }

func (tx *ImportTx) Batch() *storage.Batch
func (tx *ImportTx) Imported(refs ...entity.Ref)

type ImportResult struct {
	Imported []entity.Ref
	Blocked  []ImportProblem // ImportBlocked: the user must act
	Pending  bool            // retry later
}

type ImportProblem struct {
	Ref     entity.Ref
	Reason  Reason
	Message string
}

// ---------- Tracked downloads, history, queue ----------

type State uint8 // Grabbing, Downloading, ImportBlocked, ImportPending, Importing, Imported, FailedPending, Failed, Ignored

type TrackedDownload struct {
	ID         string           // ULID; GrabRequest.IdempotencyKey
	Module     string
	Refs       []entity.Ref
	Grab       download.GrabID
	Item       download.ItemRef // zero until the grab_added event
	Release    ReleaseSnapshot  // title, indexer (integrations.Ref), protocol, size, published, Facts, Grade
	State      State
	Messages   []string
	OutputPath library.Path     // download.Item.LocalPath at completion
	Attempts   int              // import attempts
	Updated    time.Time
}

// Service is per App, built by the kernel (§10.5.4), reached as module.Deps.Acquire.
type Service struct{ /* unexported */ }

func (s *Service) History(ctx context.Context, q HistoryQuery) iter.Seq2[HistoryEvent, error]
func (s *Service) Blocklist(ctx context.Context, refs ...entity.Ref) iter.Seq2[BlocklistEntry, error]
func (s *Service) Queue(ctx context.Context, f QueueFilter) iter.Seq2[QueueItem, error] // tracked downloads joined with download.Pool.Tracked
func (s *Service) RemoveFromQueue(ctx context.Context, id string, o RemoveOptions) error
```

Types named but not spelled out here (field lists follow `raw/16` §3–§4.8 when built): `QualityDefinition`
(size bounds per runtime), `QualityProfile` (module-scoped items/groups, cutoff, `UpgradeAllowed`,
`MinFormatScore`, `CutoffFormatScore`, `MinUpgradeFormatScore`, format scores), `CustomFormat` and
`ConditionSpec`, `DelayProfile` (byte-identical in Sonarr and Radarr, raw/16 §7.2), `ReleaseRestriction`
(indexer scope as `[]integrations.Ref`), `HistoryEvent` (one `entity.Ref`, event type, grade, download ID),
`BlocklistEntry` (`[]entity.Ref`, info-hash, indexer), `QueueItem`, `ReleaseSnapshot`, `HistoryQuery`,
`QueueFilter`, `RemoveOptions`.

**Invariants the API must keep:**

1. **Held files are re-scored, never trusted.** `UpgradeDisk` and `CutoffUnmet` compute the held grade as
   `Engine.Grade` over each `Target.HeldFacts()` entry with the current snapshot, taking the worst across a
   multi-entity target, so a custom-format edit applies at once and a release whose score equals the file's
   is never an upgrade (no upgrade loop, COR-5).
2. **Upgrade order:** quality rank, then revision under `Propers`, then format score with
   `MinUpgradeFormatScore`; never an upgrade when `!UpgradeAllowed` and something is held.
3. **Custom-format matching** (raw/16 §3.3, `CustomFormatCalculationService.cs` L153-L165): conditions grouped
   by kind; every group must match; within a group all `Required` and, if any non-required exist, at least one
   of them; `Negate` inverts one condition. Regex parameters are compiled once per snapshot; a bad pattern is
   rejected at compile time, never at match time.
4. **Stages:** evaluation stops after the first stage that produced a rejection; a panicking spec rejects
   `core.spec_failed` (fail-closed for that candidate).
5. **Namespacing:** profiles, formats, delay profiles and restrictions are module-scoped; condition kinds are
   `core.*` or `<module>.*`; a Newznab category is routed to exactly one module.
6. **Comparer order** (`DownloadDecisionComparer.cs` L28-L40): quality, format score, protocol per delay profile,
   module comparators, indexer priority, peers (torrent), age (usenet), size closest to preferred, then GUID as
   a total tie-break.

Generic specs `acquire` ships (raw/16 §7.1 row 8): Default stage — `AcceptableSize` (per-runtime bounds),
`MaximumSize`, `MinimumAge` (T), `Retention`, `ProtocolAllowed`, `QualityAllowed`, `MinimumFormatScore`,
`UpgradeAllowed`, `UpgradeDisk`, `Restrictions`, `IndexerTags` (RSS), `Monitored` (RSS); Database stage —
`Blocklisted`, `AlreadyImported`, `QueueHasBetter`, `HistoryHasBetter` (RSS), `Delay` (T; RSS/automatic
only); Disk stage — `FreeSpace` (T when the check itself errors). "T" marks Temporary rejections; all others
are Permanent.

**Module-side specs** the proof needs and that therefore must be expressible as `Spec[T]` (they are):

| Module | Spec | Stage/Type | Source |
|---|---|---|---|
| tv | `SeasonPackRules` (FullSeason / SeasonPackOnly) | Default/P | `raw/16 §4.4` table |
| tv | `AnimeVersionUpgrade`, `SplitEpisode`, `NotSample`, `RawDisk` | Default/P | same |
| tv | `SearchMatch` (series, season, episode requested) | Default/P, search only | `raw/16 §4.3` |
| tv | `DeletedEpisodeFile` | Disk/T | `raw/16 §4.4` |
| tv | `AirDateRestriction` (Sonarr v5 release-profile fields) | Default/P | `raw/16 §3.3` |
| movies | `MinimumAvailability` (`Movie.IsAvailable` at query time) | Default/**P**; skipped for `SourceUserSearch`/`SourceInteractive` (Radarr `AvailabilitySpecification`; COR-6) | `raw/16 §2` |
| movies | `SearchMatch` (tmdb/imdb id equals) | Default/P, search only | `raw/16 §4.2` |
| both | `Monitored` is generic (`Target.Monitored()`); the *meaning* (series ∧ season ∧ episode vs movie flag) lives in the module's `Target` | — | `raw/16 §1.3`, §2 |

`MinimumAvailability` is Permanent, not Temporary: a Temporary reject would park every RSS release of every
unavailable monitored movie in the pending store on every sync until `core.acquire.pending.max_age`.

### 10.5.4 Lifecycle

1. **Construction (COR-1).** A module that uses acquisition declares `Acquire *AcquireDecl{Vocabulary,
   Categories}` in its manifest (06). In P5 the kernel builds one `acquire.Service` per `App` when any enabled
   manifest declares it (otherwise the service, its routes and its tasks do not exist — G1 of 02 §2.4), with
   `storage.Keyspace("c/acquire/")`, `download.Pool`, `indexer.Pool`, `library`, `tasks`, `outbox`, `events`, a
   child logger and the per-App metrics registry. Modules reach it as `module.Deps.Acquire`.
2. **Registration (P6).** `Module.Register` calls `acquire.RegisterVocabulary`, then `acquire.Register[T]`, which
   only attaches the typed hooks, and registers its import kind with `r.Tasks(acquire.ImportTask(engine,
   tasks.Writes(...)))`. Boot fails (fail-closed) on a duplicate module, a missing import kind, a condition-kind collision, a
   category claimed by two modules (an overlapping claim would double-grab), an unknown vocabulary, a
   vocabulary registered twice with different content, or a `MediaType` that disagrees with its manifest
   `AcquireDecl`.
3. **Keyspace** `c/acquire/`: `profile/<module>/<id>`, `format/<module>/<id>`, `delay/<module>/<id>`,
   `restriction/<module>/<id>`, `tag/<id>`, `pending/<module>/<guid>`, `tracked/<id>`,
   `ix/tracked-by-grab/<grabID>`, `ix/tracked-by-ref/<module>/<kind>/<id>/<tdID>` (live tracked downloads per
   entity, COR-7), `history/<ulid>`, `ix/history-by-ref/<module>/<kind>/<id>/<ulid>`, `blocklist/<ulid>`,
   `ix/blocklist-by-ref/<module>/<kind>/<id>/<ulid>`, `ix/blocklist-by-hash/<hash>`,
   `ix/import-retry/<due:be64-unix>/<tdID>` (ImportPending TDs awaiting a retry), `seeded/<module>`. **Seed data
   (COR5-8).** Default profiles per module are seed data the module declares (`MediaType.DefaultProfiles`) and
   acquire writes, not a migration: a module's migration may write only its own keyspace (R8), never
   `c/acquire/`, and the fresh-install stamp records every non-seed versioned step as applied without running it
   (05-storage §5S.4 step 6), which on a TV or Movies app — always a fresh install — would leave no profiles. When
   acquire starts (06 §6.5.2 start order: after registration, before listeners), it writes, in one batch per module,
   the declared defaults and the `seeded/<module>` marker for every registered module that has defaults and no
   marker; a module with a marker is never seeded again, so an admin's deletion of a default profile is kept. A
   fresh install and a module enabled later both get their defaults. A module's own seed data in `m/<module>/` is
   a seed step of its migrations (`storage.Migration{Seed: true}`, `Additive`, 05-storage §5S.3.5), which the
   fresh-install stamp leaves pending so that it runs in P4b.
4. **Tasks** (03 §3.6 kinds; every schedule is a declared key registered through `tasks.ScheduleFromConfig`,
   R2-7):

   | Kind | Schedule | Parallelism | What |
   |---|---|---|---|
   | `core.acquire.rss-sync` | `tasks.ScheduleFromConfig("core.acquire.schedules.rss_sync")` | fetch: `tasks.Fixed(4, "indexer rate limits; providerhttp throttles per indexer")`; decide: `tasks.CPU()` passed to `Decide` | `indexer.Pool.RSS` with one `Feed` per registered media type (its `RSSType`, `Categories`, own cursor; COR-25), route by category, append that module's pending releases, `Decide`, `Engine.Grab(ctx, rc, …)` approved decisions in comparer order, park temporary rejections. Pages back to the last processed release after downtime (`raw/16 §4.3`). |
   | `<module>.acquire.import` (a **module** kind, `ImportTask`) | none: staged by acquire's consumer in the transition batch (below), or by `core.acquire.import-retry` | one run per TD; `Subjects` = `TD.Refs`, so overlapping imports serialise and disjoint ones run in parallel (dispatch conflict rule); file transfers inside a pack: `tasks.Fixed(2, "disk-bound moves/hardlinks")` | call the module `Importer` with an `ImportTx`, commit, then `download.Pool.MarkImported`. |
   | `core.acquire.import-retry` | `tasks.ScheduleFromConfig("core.acquire.schedules.import_retry")` | `tasks.Sequential("one key-range scan of ix/import-retry/; stages runs only")` | stage the module import run of every TD in `ImportPending` whose backoff has passed (through `tasks.Stager.StartIn`, the stage-only handle the kernel gives the service in P5; `ModeLive`; the kernel stamps the owning module's system actor; COR3-8); it never calls module code. |
   | `core.acquire.housekeeping` | `tasks.ScheduleFromConfig("core.acquire.schedules.housekeeping")` | `tasks.Sequential("single pass over small keyspace; prunes pending/history by retention")` | prune expired pending releases and history/blocklist past retention. |

   There is **no** acquire poller (COR-3). acquire has **one** outbox consumer of download's events,
   `core.acquire.download-events` (COR2-10): `ConsumeTx` over the **one** durable topic `core.download.item.transitioned.v1`
   with the filter `outbox.Where(Owner == "core.acquire")`; messages of other owners are acked without effect.
   Every event of a grab carries the message key `GrabID`, so this consumer reads a download's whole lifecycle
   through one cursor in staging order, and download guarantees that `grab_added` is staged before any item event
   of that grab (05-download §4.3; COR3-4), also when both are staged in one batch (`Stage`'s monotonic
   per-`Outbox` IDs, 05-events §5E.4, §5E.5.2). As defence in depth, a `completed` that reaches a TD still in `Grabbing`
   applies `Grabbing → Downloading → ImportPending` (or `→ ImportBlocked` when `LocalPath` is unmapped) in **one**
   transition, recording the `GrabID`/`ItemRef` that `grab_added` would have recorded, so a reorder can never
   leave the TD `Downloading` with no import queued (COR4-14); the late `grab_added` is then a stale transition,
   acked and counted. It applies the tracked-download transition in the relay batch and,
   when the transition is `Downloading → ImportPending` (with `OutputPath` set), stages the module's import run
   **in that same batch** (`tx.StartRun(kind, tasks.StartRequest{Mode: tasks.ModeLive, Params: mustJSON(ImportParams{TD: td.ID})})`,
   which the relay backs with its own `Executor.StartIn`; COR3-13). An import therefore never starts before its
   transition has committed, and a completion cannot be consumed without its import being queued. A transition
   the table below does not allow from the TD's current state (a replay, a user action or import meanwhile) is
   **acked**, logged and counted in `mediacore_acquire_transition_refused_total`; the handler never returns an
   error for it, so it cannot head-of-line-block the partition or dead-letter a later valid event (COR3-4). The
   consumer is in the kernel's static core-consumer list (05-events), so retention keeps each message until it
   has acked. Module search tasks (`tv.search.*`, `movies.search.*`) call `Engine.Decide` with
   `SourceSearch`/`SourceUserSearch` and their run's parallelism, and `Engine.Grab` with their `rc`.
5. **Grab** (`Engine.Grab`, COR-3 + COR-7): take the per-entity grab lock on the decision's refs through the
   caller's `RefLocker` (§10.5.6) →
   re-run the StageDatabase specs against `ix/tracked-by-ref`, history and blocklist under the lock (a better or
   equal live download rejects `core.already_grabbed`) → allocate `TD.ID` → `download.Pool.Grab(GrabRequest{
   Owner: "core.acquire", Refs, IdempotencyKey: TD.ID, …}, sel, rec)` where `rec` binds `c/acquire/` and writes
   the TD (`StateGrabbing`, `GrabID`) and its index rows into **the batch that persists download's pending grab
   record** → release the lock → `Pool.Refresh`. A TD therefore exists iff download's grab record exists;
   acquire keeps no intent row and does no client-side recovery of its own.
6. **Config change.** Profiles/formats/delay profiles are edited through core routes; each write rebuilds the
   module's snapshot and swaps it with `atomic.Pointer` — in-flight `Decide` calls keep the old snapshot.
7. **Shutdown.** RSS sync observes `ctx`; a grab either committed its grab-record batch (download recovers the
   client side) or wrote nothing; an import is an idempotent module run, resumed per its declared `Resume`
   policy or re-staged by `core.acquire.import-retry`.

Tracked-download transitions (complete table; `→` legal; the event names are the `Kind`s of download's
`core.download.item.transitioned.v1`):

| From \ To | Downloading | ImportBlocked | ImportPending | Importing | Imported | FailedPending | Failed | Ignored |
|---|---|---|---|---|---|---|---|---|
| Grabbing | `grab_added` | `completed`, `LocalPath` unmapped (via `Downloading`, one transition; COR4-14) | `completed`, mapped (via `Downloading`, one transition; COR4-14) | — | — | — | `grab_failed` (release re-parked as pending `core.client_unavailable` when the class is retryable; blocklisted when `ClassInvalid`) | user |
| Downloading | — | `completed`, `LocalPath` unmapped | `completed`, mapped | — | — | `failed` | — | `removed`, user |
| ImportBlocked | — | — | user resolved / rescan | — | — | — | — | user |
| ImportPending | — | — | — | import run started (the run's first fenced write) | — | — | — | user |
| Importing | — | importer Blocked | importer Pending | — | importer done (same batch as the module's rows) | — | — | — |
| FailedPending | — | — | — | — | — | — | blocklisted (+research) | user |
| Imported, Failed, Ignored | terminal (removed from queue view; history retained) | | | | | | | |

### 10.5.5 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| One indexer fetch fails / times out | **fail-open** for the sync: other indexers continue; the indexer's `providerhttp` breaker records it | One bad indexer must not stop acquisition; Sonarr behaves the same per indexer. |
| Release title does not parse / maps to no target | reject `core.unparsed` / `core.unmapped` (not an error) | Normal on RSS feeds; shown in interactive search. |
| Spec panics | recover, reject `core.spec_failed` with the spec name, log at error, metric | **fail-closed for that candidate**: an unevaluated rule must never let a grab through. |
| Profile referenced by target missing | reject all candidates for that target `core.profile_missing`, health warning | **fail-closed**: grabbing without a profile could download anything. |
| Runtime unknown | reject `core.size.unknown_runtime` | Sonarr does the same (`raw/16 §3.1`). |
| Blocklist/history/queue read error (StageDatabase, incl. the re-check under the grab lock) | reject `core.storage_unavailable`, Temporary | **fail-closed but retryable**: a missed blocklist hit re-grabs a known-bad release. |
| Re-check under the grab lock finds a better or equal live download | reject `core.already_grabbed`; nothing written | Concurrent rss-sync and search tasks must not double-grab (COR-7). |
| Free-space check errors | Temporary reject `core.free_space_unknown` | **fail-closed**: filling the disk breaks every app on the host. |
| `Pool.Grab` fails on every instance | `grab_failed` → TD `Failed`; release re-parked as pending `core.client_unavailable` | **fail-open to pending**: the decision stands, delivery is retried. |
| Crash during a grab | handled by download's single recovery (05-download §4.3); the TD was written in the grab-record batch, so there is nothing for acquire to reconcile | One owner for intent and recovery (COR-3). |
| Client unreachable | download freezes its snapshots and emits nothing; the queue shows the instance's health | **fail-open**: never mark Failed (and blocklist) because a client was briefly down. |
| Client reports failed / encrypted / dangerous extension | `failed` event → `FailedPending` → `Failed`, blocklist, optional remove and `Research` (`raw/16 §4.8`) | Matches Failed Download Handling semantics. |
| Importer returns error or `Pending` | the import run's `ImportTx` batch is discarded; a separate fenced write records `ImportPending`, attempts++ and the next attempt time in `ix/import-retry/`; `core.acquire.import-retry` re-stages the run when it is due; after `core.acquire.import.max_attempts` → `ImportBlocked` with message | Never delete from client on import failure (data safety); retries do not depend on a redelivery the outbox will not make (05-tasks §5T.3.12). |
| Import run started for a TD that is not `ImportPending` (replay, user action meanwhile) | the run ends without effect | Idempotent by TD state; the transition table stays authoritative. |
| Importer reports Blocked | `ImportBlocked` | User action required. |
| `Research` callback errors | history event + log; no automatic retry | Avoid search storms against indexers. |
| Illegal or stale state transition from a download event | transition refused (`ErrIllegalTransition` internally), message **acked**, logged at warn and counted in `mediacore_acquire_transition_refused_total`; never returned as a handler error | State stays consistent, and one refused event must not block the partition or dead-letter later valid ones (COR3-4). |
| Illegal state transition from a user action or the import run | `ErrIllegalTransition` to the caller, nothing written | Bug or race signal; the caller re-reads. |
| History write for an import | in the **same storage batch** as the module's file rows and the journaled transfer (`ImportTx`, a multi-keyspace `storage.Batch`, 05-storage §5S.3.2) | Correctness-critical (02 §2.4 G4; COR-4). |

### 10.5.6 Concurrency contract

- `Service`, `Engine[T]` and compiled formats are goroutine-safe. Snapshots are immutable and swapped via
  `atomic.Pointer`; compiled regexes are cached per pattern.
- `Decide` evaluates candidates with `tasks.ForEach(ctx, par, …)`, `par` being the calling task's declared
  parallelism (rss-sync passes `tasks.CPU()`), so it joins that run's CPU budget and pause gate (COR-23,
  COR2-16). StageDatabase/StageDisk specs do I/O with the task context and check `ctx.Err()`.
- **Per-entity grab lock (COR-7, COR2-16).** `Engine.Grab` locks every ref of the decision in the executor's
  one ref-lock table (`rc.LockRefs`/`ic.LockRefs` through `RefLocker`, 05-tasks §5T.3.4), keyed by the ref as a
  `tasks.Resource` (= `entity.Ref`, SIMP-16) and acquired in sorted `tasks.FormatRef` order, all-or-wait, so two
  grabs never deadlock. There is no separate acquire mutex: the grab lock, module `ExclusiveRefs` and run
  `Subjects` share one table and interlock. The re-check and the TD write happen under the lock, so rss-sync and
  any number of `tv.search.*`/`movies.search.*` runs cannot grab two releases for one entity, and a season pack
  and an overlapping single episode serialize because they share a ref. Within one rss-sync run, approved
  decisions are still grouped by union-find over their refs and processed in comparer order per group, stopping
  at the first successful grab (the CLAUDE.md "partition into disjoint sets" rule); groups run in parallel under
  `tasks.Fixed(4, …)`.
- **Imports are serialised by `SubjectsFunc` over `TD.Refs`** (dispatch conflict rule, 05-tasks §5T.3.4)
  **plus `rc.LockRefs` in the importer** for any further entity it learns while running (COR2-11): two tracked
  downloads that share any ref (a pack and an episode it contains, or two downloads for one movie) import
  serially; disjoint ones in parallel, each as its own module run.
- Tracked-download transitions from download events are applied by one outbox consumer in the relay batch.
  `c/acquire/tracked/` is a `Serialized` native sub-prefix (05-storage §5S.3.2, §5S.3.4), and **every** TD write —
  that consumer, the import run, the import retry, user actions, and writes that keep the state — derives from a
  `Batch.GetForUpdate` read of the row and its index rows, so two writers never both commit from the same read and a
  message or attempt update can never revert an `Ignored` or a user's remove (R6-1, COR3-4). The loser re-reads and
  decides again.
- Cancellation: every task honours `ctx`; persistence of a terminal state after cancellation uses
  `context.WithoutCancel` with a short timeout so a cancelled task cannot leave a half-written batch.
- No goroutine outlives `App.Run`: the service's workers are started with `wg.Go` and joined on shutdown.

### 10.5.7 Config keys

Core keys (05-config conventions; `core.*`). Module-scoped values (profiles, formats, delay profiles,
restrictions) are **data** in `c/acquire/`, not config.

Protection classes per 05-config §1.3.7 (SEC2-8). Schedule keys are `tasks.ScheduleConfig` values declared by
`acquire` under its own namespace and registered through `tasks.ScheduleFromConfig` (R2-7); they are new with
`acquire`, so none has a legacy alias.

| Key | Type | Default | Hot-reload? | Protection |
|---|---|---|---|---|
| `core.acquire.schedules.rss_sync` | `tasks.ScheduleConfig`; `every` validated to `10m`–`120m` (`raw/16 §4.1`); automatic grabbing is turned off with `enabled: false` (Sonarr's interval `0`) | `{enabled: true, every: 15m}` | yes (re-read every scheduler poll) | none |
| `core.acquire.schedules.import_retry` | `tasks.ScheduleConfig` | `{enabled: true, every: 5m}` | yes | none |
| `core.acquire.schedules.housekeeping` | `tasks.ScheduleConfig` | `{enabled: true, every: 24h}` | yes | none |
| `core.acquire.usenet.retention_days` | int | `0` (unlimited) | yes | none |
| `core.acquire.usenet.minimum_age` | duration | `0` | yes | none |
| `core.acquire.maximum_size_bytes` | int64 | `0` (unlimited) | yes | none |
| `core.acquire.prefer_propers` | enum `prefer`/`do_not_prefer`/`do_not_upgrade` → `Propers` | `prefer` | yes | none |
| `core.acquire.pending.max_age` | duration | `168h` | yes | none |
| `core.acquire.failed.enabled` | bool | `true` | yes | none |
| `core.acquire.failed.redownload` | bool | `true` | yes | none |
| `core.acquire.failed.remove_from_client` | bool | `true` | yes | none |
| `core.acquire.completed.enabled` | bool | `true` | yes | none |
| `core.acquire.import.max_attempts` | int | `5` | yes | none |
| `core.acquire.history.retention` | duration | `0` (keep) | yes | none |
| `core.acquire.availability_delay` | duration | `0` (movies; may be negative, `raw/16 §2`); read at query time, never written into an index (COR-6) | yes | none |

Client polling cadence is the schedule key `core.download.schedules.poll` (05-download), not an acquire key.

Module keys (declared by the module, R2-7; SIMP3-9):

| Key | Type | Default | Hot-reload? | Protection |
|---|---|---|---|---|
| `modules.tv.metadata.schedules.refresh` | `tasks.ScheduleConfig` for `tv.metadata.refresh` (Sonarr refreshes series every 12 h and cannot disable it, `raw/16 §1.1` notes; here it can be disabled) | `{enabled: true, every: 12h}` | yes (re-read every scheduler poll) | none |

### 10.5.8 Metrics

Per-App registry (R5). Label values are bounded enums or configured names. Names use core's default namespace
`mediacore`; an app's `app.Options.MetricNamespace` replaces it (03 §3.6, A-5).

| Name | Type | Labels |
|---|---|---|
| `mediacore_acquire_releases_evaluated_total` | counter | `module`, `source`, `verdict` (approved/rejected/temporary) |
| `mediacore_acquire_rejections_total` | counter | `module`, `reason` |
| `mediacore_acquire_grabs_total` | counter | `module`, `protocol`, `instance`, `result` |
| `mediacore_acquire_pending_releases` | gauge | `module` |
| `mediacore_acquire_tracked_downloads` | gauge | `module`, `state` |
| `mediacore_acquire_imports_total` | counter | `module`, `result` (imported/pending/blocked/error) |
| `mediacore_acquire_import_duration_seconds` | histogram | `module` |
| `mediacore_acquire_rss_sync_duration_seconds` | histogram | — |
| `mediacore_acquire_decide_duration_seconds` | histogram | `module`, `source` |
| `mediacore_acquire_spec_failures_total` | counter | `module`, `spec` |
| `mediacore_acquire_transition_refused_total` | counter | `module`, `from`, `kind` (the `download.EventKind`) |

### 10.5.9 Tests (when `acquire` is built)

| Test | Asserts |
|---|---|
| `TestProfileRankCutoffGroups` | rank of grouped qualities equal; cutoff by quality and by group |
| `TestUpgradeOrderQualityRevisionScore` | table over (current, candidate, `Propers`) incl. all three `Propers` values, `MinUpgradeFormatScore`, `UpgradeAllowed=false` |
| `TestHeldFormatScoreRecomputedNoUpgradeLoop` | a held file's `Facts` re-scored per decision; an equal-score candidate is not an upgrade; a CF edit changes the held score at once (COR-5) |
| `TestCustomFormatGroupingNegateRequired` | the wiki modifier table (`raw/16 §3.3`) |
| `TestStagesStopAtFirstRejectingStage` | a Database spec is not evaluated when a Default spec rejected |
| `TestTemporaryRejectionParksAndRetries` | `testing/synctest`: delay elapses in virtual time; pending release grabbed on next sync |
| `TestDelayProfileSelectionAndBypass` | tag intersection, order, default, both bypasses; interactive ignores delay |
| `TestAcceptableSizeUsesRuntimeHook` / `TestUnknownRuntimeRejects` | per-runtime bounds incl. profile override |
| `TestDefaultComparatorOrder` | invariant 6; deterministic tie-break |
| `TestTrackedTransitionsExhaustive` | every State×State pair against the table |
| `TestDownloadEventsDriveTransitions` | each `core.download.item.transitioned.v1` kind, filtered by `outbox.Where(Owner == "core.acquire")`, applied in the relay batch by the one consumer; other owners' messages acked with no write and no run |
| `TestCompletedBeforeGrabAddedStillImports` | a cached torrent complete on its first poll: `grab_added` and `completed` share key `GrabID` and arrive in that order; the TD reaches `ImportPending` and its import is staged (COR3-4) |
| `TestCompletedOnGrabbingAppliesInOneTransition` | a `completed` delivered to a TD in `Grabbing` (forced reorder) moves it to `ImportPending` in one transition and stages the import; the late `grab_added` is acked and counted as refused (COR4-14) |
| `TestRefusedTransitionDoesNotBlockPartition` | a stale event (TD already `Ignored`) is acked and counted; the next event in the same partition is applied at once, nothing dead-letters (COR3-4) |
| `TestTrackedWritersSerialised` | `-race`: the consumer and a user `ignore` on one TD from the same state → exactly one commits (Serialized `tracked/`, `GetForUpdate`) |
| `TestTrackedStateKeepingWriteNeverRevertsIgnore` | `-race`: an import-retry attempts++/`Messages` write and a concurrent user `ignore` on one TD → exactly one commits, the TD is never back in `ImportPending` after the `ignore` commits (R6-1) |
| `TestImportNeverRunsBeforeTransition` | the import run is staged in the batch that commits `ImportPending`; under injected crashes no import run exists without that transition, and no `ImportPending` TD lacks a queued run or a retry entry (COR2-10) |
| `TestImportRunsAsModuleKind` | the import kind is `<module>.acquire.import`; an importer write outside the module's `Effects` fails with `ErrOutsideEffects`; an import and `tv.disk.rescan` over an overlapping series never run at once (COR2-11) |
| `TestGrabWritesTDInGrabRecordBatch` | TD exists iff download's grab record exists, under injected crashes (COR-3) |
| `TestGrabLockAcrossTasksNoDoubleGrab` | `-race`: rss-sync and search runs racing on overlapping pack+episode refs → exactly one grab (COR-7) |
| `TestImportSubjectsSerialiseOverlappingRefs` | a pack and an episode it covers never import in parallel; disjoint imports do |
| `TestImportTxHistoryInSameBatch` | crash between module rows and history impossible: both or neither (COR-4) |
| `TestImportIdempotentByTDAndDigest` | replayed completion imports once |
| `TestMinimumAvailabilityAtQueryTime` | availability passing overnight and a delay change need no index rewrite; user search bypasses it (COR-6) |
| `TestRSSFeedPerModule` | TV and Movies sharing an indexer each get their own RSS type and cursor (COR-25) |
| `TestVocabularyRegisterIdempotent` | equal content twice is fine; different content fails boot |
| `TestFreshInstallSeedsDefaultProfiles` | a fresh store booted with the toy media type of `coretest/acquiretest` has its `DefaultProfiles` and the `seeded/<module>` marker after boot; a second boot writes nothing; a deleted default stays deleted; a module enabled on an existing store is seeded at its first start (COR5-8) |
| `TestRefsAcrossModules` | `tv` and `movies` refs in one keyspace never collide |
| `TestQueueViewFiltersByModule` | caller without `movies:view` sees no movie items |
| `TestSpecPanicFailsClosed` / `TestDuplicateCategoryClaimFailsBoot` | |
| `FuzzConditionRegexParams` | malformed regex params rejected at compile, never at match time |
| `BenchmarkDecide1kReleases` | `for b.Loop()`; 1 000 releases × 30 specs × 20 formats |

`coretest` provides `FakeIndexer`, `FakeDownloadClient` (scriptable item states) and a toy media type
(`coretest/acquiretest`) so modules test their specs without a real indexer.

### 10.5.10 Migration from ABO

None required: ABO has no acquisition code (§10.5.2). Related moves owned by other specs: the Deluge
client becomes `integrations/download/deluge` ([05-download](05-download.md), [04-package-mapping](04-package-mapping.md));
ABO's audiobook-specific Deluge behaviour (label discovery, move-after-organize,
`internal/server/deluge_integration.go:39-51`) stays in the `books` module. ABO *may* later adopt
`acquire` for audiobook acquisition (Newznab Audiobook 3030, `raw/16 §4.2`) with an `audio` vocabulary;
that is not planned and needs no change to `acquire` (§10.6).

### 10.5.11 Open items

1. Sonarr's MB definition for size limits (10^6 vs 2^20 bytes) and whether bounds apply to total
   size or size-per-runtime after subtracting samples: read `AcceptableSizeSpecification.cs` before the
   facade exposes `/qualitydefinition`.
2. Sonarr v5 per-profile sizes vs v4 global table: the model supports both (per-profile size override);
   which one the TV UI shows is a product choice.
3. Release-profile v5 air-date fields are TV-only; confirm no movie equivalent is wanted.
4. Metadata provider terms (§10.3) must be re-verified; flagged for 14.
5. `library.MediaInfo` does not exist in 05-library: media probe results are module types (`tv.MediaInfo`,
   `movies.MediaInfo`) until both apps use one shape, then `core/shared/mediainfo` (03 §3.4).

## 10.6 Why `acquire` is media-neutral (and not a TV engine)

Evidence from the research, each tied to a design choice:

| Evidence (`raw/16`) | Consequence in `acquire` |
|---|---|
| `DelayProfile.cs` is **byte-identical** in Sonarr and Radarr (§7.2) | `DelayProfile` has no media field. |
| `QualityProfile` has no TV-specific fields; Radarr's copy differs only by `Language` (§3.2, §7.1 row 2) | One `QualityProfile`; Radarr's `Language` becomes a language custom format (the v4 Sonarr approach, §3.4), rendered back by the facade. |
| `CustomFormatCalculationService` is generic; only condition *kinds* differ (Release Type vs Edition) (§7.1 row 4) | `ConditionRegistry` with namespaced kinds; modules register `tv.release_type`, `movies.edition`. |
| Decision-engine runner is generic; spec catalogue is mixed (§4.4, §7.1 row 8) | `Spec[T]` + generic specs in core, media specs in modules. |
| `IDownloadClient` differs only by `RemoteEpisode` vs `RemoteMovie` (§4.6) | `download.GrabRequest` carries no media type; entities travel only as opaque `[]entity.Ref` routing data, and `acquire` owns their meaning. |
| Tracked-download states have no media vocabulary (§4.7) | `State` enum in core; import is a module callback. |
| History/blocklist/queue embed episode columns (§4.8, §7.2) | Keyed by `entity.Ref`; the only media knowledge is the module's payload in `Data`. |
| Quality on the file, profile on the entity — in both apps (§7.2) | `Facts` and `Grade` on module file rows (score recomputed); `Target.Profile()`. |
| Readarr reused the pipeline unchanged; `BookFile` has the same release columns (§6) | The audiobook/ebook case fits with an `audio`/`text` vocabulary and `Target.Runtime()` returning listening time (audio) or `ok=false` with size bounds disabled (ebooks). |

The test of neutrality is mechanical: `acquire` must compile and pass its tests with only
`coretest/acquiretest`'s toy media type registered; an `archtest` rule forbids `acquire` from importing
any `shared/videoquality`-style package (vocabularies are inputs, not imports).

## 10.7 Ebooks note (answers 02 §2.5 Q2)

- **Readarr is retired.** Its README opens with "Announcement: Retirement of Readarr" (metadata
  "has become unusable", Open Library transition "has stalled") and the repo is archived; the wiki FAQ
  is retitled "Readarr FAQ (Retired)" (`raw/16 §6`). There is no maintained *arr for ebooks to be
  compatible with, so an ebook app needs no facade.
- **Recommendation for Q2 (first useful ebook milestone):** formats **EPUB** (reflowable; EPUB 3 with
  EPUB 2 read support) and **PDF** (fixed layout), detected by content sniffing, not extension;
  CBZ/CBR only if comics are ever in scope. Reader integration through an **OPDS catalog feed**
  (OPDS 1.2 Atom first because most e-reader apps speak it, OPDS 2.0 JSON later) served by the ebook
  module under `/api/ebooks/v1/opds/...`, authenticated by the feed token required in G9 — so any
  OPDS-capable reader works with no per-device integration.
- **Reuse:** `library` (roots, `os.Root`-confined moves, hashing), `integrations/metadata` with
  `Provider[ebooks.Query, ebooks.Record]` (Open Library is already integrated in ABO —
  `internal/openlibrary`, `internal/metadata`, `raw/11-integrations.md` §1 — and is the free source
  Readarr was moving to), `tasks`, `rbac` (`ebooks:view`, `ebooks:manage`), and optionally `acquire`
  with Newznab `t=book` (`author=`, `title=`; categories 7000/7020, `raw/16 §6`).
- **Lesson carried forward:** Readarr died on its metadata source (§10.3). An ebook module ships with
  two metadata adapters from day one and stores ISBN/OLID/ASIN cross-IDs.
- Linking ebooks to the `books` module's `Work` stays deferred (02 §2.2 rows 7 and 12).

## Objections to 03

None. The two objections of v1.0.0 are resolved in 03 v1.2.0: `entity.Ref` is the L0 leaf package `entity`
(former O1), and R4 places `acquire` at L5 above `integrations/*` (former O2).

## Out-of-scope defects found

None in code read for this file.

## Review history

Round 1 (`docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- Cross-cutting resolution 1 / SIMP-1: `acquire`, `integrations/indexer`, the facade and download phase 2 marked
  deferred-build (not in the ABO extraction plan).
- SIMP-24: §10.5.3 trimmed to interfaces, key types and invariants; exhaustive structs and helpers removed.
- COR-3: `core.acquire.refresh-downloads`, acquire's intent row semantics and its tag/hash/title recovery
  removed; acquire grabs via `download.Pool.Grab` (`Owner`, `Refs`, `IdempotencyKey = TD.ID`, TD written in the
  grab-record batch) and consumes download's grab/item events; `MarkImported` after the import commits.
- COR-4: `Importer.Import` takes an `ImportTx` whose multi-keyspace batch carries module rows, `TransferIn`,
  history and `StateImported`.
- COR-5: `Facts` persisted on file rows; `Target.HeldFacts`, `Engine.Grade`, `Engine.CutoffUnmet`; three-valued `Propers`.
- COR-6: TV wanted index time-free; movies `ix/avail/<availableAt>` evaluated at query time; `MinimumAvailability` Permanent, skipped for user/interactive search.
- COR-7: per-entity grab lock (keyed by `tasks.Resource` = `entity.Ref`) with re-check against `ix/tracked-by-ref`; imports partitioned by union-find over `TD.Refs` (superseded in round 2: executor ref-lock table and `Subjects`, COR2-11/COR2-16).
- COR-25: one RSS feed per media type (`MediaType.RSSType`); `acquire.RegisterVocabulary`, idempotent.
- COR-27: `library.RelPath`/`Location` → `library.Path`; `PathMap` → `PathMapper`/`Mapping`; `PreferHardlink` → mode `Auto`;
  `Transfer` → `Manager.Transfer`/`TransferIn`; `indexer.ID`/`download.ClientID` → `integrations.Ref`;
  `Keyspace.NextID` → `Sequence(name).Next`; scans → `codec.Typed[T].All`; `library.MediaInfo` → module type;
  `core:downloads.manage` → `core:integrations.manage`; open item 6 closed.
- COR-1: `AcquireDecl` manifest field; service built in P5, hooks attached in P6; `module.Deps.Acquire`.
- COR-17: `[]entity.Ref` for multi-entity grabs and imports throughout.
- SIMP-4: G8 closed by the 05-metadata M1 instance store.
- SIMP-5: TV/Movies notify topics are event topics with notify metadata.
- SIMP-6: rss-sync via `tasks.ScheduleFromConfig`; `core.acquire.refresh.interval` removed (rss key superseded in
  round 2 by `core.acquire.schedules.rss_sync`, R2-7).
- SIMP-17: `tasks.Network` replaced by `tasks.Fixed(n, reason)`.
- COR-19: objections O1/O2 removed as resolved by 03 v1.2.0.

Round 2 (`docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-7 / COR2-6 / SIMP2-1 — `core.acquire.rss.interval` (duration) and `tasks.Every(24h)` replaced by declared
  schedule keys `core.acquire.schedules.{rss_sync, housekeeping}` (`tasks.ScheduleConfig`; Sonarr's `0` is
  `enabled: false`); `core.download.schedules.poll` named.
- COR2-10 — one acquire consumer `core.acquire.download-events` (`ConsumeTx`, `outbox.Where(Owner ==
  "core.acquire")`) applies the transition and stages the import run in the same batch;
  `TestImportNeverRunsBeforeTransition`. (Its five input topics superseded in round 3 by one, COR3-4.)
- COR2-11 — the import is a module kind `<module>.acquire.import` (`acquire.ImportTask`, `Subjects` = `TD.Refs`)
  registered by the module; `ImportTx` is the run's fenced write; union-find for imports replaced by
  `SubjectsFunc` plus `rc.LockRefs`; retries via the new stage-only core kind `core.acquire.import-retry` (key
  `core.acquire.schedules.import_retry`), because a failed run is not redelivered; `TestImportRunsAsModuleKind`.
- COR2-16 — the grab lock is the executor ref-lock table through `RefLocker` (`Engine.Grab(ctx, l, d)`), sorted
  by `tasks.FormatRef`; `Decide` takes `par tasks.Parallelism` and uses `tasks.ForEach`.
- COR2-19 — `Importer` doc and the library row say `d.Library.TransferIn(tx.Batch(), …)` on the module's
  `library.Scope`.
- COR2-3 — TV/Movies notifications are produced through `module.Deps.Notify.Publish`; metadata alone sends nothing.
- SIMP2-6 — G9's feed token and the facade's query key marked deferred-build members of `auth`.
- SEC2-8 — §10.5.7 gains a Protection column.

Round 3 (`docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- COR3-4 — acquire's consumer reads the one topic `core.download.event.v1` (key `GrabID`) instead of five
  independently cursored topics; refused or stale transitions are acked, logged and counted
  (`mediacore_acquire_transition_refused_total`), never errors; `c/acquire/tracked/` Serialized with
  `GetForUpdate` for every TD writer; transition table, §10.5.5 rows, §10.5.6, three tests (matches 05-events and
  05-download).
- COR3-8 — `core.acquire.import-retry` stages through `tasks.Stager.StartIn`; only the relay uses `Executor.StartIn`.
- COR3-13 — `StartRequest.Params` is `mustJSON(ImportParams{TD: td.ID})`; `ImportParams` declared.
- SIMP3-9 — `tv.metadata.refresh` gets the schedule key `modules.tv.metadata.schedules.refresh` (§10.1.2 row and a
  §10.5.7 module-key table); other `tv.*`/`movies.*` cadences follow the same rule.

Round 4 (`docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- SIMP4-3 — §10.1.2 gains a "Persisted core state" row: TV/Movies pass no `LegacyStores` and use every core
  service's native store (users, roles, runs, integration instances, holds/quotas, rules, AI jobs); R4-1.
- COR4-14 — §10.5.4: intra-batch order cites `Stage`'s monotonic IDs; a `completed` on a `Grabbing` TD applies
  `Grabbing → Downloading → ImportPending`/`ImportBlocked` in one transition (transition table, test
  `TestCompletedOnGrabbingAppliesInOneTransition`).
- COR4-15 — download's durable topic renamed `core.download.item.transitioned.v1` throughout.

Round 5 (`docs/plans/platform-core/reviews/spec-round-5-dispositions.md`):

- COR5-8 — §10.5.4 step 3: default profiles are seed data (`MediaType.DefaultProfiles`) written once per module by
  acquire at its start with a `seeded/<module>` marker, because a module migration cannot write `c/acquire/` (R8)
  and the fresh-install stamp skips non-seed steps; a module's own `m/<module>/` seed data is a
  `Migration{Seed: true}` step; `TestFreshInstallSeedsDefaultProfiles`.
- SIMP5-1 / SIMP5-4 / SIMP5-8 — §10.1.2 "Persisted core state": cites 03 §3.3's one store list; routed stores serve
  every record natively for an app without legacy stores, and no legacy capability is consulted.

Round 6 (`docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-1 — §10.5.6: every tracked-download write, state-keeping ones (`Messages`, `Attempts`, `Updated`) included,
  stages `GetForUpdate` with its index rows; `tracked/` is `CheckedWrites`; a failed write is dropped and re-read;
  `TestTrackedStateKeepingWriteNeverRevertsIgnore`.
- R6-3 — §10.1.2 "Persisted core state": the TV app imports nothing from package `legacy`; the capabilities are
  `legacy.Covering`/`legacy.ExtConfirmer`.

Advisor (`docs/plans/platform-core/reviews/spec-advisor-dispositions.md`):

- A-15 / ADV-S9 — §10.0 and §10.5: the whole file is a design sketch (non-normative) the SDK does not freeze; §10.0 states how it still shows the frozen contracts fit TV and Movies (D5); §10.5.6 tracked-download write paragraph and the §10.5.9 test trimmed of the blind-write detail; §10.5.9 heading "when `acquire` is built".
- A-2 / ADV-S1 — §10.1.2 "Persisted core state": `legacy.ExtConfirmer` and the extension family removed; native apps keep the full native design.
- A-5 / ADV-S4 — §10.5.8: names under `app.Options.MetricNamespace`.
