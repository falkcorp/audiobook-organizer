<!-- file: docs/specs/platform-core/10-tv-movies-proof.md -->
<!-- version: 1.0.0 -->
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
repos (`falkcorp/tv-*`, `falkcorp/movies-*`). Nothing in this file is built in the ABO repo. The
`acquire` package is core and is built "when the first TV/Movies app starts" (03 §3.3); until then this
section is the design of record.

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
	Root            library.RootID    // library root (05-library)
	Path            library.RelPath   // relative to Root; never absolute
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
	Root         library.RootID
	Path         library.RelPath
	Size         int64
	Added        time.Time
	SceneName    string
	ReleaseGroup string
	Grade        acquire.Grade // quality + revision; CF score is NOT stored (computed on the fly, raw/16 §3.3)
	Languages    []string
	MediaInfo    library.MediaInfo // codec/resolution/duration probe result (05-library)
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
| `m/tv/ix/wanted/<AirDateUTC>/<EpisodeID>` | empty | maintained when monitored ∧ no file ∧ aired; drives the Wanted view |

The two `ix/` time indexes are why calendar and Wanted need no core feature (§10.4 G9).

### 10.1.2 Core capabilities the `tv` module consumes

| Capability | Core package (spec) | How `tv` uses it |
|---|---|---|
| Storage keyspace | `storage` ([05-storage](05-storage.md)) | `Deps.Storage` hands the module `Keyspace("m/tv/")`; writes outside it are refused (R8). Scans are `iter.Seq2[[]byte, Series]`. Integer IDs from a keyspace sequence (§10.4 G10). |
| Tasks | `tasks` ([05-tasks](05-tasks.md)) | Module task kinds: `tv.metadata.refresh` (every 12 h like Sonarr, `raw/16 §1.1` notes; `tasks.Network(4, "metadata provider rate limits")`), `tv.search.missing`, `tv.search.series`, `tv.search.episodes`, `tv.disk.rescan` (`tasks.CPU()`; per-series partition), `tv.recent.search` (aired in last 14 days or 1 day ahead, `raw/16 §1.3`). RSS sync and download polling are **core** `acquire` tasks (§10.5.4) that call into the module. Every `RunItems` declares parallelism (R7). |
| RSS sync & search | `acquire` + `integrations/indexer` | The module registers an `acquire.MediaType[tv.Target]` (§10.5.3): Newznab categories (TV 5000 family + anime 5070, `raw/16 §4.2`), a release parser, a mapper release → series/episodes, its search-criteria builder (`t=tvsearch` + `tvdbid/tmdbid/imdbid`, `season`/`ep`; daily `season=<yyyy>&ep=<MM>/<dd>`; anime `q=<title>+<abs>`, `raw/16 §1.2, §4.2`), and its own specs (§10.5.3 table). |
| Library roots, path mapping, import | `library` ([05-library](05-library.md)) | Root folders = `library.Root`; free-space and "unmapped folders" from `library.Root` walk; remote-path mapping of download-client paths (`raw/16 §4.6` last bullet) = `library.PathMap`; hardlink-or-copy for torrents, move for usenet (`raw/16 §4.7`) = `library.Transfer` with `library.PreferHardlink`. The module supplies naming (Standard/Daily/Anime episode formats, series/season folder formats, `raw/16 §1.4`). |
| Download clients | `integrations/download` ([05-download](05-download.md)) | Never called directly by the module: `acquire` grabs with a media-neutral `download.GrabRequest` and polls `download.Item`s (§10.4 G2). |
| Indexers | `integrations/indexer` ([05-indexer](05-indexer.md)) | Newznab/Torznab transport, caps, paging, attribute parsing are core; the module supplies only the query mapping above. |
| Metadata | `integrations/metadata` ([05-metadata](05-metadata.md)) | Two instances of the generic contract: `metadata.Provider[tv.SeriesQuery, tv.SeriesRecord]` and `metadata.Provider[tv.EpisodeListQuery, []tv.EpisodeRecord]`, adapters TMDB and TVmaze (§10.3), each behind its own `providerhttp` instance (throttle, daily quota, breaker). |
| Notify | `integrations/notify` ([05-notify](05-notify.md)) | Module event topics `tv.episode.grabbed.v1`, `tv.episode.imported.v1`, `tv.episode.upgraded.v1`, `tv.download.failed.v1`, `tv.series.added.v1`, `tv.series.deleted.v1` (03 §3.6 topic convention) are offered to notification rules; payloads are module DTOs. |
| Events / outbox | `events`, `outbox` ([05-events](05-events.md)) | "Episode imported → refresh media server" is notify-only (bus). "Import committed → mark tracked download Imported" is correctness-critical and goes through `outbox` in the same batch as the `EpisodeFile` write (G4 of 02 §2.4). |
| RBAC | `rbac` ([05-rbac](05-rbac.md)) | Module-declared permissions: `tv:view` (read library, calendar, queue entries owned by `tv`), `tv:request` (add a series with the **default** profile/root and a restricted set of monitor presets — the Seerr-style "request" path), `tv:search` (trigger automatic/interactive search, push a release), `tv:manage` (edit series, profiles, custom formats, delay profiles scoped to `tv`), `tv:files.delete` (delete files/series with files). Core permissions it relies on: `core:downloads.manage` (clients, indexers — shared across modules), `core:admin`. |
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
	Root                library.RootID
	Path                library.RelPath
	File                MovieFileID // 0 = none
	Added, LastSearch   time.Time
}

type MovieFile struct {
	ID           MovieFileID
	Movie        MovieID
	Root         library.RootID
	Path         library.RelPath
	Size         int64
	Added        time.Time
	SceneName    string
	ReleaseGroup string
	Edition      string
	Grade        acquire.Grade
	Languages    []string
	MediaInfo    library.MediaInfo
}

type Collection struct {
	ID                  CollectionID
	TMDB                int64
	Title               string
	Monitored, SearchOnAdd bool
	Profile             acquire.ProfileID
	Root                library.RootID
	MinimumAvailability Availability
	Tags                []acquire.TagID
	Members             []MetadataID // includes movies not added
}

// IsAvailable mirrors Radarr Movie.IsAvailable(delay) (RAD Movies/Movie.cs L77-L110, raw/16 §2):
// TBA/Announced → always; InCinemas → theatrical date; Released → earlier of physical/digital,
// else theatrical + 90 days. delay is the global "availability delay" (may be negative).
func (m *Movie) IsAvailable(md *Metadata, delay time.Duration, now time.Time) bool
```

Keyspace: `m/movies/movie/<id>`, `m/movies/meta/<id>`, `m/movies/file/<id>`,
`m/movies/collection/<id>`, indexes `m/movies/ix/tmdb/<tmdb>` → MetadataID,
`m/movies/ix/meta-movie/<MetadataID>` → MovieID, `m/movies/ix/release/<date>/<MovieID>` (calendar),
`m/movies/ix/wanted/<MovieID>` (monitored ∧ no file ∧ available).

### 10.2.2 Core capabilities the `movies` module consumes

Identical rows to §10.1.2 except where listed:

| Capability | Difference from `tv` |
|---|---|
| Tasks | `movies.metadata.refresh`, `movies.collections.refresh` (adds members as `Metadata` rows, optionally `Movie` rows when the collection is monitored), `movies.search.missing`, `movies.search.cutoff-unmet`, `movies.disk.rescan`. |
| RSS sync & search | `acquire.MediaType[movies.Target]`: categories Movies 2000 family (`raw/16 §4.2`); search `t=movie&tmdbid=<id>` and/or `imdbid=<digits>`, fallback `t=search` (`raw/16 §4.2`). Module specs: `MinimumAvailability` (rejects until `IsAvailable`), `MonitoredMovie`, `Edition` custom-format condition (`raw/16 §3.3`, Radarr adds Edition). |
| Library/import | One file per movie; no multi-entity files. Naming: Movie Folder Format + Standard Movie Format with `{Movie Collection}` token (`raw/16 §2`). |
| Metadata | `metadata.Provider[movies.Query, movies.MetadataRecord]` and `metadata.Provider[movies.CollectionQuery, movies.CollectionRecord]`, adapter TMDB (§10.3). |
| RBAC | `movies:view`, `movies:request`, `movies:search`, `movies:manage`, `movies:files.delete`. |
| *arr facade | Radarr-v3 subset: `/movie`, `/movie/lookup`, `/qualityProfile`, `/rootfolder`, `/tag`, `/queue`, `/history`, `/command` (`MoviesSearch`, `RefreshMonitoredDownloads`), `/system/status` (`raw/16 §5.2`). Radarr's profile-level `Language` (`RAD Profiles/Qualities/QualityProfile.cs` L24, `raw/16 §3.4`) is rendered by the facade from a language custom format, not stored as a profile field (§10.5.3). |
| Notify topics | `movies.movie.grabbed.v1`, `movies.movie.imported.v1`, `movies.movie.upgraded.v1`, `movies.download.failed.v1`, `movies.movie.added.v1`. |

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
| G1 | **Polymorphic entity reference.** Sonarr's `EpisodeHistory`, `Blocklist`, `Queue` embed `EpisodeId`/`EpisodeIds`/`SeriesId` columns (`raw/16 §4.8`, §7.2). A shared history/blocklist/queue needs `(module, kind, id)`. | 02 §2.2 row 6 ("Core gets a polymorphic `EntityRef{Module, Kind, ID}`"); 02 §2.4 G8 (`Resource = {Module, Kind, ID}`). | `entity.Ref` (proposed leaf package; see Objections to 03, O1). `acquire` keys history/blocklist/queue/tracked downloads by `[]entity.Ref`. |
| G2 | **Media-neutral grab request.** Sonarr/Radarr `IDownloadClient.Download` takes `RemoteEpisode`/`RemoteMovie` (`raw/16 §4.6`, §7.2). ABO has only the concrete `deluge.Client` (`internal/deluge/client.go:39`, constructor `:136`) and a vestigial `plugin.DownloadClient` with zero implementors and no add-torrent (`internal/plugin/plugin.go:51-63`). | `raw/11-integrations.md` §4 | [05-download](05-download.md) `download.GrabRequest`. Requirements from this proof: protocol (usenet/torrent), title, download URL or magnet, info-hash, category, priority, seed goals (ratio, time, season-pack time), post-import category, owner tag (opaque string `acquire` sets to the tracked-download ID). No media type, no entity. |
| G3 | **Per-item duration for size limits.** Quality definitions are MB per minute of runtime; no runtime → rejection `UnknownRuntime` (`raw/16 §3.1`, `AcceptableSizeSpecification.cs` L74/L95/L116). | `raw/16 §7.1` row 3 | `acquire.Target.Runtime()` hook (§10.5.3): total runtime of everything the release covers (sum over episodes for a pack; movie runtime). |
| G4 | **Quality vocabulary, profiles, custom formats, delay profiles, decision engine, comparer.** None exist in ABO (adversarial review attack 8: "no release-candidate model, quality/custom-format engine", `raw/08-prior-art.md` §2). | `raw/16 §3`, §4.4 | `acquire` (§10.5). |
| G5 | **Tracked-download state machine, poll loop, completed/failed handling.** ABO's Deluge integration polls by label and moves storage after organize (`internal/server/deluge_integration.go:39-51`, `raw/11-integrations.md` §4); there is no queue/history/failed model. | `raw/16 §4.7–4.8` | `acquire.Tracker` (§10.5). |
| G6 | **Pending-release store** (temporary rejections retried on next RSS sync). | `raw/16 §3.3` delay profiles (`ProcessDownloadDecisions.cs` L61-L63, `RssSyncService.cs` L41-L46) | `acquire` pending store (§10.5). |
| G7 | **Tags as cross-cutting scope** (series ↔ delay profile, indexer, release profile, client). | `raw/16 §7.1` row 15 | `acquire.TagID` and tag store under `c/acquire/tag/`; modules attach tag IDs to their entities. Kept inside `acquire` because only acquisition scoping uses tags; promotion to its own package follows the 03 §3.4 rule if a second consumer appears. |
| G8 | **Provider registry + settings schema** (`Implementation`, `ConfigContract`, `Fields[]`, `/schema`) shared by indexers, clients, notifications (`raw/16 §5.1`, §7.1 row 1). | ABO builds providers in a `switch src.ID` (`raw/11-integrations.md` §3) | [05-config](05-config.md) typed sections + JSON schema, consumed by [05-download](05-download.md)/[05-indexer](05-indexer.md)/[05-notify](05-notify.md). The facade renders the *arr `Fields[]` shape from that schema ([08-arr-facade](08-arr-facade.md)). |
| G9 | **Calendar** (air dates, release dates, iCal feed). | `raw/16 §7.1` row 18 (app-level); no calendar code in ABO (grep `calendar` over `internal/*.go`: test strings only) | **App-level**, by the time indexes in §10.1.1/§10.2.1. Core requirement only: an iCal client cannot send headers, so [05-authn](05-authn.md) must support a per-route opt-in **feed token** in the query string (scoped, revocable, read-only), which is also what the *arr facade's `?apikey=` needs (`raw/16 §5.1`). A shared iCal writer moves to `core/shared/ical` only once both apps use it (03 §3.4). |
| G10 | **Integer IDs.** Seerr/Bazarr send integer `seriesId`/`movieIds` (`raw/16 §5.2`). | — | [05-storage](05-storage.md) must provide a durable per-keyspace sequence `Keyspace.NextID(ctx, name) (uint64, error)` allocated in batches. Requirement recorded here; the API is 05-storage's. |
| G11 | **Release profiles** (Required/Ignored term filters, indexer and tag scope). | `raw/16 §3.3` (`ReleaseProfile.cs` L10-L17) | `acquire.ReleaseRestriction` + generic spec (§10.5.3). Sonarr-v5-only air-date fields are a `tv` spec. |
| G12 | **Remote path mapping** (client path → local path). | `raw/16 §4.6`; ABO has only iTunes path mappings (`internal/server/handlers/itunes.go:30`) | [05-library](05-library.md) `library.PathMap`, configured per download client. |
| G13 | **Hardlink-or-copy import with free-space check**. | `raw/16 §4.7`, §1.4 | [05-library](05-library.md) `library.Transfer` + free-space check; `acquire`'s `FreeSpace` spec calls it. |
| G14 | **"Re-search on failure" callback** (Redownload Failed). | `raw/16 §4.8` (`RedownloadFailedDownloadService.cs`) | `acquire.MediaType[T].Research` callback (§10.5.3). |
| G15 | **Activity count / queue view across modules** in one UI. | `raw/16 §4.8` last bullets | Core route `/api/core/v1/acquire/queue`, permission-filtered (§10.5). |

No gap requires a universal media row (02 §2.2 row 6 holds).

## 10.5 Capability contract: `acquire`

### 10.5.1 Purpose & scope

`acquire` is the **media-neutral acquisition engine**: given releases from indexers (RSS or search)
and an app's mapping of a release to the library entities it would satisfy, decide accept/reject with
reasons, rank, grab through a download client, track the download to completion, hand completed
downloads to the app's importer, and record history, blocklist and queue — all keyed by
`entity.Ref`, never by a TV or movie column.

In scope: quality vocabulary registry, quality definitions (size per unit of runtime), quality
profiles (ordered items, groups, cutoff, upgrades, format-score thresholds), custom formats
(conditions, Negate/Required, per-profile scores), delay profiles, release restrictions, tags, the
decision engine (`Spec` interface, staged evaluation, Permanent vs Temporary rejection, comparer
chain), pending releases, the tracked-download state machine and poll loop, completed- and
failed-download handling, history, blocklist, queue view, and core task kinds `core.acquire.*`.

Out of scope: release-title parsing, entity mapping, search-criteria construction, import file
matching and naming, monitoring semantics (all supplied by the module through `MediaType[T]`);
indexer transport ([05-indexer](05-indexer.md)); client wire protocols ([05-download](05-download.md));
file operations ([05-library](05-library.md)).

### 10.5.2 Current state at HEAD

Nothing in ABO performs acquisition. Evidence: no indexer, Newznab/Torznab or usenet code anywhere
(`raw/11-integrations.md` §4, case-insensitive grep over `internal`, `pkg`, `cmd`); qBittorrent and
SABnzbd settings were removed because "nothing ever read them" (`internal/config/removed_keys.go:30-35`);
the only client is concrete `deluge.Client` (`internal/deluge/client.go:39`) used for label discovery
and post-organize `MoveStorage` (`raw/11-integrations.md` §4); `plugin.DownloadClient` is documented
as vestigial with zero implementors (`internal/plugin/plugin.go:51-58`). The design source is
therefore the public Sonarr/Radarr model (`raw/16 §3–§4`), not ABO code.

### 10.5.3 Go API

Package `github.com/falkcorp/mediacore/acquire`. Imports (all ≤ its layer, see O2): `entity`,
`log`, `metrics`, `config`, `storage`, `events`, `outbox`, `tasks`, `library`,
`integrations/download`, `integrations/indexer`.

```go
package acquire

import (
	"cmp"
	"context"
	"encoding/json/jsontext"
	"errors"
	"iter"
	"time"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/integrations/indexer"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/tasks"
)

// ---------- Vocabulary ----------

// QualityID identifies a quality within a Vocabulary. IDs are stable on disk and are the
// numbers the *arr facade exposes, so an app that wants facade compatibility uses Sonarr's
// table (Unknown=0, SDTV=1, DVD=2, WEBDL-1080p=3, ... Bluray-576p=22; raw/16 §3.1).
type QualityID int32

type Quality struct {
	ID         QualityID
	Name       string // "Bluray-1080p"
	Source     string // vocabulary-defined: "bluray", "webdl", "hdtv", ...; "m4b", "mp3" for audio
	Resolution int    // 0 when not applicable (audio)
}

// Vocabulary is supplied by the app. Video apps share one (core/shared/videoquality once two
// apps use it — 03 §3.4); an audiobook app would supply its own. acquire never interprets
// Source or Resolution except through CustomFormat conditions the app registers.
type Vocabulary struct {
	Name      string    // "video", "audio"
	Qualities []Quality // order is irrelevant; ranking comes from profiles
}

// SizeBounds are per unit of runtime. Zero means "no bound". Stored as bytes per second;
// the UI and the facade convert to the MB/min unit Sonarr shows (raw/16 §3.1; the exact
// MB definition Sonarr uses is an open item, §10.5.11).
type SizeBounds struct {
	MinBps, PreferredBps, MaxBps float64
}

// QualityDefinition binds a Quality to display group and default size bounds
// (Sonarr QualityDefinition: Title, GroupName, Weight, Min/Max/PreferredSize; raw/16 §3.1).
type QualityDefinition struct {
	Quality QualityID
	Title   string
	Group   string // equal Weight == same group, e.g. "WEB 720p"
	Weight  int
	Size    SizeBounds
}

// ---------- Profiles ----------

type ProfileID uint64
type FormatID uint64
type GroupID int32 // negative space reserved for groups so a cutoff can name either
type TagID uint32

// ProfileItem is either a single quality or a named group of qualities.
// Items are stored least-preferred first; the UI shows them reversed ("higher is better").
type ProfileItem struct {
	Quality QualityID   // set when Group == 0
	Group   GroupID     // set for a group
	Name    string      // group name
	Members []QualityID // group members
	Allowed bool
	Size    *SizeBounds // optional per-profile override (Sonarr v5 moves sizes into profiles; raw/16 §3.1)
}

type QualityProfile struct {
	ID                    ProfileID
	Module                string // owning module; profiles are module-scoped (Sonarr and Radarr each keep their own)
	Name                  string
	Vocabulary            string
	Items                 []ProfileItem
	Cutoff                RankKey // QualityID or GroupID at which quality upgrades stop
	UpgradeAllowed        bool
	MinFormatScore        int // reject below
	CutoffFormatScore     int // stop format-score upgrades at or above
	MinUpgradeFormatScore int // minimum improvement that counts as an upgrade
	FormatScores          map[FormatID]int
}

// RankKey names a profile item by quality or group.
type RankKey struct {
	Quality QualityID
	Group   GroupID
}

// Revision orders propers/repacks within one quality (raw/16 §4.4 "Repack/Proper count as a
// higher quality revision unless set to Do not prefer").
type Revision struct {
	Version  int  // 1 = original, 2 = proper/repack, ...
	Real     int  // "REAL" tags
	IsRepack bool
}

// Grade is what a file on disk, a queued download or a candidate release has.
type Grade struct {
	Quality     QualityID
	Revision    Revision
	FormatScore int // computed; never persisted for files (raw/16 §3.3)
}

// Rank returns the item index of q (higher = better) and whether q is allowed.
func (p *QualityProfile) Rank(q QualityID) (rank int, allowed bool)

// CutoffMet reports whether g satisfies both the quality cutoff and CutoffFormatScore.
func (p *QualityProfile) CutoffMet(g Grade) bool

// Upgrade compares a candidate against what is held (file on disk, queued, or recently
// grabbed). Order: quality rank, then revision (if propers preferred), then format score
// with MinUpgradeFormatScore; never an upgrade when !UpgradeAllowed and current exists.
func (p *QualityProfile) Upgrade(current, candidate Grade, prefs UpgradePrefs) UpgradeVerdict

type UpgradePrefs struct{ PreferPropers bool }

type UpgradeVerdict uint8

const (
	NotUpgrade UpgradeVerdict = iota
	UpgradeQuality
	UpgradeRevision
	UpgradeFormatScore
	UpgradeNotAllowed // profile forbids upgrades
	CutoffAlreadyMet
)

// ---------- Custom formats ----------

// Condition is one custom-format specification kind (Release Title regex, Language, Source,
// Resolution, Size, Release Group, Indexer Flag, Release Type, Edition, ...; raw/16 §3.3).
type Condition interface {
	Kind() string // "core.release_title", "core.size", "tv.release_type", "movies.edition"
	Match(c *Facts) bool
}

// ConditionFactory decodes a condition's stored parameters (encoding/json/v2 value).
type ConditionFactory func(params jsontext.Value) (Condition, error)

type ConditionSpec struct {
	Kind     string
	Name     string
	Negate   bool // invert this condition
	Required bool // within a same-Kind group, Required conditions AND; others OR
	Params   jsontext.Value
}

type CustomFormat struct {
	ID                  FormatID
	Module              string
	Name                string
	IncludeWhenRenaming bool
	Conditions          []ConditionSpec
}

// Facts is what conditions see: a release, or a file on disk (the Release Title condition
// matches either the release title or the on-disk filename; raw/16 §3.3).
type Facts struct {
	Title        string // release title or file name
	Quality      Quality
	Languages    []string
	ReleaseGroup string
	Size         int64
	IndexerFlags uint32
	Extra        map[string]string // module-provided facts, e.g. "tv.release_type"="season_pack", "movies.edition"="Director's Cut"
}

// Formats compiles custom formats once per snapshot. Matching semantics (raw/16 §3.3,
// CustomFormatCalculationService.cs L153-L165): group conditions by Kind; every group must
// match; within a group the format matches if all Required conditions match and, if any
// non-Required exist, at least one of them matches; Negate inverts one condition's result.
type Formats struct{ /* unexported: compiled conditions, regex cache */ }

func CompileFormats(reg *ConditionRegistry, formats []CustomFormat) (*Formats, error)

// Match yields the IDs of every format that matches f, in format-ID order.
func (fs *Formats) Match(f *Facts) iter.Seq[FormatID]

// Score sums the profile's scores for the matched formats (0-score formats are informational).
func Score(p *QualityProfile, matched iter.Seq[FormatID]) int

type ConditionRegistry struct{ /* unexported */ }

// Register adds a condition kind. Kinds are namespaced like permissions: "core.*" by acquire,
// "<moduleID>.*" by a module. Duplicate kinds are a boot error (fail-closed).
func (r *ConditionRegistry) Register(kind string, f ConditionFactory) error

// ---------- Delay profiles and release restrictions ----------

// DelayProfile is media-neutral by construction: Sonarr's and Radarr's DelayProfile.cs are
// byte-identical (raw/16 §7.2).
type DelayProfile struct {
	ID                       uint64
	Module                   string
	Order                    int     // lower wins among profiles whose tags match
	Tags                     []TagID // empty = default profile
	EnableUsenet             bool
	EnableTorrent            bool
	Preferred                download.Protocol
	UsenetDelay, TorrentDelay time.Duration // measured from the release's publish time (raw/16 §3.3)
	BypassIfHighestQuality   bool
	BypassIfAboveFormatScore bool
	MinimumFormatScore       int
}

// SelectDelay picks the first profile by Order whose Tags intersect tags, else the default.
func SelectDelay(profiles []DelayProfile, tags []TagID) DelayProfile

type ReleaseRestriction struct {
	ID       uint64
	Module   string
	Required []string // case-insensitive substrings, or /regex/
	Ignored  []string
	Indexers []indexer.ID // empty = all
	Tags     []TagID      // empty = all
}

// ---------- Targets: what a release would satisfy ----------

// Target is the module's view of the library entities one release would fill.
// tv: the series plus the episodes the release covers; movies: one movie.
type Target interface {
	Refs() []entity.Ref           // every entity covered; history/blocklist/queue key on these
	Profile() ProfileID
	Tags() []TagID
	// Runtime is the per-item duration hook (gap G3): the total runtime the release should
	// contain. ok=false makes AcceptableSize reject with ReasonUnknownRuntime.
	Runtime() (d time.Duration, ok bool)
	// Held returns the best grade already on disk for this target (worst across covered
	// entities for a multi-entity release, so a pack is an upgrade only if it upgrades all).
	Held() (g Grade, ok bool)
	Monitored() bool
}

// Source records why a release is being evaluated (Sonarr ReleaseSourceType, raw/16 §4.3).
type Source uint8

const (
	SourceRSS Source = iota
	SourceSearch           // automatic search
	SourceUserSearch       // user-invoked automatic search
	SourceInteractive      // interactive: rejected results are shown; delay ignored
	SourcePush             // release pushed via API
)

// Candidate is one release being decided for one Target.
type Candidate[T Target] struct {
	Release indexer.Release // from 05-indexer: GUID, title, protocol, size, published, link, hash, seeders, flags, indexer
	Parsed  Parsed
	Target  T
	Source  Source
	Grade   Grade     // Quality+Revision from Parsed, FormatScore filled by the engine
	Formats []FormatID
	Search  any       // the module's search criteria when Source != SourceRSS; nil for RSS
}

// Parsed is the module parser's media-neutral output; module-specific facts go in Facts.Extra.
type Parsed struct {
	Quality      QualityID
	Revision     Revision
	Languages    []string
	ReleaseGroup string
	Facts        map[string]string
}

// ---------- Decision engine ----------

// Stage orders specs; evaluation stops after the first stage that produced any rejection
// (DownloadDecisionMaker.cs L203-L212, raw/16 §4.4). Lower stages are cheaper.
type Stage uint8

const (
	StageDefault  Stage = iota // pure: parsed facts, profile, sizes
	StageDatabase              // reads storage: blocklist, history, queue
	StageDisk                  // touches the filesystem: free space, file on disk
)

type Reason string // namespaced: "core.size.below_minimum", "tv.season_pack.not_all_aired"

// Result of one spec.
type Result struct {
	Reason    Reason // empty == accept
	Message   string
	Temporary bool   // true: park as pending and retry on the next RSS sync (delay, minimum age)
}

func Accept() Result                          { return Result{} }
func Reject(r Reason, msg string) Result      { return Result{Reason: r, Message: msg} }
func Defer(r Reason, msg string) Result       { return Result{Reason: r, Message: msg, Temporary: true} }
func (r Result) Accepted() bool               { return r.Reason == "" }

// Spec is one decision rule. Implementations must be pure with respect to the candidate and
// safe for concurrent use; StageDatabase/StageDisk specs receive ctx for I/O.
type Spec[T Target] interface {
	Name() string
	Stage() Stage
	Evaluate(ctx context.Context, c *Candidate[T]) Result
}

// Decision is the outcome for one candidate.
type Decision[T Target] struct {
	Candidate  *Candidate[T]
	Rejections []Result // empty == approved
}

func (d *Decision[T]) Approved() bool  { return len(d.Rejections) == 0 }
func (d *Decision[T]) Temporary() bool // all rejections temporary → pending

// Comparator ranks two approved decisions; negative means a is preferred.
type Comparator[T Target] func(a, b *Decision[T]) int

// Engine evaluates candidates for one module's media type. Safe for concurrent use; its
// profile/format/delay snapshot is swapped atomically on configuration change.
type Engine[T Target] struct{ /* unexported */ }

// Decide evaluates every candidate (bounded CPU pool), then sorts approved ones with the
// comparer chain. The returned sequence yields approved decisions first, best first, then
// rejected ones (for interactive search) in input order.
func (e *Engine[T]) Decide(ctx context.Context, cands []*Candidate[T]) iter.Seq[*Decision[T]]

// DefaultComparators is the Sonarr/Radarr order (DownloadDecisionComparer.cs L28-L40,
// raw/16 §4.4): quality rank, format score, protocol per delay profile, [module comparators],
// indexer priority, peers (torrent), age (usenet), size closest to preferred.
func DefaultComparators[T Target](module ...Comparator[T]) []Comparator[T]

// Generic specs provided by acquire (raw/16 §7.1 row 8 "CORE runner + generic specs").
func AcceptableSize[T Target]() Spec[T]           // Default, P: per-runtime bounds; UnknownRuntime
func MaximumSize[T Target]() Spec[T]              // Default, P: global cap
func MinimumAge[T Target]() Spec[T]               // Default, T: usenet minimum age
func Retention[T Target]() Spec[T]                // Default, P: usenet retention
func ProtocolAllowed[T Target]() Spec[T]          // Default, P: delay profile disables protocol
func QualityAllowed[T Target]() Spec[T]           // Default, P
func MinimumFormatScore[T Target]() Spec[T]       // Default, P
func UpgradeAllowed[T Target]() Spec[T]           // Default, P
func UpgradeDisk[T Target]() Spec[T]              // Default, P: not better than Held()
func Restrictions[T Target]() Spec[T]             // Default, P: release restrictions
func IndexerTags[T Target]() Spec[T]              // Default, P, RSS only
func Blocklisted[T Target]() Spec[T]              // Database, P
func AlreadyImported[T Target]() Spec[T]          // Database, P: same hash/title grabbed+imported
func QueueHasBetter[T Target]() Spec[T]           // Database, P
func HistoryHasBetter[T Target]() Spec[T]         // Database, P, RSS only
func Delay[T Target]() Spec[T]                    // Database, T, RSS/automatic only; ignored for interactive
func FreeSpace[T Target]() Spec[T]                // Disk, P (temporary when the check itself errors)
func Monitored[T Target]() Spec[T]                // Default, P, RSS only: Target.Monitored()

// ---------- Module registration ----------

// MediaType is what a module hands acquire. One per module; a module may register at most one.
type MediaType[T Target] struct {
	Module      string               // "tv"
	Vocabulary  string               // "video"
	Categories  []int                // Newznab category IDs routed to this module
	Parse       func(title string) (Parsed, bool)
	// Map resolves a release to zero or more targets (a season pack maps to one target
	// covering many episodes). An unmapped release is rejected "core.unmapped".
	Map         func(ctx context.Context, r indexer.Release, p Parsed) ([]T, error)
	Specs       []Spec[T]            // module specs, appended to the generic set
	Comparators []Comparator[T]      // inserted after protocol in DefaultComparators
	Conditions  map[string]ConditionFactory
	Importer    Importer[T]
	Research    func(ctx context.Context, refs []entity.Ref) error // re-search after a failed download (G14)
	Search      SearchBuilder[T]
}

// SearchBuilder turns a module request into indexer queries (tvsearch+season/ep, movie+tmdbid).
type SearchBuilder[T Target] interface {
	Queries(ctx context.Context, req any, caps indexer.Caps) iter.Seq[indexer.Query]
}

// Importer is called for a completed download. It finds media files under the client's
// output path (already remote-path-mapped), maps them to entities, and moves/hardlinks via
// library. It must be idempotent per tracked download ID.
type Importer[T Target] interface {
	Import(ctx context.Context, td *TrackedDownload, files iter.Seq[library.Entry]) (ImportResult, error)
}

type ImportResult struct {
	Imported []entity.Ref
	Blocked  []ImportProblem // reason the user must resolve (ImportBlocked)
	Pending  bool            // retry later (e.g. waiting for episode title, raw/16 §1.4)
}

type ImportProblem struct {
	Ref     entity.Ref
	Reason  Reason
	Message string
}

// Register wires the media type into the acquisition service. Called from Module.Register.
func Register[T Target](svc *Service, mt MediaType[T]) (*Engine[T], error)

// ---------- Tracked downloads ----------

type State uint8

const (
	StateGrabbing      State = iota // intent persisted, client add not yet acknowledged (see §10.5.6)
	StateDownloading
	StateImportBlocked
	StateImportPending
	StateImporting
	StateImported
	StateFailedPending
	StateFailed
	StateIgnored
)

// CanTransition reports whether from→to is in the legal-transition table (§10.5.4); the
// Tracker refuses anything else with ErrIllegalTransition. States are Sonarr's
// (TrackedDownload.cs L49-L59, raw/16 §4.7) plus StateGrabbing, which the facade reports
// as "downloading".
func (from State) CanTransition(to State) bool

type TrackedDownload struct {
	ID         string            // ULID; also set as GrabRequest owner tag
	Module     string
	Refs       []entity.Ref
	Client     download.ClientID
	DownloadID string            // client's id (torrent info-hash, usenet nzo id)
	Release    ReleaseSnapshot   // title, indexer, protocol, size, published, grade
	State      State
	Status     Health            // ok | warning | error, with messages
	Messages   []string
	Item       download.Item     // last polled client status
	OutputPath library.Location  // after remote-path mapping
	Attempts   int               // import attempts
	Updated    time.Time
}

type Health uint8

const (
	HealthOK Health = iota
	HealthWarning
	HealthError
)

type ReleaseSnapshot struct {
	Title, Indexer string
	Protocol       download.Protocol
	Size           int64
	Published      time.Time
	Grade          Grade
	InfoHash       string
}

// ---------- History, blocklist, queue ----------

type EventType uint8

const (
	EventGrabbed EventType = iota + 1
	EventImported            // DownloadFolderImported
	EventLibraryImported     // SeriesFolderImported / existing-file import
	EventDownloadFailed
	EventFileDeleted
	EventFileRenamed
	EventDownloadIgnored
)

// HistoryEvent mirrors EpisodeHistory (raw/16 §4.8) with entity.Ref instead of episode columns.
type HistoryEvent struct {
	ID         string // ULID: time-ordered
	Ref        entity.Ref
	Type       EventType
	SourceTitle string
	Grade      Grade
	Languages  []string
	DownloadID string
	At         time.Time
	Data       map[string]string // indexer, release group, size, client (raw/16 §4.8 consts)
}

type BlocklistEntry struct {
	ID         string
	Refs       []entity.Ref
	SourceTitle string
	InfoHash   string
	Indexer    string
	Protocol   download.Protocol
	Size       int64
	Published  time.Time
	Grade      Grade
	Message    string
	At         time.Time
}

// QueueItem is a VIEW over tracked downloads (Sonarr Queue is not a table; raw/16 §4.8).
type QueueItem struct {
	Tracked     *TrackedDownload
	SizeLeft    int64
	TimeLeft    time.Duration
	EstimatedAt time.Time
}

// Service is the per-App acquisition service (constructed by the kernel, handed to modules
// via module.Deps). One per process regardless of how many modules register media types.
type Service struct{ /* unexported */ }

func (s *Service) History(ctx context.Context, q HistoryQuery) iter.Seq2[HistoryEvent, error]
func (s *Service) Blocklist(ctx context.Context, refs ...entity.Ref) iter.Seq2[BlocklistEntry, error]
func (s *Service) Queue(ctx context.Context, filter QueueFilter) iter.Seq2[QueueItem, error]
func (s *Service) Grab(ctx context.Context, module string, d any) (*TrackedDownload, error) // d is *Decision[T]; type-checked against the module's registered T
func (s *Service) RemoveFromQueue(ctx context.Context, id string, opt RemoveOptions) error

type HistoryQuery struct {
	Refs   []entity.Ref
	Types  []EventType
	Since  time.Time
	Module string // "" = every module the caller may view
}

type QueueFilter struct {
	Modules []string // already intersected with the caller's <module>:view permissions by httpapi
}

type RemoveOptions struct {
	RemoveFromClient bool
	Blocklist        bool
	Research         bool
}

// Comparator helper used by DefaultComparators; shown to make the cmp.Or chain explicit.
func chain[T Target](cs []Comparator[T]) Comparator[T] {
	return func(a, b *Decision[T]) int {
		for _, c := range cs {
			if r := c(a, b); r != 0 {
				return r
			}
		}
		return cmp.Compare(a.Candidate.Release.GUID, b.Candidate.Release.GUID) // total order: stable, deterministic
	}
}

var (
	ErrIllegalTransition = errors.New("acquire: illegal tracked-download transition")
	ErrUnknownModule     = errors.New("acquire: module has no registered media type")
	ErrNoClient          = errors.New("acquire: no enabled download client for protocol")
	ErrProfileMissing    = errors.New("acquire: quality profile not found")
)
```

**Module-side specs** the proof needs and that therefore must be expressible as `Spec[T]` (they are):

| Module | Spec | Stage/Type | Source |
|---|---|---|---|
| tv | `SeasonPackRules` (FullSeason / SeasonPackOnly) | Default/P | `raw/16 §4.4` table |
| tv | `AnimeVersionUpgrade`, `SplitEpisode`, `NotSample`, `RawDisk` | Default/P | same |
| tv | `SearchMatch` (series, season, episode requested) | Default/P, search only | `raw/16 §4.3` |
| tv | `DeletedEpisodeFile` | Disk/T | `raw/16 §4.4` |
| tv | `AirDateRestriction` (Sonarr v5 release-profile fields) | Default/P | `raw/16 §3.3` |
| movies | `MinimumAvailability` (`Movie.IsAvailable`) | Default/P (Temporary for RSS: becomes eligible later) | `raw/16 §2` |
| movies | `SearchMatch` (tmdb/imdb id equals) | Default/P, search only | `raw/16 §4.2` |
| both | `Monitored` is generic (`Target.Monitored()`); the *meaning* (series ∧ season ∧ episode vs movie flag) lives in the module's `Target` | — | `raw/16 §1.3`, §2 |

### 10.5.4 Lifecycle

1. **Construction.** The kernel builds one `acquire.Service` per `App` if any module registers a
   media type (otherwise the service, its routes and its tasks do not exist — G1 of 02 §2.4). It
   receives `storage.Keyspace("c/acquire/")`, the download and indexer registries, `library`, `tasks`,
   `outbox`, `events`, a child logger and the per-App metrics registry.
2. **Registration.** `Module.Register` calls `acquire.Register[T]`. Boot fails (fail-closed) on a
   duplicate module, a condition-kind collision, a category claimed by two modules (a release is routed
   to exactly one module per category — an overlapping claim would double-grab), or an unknown vocabulary.
3. **Migrations.** Keyspace `c/acquire/`: `profile/<module>/<id>`, `format/<module>/<id>`,
   `delay/<module>/<id>`, `restriction/<module>/<id>`, `tag/<id>`, `pending/<module>/<guid>`,
   `tracked/<id>`, `ix/tracked-by-client/<client>/<downloadID>`, `history/<ulid>`,
   `ix/history-by-ref/<module>/<kind>/<id>/<ulid>`, `blocklist/<ulid>`,
   `ix/blocklist-by-ref/<module>/<kind>/<id>/<ulid>`, `ix/blocklist-by-hash/<hash>`. Seed data (default
   profiles per module) is the module's migration, not acquire's.
4. **Tasks** (03 §3.6 kinds; registered by the service, scheduled through `tasks.Schedule`):

   | Kind | Default schedule | Parallelism | What |
   |---|---|---|---|
   | `core.acquire.rss-sync` | `core.acquire.rss.interval` (15 m) | fetch: `tasks.Network(4, "indexer rate limits; providerhttp throttles per indexer")`; decide: `tasks.CPU()` | fetch recent from every RSS-enabled indexer, route by category to modules, append that module's pending releases, `Decide`, grab approved in comparer order, park temporary rejections. Pages back to the last processed release after downtime (`raw/16 §4.3`). |
   | `core.acquire.refresh-downloads` | 1 m, debounced 5 s on demand (Sonarr debounce, `raw/16 §4.7`) | `tasks.Network(4, "one poll per download client")` | poll every enabled client's items, update tracked downloads, run completed/failed checks, enqueue imports. |
   | `core.acquire.import` | on demand | `tasks.Network(2, "disk-bound moves/hardlinks")`, partitioned by owner `entity.Ref` | call the module `Importer`. |
   | `core.acquire.housekeeping` | daily | sequential: `tasks.Sequential("single pass over small keyspace; prunes pending/history by retention")` | prune expired pending releases and history/blocklist past retention. |

   Module search tasks (`tv.search.*`, `movies.search.*`) call `Engine.Decide` with
   `SourceSearch`/`SourceUserSearch` and `Service.Grab`.
5. **Config change.** Profiles/formats/delay profiles are edited through core routes; each write
   rebuilds the module's snapshot and swaps it with `atomic.Pointer` — in-flight `Decide` calls keep
   the old snapshot.
6. **Shutdown.** RSS sync and polls observe `ctx`; an in-flight grab completes or is left in
   `StateGrabbing` and reconciled on next boot (§10.5.6); imports are idempotent and resumed.

Tracked-download transitions (complete table; `→` legal):

| From \ To | Downloading | ImportBlocked | ImportPending | Importing | Imported | FailedPending | Failed | Ignored |
|---|---|---|---|---|---|---|---|---|
| Grabbing | client ack | — | — | — | — | add failed on all clients | — | user |
| Downloading | — | completed, unmapped | completed, mapped | — | — | client reports failure | — | user |
| ImportBlocked | — | — | user resolved / rescan | — | — | — | — | user |
| ImportPending | — | — | — | import started | — | — | — | user |
| Importing | — | importer Blocked | importer Pending | — | importer done | — | — | — |
| FailedPending | — | — | — | — | — | — | blocklisted (+research) | user |
| Imported, Failed, Ignored | terminal (removed from queue view; history retained) | | | | | | | |

### 10.5.5 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| One indexer fetch fails / times out | **fail-open** for the sync: other indexers continue; indexer's `providerhttp` breaker records it | One bad indexer must not stop acquisition; Sonarr behaves the same per indexer. |
| Release title does not parse | reject `core.unparsed` (not an error) | Normal on RSS feeds. |
| Release maps to no target | reject `core.unmapped` | Normal; shown in interactive search. |
| Spec panics | recover, reject `core.spec_failed` with the spec name, log at error, metric | **fail-closed for that candidate**: an unevaluated rule must never let a grab through. |
| Profile referenced by target missing | reject all candidates for that target `core.profile_missing`, health warning | **fail-closed**: grabbing without a profile could download anything. |
| Runtime unknown | reject `core.size.unknown_runtime` | Sonarr does the same (`raw/16 §3.1`); a size check without runtime is meaningless. |
| Blocklist/history/queue read error (StageDatabase) | reject `core.storage_unavailable`, Temporary | **fail-closed but retryable**: a missed blocklist hit re-grabs a known-bad release. |
| Free-space check errors | Temporary reject `core.free_space_unknown` | **fail-closed**: filling the disk breaks every app on the host. |
| Download-client add fails | try next client by priority (round-robin among equal priority, `raw/16 §4.5`); all fail → park as pending `core.client_unavailable` | **fail-open to pending**: the decision stands, delivery is retried. |
| Crash between client add and DownloadID persist | `StateGrabbing` row exists; on boot/poll reconcile by owner tag, then info-hash, then title on that client | Prevents orphan downloads and double grabs (improvement over Sonarr, which has no intent row). |
| Client unreachable during poll | keep last state, set `HealthWarning` "client unavailable" | **fail-open**: never mark Failed (and blocklist) because a client was briefly down. |
| Client reports failed / encrypted / dangerous extension | `FailedPending` → `Failed`, blocklist, optional remove and `Research` (`raw/16 §4.8`) | Matches Failed Download Handling semantics. |
| Importer returns error | `ImportPending`, attempts++, backoff; after `core.acquire.import.max_attempts` → `ImportBlocked` with message | Never delete from client on import failure (data safety). |
| Importer reports Blocked | `ImportBlocked` | User action required. |
| `Research` callback errors | history event + log; no automatic retry | Avoid search storms against indexers. |
| Illegal state transition | `ErrIllegalTransition`, transition refused, error logged | Bug signal; state stays consistent. |
| History write after import | in the **same storage batch** as the module's file write via `outbox` | Correctness-critical (02 §2.4 G4). |

### 10.5.6 Concurrency contract

- `Service`, `Engine[T]`, `Formats` are goroutine-safe. Snapshots are immutable and swapped via
  `atomic.Pointer`; compiled regexes are cached per pattern (shared across profiles).
- `Decide` evaluates candidates on a bounded pool sized by the calling task's `tasks.CPU()`
  parallelism (CPU-bound regex and comparisons; CLAUDE.md concurrency rule). Specs at `StageDatabase`
  and `StageDisk` do I/O and receive the task context; they must check `ctx.Err()`.
- **Grab is partitioned by entity.** Approved decisions are grouped by the set of `entity.Ref`s they
  cover; within a group they are processed strictly in comparer order and stop at the first
  successful grab; groups run in parallel (bounded by `tasks.Network(4, …)`). Two releases for the
  same episode can therefore never both be grabbed in one sync, and an overlapping season pack and
  single episode are serialized because they share a ref (groups are merged by union-find over refs
  before scheduling). This is the CLAUDE.md "partition into disjoint sets" rule.
- Tracked-download updates use a per-ID keyed mutex plus storage compare-and-set on the row, so a
  poll and a user "remove" cannot interleave.
- Imports are partitioned by owner `entity.Ref` (two downloads for the same movie import serially).
- Cancellation: every task honours `ctx`; `context.AfterFunc` closes in-flight client HTTP bodies when
  the task context is cancelled; persistence of a terminal state after cancellation uses
  `context.WithoutCancel` with a short timeout so a cancelled task cannot leave a half-written batch.
- No goroutine outlives `App.Run`: the service's workers are started with `wg.Go` and joined on
  shutdown.

### 10.5.7 Config keys

Core keys (05-config conventions; `core.*`). Module-scoped values (profiles, formats, delay profiles,
restrictions) are **data** in `c/acquire/`, not config.

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.acquire.rss.interval` | duration | `15m` (min `10m`, max `120m`, `0` disables automatic grabbing — `raw/16 §4.1`) | yes (reschedules) |
| `core.acquire.refresh.interval` | duration | `1m` | yes |
| `core.acquire.usenet.retention_days` | int | `0` (unlimited) | yes |
| `core.acquire.usenet.minimum_age` | duration | `0` | yes |
| `core.acquire.maximum_size_bytes` | int64 | `0` (unlimited) | yes |
| `core.acquire.prefer_propers` | enum `prefer`/`do_not_prefer`/`do_not_upgrade` | `prefer` | yes |
| `core.acquire.pending.max_age` | duration | `168h` | yes |
| `core.acquire.failed.enabled` | bool | `true` | yes |
| `core.acquire.failed.redownload` | bool | `true` | yes |
| `core.acquire.failed.remove_from_client` | bool | `true` | yes |
| `core.acquire.completed.enabled` | bool | `true` | yes |
| `core.acquire.import.max_attempts` | int | `5` | yes |
| `core.acquire.history.retention` | duration | `0` (keep) | yes |
| `core.acquire.availability_delay` | duration | `0` (movies uses it; may be negative, `raw/16 §2`) | yes |

### 10.5.8 Metrics

Per-App registry (R5). Label values are bounded enums or configured names.

| Name | Type | Labels |
|---|---|---|
| `mediacore_acquire_releases_evaluated_total` | counter | `module`, `source`, `verdict` (approved/rejected/temporary) |
| `mediacore_acquire_rejections_total` | counter | `module`, `reason` |
| `mediacore_acquire_grabs_total` | counter | `module`, `protocol`, `client`, `result` |
| `mediacore_acquire_pending_releases` | gauge | `module` |
| `mediacore_acquire_tracked_downloads` | gauge | `module`, `state` |
| `mediacore_acquire_imports_total` | counter | `module`, `result` (imported/pending/blocked/error) |
| `mediacore_acquire_import_duration_seconds` | histogram | `module` |
| `mediacore_acquire_rss_sync_duration_seconds` | histogram | — |
| `mediacore_acquire_decide_duration_seconds` | histogram | `module`, `source` |
| `mediacore_acquire_spec_failures_total` | counter | `module`, `spec` |

### 10.5.9 Tests (core must ship)

| Test | Asserts |
|---|---|
| `TestProfileRankCutoffGroups` | rank of grouped qualities equal; cutoff by quality and by group |
| `TestUpgradeOrderQualityRevisionScore` | table over (current, candidate, prefs) incl. `MinUpgradeFormatScore`, `UpgradeAllowed=false` |
| `TestCustomFormatGroupingNegateRequired` | the wiki modifier table (`raw/16 §3.3`): AND across kinds, OR within, Required, Negate |
| `TestFormatScoreSum` | |
| `TestStagesStopAtFirstRejectingStage` | a Database spec is not evaluated when a Default spec rejected |
| `TestTemporaryRejectionParksAndRetries` | `testing/synctest`: delay elapses in virtual time; pending release grabbed on next sync |
| `TestDelayProfileSelectionAndBypass` | tag intersection, Order, default, BypassIfHighestQuality, BypassIfAboveFormatScore; interactive ignores delay |
| `TestAcceptableSizeUsesRuntimeHook` / `TestUnknownRuntimeRejects` | per-runtime bounds incl. profile override |
| `TestDefaultComparatorOrder` | quality > score > protocol > module > indexer priority > peers > age > size; deterministic tie-break |
| `TestTrackedTransitionsExhaustive` | every State×State pair against the table |
| `TestPollClientUnavailableKeepsState` | no Failed on transport error |
| `TestGrabIntentReconcileAfterCrash` | `StateGrabbing` reconciled by owner tag, hash, title |
| `TestGrabPartitionNoDoubleGrab` | `-race`, many workers, overlapping pack+episode: exactly one grab per ref set |
| `TestRefsAcrossModules` | `tv` and `movies` refs in one keyspace never collide |
| `TestQueueViewFiltersByModule` | caller without `movies:view` sees no movie items |
| `TestSpecPanicFailsClosed` | |
| `TestDuplicateCategoryClaimFailsBoot` | |
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
2. Sonarr v5 per-profile sizes vs v4 global table: the model supports both (`ProfileItem.Size`
   override); which one the TV UI shows is a product choice.
3. Whether `Grab` should accept `*Decision[T]` via a typed per-module handle (`Engine[T].Grab`) instead
   of `any` on `Service`; the typed form is preferred and is likely the final API — kept on `Service`
   here only so cross-module queue operations have one entry point.
4. Release-profile v5 air-date fields are TV-only; confirm no movie equivalent is wanted.
5. Metadata provider terms (§10.3) must be re-verified; flagged for 14.
6. Identifiers borrowed from sibling contracts — `library.RootID`, `RelPath`, `Location`, `Entry`,
   `MediaInfo`, `PathMap`, `Transfer`; `indexer.Release`, `Query`, `Caps`, `ID`; `download.Protocol`,
   `ClientID`, `Item`, `GrabRequest`; `metadata.Provider[Q, R]` — were written in parallel with
   [05-library](05-library.md), [05-indexer](05-indexer.md), [05-download](05-download.md) and
   [05-metadata](05-metadata.md). Where those specs chose different names, theirs win and this file is
   renamed to match; the *requirements* stated here (G2, G10, G12, G13 and the fields `acquire` reads)
   are what must survive.

## 10.6 Why `acquire` is media-neutral (and not a TV engine)

Evidence from the research, each tied to a design choice:

| Evidence (`raw/16`) | Consequence in `acquire` |
|---|---|
| `DelayProfile.cs` is **byte-identical** in Sonarr and Radarr (§7.2) | `DelayProfile` has no media field. |
| `QualityProfile` has no TV-specific fields; Radarr's copy differs only by `Language` (§3.2, §7.1 row 2) | One `QualityProfile`; Radarr's `Language` becomes a language custom format (the v4 Sonarr approach, §3.4), rendered back by the facade. |
| `CustomFormatCalculationService` is generic; only condition *kinds* differ (Release Type vs Edition) (§7.1 row 4) | `ConditionRegistry` with namespaced kinds; modules register `tv.release_type`, `movies.edition`. |
| Decision-engine runner is generic; spec catalogue is mixed (§4.4, §7.1 row 8) | `Spec[T]` + generic specs in core, media specs in modules. |
| `IDownloadClient` differs only by `RemoteEpisode` vs `RemoteMovie` (§4.6) | `download.GrabRequest` carries no entity; `acquire` owns the entity link. |
| Tracked-download states have no media vocabulary (§4.7) | `State` enum in core; import is a module callback. |
| History/blocklist/queue embed episode columns (§4.8, §7.2) | Keyed by `entity.Ref`; the only media knowledge is the module's payload in `Data`. |
| Quality on the file, profile on the entity — in both apps (§7.2) | `Grade` on module file rows; `Target.Profile()`. |
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

**O1 — `entity.Ref` needs a home below every consumer.** 02 commits to a polymorphic
`EntityRef{Module, Kind, ID}` (02 §2.2 row 6) and to module-namespaced resource identities (02 §2.4 G8,
`tasks.Resource`), but 03 §3.3 names no package for it. It cannot live in `module` (fifth of R4's seven layers —
`acquire`, `tasks`, `audit` and `events` are lower and must use it) or in `acquire` (then `tasks` and
`audit` would import the acquisition engine). Proposal: add a public leaf package **`entity`** in R4's
lowest layer (with `log`, `metrics`):

```go
package entity

// Ref names one entity owned by a module. Module is a module ID or "core"; Kind and ID are
// module-defined. Ref is comparable and usable as a map key.
type Ref struct {
	Module string // "tv"
	Kind   string // "episode"
	ID     string // canonical string form of the module's ID
}

func (r Ref) String() string                 // "tv:episode:42"
func Parse(s string) (Ref, error)
func (r Ref) AppendKey(b []byte) []byte      // length-prefixed, order-preserving key encoding
```

and make `tasks.Resource` an alias (`type Resource = entity.Ref`) so write-set locks, audit targets,
history and blocklist all speak one type. If 05-tasks has already defined `Resource` with these three
fields, the alias is source-compatible.

**O2 — R4 does not place `acquire`.** 03 §3.3 lists `acquire` but R4's layer list omits it. Proposal:
put it in the `events, outbox, tasks, library, …, integrations/*` layer (it imports `integrations/download`,
`integrations/indexer`, `library`, `tasks`, `outbox`, `events`, `storage`; nothing in that layer imports
it), and allow `arrfacade` and `httpapi` route packages above it to import it. The core layering test
(03 §3.5.1 item 2) needs the entry or it fails on `layerOf` completeness.

## Out-of-scope defects found

None in code read for this file.
