<!-- file: docs/specs/platform-core/05-indexer.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5e2d5cda-5f98-4db2-87b1-b4c8d6519ec7 -->
<!-- last-edited: 2026-11-04 -->

# 05 — Indexers (`integrations/indexer`)

Evidence commit `748fd787a`. Raw inputs: [`raw/16-arr-domain-research.md`](../../plans/platform-core/raw/16-arr-domain-research.md)
§4.1-§4.4, §5.2 (Prowlarr rows), §6, §7.1 rows 1, 6, 7, 15; [`raw/11-integrations.md`](../../plans/platform-core/raw/11-integrations.md) §1, §4.
Builds on [05-metadata](05-metadata.md) §M1 (instances, health, taxonomy) and §M2 (`providerhttp`).

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

// Indexer is what every indexer adapter instance implements.
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

// ---- pool (per App) ---------------------------------------------------------

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

// RSS pages each RSS-enabled indexer from newest back to its persisted cursor
// (last GUID + published time), capped at core.indexer.rss_max_pages, and
// advances the cursor only after the caller acknowledges processing.
func (p *Pool) RSS(ctx context.Context, o RSSOptions) iter.Seq2[RSSBatch, error]

type RSSOptions struct {
	Types       map[integrations.Ref]SearchType // per-indexer RSS type; default TypeSearch with empty q (TV app passes TypeTV, Movies TypeMovie: raw/16 §4.2 RSS row)
	Parallelism tasks.Parallelism
}

type RSSBatch struct {
	Indexer  integrations.Ref
	Releases []Release
	Ack      func(ctx context.Context) error // advances the cursor in storage
}

func (p *Pool) Fetch(ctx context.Context, r Release) (download.Source, error)
func (p *Pool) Caps(ctx context.Context, ref integrations.Ref, refresh bool) (Caps, error)
```

Core-shipped adapters: `core.newznab` and `core.torznab` — one implementation, the adapter ID fixes
`Release.Protocol` and enables the Torznab attribute set. Settings: `base_url`, `api_path` (default `/api`),
`api_key` (secret), plus `InstanceSettings`. Auth is the `apikey` query parameter (raw/16 §4.2), always
redacted by providerhttp. Phase: built with `acquire` (phase 2 of [05-download](05-download.md) §4.1); ABO does
not need indexers to run on core, and an audiobook search (`CatAudiobook`, `TypeBook`/`TypeSearch`) is
possible later without contract change.

### 3.1 Prowlarr interop through the *arr facade

Prowlarr does not get a core-native integration; it drives the app's `/api/v3` facade ([08-arr-facade](08-arr-facade.md)),
which translates onto core's instance API. The contract core must offer the facade:

| Prowlarr call (raw/16 §5.2) | Facade translation onto core |
|---|---|
| `GET /api/v3/indexer/schema` | `GET /api/core/v1/integrations/adapters?kind=indexer` → *arr `{implementation: "Newznab"\|"Torznab", configContract, fields[]}`; field names `baseUrl`, `apiPath`, `apiKey`, `categories`, `animeCategories` (→ `ExtraCategories["anime"]` for a TV app; omitted by Movies), `minimumSeeders`, `seedCriteria.seedRatio/seedTime/seasonPackSeedTime` are a **fixed alias table** in the facade, not core field names |
| `POST /api/v3/indexer` (`?forceSave=true` retry on 400) | `Registry.Put` with `Origin=arrfacade`, tag `managed-by:prowlarr`; `forceSave` skips the connectivity test, never schema validation |
| `GET/PUT/DELETE /api/v3/indexer/{id}` | facade keeps a stable integer id ↔ `InstanceID` map in its own keyspace; PUT/DELETE map to `Put`/`Delete` |
| `POST /api/v3/indexer/test` | `Registry.Test` (unsaved); response header `X-Application-Version` set by the facade |
| `GET /api/v3/indexer` (Prowlarr finds "its" indexers by `baseUrl`/`apiKey`) | list instances; the facade compares `base_url` and a **hash** of the key (core never returns secret values: the facade answers with the masked form and Prowlarr's own match on `baseUrl` + implementation) — open item 2 |

Requirements this places on core (all satisfied by 05-metadata §M1): instances creatable by an API caller
holding `core:integrations.manage` via an API key; a stable external-origin marker; unsaved `Test`; settings
schema export; instance tags. Prowlarr then talks to each pushed indexer as an ordinary `core.newznab`/
`core.torznab` instance at `<prowlarr>/<id>/api` — no special code path.

## 4. Lifecycle

1. Instances `indexer/<id>` are created via UI, file, or facade (05-metadata §M1).
2. **Caps**: fetched lazily on first use and by `Test`, cached in `c/indexer/caps/<instance>` for
   `core.indexer.caps_ttl`; a caps fetch failure leaves the previous caps in use (marked stale) and the
   instance `HealthDegraded`. Defaults from raw/16 §4.2 apply when the server omits a section.
3. **Search**: `Pool.SearchAll` → per instance `Plan` → `Gate.Acquire("query")` (daily query limit) →
   paged requests (`offset += limit`, stop at `MaxPages` or a short page) → parse XML (RSS 2.0 items with
   `newznab:attr`/`torznab:attr`) → normalize → de-duplicate.
4. **RSS**: scheduled by the app (TV/Movies RSS-sync task in `acquire`, interval 10-120 min per raw/16 §4.1);
   core provides paging back to the cursor and acknowledgement-based cursor advance, so a crash mid-sync
   re-delivers rather than skips.
5. **Fetch** at grab time: `Gate.Acquire("grab")`, download through providerhttp with
   `EgressSameHostOrPublic` (links normally point at the indexer host or Prowlarr), size cap
   `core.indexer.max_fetch_bytes`, then hand `download.Source` to `download.Pool.Grab`.
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
| Every eligible indexer failed | closed: `ErrNoIndexerAnswered` (joined) | — | "No results" must be distinguishable from "nobody answered". |
| Caps refresh fails with cached caps present | open (stale caps used) | — | A transient caps failure must not stop searching. |
| Fetched file exceeds size cap / not a torrent or NZB | `ClassBadOutput` | closed | Never hand garbage to a download client. |
| RSS cursor GUID no longer on any page | open: process up to `rss_max_pages`, emit `RSSGap` outcome | — | Visible gap instead of silent loss. |

## 6. Concurrency contract

`Pool` and adapters are goroutine-safe. `SearchAll`/`RSS` fan out with `errgroup` + `SetLimit` from the
required `tasks.Parallelism` (network-bound; default recommendation 4); each indexer's own providerhttp
bucket enforces its rate regardless of fan-out. Pages of one indexer are fetched sequentially (offset
order). Caps fetch is single-flight per instance. Cursor `Ack` is a storage write keyed per instance;
concurrent RSS runs for the same instance are prevented by a per-instance keyed mutex (the app's RSS task
should also declare a `tasks.Resource` on `indexer/<id>`).

## 7. Config keys

| Key | Type | Default | Hot-reload |
|---|---|---|---|
| `core.integrations.indexer.instances.<id>.*` | instance row (+ `InstanceSettings`) | none | yes |
| per instance `limits.*` | providerhttp limits | RPS 0.5, burst 2, timeout 30s, retries 2 | yes |
| `core.indexer.caps_ttl` | duration | `24h` | yes |
| `core.indexer.per_indexer_timeout` | duration | `30s` | yes |
| `core.indexer.rss_max_pages` | int | `5` | yes |
| `core.indexer.max_fetch_bytes` | int | `10 MiB` | yes |
| `core.indexer.max_attrs_per_item` | int | `64` | yes |

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
`TestFetch_GrabCounterAndSizeCap`, `TestFetch_SameHostOrPublicEgress`, `TestRedaction_ApikeyNeverLogged`.
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

None beyond [05-metadata](05-metadata.md) "Objections to 03".

## Out-of-scope defects found

None.
