<!-- file: docs/specs/platform-core/05-indexer.md -->
<!-- version: 1.5.0 -->
<!-- guid: 5e2d5cda-5f98-4db2-87b1-b4c8d6519ec7 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Indexers (`integrations/indexer`)

Evidence commit `748fd787a`. Raw inputs: [`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md)
§4.1-§4.4, §5.2 (Prowlarr rows), §6, §7.1 rows 1, 6, 7, 15; [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) §1, §4.
Builds on [05-metadata](05-metadata.md) §M1 (instances, health, egress trust, taxonomy) and §M2 (`providerhttp`).

> **Deferred-build: not in the ABO extraction plan** (cross-cutting resolution 1, SIMP-1). ABO consumes no
> indexer. This file is the contract of record at proof depth; `integrations/indexer` is built with `acquire`
> when the first TV/Movies app starts ([10](10-tv-movies-proof.md)), and [09](09-abo-on-core.md) S7 does not
> create the package.

## 1. Purpose & scope

A Newznab/Torznab client every app shares: **capability discovery** (`t=caps`), the five search types
(`search`, `tvsearch`, `movie`, `book`, `music`), standard **categories** (including 3030 Audiobook), **RSS**
paging with a persisted cursor, result normalization into a media-neutral `Release`, fetching the
`.torrent`/`.nzb`, per-indexer **rate limits, daily query/grab limits and back-off**, and the instance shape
that lets **Prowlarr push indexers** into an app through the *arr facade.

The split, from the evidence (raw/16 §7.1 rows 6-7): **core** owns transport, caps, paging, category map,
item-attribute parsing, per-indexer flags and back-off; the **app** owns the mapping from its search criteria
to a `Query` (`tvsearch`+`season/ep`, `movie`+`tmdbid`, `book`+`author/title`) and parses release titles into
its own type. Decision engine and RSS-sync orchestration belong to `acquire` (10).

## 2. Current state at HEAD

| Fact | Evidence |
|---|---|
| No indexer code of any kind: case-insensitive grep for newznab/torznab/prowlarr/jackett over `internal`, `pkg`, `cmd` finds nothing | raw/11 §1 row "qBittorrent / SABnzbd / Transmission / NZB / indexers", §4 last bullet; re-verified at `748fd787a` |
| XML parsing in the repo uses only `encoding/xml` (iTunes plist) | `internal/itunes/plist_parser.go:10`, `internal/itunes/parser.go:10` |
| Sonarr's protocol use: caps XML with `<searching>`/`<categories>`; defaults when caps absent (page 100; tv params `q,rid,season,ep`; Radarr `q,imdbid,imdbtitle,imdbyear`); request `?t=&cat=&extended=1&apikey=&offset=&limit=`; id params only if caps list them; search skipped if nothing usable | raw/16 §4.2 table |
| Torznab = Newznab + `seeders, peers, infohash, magneturl, downloadvolumefactor`; one generator serves both | raw/16 §4.2 |
| Indexer definition flags: `EnableRss`, `EnableAutomaticSearch`, `EnableInteractiveSearch`, `Priority` (default 25, 1 = highest), `DownloadClientId`, `Tags`; torrent extras: min seeders, seed ratio/time | raw/16 §4.1 |
| RSS: fetch recent from every RSS-enabled indexer; page back to the last processed release after downtime; interval 10-120 min | raw/16 §4.1, §4.3 |
| Prowlarr pushes indexers into Sonarr/Radarr via `/api/v3/indexer` (POST/PUT/DELETE), `/indexer/schema`, `/indexer/test` (reads `X-Application-Version`), with `baseUrl=<prowlarr>/<id>/`, `apiPath=/api`, Prowlarr's `apiKey`; sync levels Disabled/AddOnly/FullSync | raw/16 §5.2 Prowlarr rows and the paragraph after the table |
| Readarr searched with `t=book&author=&title=` and fell back to `t=search` | raw/16 §6 |

## 3. Go API

```go
package indexer // github.com/falkcorp/mediacore/integrations/indexer

type SearchType string

const (
	TypeSearch   SearchType = "search"
	TypeTV       SearchType = "tvsearch"
	TypeMovie    SearchType = "movie"
	TypeBook     SearchType = "book"
	TypeMusic    SearchType = "music"
)

// Param is a Newznab query parameter name as advertised in caps supportedParams.
type Param string

const (
	ParamQ        Param = "q"
	ParamSeason   Param = "season"
	ParamEp       Param = "ep"
	ParamTVDB     Param = "tvdbid"
	ParamTMDB     Param = "tmdbid"
	ParamIMDB     Param = "imdbid" // digits without "tt" (raw/16 §4.2 movie row)
	ParamRage     Param = "rid"
	ParamTVMaze   Param = "tvmazeid"
	ParamAuthor   Param = "author"
	ParamTitle    Param = "title"
	ParamArtist   Param = "artist"
	ParamAlbum    Param = "album"
	ParamYear     Param = "year"
)

// Standard Newznab categories (raw/16 §4.2, Prowlarr NewznabStandardCategory).
const (
	CatMovies    = 2000
	CatMoviesSD  = 2030
	CatMoviesHD  = 2040
	CatMoviesUHD = 2045
	CatAudio     = 3000
	CatAudiobook = 3030
	CatTV        = 5000
	CatTVSD      = 5030
	CatTVHD      = 5040
	CatTVUHD     = 5045
	CatTVAnime   = 5070
	CatBooks     = 7000
	CatEBook     = 7020
)

// Query is media-neutral; apps build it from their own criteria.
type Query struct {
	Type       SearchType
	Params     map[Param]string // only params the indexer's caps support are sent (Plan drops the rest)
	Categories []int
	Offset     int
	Limit      int  // 0 = caps default
	Extended   bool // default true: request all newznab:attr
}

type Category struct {
	ID       int
	Name     string
	Children []Category
}

type SearchCaps struct {
	Available bool
	Params    []Param
}

type Caps struct {
	ServerTitle, ServerVersion string
	LimitDefault, LimitMax     int                       // default 100 when absent
	Searching                  map[SearchType]SearchCaps // defaults per raw/16 §4.2 when absent
	Categories                 []Category
	RetentionDays              int // 0 unknown
	FetchedAt                  time.Time
}

// Plan validates q against caps: drops unsupported params, rejects a search
// with no usable param (ClassUnsupported), and falls back from a specific
// type to TypeSearch with ParamQ when the specific type is unavailable and
// q carries text (Readarr's book→search fallback, raw/16 §6).
func Plan(q Query, caps Caps) (Query, error)

// Release is the normalized item. Neutral fields only; app-specific meaning
// comes from Attrs/IDs and the app's own title parser.
type Release struct {
	Indexer      integrations.Ref
	GUID         string
	Title        string
	Protocol     download.Protocol // Torrent for Torznab instances, Usenet for Newznab
	DownloadURL  string            // .torrent/.nzb link (redacted in logs: carries apikey)
	MagnetURL    string
	InfoURL      string            // comments/details page
	InfoHash     string            // torrent, lowercase hex
	Size         int64
	PublishedAt  time.Time
	Categories   []int
	Grabs        *int
	Files        *int
	Seeders      *int
	Peers        *int
	DownloadVolumeFactor *float64 // 0 = freeleech
	UploadVolumeFactor   *float64
	MinimumRatio         *float64
	MinimumSeedTime      *time.Duration
	IDs          map[Param]string    // tvdbid, imdb, tmdbid, rageid... as the item reported them
	Attrs        map[string][]string // every newznab:/torznab:attr, verbatim (bounded)
	IndexerPriority int              // copied from the instance for comparers (raw/16 §4.4)
}

// Parsed attaches an app's parse result to a release.
type Parsed[P any] struct {
	Release
	Info P
}

// Parse runs an app-supplied parser over releases; unparseable ones are
// returned separately with their reason (the decision engine's UnableToParse).
func Parse[P any](rs []Release, parse func(Release) (P, error)) (ok []Parsed[P], rejected []Rejected)

type Page struct {
	Releases []Release
	Offset   int
	Total    int // -1 unknown
}

// Indexer is what every indexer adapter instance implements. Adapters declare
// Meta.Egress = EgressConfigured for the API URL; Fetch uses
// EgressSameHostOrPublic (05-metadata M1.3, SEC-15).
type Indexer interface {
	integrations.Instance
	Caps(ctx context.Context, refresh bool) (Caps, error) // cached; refresh forces t=caps
	Search(ctx context.Context, q Query) (Page, error)    // q already Planned by the Pool
	// Fetch downloads the .torrent/.nzb behind a release with the indexer's
	// credentials (counts against the grab limit), or returns the magnet.
	Fetch(ctx context.Context, r Release) (download.Source, error)
}

func Adapter(m integrations.Meta,
	build func(ctx context.Context, env integrations.Env, inst integrations.InstanceConfig) (Indexer, error),
) integrations.Descriptor

// Settings common to every indexer instance (appended to the adapter schema).
type InstanceSettings struct {
	EnableRSS               bool
	EnableAutomaticSearch   bool
	EnableInteractiveSearch bool
	DownloadClient          *integrations.Ref // pinned client (raw/16 §4.1)
	Categories              []int
	ExtraCategories         map[string][]int  // app-named extra sets, e.g. TV "anime" (Sonarr AnimeCategories)
	MinimumSeeders          int               // torrent
	SeedGoal                *download.SeedGoal
	QueryLimitPerDay        int               // 0 = none → providerhttp daily counter "query"
	GrabLimitPerDay         int               // 0 = none → providerhttp daily counter "grab"
}

// ---- pool (per App; modules reach it as module.Deps.Indexers, 06 / COR-1) ----

type Pool struct{ /* unexported */ }

type Purpose uint8

const (
	PurposeRSS Purpose = iota
	PurposeAutomatic
	PurposeInteractive
)

type SearchOptions struct {
	Purpose     Purpose          // filters instances by their Enable* flag
	Tags        []string         // instance tags must intersect (raw/16 §4.4 IndexerTag spec)
	Instances   []integrations.Ref // explicit subset; nil = all eligible
	Parallelism tasks.Parallelism  // network-bound; required (03 R7)
	PerIndexer  time.Duration      // default 30s
	MaxPages    int                // default 1 for automatic, up to caps for interactive
}

type IndexerOutcome struct {
	Indexer  integrations.Ref
	Releases int
	Class    integrations.Class
	Err      error
	Elapsed  time.Duration
}

// SearchAll fans a query out (each indexer Plans it against its own caps),
// de-duplicates by (indexer, GUID) and returns partial results plus one
// outcome per indexer. err is non-nil only when every eligible indexer failed.
func (p *Pool) SearchAll(ctx context.Context, q Query, o SearchOptions) ([]Release, []IndexerOutcome, error)

// RSS pages each (RSS-enabled indexer, feed) pair from newest back to its
// persisted cursor (last GUID + published time), capped at
// core.indexer.rss_max_pages, and advances the cursor only after the caller
// acknowledges processing. One feed per consuming module (COR-25): with TV and
// Movies in one app sharing an indexer, each gets its own query type,
// categories and cursor, so neither module's RSS type wins over the other's.
func (p *Pool) RSS(ctx context.Context, o RSSOptions) iter.Seq2[RSSBatch, error]

type RSSOptions struct {
	Feeds       []Feed // required, ≥1; acquire passes one per registered media type
	Parallelism tasks.Parallelism
}

// Feed is one RSS query shape (raw/16 §4.2 RSS row: TV passes TypeTV, Movies
// TypeMovie). An indexer whose caps lack Type falls back to TypeSearch with
// empty q and the same Categories (Plan).
type Feed struct {
	Name       string // cursor namespace, the module ID ("tv", "movies")
	Type       SearchType
	Categories []int
}

type RSSBatch struct {
	Indexer  integrations.Ref
	Feed     string
	Releases []Release
	Ack      func(ctx context.Context) error // advances c/indexer/rss/<instance>/<feed>
}

func (p *Pool) Fetch(ctx context.Context, r Release) (download.Source, error)
func (p *Pool) Caps(ctx context.Context, ref integrations.Ref, refresh bool) (Caps, error)
```

Core-shipped adapters: `core.newznab` and `core.torznab` — one implementation, the adapter ID fixes
`Release.Protocol` and enables the Torznab attribute set. Settings: `base_url`, `api_path` (default `/api`),
`api_key` (secret), plus `InstanceSettings`. Auth is the `apikey` query parameter (raw/16 §4.2), always
redacted by providerhttp. Phase: built with `acquire` (phase 2 of [05-download](05-download.md) §1; deferred-build);
ABO does not need indexers to run on core, and an audiobook search (`CatAudiobook`, `TypeBook`/`TypeSearch`) is
possible later without contract change. Instances are records in the one instance registry (05-metadata M1,
SIMP-4), always in core's `NativeInstanceStore`: no app has pre-core indexer instances, so no legacy store covers
them (R4-1); their `query`/`grab` quota counters are in `NativeGateStore` (05-metadata M2.4). The API key is the
secret `integrations.indexer.<id>.api_key` (05-config §2.3 grammar).

### 3.1 Prowlarr interop through the *arr facade

Prowlarr does not get a core-native integration; it drives the app's `/api/v3` facade ([08-arr-facade](08-arr-facade.md)),
which translates onto core's instance API. The contract core must offer the facade:

| Prowlarr call (raw/16 §5.2) | Facade translation onto core |
|---|---|
| `GET /api/v3/indexer/schema` | `GET /api/core/v1/integrations/adapters?kind=indexer` → *arr `{implementation: "Newznab"\|"Torznab", configContract, fields[]}`; field names `baseUrl`, `apiPath`, `apiKey`, `categories`, `animeCategories` (→ `ExtraCategories["anime"]` for a TV app; omitted by Movies), `minimumSeeders`, `seedCriteria.seedRatio/seedTime/seasonPackSeedTime` are a **fixed alias table** in the facade, not core field names |
| `POST /api/v3/indexer` (`?forceSave=true` retry on 400) | `FacadeWriter.Put` (forces `Origin=arrfacade`, `Local=false`), tag `managed-by:prowlarr`; `forceSave` skips the connectivity test, never schema or URL validation |
| `GET/PUT/DELETE /api/v3/indexer/{id}` | facade keeps a stable integer id ↔ `InstanceID` map in its own keyspace; PUT/DELETE map to `FacadeWriter.Put`/`Delete` (only `Origin=arrfacade` instances) |
| `POST /api/v3/indexer/test` | `FacadeWriter.Test` (unsaved; class and status only, no body); response header `X-Application-Version` set by the facade |
| `GET /api/v3/indexer` (Prowlarr finds "its" indexers by `baseUrl`/`apiKey`) | list instances; the facade compares `base_url` and a **hash** of the key (core never returns secret values: the facade answers with the masked form and Prowlarr's own match on `baseUrl` + implementation) — open item 2 |

Requirements this places on core (all satisfied by 05-metadata §M1, as **deferred-build members** of the
packages that hold them; SIMP2-6): the facade calls `Registry.Put` only
through **`integrations.FacadeWriter`**, which accepts the principal when it holds `<m>:arr.indexers` (the
facade-confined key of [08](08-arr-facade.md) §8.5, which never carries `core:integrations.manage`) and forces
`Origin=arrfacade` with the egress restrictions of SEC-15: `Local` is always false, and every URL field must be
http(s) and resolve to public addresses or a host the operator listed in `core.integrations.egress.lan_allow`
(a Prowlarr on the LAN needs that entry). Also: a stable external-origin marker; unsaved `Test` that returns no
remote text; settings schema export; instance tags. The rule that a facade-confined key is refused on every
non-facade route is owned by [05-http-api](05-http-api.md) (the stage-13 facade clause, SEC-5). Prowlarr then
talks to each pushed indexer as an ordinary `core.newznab`/`core.torznab` instance at `<prowlarr>/<id>/api` — no
special code path.

**Deferred-build members this section depends on** (SIMP2-6; deferral applies to features inside built
packages as well as to packages, 03 §3.4). None of them is built in 09's S2, S4 or S7; all land with `arrfacade`
([08](08-arr-facade.md)) together with this package:

| Member | Package (built stage) | Owning spec |
|---|---|---|
| `integrations.FacadeWriter`, `Registry.FacadeWriter`, `Origin "arrfacade"` | `integrations` (S7) | [05-metadata](05-metadata.md) M1.3 |
| `KeySpec.Purpose`/`Module`, `PurposeFacade`, `X-Api-Key`/`apikey` query-key extraction | `auth` (S4) | [05-authn](05-authn.md) |
| `Router.FacadeGroup`, `RouteKind Facade`, the stage-13 facade refusal | `httpapi` (S2) | [05-http-api](05-http-api.md) |

Until they land, no facade-confined key can be minted, so SEC-5's refusal holds trivially.

## 4. Lifecycle

1. Instances `indexer/<id>` are created via UI, API, or the facade's `FacadeWriter` (05-metadata §M1).
2. **Caps**: fetched lazily on first use and by `Test`, cached in `c/indexer/caps/<instance>` for
   `core.indexer.caps_ttl`; a caps fetch failure leaves the previous caps in use (marked stale) and the
   instance `HealthDegraded`. Defaults from raw/16 §4.2 apply when the server omits a section.
3. **Search**: `Pool.SearchAll` → per instance `Plan` → `Gate.Acquire("query")` (daily query limit) →
   paged requests (`offset += limit`, stop at `MaxPages` or a short page) → parse XML (RSS 2.0 items with
   `newznab:attr`/`torznab:attr`) → normalize → de-duplicate.
4. **RSS**: scheduled by `acquire` (`core.acquire.rss-sync` on the schedule key `core.acquire.schedules.rss_sync`,
   every 10-120 min per raw/16 §4.1; [10](10-tv-movies-proof.md) §10.5.7); core
   provides one cursor per (instance, feed), paging back to it, and acknowledgement-based cursor advance, so a
   crash mid-sync re-delivers rather than skips.
5. **Fetch** at grab time: `Gate.Acquire("grab")`, download through providerhttp with
   `EgressSameHostOrPublic` (links normally point at the indexer host or Prowlarr; the "same host" is the
   instance's configured host under that instance's own guard, so a facade-pushed indexer cannot use it to
   reach the LAN), size cap `core.indexer.max_fetch_bytes`, then hand `download.Source` to `download.Pool.Grab`.
6. **Back-off**: providerhttp breaker and holds per instance (05-metadata §M2) plus Newznab error-code
   classification (§5); `Health` exposes `Hold.Until`, which is the "disabled till" users see in *arr UIs.

## 5. Error semantics

Newznab returns errors as `<error code="N" description="…"/>` with HTTP 200 on many servers; the client
parses the body before trusting the status (the same "body before status" rule as
`internal/metadata/throttle.go:168-176`). Codes per the Newznab API specification, confirmed with recorded
fixtures when built:

| Error path | Class | Fail-open / closed | Why |
|---|---|---|---|
| Code 100/101/102 (credentials, suspended, not authorized), 910 (API disabled), HTTP 401/403 without rate vocabulary | `ClassAuth` | closed for that indexer; hold 6h; others continue | Needs a human. |
| Code 500 (request limit reached) | `ClassQuota` (query counter) | closed until next quota day or `Retry-After` | Retrying burns goodwill. |
| Code 501 (download limit reached) | `ClassQuota` (grab counter) | closed for Fetch; Search continues | Separate allowance. |
| Code 200/201 (missing/incorrect parameter), 202/203 (no such/unavailable function) | `ClassInvalid` / `ClassUnsupported`; 202/203 also invalidate cached caps | closed for that query | Our query was wrong for this server. |
| Code 300 (no such item) | `ClassNotFound` | n/a | An answer, not a failure. |
| Code 900, HTTP 5xx, transport | `ClassTransient` | retry per providerhttp; then breaker | Server-side blip. |
| HTTP 429 / rate vocabulary | `ClassRateLimited` | hold ≥15m (Retry-After honoured, clamped) | M2 rule. |
| Malformed XML / not RSS | `ClassBadOutput` | closed for that indexer; does not count against breaker | Likely a proxy/login page; surfaced in health. |
| `Plan` finds no usable param | `ClassUnsupported` | skip that indexer, outcome recorded | Sonarr skips such searches (raw/16 §4.2). |
| `core.integrations.disabled` true (a staging copy, R6-7; 05-metadata M1.4) | `ClassRefusedLocal` (`ErrDisabled` from `Lease`) | closed for every indexer: no search, RSS page or fetch is sent, no RSS cursor moves | A copy of a production store must never query or download from production indexers (OPS6-4). |
| Every eligible indexer failed | closed: `ErrNoIndexerAnswered` (joined) | — | "No results" must be distinguishable from "nobody answered". |
| Caps refresh fails with cached caps present | open (stale caps used) | — | A transient caps failure must not stop searching. |
| Fetched file exceeds size cap / not a torrent or NZB | `ClassBadOutput` | closed | Never hand garbage to a download client. |
| RSS cursor GUID no longer on any page | open: process up to `rss_max_pages`, emit `RSSGap` outcome | — | Visible gap instead of silent loss. |

## 6. Concurrency contract

`Pool` and adapters are goroutine-safe. `SearchAll`/`RSS` fan out with `errgroup` + `SetLimit` from the
required `tasks.Parallelism` (network-bound; default recommendation 4); each indexer's own providerhttp
bucket enforces its rate regardless of fan-out. Pages of one indexer are fetched sequentially (offset
order). Caps fetch is single-flight per instance. Cursor `Ack` is a storage write keyed per instance;
concurrent RSS runs for the same instance are prevented by a per-instance keyed mutex (the RSS task also
declares the write resource `tasks.Res("core", "indexer").With(<id>)`, a `tasks.Resource` = `entity.Ref`).

## 7. Config keys

Protection classes per 05-config §1.3.7 (SEC2-8); egress trust is the instance's `Local` flag and
`core.integrations.egress.lan_allow` (05-metadata M1.3, M1.7).

| Key | Type | Default | Hot-reload | Protection |
|---|---|---|---|---|
| `indexer/<id>` instance records (+ `InstanceSettings`) | 05-metadata M1 store, not config (SIMP-4) | none | yes | instance rules (05-metadata M1.3) |
| instance setting `limits.*` | providerhttp limits | RPS 0.5, burst 2, timeout 30s, retries 2 | yes | instance rules |
| `core.indexer.caps_ttl` | duration | `24h` | yes | none |
| `core.indexer.per_indexer_timeout` | duration | `30s` | yes | none |
| `core.indexer.rss_max_pages` | int | `5` | yes | none |
| `core.indexer.max_fetch_bytes` | int | `10 MiB` | yes | none |
| `core.indexer.max_attrs_per_item` | int | `64` | yes | none |

## 8. Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_indexer_queries_total` | counter | `instance`, `type`, `class` |
| `mediacore_indexer_releases_total` | counter | `instance` |
| `mediacore_indexer_query_seconds` | histogram | `adapter`, `type` |
| `mediacore_indexer_fetches_total` | counter | `instance`, `class` |
| `mediacore_indexer_caps_refresh_total` | counter | `instance`, `result` |
| `mediacore_indexer_rss_gap_total` | counter | `instance` |

## 9. Tests

`TestPlan_DropsUnsupportedParams`, `TestPlan_SkipsWhenNothingUsable`, `TestPlan_BookFallsBackToSearch`,
`TestCaps_DefaultsWhenSectionsMissing`, `TestCaps_StaleOnRefreshFailure`, `TestParse_TorznabAttrs`
(seeders, peers, infohash, magneturl, downloadvolumefactor), `TestParse_NewznabAttrs`,
`TestErrors_CodeTable` (one fixture per code above), `TestErrors_Error200BodyBeatsStatus`,
`TestSearchAll_PartialResultsAndOutcomes`, `TestSearchAll_AllFailedIsError`, `TestSearchAll_FanOutBounded`,
`TestRSS_PagesBackToCursor`, `TestRSS_AckAdvancesCursorOnly`, `TestRSS_GapReported`,
`TestRSS_FeedPerModuleOwnCursor`, `TestFetch_GrabCounterAndSizeCap`, `TestFetch_SameHostOrPublicEgress`,
`TestFetch_FacadeIndexerSameHostStillGuarded`, `TestRedaction_ApikeyNeverLogged`,
`TestSearchAll_IntegrationsDisabledSendsNothing` (`core.integrations.disabled`: the fake server sees no request and
no cursor advances; R6-7).
Fakes: `coretest.NewznabServer(t, fixtures)` — an `httptest.Server` serving caps/search/RSS/error XML from
fixtures with request assertions; `coretest.FakeIndexer` for app-level tests. Facade-side Prowlarr
round-trip tests live with 08.

## 10. Migration from ABO

Nothing to migrate: ABO has no indexer code (§2). The package is new. ABO may later register an audiobook
search criteria → `Query` mapping (`TypeBook` with `author`/`title`, categories `CatAudiobook`) when an
acquisition feature is planned for it; that is a books-module decision, not part of the extraction.

## 11. Open items

1. Newznab error-code meanings and HTTP status conventions vary by server; the table in §5 is the
   specification, verified against recorded fixtures from at least two server implementations.
2. Prowlarr's `GET /api/v3/indexer` matching expects to read back `apiKey`; core never returns secret values.
   The facade must either return a stable masked token that Prowlarr's comparison tolerates or accept that
   Prowlarr re-PUTs on each sync. To be settled in 08 with a recorded Prowlarr session.
3. Native (non-facade) indexer management APIs of Prowlarr are not used; if a future app wants to *pull*
   from Prowlarr instead of being pushed to, that is an additional adapter, not a contract change.

## Objections to 03

None.

## Out-of-scope defects found

None.

## Review history

Round 1 (`docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- Cross-cutting resolution 1 / SIMP-1: marked deferred-build (not in the ABO extraction plan); contract kept at proof depth.
- SEC-5: facade writes go through `integrations.FacadeWriter` (`<m>:arr.indexers`, forced `Origin=arrfacade`);
  the `core:integrations.manage` API-key requirement removed; refusal rule referenced to 05-http-api §4.2.
- SEC-15: facade-pushed URLs validated (public or `lan_allow`); same-host fetches stay under the instance's guard.
- COR-25: `RSSOptions.Feeds` — one feed (type, categories, cursor) per consuming module.
- COR-1: modules reach the Pool as `module.Deps.Indexers`.
- SIMP-4: instance records in the 05-metadata M1 store; secret `integrations/indexer/<id>/api_key` (superseded by
  the dotted form `integrations.indexer.<id>.api_key` in the integration pass).
- SIMP-16 / COR-17: RSS resource written as `tasks.Res(...)` (= `entity.Ref`).

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-E request (SIMP-4) — secret name in dotted form.

Round 2 (`docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- SIMP2-6 — §3.1 lists the facade machinery it relies on (`FacadeWriter`, `PurposeFacade`, query-key extraction,
  `FacadeGroup`, the stage-13 clause) as deferred-build members of `integrations`, `auth` and `httpapi`, built with
  `arrfacade`, not in S2/S4/S7; the refusal reference points at the stage-13 clause.
- R2-7 — §4 names acquire's schedule key `core.acquire.schedules.rss_sync`.
- SEC2-8 — §7 gains a Protection column.
- SIMP2-16 — the slash-form secret-name history line marked superseded by the dotted form.

Round 4 (`docs/plans/platform-core/reviews/spec-round-4-dispositions.md`):

- R4-1 / SIMP4-3 — §3: indexer instances and their named quota counters live in core's native stores
  (`NativeInstanceStore`, `NativeGateStore`); no legacy store covers them.

Round 6 (`docs/plans/platform-core/reviews/spec-round-6-dispositions.md`):

- R6-7 / OPS6-4 — §5 row and §9 `TestSearchAll_IntegrationsDisabledSendsNothing`: with
  `core.integrations.disabled` (05-metadata M1.4) no indexer request is sent and no RSS cursor moves.
