<!-- file: docs/plans/platform-core/raw/16-arr-domain-research.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1c8c22b5-d320-4821-9f81-9dad5586555b -->
<!-- last-edited: 2026-11-03 -->

# 16 - *arr domain research (Sonarr / Radarr / Prowlarr / Seerr family / Bazarr / Readarr)

Scope: public documentation and public source only. No ABO source was read for this file, so no
repo `file:line` anchors appear; every claim cites a public URL instead.

## 0. Provenance and limits (read first)

- Sandbox network: `wiki.servarr.com`, `trash-guides.info` and `github.com` HTML pages were NOT reachable
  (proxy refused / DNS failure). `raw.githubusercontent.com` WAS reachable. So:
  - Wiki claims are read from the wiki's own markdown source, the public repo
    `https://github.com/Servarr/Wiki` (file per page). Citations name the rendered wiki URL
    (what a human would open) and the section anchor.
  - TRaSH claims are read from `https://github.com/TRaSH-Guides/Guides` markdown (rendered at trash-guides.info).
  - Source files were read from each project's default branch (`develop` for Sonarr/Radarr/Readarr/Prowlarr,
    `main` for Jellyseerr, `master` for Bazarr) on 2026-11-03. Line anchors (`#Lnn`) are for that fetch and
    will drift; the file path is the stable part.
- Sonarr `develop` is ahead of the v4 release: the wiki itself marks some items "Sonarr v5 only"
  ([WIKI-S settings#release-profiles](https://wiki.servarr.com/sonarr/settings#release-profiles),
  [#quality-table-meanings](https://wiki.servarr.com/sonarr/settings#quality-table-meanings)). Where I note a
  v5-only item I say so.
- Citation shorthand (each cite = prefix + path; the prefix is the full URL below):

| Key | URL prefix |
|---|---|
| SON | `https://github.com/Sonarr/Sonarr/blob/develop/src/NzbDrone.Core/` |
| SONHTTP | `https://github.com/Sonarr/Sonarr/blob/develop/src/Sonarr.Http/` |
| RAD | `https://github.com/Radarr/Radarr/blob/develop/src/NzbDrone.Core/` |
| RDR | `https://github.com/Readarr/Readarr/blob/develop/src/NzbDrone.Core/` |
| PRW | `https://github.com/Prowlarr/Prowlarr/blob/develop/src/NzbDrone.Core/` |
| JSR | `https://github.com/Fallenbagel/jellyseerr/blob/main/server/` (project now continues as `https://github.com/seerr-team/seerr`; the servarr API files had the same byte size there at fetch time) |
| OVS | `https://github.com/sct/overseerr/blob/develop/server/` |
| BAZ | `https://github.com/morpheus65535/bazarr/blob/master/bazarr/` |
| WIKI-S / WIKI-R / WIKI-P | `https://wiki.servarr.com/sonarr/` / `.../radarr/` / `.../prowlarr/` (+ `settings`, `faq`, `library`) |
| TRASH | `https://trash-guides.info/Sonarr/sonarr-setup-quality-profiles/` (source: `https://github.com/TRaSH-Guides/Guides/blob/master/docs/Sonarr/sonarr-setup-quality-profiles.md`) |

## 1. Sonarr domain model

### 1.1 Entities

| Entity | Key fields (verbatim names) | Cite |
|---|---|---|
| Series | `TvdbId, TvRageId, TvMazeId, ImdbId, TmdbId`, `Title/CleanTitle/SortTitle`, `Status`, `Monitored`, `MonitorNewItems` (All/None), `QualityProfileId`, `SeasonFolder`, `SeriesType`, `UseSceneNumbering`, `Path`, `RootFolderPath`, `Seasons` (embedded list), `Tags` (set of int), `Language`, `OriginalLanguage`, `AddOptions` | `SON Tv/Series.cs` L26 (TvdbId), L39-L42, L46 (SeriesType), L48, L50, L57, L62-L63, L70-L71 |
| Season | NOT a table: embedded document `IEmbeddedDocument` with only `SeasonNumber, Monitored, Title, Images` | `SON Tv/Season.cs` L6, L14 |
| Episode | `SeriesId, TvdbId, EpisodeFileId, SeasonNumber, EpisodeNumber, Title, AirDate (string yyyy-MM-dd), AirDateUtc, Monitored, AbsoluteEpisodeNumber?, SceneAbsoluteEpisodeNumber?, SceneSeasonNumber?, SceneEpisodeNumber?, AiredAfterSeasonNumber?, UnverifiedSceneNumbering, LastSearchTime, Runtime`; `HasFile => EpisodeFileId > 0` | `SON Tv/Episode.cs` L9-L49 |
| EpisodeFile | `SeriesId, SeasonNumber, RelativePath, Path, Size, DateAdded, OriginalFilePath, SceneName, ReleaseGroup, ReleaseHash, Quality (QualityModel), IndexerFlags, MediaInfo, Languages, ReleaseType`; one file can map to many episodes (`LazyLoaded<List<Episode>> Episodes`) | `SON MediaFiles/EpisodeFile.cs` L13-L30 |
| MonitoringOptions (add-time) | `IgnoreEpisodesWithFiles, IgnoreEpisodesWithoutFiles, Monitor` enum `Unknown, All, Future, Missing, Existing, FirstSeason, LastSeason, Pilot, Recent, MonitorSpecials, UnmonitorSpecials, None, Skip` | `SON Tv/MonitoringOptions.cs` |

Notes:
- Metadata comes from a TVDb proxy ("Skyhook"); series + episode info is refreshed every 12 hours and that task
  cannot be disabled ([WIKI-S faq#why-does-sonarr-refresh-series-information-so-frequently](https://wiki.servarr.com/sonarr/faq#why-does-sonarr-refresh-series-information-so-frequently)).
- Quality lives on the FILE (`EpisodeFile.Quality`, `Languages`), the profile lives on the SERIES
  (`Series.QualityProfileId`). Same split in Radarr (section 2).

### 1.2 Series types and numbering

| Type | Meaning / search behaviour | Cite |
|---|---|---|
| Standard (0) | `SxxEyy` releases | enum `SON Tv/SeriesTypes.cs`; [WIKI-S faq#series-types](https://wiki.servarr.com/sonarr/faq#whats-the-different-series-types) |
| Daily (1) | year-month-day releases; Newznab query is `season=<yyyy>&ep=<MM>/<dd>` | `SON Indexers/Newznab/NewznabRequestGenerator.cs` L333; faq anchor above |
| Anime (2) | absolute episode numbers; searched with the indexer's *anime categories*; "does not have any concept of Season Packs or Seasons" per the FAQ; query `q=<absolute:00>` or `q=<title>+<abs>`; optional extra standard-format search ("Anime Standard Format Search") | `SON Indexers/Newznab/NewznabRequestGenerator.cs` L411-L441; faq anchor above; [WIKI-S settings#indexers](https://wiki.servarr.com/sonarr/settings#usenet-indexer-configuration) |

- Absolute numbering: `Episode.AbsoluteEpisodeNumber` plus scene variants (`SceneAbsoluteEpisodeNumber`,
  `SceneSeasonNumber`, `SceneEpisodeNumber`) and `UnverifiedSceneNumbering`; `Series.UseSceneNumbering`
  toggles scene mapping (`SON Tv/Episode.cs` L28-L29; `SON Tv/Series.cs` L48). Anime naming token `{absolute:00}`
  ([WIKI-S settings#absolute-episode-number](https://wiki.servarr.com/sonarr/settings#absolute-episode-number)).
- Scene-numbering mismatches are a documented source of grabs failing to map
  ([WIKI-S faq#how-does-sonarr-handle-scene-numbering-issues](https://wiki.servarr.com/sonarr/faq#how-does-sonarr-handle-scene-numbering-issues-american-dad-etc)).

### 1.3 Monitoring

- Flags exist at three levels: `Series.Monitored`, `Season.Monitored` (embedded), `Episode.Monitored`
  (`SON Tv/Series.cs` L39; `SON Tv/Season.cs` L14; `SON Tv/Episode.cs` L27).
- Add-time presets: All, Future, Missing, Existing, Recent (aired in last 90 days + future), Pilot, First Season,
  Last Season, Monitor Specials, Unmonitor Specials, None
  ([WIKI-S library#add-new](https://wiki.servarr.com/sonarr/library#add-new)).
- `MonitorNewItems` (All/None) decides whether newly discovered seasons/episodes are monitored
  (`SON Tv/MonitoringOptions.cs`, enum `NewItemMonitorTypes`).
- RSS decisions reject unmonitored series/episodes (`SeriesNotMonitored`, `EpisodeNotMonitored`)
  (`SON DecisionEngine/Specifications/RssSync/MonitoredEpisodeSpecification.cs` L30, L42, L54).
- "Unmonitor Deleted Episodes" is a media-management option
  ([WIKI-S settings#file-management](https://wiki.servarr.com/sonarr/settings#file-management)).
- Sonarr does NOT poll indexers for missing items; it pulls the newest releases (RSS) and matches against wanted
  episodes; active search happens only on user/API trigger, add-with-search, Wanted tab, and for recently aired
  episodes (aired within last 14 days or 1 day ahead)
  ([WIKI-S faq#how-does-sonarr-find-episodes](https://wiki.servarr.com/sonarr/faq#how-does-sonarr-find-episodes)).

### 1.4 Root folders, paths, naming

- Root folder = a library root path; shows free space and "unmapped folders" (folders with no series).
  Series get `RootFolderPath` + `Path`
  ([WIKI-S settings#root-folders](https://wiki.servarr.com/sonarr/settings#root-folders); `SON Tv/Series.cs` L50, L57).
- Download folder and media folder cannot be the same location
  ([WIKI-S library#library-import](https://wiki.servarr.com/sonarr/library#library-import)).
- Naming has SEPARATE templates per series type: Standard Episode Format, Daily Episode Format, Anime Episode
  Format, plus Series Folder Format and Season Folder Format; multi-episode style
  (Extend/Duplicate/Repeat/Scene/Range/Prefixed Range)
  ([WIKI-S settings#episode-naming](https://wiki.servarr.com/sonarr/settings#episode-naming),
  [#multi-episode-style](https://wiki.servarr.com/sonarr/settings#multi-episode-style)).
- Import options: hardlink instead of copy, minimum free space, skip free-space check, "Episode Title Required"
  (wait up to 48h for TBA titles), import extra files, import via script, recycling bin, chmod/chown, unmonitor
  deleted episodes, rescan-after-refresh
  ([WIKI-S settings#importing](https://wiki.servarr.com/sonarr/settings#importing),
  [#file-management](https://wiki.servarr.com/sonarr/settings#file-management)).

## 2. Radarr domain model

| Entity | Key fields | Cite |
|---|---|---|
| Movie | `MovieMetadataId`, `Monitored`, `MinimumAvailability`, `QualityProfileId`, `Path`, `RootFolderPath`, `Tags`, `AddOptions`, `LastSearchTime`, `MovieFileId`; `HasFile => MovieFileId > 0`; `Title/TmdbId/ImdbId/Year` proxy to metadata | `RAD Movies/Movie.cs` L11-L38 |
| MovieMetadata (split from Movie) | `TmdbId, ImdbId, Title, Status, InCinemas, PhysicalRelease, DigitalRelease, CollectionTmdbId, CollectionTitle, Runtime, Year, Genres, Ratings, AlternativeTitles, Translations, OriginalLanguage` | `RAD Movies/MovieMetadata.cs` L24-L41 |
| MovieFile | `MovieId, RelativePath, Path, Size, DateAdded, OriginalFilePath, SceneName, ReleaseGroup, Quality, IndexerFlags, MediaInfo, Edition, Languages` (one file per movie; no Season/Episode) | `RAD MediaFiles/MovieFile.cs` L13-L28 |
| MovieCollection | own table: `TmdbId, Title, Monitored, QualityProfileId, RootFolderPath, SearchOnAdd, MinimumAvailability, Movies (metadata list), Tags` | `RAD Movies/Collections/MovieCollection.cs` L7-L38 |
| AddMovieOptions | extends MonitoringOptions; `SearchForMovie`, `AddMethod` (Manual/List/Collection) | `RAD Movies/AddMovieOptions.cs` L3-L9 |

- Metadata and the user's monitoring decision are separate rows (`Movie` vs `MovieMetadata`), which is what lets
  collections list movies the user has not added (`MovieCollection.Movies` is `List<MovieMetadata>`).
- Minimum availability: status enum `Deleted=-1, TBA, Announced, InCinemas, Released`
  (`RAD Movies/MovieStatusType.cs` L5-L9). `Movie.IsAvailable(delay)` computes a date: TBA/Announced = always;
  InCinemas = theatrical date; Released = earlier of physical/digital release, else theatrical + 90 days
  (`RAD Movies/Movie.cs` L77-L110). Wiki: "Released" recommended, combined with an Availability Delay of -14 or -21
  days set under Indexers
  ([WIKI-R faq#what-is-minimum-availability](https://wiki.servarr.com/radarr/faq#what-is-minimum-availability),
  [WIKI-R settings#options](https://wiki.servarr.com/radarr/settings#options)).
- Monitoring is a single boolean on Movie (and on Collection); no season/episode tree
  (`RAD Movies/Movie.cs` L21; `RAD Movies/Collections/MovieCollection.cs` L20). RSS rejects unmonitored movies
  (`RAD DecisionEngine/Specifications/RssSync/MonitoredMovieSpecification.cs`).
- Radarr's quality profile still carries a `Language` field (`RAD Profiles/Qualities/QualityProfile.cs` L24);
  Sonarr's does not (section 3.4).
- Naming: Movie Folder Format + Standard Movie Format; collection tokens `{Movie Collection}` exist
  ([WIKI-R settings#movie-naming](https://wiki.servarr.com/radarr/settings#movie-naming)).
- Lists/collections add movies "eventually"; lists can set quality profile, monitor and auto-add
  ([WIKI-R faq#what-are-lists-and-what-can-they-do-for-me](https://wiki.servarr.com/radarr/faq#what-are-lists-and-what-can-they-do-for-me)).

## 3. Quality

### 3.1 Quality definitions (sizes)

- A `Quality` is a hardcoded (id, name, source, resolution) tuple: Unknown(0), SDTV(1), DVD(2), WEBDL-1080p(3),
  HDTV-720p(4) ... Bluray-2160p Remux(21), Bluray-576p(22)
  (`SON Qualities/Quality.cs` L77-L124).
- A `QualityDefinition` binds a Quality to `Title, GroupName, Weight, MinSize, MaxSize, PreferredSize`
  (`SON Qualities/QualityDefinition.cs` L14-L16). Units are megabytes PER MINUTE of runtime
  ([WIKI-S settings#quality-table-meanings](https://wiki.servarr.com/sonarr/settings#quality-table-meanings)).
  Defaults e.g. Bluray-1080p min 4 / max 155 / preferred 95; Remux min 35 / max null (unlimited)
  (`SON Qualities/Quality.cs` L173-L174).
- Size is checked against runtime: no runtime = rejection `UnknownRuntime`; below/above bounds = `BelowMinimumSize` /
  `AboveMaximumSize` (`SON DecisionEngine/Specifications/AcceptableSizeSpecification.cs` L74, L95, L116).
  Consequence for a shared core: size limits need a per-item "duration" abstraction.
- Sonarr v5 moves min/preferred/max size from the global table into each quality profile; only Title stays global
  ([WIKI-S settings#quality-table-meanings](https://wiki.servarr.com/sonarr/settings#quality-table-meanings)).
- Same `Weight` = grouped qualities (e.g. WEBRip-720p + WEBDL-720p both Weight 10, GroupName "WEB 720p")
  (`SON Qualities/Quality.cs` L168-L169).

### 3.2 Quality profiles

`QualityProfile` (`SON Profiles/Qualities/QualityProfile.cs` L17-L22, L91-L93):

| Field | Meaning (wiki) |
|---|---|
| `Items` | ordered list of qualities and quality GROUPS; "qualities higher in the list are more preferred"; only checked ones are wanted |
| `Cutoff` | quality id/group id at which upgrading stops ("Upgrade Until") |
| `UpgradeAllowed` | master switch for upgrades |
| `MinFormatScore` | releases whose summed custom-format score is below this are rejected |
| `CutoffFormatScore` | "Upgrade Until" custom-format score; upgrades stop once reached |
| `MinUpgradeFormatScore` | minimum score improvement to count as an upgrade |
| `FormatItems` | list of (CustomFormat, Score); `CalculateCustomFormatScore` = sum of scores of matched formats |

- Wiki: [WIKI-S settings#quality-profiles](https://wiki.servarr.com/sonarr/settings#quality-profiles) (Upgrades Allowed,
  Upgrade Until, Qualities, Edit Groups); [WIKI-S settings#profiling-settings-and-ranking](https://wiki.servarr.com/sonarr/settings#profiling-settings-and-ranking).
  TRaSH: [TRASH](https://trash-guides.info/Sonarr/sonarr-setup-quality-profiles/) (Upgrade Until, Minimum Custom Format
  Score; "quality rank beats custom format score").
- The profile is evaluated at three points: allowed? (`QualityAllowedByProfileSpecification` L29), is it an upgrade
  over the file on disk? (`UpgradeDiskSpecification`, `UpgradeAllowedSpecification` L50), and does the queue/history
  already hold something at or above cutoff? (`QueueSpecification` L67-L93, `RssSync/HistorySpecification` L83-L103).

### 3.3 Custom formats, release profiles, delay profiles

**Custom formats (CF)**
- Model: `CustomFormat { Name, IncludeCustomFormatWhenRenaming, Specifications[] }`
  (`SON CustomFormats/CustomFormat.cs` L22-L24). Scores are NOT on the CF; they live per quality profile
  (`FormatItems`). A score of 0 = informational only
  ([WIKI-S settings#profiling-settings-and-ranking](https://wiki.servarr.com/sonarr/settings#profiling-settings-and-ranking)).
- Specification kinds (Sonarr): Release Title (regex), Language, Source, Resolution, Size (GB), Release Group,
  Indexer Flag, Release Type (single/multi-episode/season pack). Radarr adds Edition
  ([WIKI-S settings#conditions](https://wiki.servarr.com/sonarr/settings#conditions),
  [WIKI-R settings](https://wiki.servarr.com/radarr/settings) "Edition" condition). All regex is case-insensitive.
- Modifiers: `Negate` inverts a condition; `Required` changes OR to AND within a same-type group. Different spec
  types AND together; multiple of the same type OR together unless Required
  ([WIKI-S settings#modifiers](https://wiki.servarr.com/sonarr/settings#modifiers)). Implementation: group specs by
  concrete type, all groups must match (`SON CustomFormats/CustomFormatCalculationService.cs` L153-L165).
- Evaluated on the fly, never stored on the file row: "Custom formats are calculated on-the-fly instead of being
  stored in the database" ([WIKI-S settings#custom-formats-2](https://wiki.servarr.com/sonarr/settings#custom-formats-2));
  CFs "have no influence on what is searched - only how the results are evaluated" (same section).
- Release-title spec matches either the release title or the on-disk filename
  (`SON CustomFormats/Specifications/ReleaseTitleSpecification.cs` L9-L12).
- Score ranking: in the decision comparer, CF score is the 2nd key after quality (section 4.4).

**Release profiles (Sonarr v4)**
- Reduced to filters: `Required`, `Ignored` term lists (regex allowed), `IndexerIds`, `Tags`; "Preferred words have
  been replaced with Custom Formats" ([WIKI-S settings#release-profiles](https://wiki.servarr.com/sonarr/settings#release-profiles)).
  Source on `develop` additionally has `ExcludedTags`, `AirDateRestriction`, `AirDateGracePeriod`,
  `AllowSeasonPackWithoutAllEpisodesAired` (the wiki labels those v5 only)
  (`SON Profiles/Releases/ReleaseProfile.cs` L10-L17; enforced by `ReleaseRestrictionsSpecification` L45, L58).

**Delay profiles**
- Fields: `EnableUsenet, EnableTorrent, PreferredProtocol, UsenetDelay, TorrentDelay, Order, BypassIfHighestQuality,
  BypassIfAboveCustomFormatScore, MinimumCustomFormatScore, Tags` (`SON Profiles/Delay/DelayProfile.cs` L9-L18).
- Semantics: delay (minutes) is measured from the release's publish time, not when Sonarr saw it; manual searches
  ignore delay; a profile with no tags is the default; tags bind a profile to series
  ([WIKI-S settings#how-delay-profiles-work](https://wiki.servarr.com/sonarr/settings#how-delay-profiles-work)).
- Mechanism: delay is a TEMPORARY rejection (`RssSync/DelaySpecification` Type = Temporary, L30); temporarily
  rejected decisions are parked as pending releases and retried by later RSS syncs
  (`SON Download/ProcessDownloadDecisions.cs` L61-L63, L139-L143, L164; `SON Indexers/RssSyncService.cs` L41-L46,
  which concatenates `rssReleases` with `GetPending()`).

### 3.4 Language handling

- Sonarr v4: language profiles are gone. Language is a custom-format condition ("All languages previously
  selectable in profiles work here") ([WIKI-S settings#conditions](https://wiki.servarr.com/sonarr/settings#conditions));
  `QualityProfile` has no Language field (`SON Profiles/Qualities/QualityProfile.cs`); Bazarr comments that Sonarr >= v4
  "does not support series languages profiles anymore" (`BAZ sonarr/sync/utils.py` L20). `GET /languageprofile` still
  exists for v3 and is what Seerr calls (`JSR api/servarr/sonarr.ts` L337).
- Files carry `Languages` (`SON MediaFiles/EpisodeFile.cs` L30; `RAD MediaFiles/MovieFile.cs` L28). Series carries
  `Language` + `OriginalLanguage` (`SON Tv/Series.cs` L62-L63).
- Radarr keeps a profile-level `Language` (`RAD Profiles/Qualities/QualityProfile.cs` L24), and the wiki lists a
  profile "Language" setting ([WIKI-R settings#quality-profiles](https://wiki.servarr.com/radarr/settings#quality-profiles)).

## 4. Acquisition pipeline

### 4.1 Indexers

- Indexer settings (shared by Newznab and Torznab): `EnableRss`, `EnableAutomaticSearch`, `EnableInteractiveSearch`,
  `Priority` (default 25; wiki says 1 = highest, 50 = lowest), `DownloadClientId` (pin a client), `Tags`,
  `SeasonSearchMaximumSingleEpisodeAge`; `Enable` is the OR of the three flags
  (`SON Indexers/IndexerDefinition.cs` L11-L35;
  [WIKI-S settings#indexer-settings](https://wiki.servarr.com/sonarr/settings#indexer-settings)).
- Torrent-only extras: minimum seeders, seed ratio, seed time, season-pack seed time
  ([WIKI-S settings#torrent-tracker-configuration](https://wiki.servarr.com/sonarr/settings#torrent-tracker-configuration)).
- Provider interface (`IIndexer`): `SupportsRss`, `SupportsSearch`, `Protocol`, `FetchRecent()`, one `Fetch(...)`
  overload per search-criteria type (Season, SingleEpisode, DailyEpisode, DailySeason, AnimeEpisode, AnimeSeason,
  SpecialEpisode), `GetDownloadRequest(link)` (`SON Indexers/IIndexer.cs` L12-L28).
- Global options: RSS sync interval (min 10, max 120 minutes; 0 disables all automatic grabbing), retention (usenet),
  minimum age (usenet), maximum size
  ([WIKI-S settings#options](https://wiki.servarr.com/sonarr/settings#options)).

### 4.2 Newznab / Torznab protocol as actually used

| Step | Wire detail | Cite |
|---|---|---|
| Capabilities | `GET <base><apiPath>?t=caps[&apikey=...]`; XML `<searching>` with `<search supportedParams>` and `<tv-search supportedParams>` (Radarr also movie-search), and `<categories>` tree | `SON Indexers/Newznab/NewznabCapabilitiesProvider.cs` L44, L121-L148, L156-L179 |
| Caps defaults when absent | page size 100; tv params `q,rid,season,ep`; Radarr movie params `q,imdbid,imdbtitle,imdbyear` | `SON Indexers/Newznab/NewznabCapabilities.cs` L18-L21; `RAD Indexers/Newznab/NewznabCapabilities.cs` L21 |
| Request shape | `?t=<type>&cat=<csv>&extended=1<extra>&apikey=<key>&offset=<page*size>&limit=<size><params>`; auth for indexers is the `apikey` query parameter | `SON Indexers/Newznab/NewznabRequestGenerator.cs` L618-L643 |
| RSS (recent) | `t=tvsearch` (TV, plus anime cats) with empty params, paged; `t=movie` for Radarr; falls back to `t=search` | `SON ...NewznabRequestGenerator.cs` L204-L208; `RAD ...NewznabRequestGenerator.cs` L95-L101 |
| TV search params | id params only if the indexer's caps list them: `tvdbid`, `imdbid`, `rid` (TVRage), `tmdbid`; `season=<n>&ep=<n>`; text fallback `q=<title>`; if caps lack `season`/`ep` or any usable id/q param the search is skipped | `SON ...NewznabRequestGenerator.cs` L86-L130, L220-L227, L239-L257, L286-L304 |
| Movie search params | `t=movie&tmdbid=<id>` and/or `&imdbid=<digits without tt>`; text fallback `t=search` | `RAD ...NewznabRequestGenerator.cs` L127-L151, L171 |
| Item attributes parsed | newznab: `size, tvdbid, rageid, imdb`; torznab adds `seeders, peers, infohash, magneturl, downloadvolumefactor` (freeleech) | `SON Indexers/Torznab/TorznabRssParser.cs` L129-L239; `SON Indexers/Newznab/NewznabRssParser.cs` L141-L189 |
| Categories | Newznab standard ids: Movies 2000 (SD 2030 / HD 2040 / UHD 2045), Audio 3000 (Audiobook 3030), TV 5000 (SD 5030 / HD 5040 / UHD 5045 / Anime 5070), Books 7000 (EBook 7020). Sonarr keeps separate `Categories` and `AnimeCategories` per indexer | `PRW Indexers/NewznabStandardCategory.cs` L28-L83; wiki [WIKI-S settings#usenet-indexer-configuration](https://wiki.servarr.com/sonarr/settings#usenet-indexer-configuration) |

Torznab is Newznab plus torrent attributes; Sonarr implements both on one generator and the Torznab preset is the
Jackett/Prowlarr path ([WIKI-S settings#torrent-tracker-configuration](https://wiki.servarr.com/sonarr/settings#torrent-tracker-configuration)).

### 4.3 RSS sync, automatic and interactive search

- RSS sync: fetch recent from every RSS-enabled indexer, append pending (delayed) releases, run the decision engine,
  grab approved ones, park temporarily rejected ones (`SON Indexers/RssSyncService.cs` L41-L46). If Sonarr was
  offline it pages back to the last processed release
  ([WIKI-S settings#options](https://wiki.servarr.com/sonarr/settings#options) note).
- Automatic search (`EnableAutomaticSearch`) and interactive search (`EnableInteractiveSearch`) use the same decision
  engine with `SearchCriteria`; `ReleaseSourceType` records Rss / Search / UserInvokedSearch / InteractiveSearch /
  ReleasePush (`SON DecisionEngine/DownloadDecisionMaker.cs` L20-L21 `GetRssDecision` vs `GetSearchDecision`;
  `GetReleaseSource` at L240). Search-only specifications (`Search/SeriesSpecification`,
  `Search/SeasonMatchSpecification`, `Search/EpisodeRequestedSpecification`,
  `Search/SingleEpisodeSearchMatchSpecification`) reject results that do not match what was searched.
- Interactive search shows rejected results too; manual searches ignore delay profiles
  ([WIKI-S settings#how-delay-profiles-work](https://wiki.servarr.com/sonarr/settings#how-delay-profiles-work)).

### 4.4 Decision engine

Pipeline (`SON DecisionEngine/DownloadDecisionMaker.cs`):
1. Parse release title into `ParsedEpisodeInfo` (series title, season/episode or date or absolute, quality, language,
   release group) (L80); special-episode and season-title fallbacks follow.
2. Map to a Series + Episodes via IDs from the indexer item or title matching; unmapped = rejection `UnknownSeries` /
   `UnknownEpisode` / `UnableToParse`.
3. Augment, compute matching custom formats and `CustomFormatScore` against the series' quality profile (L126).
4. Run specifications grouped by `Priority` (Default, Database, Disk, ...) in ascending order, stopping at the first
   group that yields any rejection (L203-L212). Each spec returns `Accept()` or `Reject(reason, message)` and has a
   `Type` of Permanent or Temporary.
5. Sort accepted decisions with `DownloadDecisionComparer`.

Comparer order (source): Quality, CustomFormatScore, Protocol (per delay profile), EpisodeCount, EpisodeNumber,
IndexerPriority, Peers (torrent), Age (usenet), Size (`SON DecisionEngine/DownloadDecisionComparer.cs` L28-L40).
Wiki lists the same for Sonarr ([WIKI-S faq#how-are-possible-downloads-compared](https://wiki.servarr.com/sonarr/faq#how-are-possible-downloads-compared))
and for Radarr: Quality, CF score, Protocol, Indexer priority, Indexer flags, Seeds/Peers, Age, Size
([WIKI-R faq#how-are-possible-downloads-compared](https://wiki.servarr.com/radarr/faq#how-are-possible-downloads-compared)).
Repack/Proper count as a higher quality revision unless set to "Do not prefer".

Specifications found in Sonarr `DecisionEngine/Specifications/` (directory
`https://github.com/Sonarr/Sonarr/tree/develop/src/NzbDrone.Core/DecisionEngine/Specifications`; type shown as P=Permanent, T=Temporary):

| Spec | Priority / Type | Rejects when |
|---|---|---|
| AcceptableSize | Default / P | outside quality size bounds (per-minute * runtime) |
| MaximumSize | Default / P | above global max release size |
| MinimumAge | Default / T | usenet NZB younger than minimum age |
| Retention | Default / P | older than configured usenet retention |
| Protocol | Default / P | usenet/torrent disabled by the series' delay profile |
| QualityAllowedByProfile | Default / P | quality not checked in profile |
| UpgradeAllowed | Default / P | existing file but profile forbids upgrades |
| UpgradeDisk | Default / P | not better than file on disk (quality, CF score; season-pack partial-upgrade rule) |
| AnimeVersionUpgrade | Default / P | anime release-group mismatch with existing file |
| Repack | Database / P | repack disabled / unknown group / not same group |
| ReleaseRestrictions | Default / P | release-profile Required/Ignored terms |
| FullSeason / SeasonPackOnly | Default / P | season pack before all aired; pack required for old seasons |
| NotSample, RawDisk, SplitEpisode | Default / P | sample / raw disc / split-episode releases |
| Blocklist | Database / P | release is on the blocklist |
| AlreadyImported | Database / P | same torrent hash or release name already grabbed+imported |
| Queue | Default / P | queue already holds equal/better (quality, revision, CF score, cutoff) |
| FreeSpace | Disk / P | import would drop below minimum free space |
| RssSync/MonitoredEpisode | Default / P | series/episode not monitored |
| RssSync/IndexerTag | Default / P | series tags do not intersect indexer tags |
| RssSync/History | Database / P | recent grab in history already meets cutoff/preference/revision |
| RssSync/Delay | Database / T | delay profile timer not elapsed |
| RssSync/Proper, RssSync/DeletedEpisodeFile | Default,Disk / P,T | proper for old file; episode file in DB but missing on disk (will be unmonitored on next disk scan; skipped when searching) |

Cites: `SON DecisionEngine/Specifications/<name>Specification.cs`, Priority/Type lines are the `Priority =>` /
`Type =>` properties near each file's top (e.g. `BlocklistSpecification.cs` L18-L19, `RssSync/DelaySpecification.cs`
L29-L30, `AcceptableSizeSpecification.cs` L20-L21, `FreeSpaceSpecification.cs` L29-L30).
Radarr has the same architecture with movie-specific specs (e.g. `RAD DecisionEngine/Specifications/RssSync/MonitoredMovieSpecification.cs`).

### 4.5 Grab: download-client selection and event

- `ProcessDecisions` skips already-grabbed episodes, parks Temporary rejections as pending (Delay) and releases whose
  protocol has no client as pending (DownloadClientUnavailable), grabs the rest in comparer order
  (`SON Download/ProcessDownloadDecisions.cs` L38-L112, L164).
- `DownloadService.DownloadReport(remoteEpisode, downloadClientId?)`: use the indexer's pinned client if set, else
  pick among enabled clients for the protocol by priority (round-robin among equal priority), try the next client on
  failure, then publish `EpisodeGrabbedEvent` (history "Grabbed") with client id and `DownloadId`
  (`SON Download/DownloadService.cs` L55-L118, L186-L197;
  [WIKI-S settings#usenet-client-settings](https://wiki.servarr.com/sonarr/settings#usenet-client-settings) "Client Priority").

### 4.6 Download-client abstraction

`IDownloadClient : IProvider` (`SON Download/IDownloadClient.cs` L9-L19; Radarr identical but `RemoteMovie`,
`RAD Download/IDownloadClient.cs`):

| Method | Role |
|---|---|
| `Protocol` | Usenet or Torrent (`SON Indexers/DownloadProtocol.cs`) |
| `Download(remote, indexer)` | add; returns the client's id (torrent: info-hash; usenet: client nzo/id) |
| `GetItems()` | poll queue + recent history, normalised to `DownloadClientItem` |
| `GetFiles(item)` | file list for an item |
| `GetImportItem(item, previousAttempt)` | resolve final output path (client-specific) |
| `RemoveItem(item, deleteData)` | remove from client |
| `GetStatus()` | client info (e.g. output paths) |
| `MarkItemAsImported(item)` | set post-import category/label |

Normalised item (`SON Download/DownloadClientItem.cs` L12-L25): `DownloadId, Category, Title, TotalSize, RemainingSize,
RemainingTime, SeedRatio, OutputPath, Message, Status, IsEncrypted, CanMoveFiles, CanBeRemoved`, plus client info
(`Protocol, Type, Id, Name, RemoveCompletedDownloads, HasPostImportCategory`). Status enum:
`Queued, Paused, Downloading, Completed, Failed, Warning` (`SON Download/DownloadItemStatus.cs`).
Definition-level settings: `Priority` (default 1), `RemoveCompletedDownloads` (true), `RemoveFailedDownloads` (true)
(`SON Download/DownloadClientDefinition.cs` L15-L18). Shared bases: `TorrentClientBase`, `UsenetClientBase`
(`SON Download/TorrentClientBase.cs`, `UsenetClientBase.cs`).

Concrete clients (wire protocol evidence):

| Client | Protocol surface used | Cite |
|---|---|---|
| qBittorrent | WebUI v2: `/api/v2/auth/login`, `app/webapiVersion`, `app/version`, `app/preferences`, `torrents/info`, `torrents/properties`, `torrents/files`, `torrents/add`, `torrents/delete`, `torrents/setCategory`, `torrents/createCategory`, `torrents/categories`, `torrents/setShareLimits`, `torrents/topPrio`, `torrents/setForceStart`, `torrents/addTags`; state strings mapped to the status enum | `SON Download/Clients/QBittorrent/QBittorrentProxyV2.cs` L31-L479; `QBittorrent.cs` L286-L357 |
| Transmission | JSON-RPC: `torrent-add`, `torrent-get`, `torrent-set`, `torrent-remove`, `session-get`, `session-stats` | `SON Download/Clients/Transmission/TransmissionProxy.cs` |
| Deluge | JSON-RPC via web UI: `auth.login`, `web.connected`, `web.get_hosts`, `web.connect`, `core.add_torrent_magnet`, `core.add_torrent_file`, `core.get_torrents_status`, `core.remove_torrent`, `core.set_torrent_options`, `label.*` | `SON Download/Clients/Deluge/DelugeProxy.cs` |
| SABnzbd | HTTP API `mode=` values `addfile`/`addurl`, `queue`, `history`, `fullstatus`, `get_config`, `version`, `delete` | `SON Download/Clients/Sabnzbd/SabnzbdProxy.cs` |
| NZBGet | JSON-RPC: `append`, `listgroups`, `history`, `editqueue`, `config`, `status`, `version` | `SON Download/Clients/Nzbget/NzbgetProxy.cs` |

Seed-goal support differs per client (ratio vs time vs idle limit) and is a documented compatibility matrix
([WIKI-S settings#torrent-client-remove-download-compatibility](https://wiki.servarr.com/sonarr/settings#torrent-client-remove-download-compatibility)).
Remote path mapping is a plain find/replace of the client-reported path to a local path
([WIKI-S settings#remote-path-mappings](https://wiki.servarr.com/sonarr/settings#remote-path-mappings)).

### 4.7 Status polling, completed-download handling, import

- Poll loop: `RefreshMonitoredDownloads` debounced to 5 s; for every enabled client call `GetItems()`, map to
  `TrackedDownload`, persist trackable ones, then enqueue `ProcessMonitoredDownloadsCommand`
  (`SON Download/TrackedDownloads/DownloadMonitoringService.cs` L55-L97). Seerr triggers this very command through
  the API before reading the queue (section 5).
- Tracked-download state machine: `Downloading, ImportBlocked, ImportPending, Importing, Imported, FailedPending,
  Failed, Ignored` (`SON Download/TrackedDownloads/TrackedDownload.cs` L49-L59).
- `CompletedDownloadService.Check` moves Downloading to ImportPending when the client says Completed and the item is
  matched to a series/episodes; `Import` runs the manual-import pipeline against `OutputPath`, then `VerifyImport`
  decides Imported vs ImportPending vs ImportBlocked (`SON Download/CompletedDownloadService.cs` L72-L216).
- Torrents are hardlinked (copy fallback) so seeding continues; usenet is moved; removal happens only if
  "Remove Completed" is on and (torrents) the client reports seeding goals met and stopped, in the same category
  ([WIKI-S settings#download-client-processes](https://wiki.servarr.com/sonarr/settings#download-client-processes),
  [#completed-download-handling](https://wiki.servarr.com/sonarr/settings#completed-download-handling)).
- Grabbed episodes are matched back to the grab by `DownloadId` stored in history (`SON History/EpisodeHistory.cs` L28
  `DownloadId`); when no grab history exists the import is "matched by ID" and automatic import is not possible
  ([WIKI-S faq](https://wiki.servarr.com/sonarr/faq) entry "Found matching series via grab history...").

### 4.8 Failed downloads, blocklist, history, queue

- Failed handling: detect from client (queue encrypted, history failure, dangerous/executable file extensions per
  indexer "Fail Downloads"), set `FailedPending` then `Failed`, write a history event, optionally remove from client,
  blocklist, and optionally search for a replacement ("Redownload Failed"); compatible by default only with SABnzbd
  and NZBGet ([WIKI-S settings#failed-download-handling](https://wiki.servarr.com/sonarr/settings#failed-download-handling);
  `SON Download/FailedDownloadService.cs` L15-L18, L34, L105-L134; replacement search in
  `SON Download/RedownloadFailedDownloadService.cs`).
- Blocklist row: `SeriesId, EpisodeIds, SourceTitle, Quality, Date, PublishedDate, Size, Protocol, Indexer,
  IndexerFlags, ReleaseType, Message, Source, TorrentInfoHash, Languages` (`SON Blocklisting/Blocklist.cs` L4-L28);
  consumed by `BlocklistSpecification` (L18-L26). A blocklisted release can still be forced via manual search
  ([WIKI-S settings#failed-download-handling](https://wiki.servarr.com/sonarr/settings#failed-download-handling)).
- History row (`EpisodeHistory`): `EpisodeId, SeriesId, SourceTitle, Quality, Date, EventType, Data (string map),
  Languages, DownloadId`; event types `Unknown, Grabbed, SeriesFolderImported, DownloadFolderImported, DownloadFailed,
  EpisodeFileDeleted, EpisodeFileRenamed, DownloadIgnored` (`SON History/EpisodeHistory.cs` L28-L47). The free-form
  `Data` map carries indexer, release group, size, download client (consts at L10-L15). Bazarr and the history
  specifications read these rows.
- Queue is a VIEW over tracked downloads, not a stored table: `Queue { Series, Episodes, Languages, Quality, Size,
  SizeLeft, TimeLeft, EstimatedCompletionTime, Status, TrackedDownloadStatus, TrackedDownloadState, StatusMessages,
  DownloadId, Protocol, DownloadClient, Indexer, OutputPath, ErrorMessage }` (`SON Queue/Queue.cs`). Activity badge
  counts items in client queue + last 60 history items not yet imported
  ([WIKI-S faq#why-is-there-a-number-next-to-activity](https://wiki.servarr.com/sonarr/faq#why-is-there-a-number-next-to-activity)).
- Scheduled tasks visible in System > Tasks include RSS Sync, Refresh Series, Check Health, Housekeeping, Backup,
  Messaging Cleanup ([WIKI-S system#scheduled](https://wiki.servarr.com/sonarr/system#scheduled)).

## 5. *arr v3 API surface used by integrators

### 5.1 Conventions

- Base path `/api/v3` (Seerr: `JSR routes/settings/sonarr.ts` L43; Prowlarr: `PRW Applications/Sonarr/SonarrV3Proxy.cs` L29;
  Bazarr: `BAZ sonarr/info.py` L111-L112). Radarr uses the same base (`PRW Applications/Radarr/RadarrV3Proxy.cs` L30).
- Authentication: server accepts the key as query `apikey`, or header `X-Api-Key`, or `Authorization: Bearer <key>`
  (`SONHTTP Authentication/ApiKeyAuthenticationHandler.cs` L36-L53; names `X-Api-Key` / `apikey` set in
  `SONHTTP Authentication/AuthenticationBuilderExtensions.cs` L49-L50; SignalR uses `access_token` L54-L55).
  Callers: Seerr sends `apikey` as a default query param (`OVS api/servarr/base.ts` L104; `JSR api/servarr/base.ts` L106);
  Prowlarr sends header `X-Api-Key` (`PRW Applications/Sonarr/SonarrV3Proxy.cs` L201); Bazarr sends `?apikey=` on
  every REST call (e.g. `BAZ sonarr/sync/utils.py` L74) and `access_token` on SignalR (`BAZ app/signalr_client.py` L164, L231).
- Lists are returned as bare JSON arrays except `/queue`, which is a paged envelope
  `{page, pageSize, sortKey, sortDirection, totalRecords, records[]}` (`JSR api/servarr/base.ts` interface `QueueResponse`).
- Provider-style resources (indexer, downloadclient, ...) share a shape: `Id, Name, Implementation, ImplementationName,
  ConfigContract, InfoLink, Tags, Fields[{name,value}]`, with a `/schema` endpoint listing available implementations
  (`PRW Applications/Sonarr/SonarrIndexer.cs` L10-L23; `.../SonarrV3Proxy.cs` L79).
- Long-running work is requested by `POST /command` `{name, ...args}` (Seerr `JSR api/servarr/base.ts` L236-L246).

### 5.2 Endpoint table

Callers: SEERR = Overseerr/Jellyseerr/Seerr, PRW = Prowlarr, BAZ = Bazarr. "Fields" = fields the caller reads or sends
(not the full resource).

| Endpoint | Method | Caller(s) | Purpose | Fields required (as used by caller) | Cite |
|---|---|---|---|---|---|
| `/system/status` | GET | SEERR, PRW, BAZ | connectivity + version (Bazarr falls back to unversioned `/api/system/status` for legacy v2) | `version` (BAZ reads `['version']`; PRW sends `X-Application-Version` check on `/indexer/test`) | `JSR base.ts` L119; `PRW Sonarr/SonarrV3Proxy.cs` L43; `BAZ sonarr/info.py` L33-L44 |
| `/qualityProfile` (Radarr/Sonarr v4) | GET | SEERR, BAZ | populate profile pickers; Bazarr maps `qualityProfileId` to names | `id`, `name` | `JSR base.ts` L133; `BAZ radarr/sync/utils.py` L15-L16 |
| `/languageprofile` (Sonarr v3 only) | GET | SEERR, BAZ | language profile pickers | `id`, `name` | `JSR sonarr.ts` L337; `BAZ sonarr/sync/utils.py` L20-L22 |
| `/rootfolder` | GET | SEERR, BAZ | destination folders; Bazarr mirrors them | `id`, `path` (+ `freeSpace`, `totalSpace`, `unmappedFolders[]` for Seerr UI) | `JSR base.ts` L150; `BAZ sonarr/rootfolder.py` L19 |
| `/tag` | GET / POST / PUT `/tag/{id}` | SEERR (all), BAZ (GET) | tag lookup / create / rename (Seerr tags requests by user) | `id`, `label` | `JSR base.ts` L186, L199, L219; `BAZ sonarr/sync/utils.py` L55 |
| `/queue?includeEpisode=true` | GET | SEERR | download progress for request cards | `records[].id, title, size, sizeleft, timeleft, estimatedCompletionTime, status, trackedDownloadStatus, trackedDownloadState, downloadId, protocol, downloadClient, indexer` + `episode` (Sonarr) / `movieId` (Radarr) | `JSR base.ts` L167; `JSR lib/downloadtracker.ts` L89-L99, L167-L179 |
| `/command` `{name:"RefreshMonitoredDownloads"}` | POST | SEERR | force queue refresh before reading `/queue` | `name` | `JSR base.ts` L233-L241; `downloadtracker.ts` L89, L167 |
| `/command` `{name:"MissingEpisodeSearch", seriesId}` | POST | SEERR | search a series | `name, seriesId` | `JSR sonarr.ts` L363 |
| `/command` `{name:"MoviesSearch", movieIds:[]}` | POST | SEERR | search a movie | `name, movieIds` | `JSR radarr.ts` L277 |
| `/series` | GET | SEERR (library scan), BAZ (full sync) | list all series | SEERR scanner: `tvdbId, seasons[].seasonNumber, seasons[].monitored, seasons[].statistics.episodeFileCount, .totalEpisodeCount`. BAZ parser: `id, title, sortTitle, year, path, tags, seriesType, qualityProfileId/profileId, languageProfileId, imdbId, tvdbId, overview, alternateTitles, images[].coverType/url, ended, lastAired, originalLanguage, monitored` | `JSR lib/scanners/sonarr/index.ts` L89, L193-L219; `BAZ sonarr/sync/utils.py` L72-L74 and `sonarr/sync/parser.py` |
| `/series?tvdbId=` , `/series/{id}` | GET | SEERR, BAZ | single lookup of an existing series | as above | `JSR sonarr.ts` L137, L152; `BAZ sonarr/sync/utils.py` L72-L74 |
| `/series/lookup?term=tvdb:<id>` | GET | SEERR | resolve a not-yet-added series (returns seasons, images, `remotePoster`) | `tvdbId, title, seasons[]` | `JSR sonarr.ts` L165, L189 |
| `/series` | POST | SEERR | add series on request | `tvdbId, title, qualityProfileId, languageProfileId?, seasons[{seasonNumber,monitored}], tags, seasonFolder, monitored, monitorNewItems, rootFolderPath, seriesType, addOptions{ignoreEpisodesWithFiles, searchForMissingEpisodes}` | `JSR sonarr.ts` L280-L303 |
| `/series` | PUT | SEERR | update an existing series (monitor more seasons, tags) | full series object with changed `seasons[].monitored`, `tags`, `monitored` | `JSR sonarr.ts` L222-L224 |
| `/series/{id}?deleteFiles=true&addImportExclusion=false` | DELETE | SEERR | remove on request deletion | `id` | `JSR sonarr.ts` L443 |
| `/episode?seriesId=` (BAZ adds `&includeEpisodeFile=true`), `/episode/{id}` | GET | SEERR, BAZ | episodes, monitored flags, file info | SEERR: `id, seasonNumber, episodeNumber, monitored, hasFile, episodeFileId, absoluteEpisodeNumber`. BAZ: `id, seriesId, seasonNumber, episodeNumber, title, monitored, hasFile, episodeFile{path,size,quality,mediaInfo{audioCodec,videoCodec,...},sceneName,languages}` | `JSR sonarr.ts` L378; `BAZ sonarr/sync/utils.py` L106-L111; `sonarr/sync/parser.py` |
| `/episode/monitor` | PUT | SEERR | re-monitor unmonitored episodes | `{episodeIds[], monitored:true}` | `JSR sonarr.ts` L394 |
| `/episodeFile?seriesId=`, `/episodeFile/{id}` | GET | BAZ | file path and media info for subtitle search | `id, path, size, quality, mediaInfo, sceneName, languages` | `BAZ sonarr/sync/utils.py` L140-L144 |
| `/history?eventType=1&episodeId=` | GET | BAZ | find original release name for the grabbed file | event `sourceTitle`, `data` | `BAZ sonarr/sync/utils.py` L175 |
| `/movie` , `/movie?tmdbId=`, `/movie/{id}` | GET | SEERR (scan), BAZ | list/lookup movies | SEERR scanner: `tmdbId, hasFile, monitored`. BAZ: `id, title, sortTitle, year, path, tags, imdbId, qualityProfileId, overview, alternativeTitles, images, originalLanguage, movieFile{path,size,quality,mediaInfo,sceneName,languages}, monitored` | `JSR radarr.ts` L74, L88, L103; `JSR lib/scanners/radarr/index.ts` L80, L156-L157; `BAZ radarr/sync/utils.py` L69-L70 |
| `/movie/lookup?term=tmdb:<id>` | GET | SEERR | resolve before add | `tmdbId, title, year` | `JSR radarr.ts` L116 |
| `/movie` | POST | SEERR | add movie on request | `title, qualityProfileId, profileId, titleSlug(=tmdbId), minimumAvailability, tmdbId, year, rootFolderPath, monitored, tags, addOptions{searchForMovie}` | `JSR radarr.ts` L226-L240 |
| `/movie` | PUT | SEERR | re-monitor/update existing movie | full movie object | `JSR radarr.ts` L156 |
| `/movie/{id}?deleteFiles=true&addImportExclusion=false` | DELETE | SEERR | remove | `id` | `JSR radarr.ts` L300 |
| `/history?eventType=1&movieIds=` | GET | BAZ | grabbed release name | event `sourceTitle` | `BAZ radarr/sync/utils.py` L100 |
| `/indexer` | GET | PRW | list indexers already in the app (filters `Implementation` in Newznab/Torznab to find ones Prowlarr owns by `baseUrl`/`apiKey`) | `id, implementation, fields[baseUrl, apiKey]` | `PRW Sonarr/SonarrV3Proxy.cs` L49; `PRW Sonarr/Sonarr.cs` L97-L107 |
| `/indexer/{id}` | GET / PUT / DELETE | PRW | read, update, remove a synced indexer | full indexer resource | `PRW Sonarr/SonarrV3Proxy.cs` L57, L73, L106 |
| `/indexer/schema` | GET | PRW | discover field definitions of `Newznab` and `Torznab` implementations (cached 7 days) | `implementation`, `fields[]` | `PRW Sonarr/SonarrV3Proxy.cs` L79; `PRW Sonarr/Sonarr.cs` L239 |
| `/indexer` (+`?forceSave=true` retry on HTTP 400) | POST | PRW | push an indexer | `enableRss, enableAutomaticSearch, enableInteractiveSearch, priority, name, implementation ("Newznab"/"Torznab"), configContract, tags, fields[baseUrl, apiPath("/api"), apiKey, categories, animeCategories, animeStandardFormatSearch, minimumSeeders, seedCriteria.seedRatio, seedCriteria.seedTime, seedCriteria.seasonPackSeedTime, plus a reject-blocklisted-torrent-hashes toggle]` (Radarr: same minus anime fields) | `PRW Sonarr/SonarrV3Proxy.cs` L85-L101; `PRW Sonarr/Sonarr.cs` L240, L257-L289; `PRW Radarr/Radarr.cs` L232, L253 |
| `/indexer/test` | POST | PRW | validate API key and URL before saving; reads response header `X-Application-Version` (min 3.0.5) | indexer resource | `PRW Sonarr/SonarrV3Proxy.cs` L127-L142 |
| `/signalr/messages?access_token=<key>` | WS (SignalR) | BAZ | real-time series/episode/movie change events to trigger partial syncs | event `name` in `series`/`episode`/`movie`, `action`, `body` | `BAZ app/signalr_client.py` L164, L231, L365-L395 |

Prowlarr in the other direction: it sets Sonarr's indexer `baseUrl` to `<ProwlarrUrl>/<indexerId>/` and `apiKey` to
Prowlarr's own key, i.e. Prowlarr itself serves a per-indexer Newznab/Torznab endpoint, and Sonarr/Radarr consume it
through the ordinary indexer client (`PRW Sonarr/Sonarr.cs` L269-L273). Sync level is Disabled / AddOnly / FullSync
(`PRW Applications/ApplicationSyncLevel.cs`).

Implication for any *arr-compatible server: Seerr needs exactly the field names above (camelCase JSON) and the
`/command` names; Prowlarr needs `/indexer`, `/indexer/schema`, `/indexer/test` plus the `X-Application-Version`
response header; Bazarr needs `/series`, `/episode`, `/episodeFile`, `/movie`, `/history`, SignalR.

## 6. Ebooks note: Readarr

- Status: RETIRED. The project README starts with "Announcement: Retirement of Readarr" (metadata "has become
  unusable", Open Library transition "has stalled", effective immediately, open to a new maintainer); the repo is
  archived. Source: `https://github.com/Readarr/Readarr/blob/develop/README.md`. The wiki FAQ is retitled
  "Readarr FAQ (Retired)" ([WIKI readarr/faq](https://wiki.servarr.com/readarr/faq)). The README names a third-party
  metadata mirror (rreading-glasses) as unsupported/at-your-own-risk.
- Model it used (useful as the nearest *arr analogue to ABO): `Author` -> `Book` -> `Edition` -> `BookFile`;
  `Book` has `ForeignBookId, Title, ReleaseDate, Monitored, AnyEditionOk, Genres, Ratings`; `Edition` has `Asin,
  Language, Format, IsEbook, Publisher, PageCount, Monitored`; `BookFile` has `Path, Size, Quality, MediaInfo,
  EditionId, CalibreId, Part, PartCount` (`RDR Books/Model/Book.cs`, `Edition.cs`; `RDR MediaFiles/BookFile.cs`).
  The README states "only one type of a given book is supported" per instance (ebook OR audiobook).
- Search: Newznab `t=book` with `author=` and `title=` params, `t=search` fallback
  (`RDR Indexers/Newznab/NewznabRequestGenerator.cs` L46-L68, L104). Standard categories Books 7000 / EBook 7020 and
  Audio 3000 / Audiobook 3030 (section 4.2).
- Lesson: the failure was the metadata source, not the acquisition pipeline; the pipeline in sections 4.1-4.8 was
  reused unchanged across Sonarr, Radarr and Readarr (same `IDownloadClient`, decision engine, tracked-download
  states; Readarr's `BookFile` carries the same `SceneName/ReleaseGroup/Quality/IndexerFlags/MediaInfo` columns as
  `EpisodeFile`/`MovieFile`).

## 7. Implications for a shared core

### 7.1 Capabilities a TV + Movies core must provide

| # | Capability | Evidence it is media-agnostic | Core or app? |
|---|---|---|---|
| 1 | Provider registry + settings schema (`Implementation`, `ConfigContract`, `Fields[]`, `/schema`, tags, enable flags) used for indexers, download clients, notifications, import lists | same resource shape for every provider; Prowlarr drives it generically (5.1) | CORE |
| 2 | Quality-profile ENGINE: ordered items + groups, cutoff, upgradeAllowed, min / cutoff / min-upgrade format score, index lookup, `IsUpgrade` comparison | `QualityProfile` has no TV-specific fields (3.2); Radarr's copy differs only by `Language` | CORE engine; the quality VOCABULARY (resolution/source for video; formats for audiobook) is app-supplied |
| 3 | Quality-definition size limits, per unit of duration | MB/min in both apps (3.1); needs runtime per item (`UnknownRuntime` rejection) | CORE with an app-supplied "duration of this wanted item" hook |
| 4 | Custom-format engine: spec list, Negate/Required, AND across types / OR within, per-profile scores, computed on the fly, usable on title or on-disk filename | `CustomFormatCalculationService` is generic; only spec TYPES differ (Sonarr: Release Type; Radarr: Edition) | CORE engine + generic specs (title regex, size, language, release group, indexer flag); app registers extras |
| 5 | Delay profiles + pending-release store (temporary rejection, retry on next RSS) | generic (3.3) | CORE |
| 6 | Indexer client: Newznab/Torznab caps, paging, category map, RSS fetch, item-attribute parsing, per-indexer priority/RSS/auto/interactive flags, tag scoping, rate-limit/failure back-off | one generator class per app but same base (4.2) | CORE transport + parser; the SEARCH-CRITERIA-to-query mapping (`tvsearch`+`season/ep`, `movie`+`tmdbid`, `book`+`author/title`) is app-level, registered per media type |
| 7 | Release-title parser (series/season/episode/date/absolute vs movie title+year vs author/title) | separate parsers per app (4.4 step 1) | APP-LEVEL |
| 8 | Decision engine runner: priority-grouped specs, Permanent vs Temporary rejections, accept/reject with reason enum, comparer chain | runner is generic; spec catalogue is mixed (4.4 table) | CORE runner + generic specs (size, age, retention, protocol, blocklist, queue-has-better, history, delay, free space, indexer tag, quality-allowed, upgrade-allowed, custom-format-min-score); APP specs: monitored-entity, season-pack, full-season, anime version, search-match, minimum availability |
| 9 | Download-client client: `IDownloadClient` (add / items / files / import-item / remove / status / mark-imported) with qBittorrent, Transmission, Deluge, SABnzbd, NZBGet; per-client priority, categories, post-import category, seed goals, remote path mapping | interface differs only by `RemoteEpisode` vs `RemoteMovie` parameter (4.6) | CORE (take an opaque "grab request" with title, link, hash, category) |
| 10 | Tracked-download state machine + poll loop (`Downloading -> ImportPending -> Importing -> Imported`, `FailedPending -> Failed`, `ImportBlocked`, `Ignored`) | states have no media vocabulary (4.7) | CORE state machine; the IMPORT step is an app callback (find media files, parse, map to library entity, rename, move/hardlink) |
| 11 | Import pipeline (naming-token engine, root folders, hardlink-or-copy, free-space check, recycle bin, extra files) | token engine and file ops generic; token sets and entity mapping differ (1.4, 2) | CORE file ops + token engine; APP token catalogue and matcher |
| 12 | History + blocklist + queue-view | event rows keyed by `EpisodeId`/`SeriesId`; free-form `Data` map (4.8) | CORE tables with (entityType, entityId) instead of episode columns; APP supplies event payloads |
| 13 | Failed-download handling (detect, mark failed, remove, blocklist, optional re-search) | generic (4.8) | CORE; "re-search" is an app callback |
| 14 | Command/task queue + scheduler + events (`POST /command`, RSS Sync, Refresh, Housekeeping, Backup) | Seerr/Prowlarr use `/command`; System > Tasks (4.8, 5.2) | CORE |
| 15 | Tags (series, indexers, delay profiles, release profiles) | cross-cutting scoping (3.3, 4.1) | CORE |
| 16 | API shell: `/api/v3`-style versioning, key auth via query / `X-Api-Key` / Bearer, paged `/queue` envelope, SignalR-style event feed | 5.1 | CORE |
| 17 | Metadata provider (TVDb proxy / TMDB / book source) + refresh job | per-app source; Readarr died on this (6) | APP-LEVEL (core supplies refresh scheduling only) |
| 18 | Calendar (air date / release-date views, iCal feed) | depends on `AirDateUtc` or Movie release dates (1.1, 2) | APP-LEVEL, core supplies a generic "dated wanted item" query if wanted |
| 19 | Monitoring semantics (Series/Season/Episode vs Movie flag; MonitorNewItems; add-time presets; MinimumAvailability) | structurally different (1.3, 2) | APP-LEVEL; core only stores a `monitored` boolean convention |
| 20 | Import lists, collections, series types, scene numbering/XEM, anime handling | TV- or movie-only | APP-LEVEL |
| 21 | Notifications (Connect) and media-server hooks | generic provider plumbing | CORE (provider) / APP (event payload) |

### 7.2 Design pressure points seen in the evidence

- Do not copy the Episode/Movie columns into core tables: `EpisodeHistory`, `Blocklist` and `Queue` all embed
  episode ids (`EpisodeIds`, `EpisodeId`); a shared core needs a polymorphic reference or per-app tables sharing a
  base schema.
- `IDownloadClient.Download` takes the app's `RemoteEpisode`/`RemoteMovie`; a shared interface should take a
  media-neutral grab request (title, download link or magnet, info-hash, category, seed goals, protocol).
- Quality is attached to the FILE row and the profile to the library-entity row, in both apps; keep that split.
- CF scores are per-profile and CFs have no effect on what is searched: keep scoring in the profile and keep search
  construction independent from CFs.
- `registry`-style duplication is real in the original: Sonarr and Radarr each carry their own `NewznabRequestGenerator`,
  `DownloadDecisionMaker`, `QualityProfile`, `DelayProfile` (`DelayProfile.cs` is byte-identical in both repos:
  `SON Profiles/Delay/DelayProfile.cs` vs `RAD Profiles/Delay/DelayProfile.cs`), which is the duplication a
  shared core would remove.
- Compatibility with Seerr/Prowlarr/Bazarr is a separate decision: it requires matching the exact v3 resource names
  in section 5.2, which is naturally an app-level adapter over core data, not a core feature.

## 8. Not verified / gaps

- `wiki.servarr.com` and `trash-guides.info` were unreachable; wiki claims come from the Servarr/Wiki markdown
  source (dated 2026-06 in the front-matter of `sonarr/system.md`) and may lag the live site by days.
- `RssSync/DelaySpecification` internals (bypass rules) and `CustomFormatAllowedByProfile` / minimum-format-score
  spec file were not located by guessing raw paths; the minimum-score behaviour is cited from the wiki and TRaSH only.
- Sonarr `develop` is ahead of the latest stable release; `MinUpgradeFormatScore`, release-profile air-date fields
  and per-profile quality sizes are v5-era per the wiki wording.
- Seerr: only `server/api/servarr/*.ts`, the two scanners and the download tracker were read; other call sites
  (e.g. request-to-Sonarr option assembly in `server/entity/MediaRequest.ts`) were not.
- Prowlarr's own outward Newznab/Torznab server endpoints were inferred from the `baseUrl` it pushes, not read.
- Radarr download-client and decision-engine internals were sampled (interface, comparer per wiki), not read line by line.
