<!-- file: docs/plans/platform-core/raw/11-integrations.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3c77ab87-cee3-4691-8d1d-b3629df429c6 -->
<!-- last-edited: 2026-11-03 -->

# 11 - External integrations inventory

Anchors are against the checked-out HEAD `57fd77ffc` (the brief said `748fd787a`; the working tree is at `57fd77ffc`, a docs-only commit later, so source anchors should be equivalent but re-verify if the SHA matters). LOC = non-test Go lines (`wc -l`), rounded.

## 1. Summary table

| Integration | Package (LOC) | Abstraction? | Impl count | Secrets/config source | Rate limit / health | Generic vs audiobook |
|---|---|---|---|---|---|---|
| OpenAI chat / batch / vision | `internal/ai` (7.1k), `internal/ai/aijobs` (575), `internal/ai/resultjournal` (263) | concrete `*OpenAIParser` (`internal/ai/openai_parser.go:261`); narrow consumer-side interfaces only | 1 client, pointed at any OpenAI-compatible base URL (`NewOpenAIParserWithBaseURL`, :358) | `config.OpenAIAPIKey`, per-endpoint `auth_ref` (`internal/ai/pool_routing.go:75`) | per-endpoint cooldown + slots (`internal/aidispatch/health.go`); `TestConnection` (`openai_parser.go:878`) | Client mechanics generic; prompts/methods (ParseFilename, ParseAudiobook, ParseCoverArt) audiobook |
| AI capability routing | `internal/aidispatch` (1.8k) | yes, concrete but generic: `Dispatcher`, `Endpoint`, `Capability`, generic `Call[T]` (`dispatch.go:347`) | n/a (router) | endpoints from setting `ai_endpoints` (`internal/config/ai_endpoints.go:36`); `AuthRef` names a secret, never stores it (`dispatch.go:75`) | `CooldownBase 30s`, `CooldownMax 5m`, `QuotaCooldown 30m` (`health.go:16-24`); per-endpoint `Concurrency` | Mechanism generic; the registered capability list is audiobook (see 2) |
| Embeddings | `internal/ai/embedding_client.go` | `embeddingAPI` (private, `embedding_scorer.go:19`); `EmbeddingCache` (:55) | 1 | OpenAI key / `Embedding.BaseURL` | routed via pool (`pool_routing.go:199`) | generic client, audiobook scorer |
| Whisper transcription | `internal/transcribe` (8.3k) | none (package-level funcs `TranscribeBatch`, `TranscribeBatchOpts`, `batch.go:68,121`); `ResultJournal` iface (`batch.go:75`) | HTTP whisper servers + local process (`aidispatch.ProtocolWhisperServer/LocalProcess`, `dispatch.go:30-34`) | `ParseWhisperEndpoints` (`config.go:2122`), 6 `whisper.*` keys | own pool cooldown (`transcribe/dispatcher.go:77-92`), canary probes (`canary.go`) | Audiobook (intro-clip transcription); dispatcher is a near-duplicate of aidispatch |
| Metadata providers (Audible, Audnexus, Google Books, Hardcover, Open Library, Wikipedia) | `internal/metadata` (6 clients ~2.3k LOC), `internal/metafetch` (21k) | YES: `MetadataSource` (`internal/metadata/source.go:52`) | 6 (+ decorator `ProtectedSource`, `circuitbreaker.go:138`) | `config.MetadataSource.Credentials map` (`config.go:148-163`), plus flat `HardcoverAPIToken`, `GoogleBooksAPIKey` (`config.go:1119-1123`) | per-provider token bucket `providerhttp`; persisted throttle registry; daily-quota budget; circuit breaker | Interface shape generic; `BookMetadata` struct is audiobook (Narrator, ASIN, DurationSec, Abridged: `openlibrary.go:148-190`) |
| Open Library offline dumps | `internal/openlibrary` (843) | none; `OLStore` concrete Pebble store (`store.go:28`) | 1 | `openlibrary_dump_*` config | download tracker only | generic-ish book data |
| AcoustID | `internal/acoustid` (224), `internal/plugins/acoustid` | none; `acoustid.Client` concrete (`client.go:84`) | 1 | `acoustid_api_key` / env (`config.go:2702`) | 15s HTTP timeout, `parseRetryAfter` (`client.go:50`); `acoustid_nightly_limit` | Audiobook-specific use (fingerprint dedup) |
| Fingerprinting (fpcalc + windowing) | `internal/fingerprint` (5.2k) | `ToolResolver` (`window_exec.go:111`), `FileWithFingerprint` (`calculator.go:21`) | subprocess only | tool path resolution | n/a | Audiobook |
| Deluge | `internal/deluge` (2.3k), `internal/plugins/deluge`, `internal/server/deluge_integration.go` | NONE: `deluge.Client` concrete (`client.go:39`), built with `deluge.New(url,password)` (`client.go:136`) | 1 | `DelugeWebURL`, `DelugeWebPassword` (`config.go:1776-1777`), env-or-file authoritative (`config.go:2236,2546`) | 5-min HTTP timeout (`client.go:145`); `Connected()`/`TestConnection` handler (`deluge_integration.go:59`) | Torrent-generic client methods, but integration logic (move-after-organize, discovery by label, protected paths) is audiobook |
| qBittorrent / SABnzbd / Transmission / NZB / indexers (Prowlarr, Torznab) | none | n/a | 0 | qBittorrent+SABnzbd settings were REMOVED (`internal/config/removed_keys.go:30-35`); no Transmission/NZBGet/Prowlarr/Torznab code anywhere (case-insensitive grep over internal, pkg, cmd) | n/a | n/a |
| iTunes / Apple Music library | `internal/itunes` (14.4k), `internal/itunes/service`, `internal/plugins/itunes` | no integration interface; `BookOrganizer` (`itunes/service/types.go:25`) is an inbound dependency; `Store` composite (`service/store.go:33`) | 1 (ITL binary + XML reader/writer, local file based, no network) | library paths in config (`itunes_libraries.go`) | n/a | Audiobook / Apple-specific |
| Audiobookshelf (ABS) | `internal/server/absauth`, `internal/server/handlers/abs`, `internal/server/middleware/absauth.go` | INBOUND compat API (the app impersonates an ABS server, `absauth/config.go:1-27`); no outbound ABS client | 1 | `ABS_JWT_SECRET` required when enabled, never autogenerated (`absauth/config.go:14-20`); auth-surface keys blob can never enable (`config.go:2435+`) | login/refresh throttle (`absauth`) | Audiobook |
| Plex / Jellyfin / Emby | none | `plugin.MediaPlayer` iface exists, 0 implementors (`internal/plugin/plugin.go:76-81`) | 0 | n/a | n/a | n/a |
| Webhooks | `internal/plugins/webhook` | implements `plugin.Plugin` with `CapEventSubscriber` (`plugin.go:33-40`) | 1 | `deps.Config["urls"]`, `["secret"]` (HMAC), `["events"]` (`webhook/plugin.go:44-62`) | `HealthCheck` only checks urls non-empty (`:93`) | Generic (event type list is audiobook: see 3) |
| Notifications generic | `plugin.Notifier` iface (`plugin.go:98-102`), 0 implementors | iface only | 0 | n/a | n/a | n/a |
| Email (SMTP) | `internal/mail` (412) | YES: `Sender` (`mail.go:60-62`: `Send(ctx, Message) error`) | 1 (`SMTPSender`, `mail.go:85`) + test override `mailSenderOverride` (`server.go:424`) | `smtp_*` keys / env (`config/invite_config.go:40-42`), password treated as secret | 15s `DefaultTimeout` (`mail.go:49`); header-injection guard `ErrInvalidHeader` | Fully generic (used for invite delivery only) |
| OAuth / OIDC sign-in | `internal/oauth` (827) | YES: `Provider` (`providers.go:23-27`: Name, AuthCodeURL, Exchange); `UserStore` (`resolve.go:32`) | 2 (GitHub `:37`, Google OIDC `:119`) | client id/secret config, auth-surface class | n/a | Generic |
| Cloudflare Access / service tokens / mTLS | `internal/cfaccesspolicy`, `internal/cfservicetoken`, `internal/mtls`, `internal/server/middleware` | concrete; `CFAssertionVerifier` iface in `middleware/absauth.go:53` | 1 each | env/config; Cloudflare policy API token | n/a | Generic edge-auth |
| GitHub Releases (self-update) | `internal/updater` (547) | none | 1 | repo slug hard-coded in `NewUpdater` (`updater.go:68-76`) | 30s HTTP timeout | Generic mechanism, hard-coded repo |
| OpenTelemetry OTLP export | `internal/telemetry` | n/a | n/a | `otel.*` config keys | n/a | Generic (see 12) |

## 2. AI layer detail

- Two-tier design. `internal/ai` holds the *concrete* OpenAI client and all feature code (dedup review, author dedup, metadata rerank, cover-text, embeddings). `internal/aidispatch` is a separate routing layer that picks an *endpoint* per *capability*.
- aidispatch protocols are an enum, not an interface: `openai_compat`, `whisper_server`, `local_process` (`dispatch.go:28-34`); `protocolsFor` maps `Kind` (chat/embed/whisper, `capabilities.go:24-30`) to protocols (`dispatch.go:36-40`). There is exactly one *wire* implementation per protocol and no Anthropic/Gemini/Ollama-native client (Ollama only through the OpenAI-compatible URL; comment at `openai_parser.go:270-275`).
- Capability registry (`capabilities.go:67-95`, registered at import time with `newCapability`, which is unexported so only that file may add one): ten capabilities: `LLMFilenameParse`, `LLMAudiobookParse`, `LLMCoverArtVision`, `LLMAuthorReview`, `LLMAuthorDiscovery`, `LLMMetadataRerank`, `LLMDedupReview`, `LLMAuthorDedupBatch`, `LLMDiagnostics`, `EmbedText` (grep of `newCapability(Spec{`). Each carries a `DataSent` string (privacy disclosure). For reuse: the registry mechanism is generic, the ID list is audiobook and would need to become per-app registration.
- Dispatcher is a value built per call from the live endpoint list: `aidispatch.New(endpoints, opts...)` (`dispatch.go:151`); the process-wide `defaultHealth` (`health.go:46`) is a package singleton.
- Consumer-side interfaces (the reusable shape): `MetadataCandidateScorer` with `Score(ctx,Query,[]Candidate) ([]float64,error)` + `Name()` (`internal/ai/metadata_scorer.go:29-32`, contract documented :15-28); `aijobs.BatchClient` with `UploadBatchFile`, `CreateBatchWithMetadata` (`aijobs/aijobs.go:31-36`); `aiscan.RealtimeLLM`/`BatchLLM`/`LLM` (`aiscan/pipeline.go:47-70`); `EmbeddingCache` (`embedding_client.go:55-58`).
- `aijobs` has a package-level callback registry `registry = map[string]CompletionCallback` (`aijobs.go:~96`, `Register` at init). Batch jobs persist in an `ai_jobs` table via `database.AIJobsStore` (`aijobs.go:40-43`).
- Usage/cost: OTel instruments `ai.requests`, `ai.duration`, `ai.tokens`, failures, parse results (`internal/ai/ai_metrics.go:353-397`); the comment says batch-API tokens are not recorded (:397). No separate persisted "OpenAI usage" ledger found (UNKNOWN beyond the metrics; grep for ai_usage/OpenAIUsage in internal/ai and internal/database found nothing relevant).
- Secrets: single resolver `poolSecret` accepts only `auth_ref == "openai_api_key"` (`pool_routing.go:75-80`), so there is no generic secret-reference mechanism yet.

## 3. Metadata-provider layer detail (the most reusable existing contract)

- `MetadataSource`: `Name()`, `SearchByTitle(ctx,title)`, `SearchByTitleAndAuthor(ctx,title,author)` all returning `[]BookMetadata` (`source.go:52-56`).
- Optional capability interfaces: `ContextualSearch.SearchByContext(ctx,*SearchContext)` (`source.go:92-94`; `SearchContext` carries ISBN/ASIN/Narrator/Series, :64-72) and `ProviderIdentified.ProviderID()` (`provider_keys.go:37-39`), deliberately optional so mocks need not implement (comment :27-36). Also `AuthorLister` (`audible_author_list.go:32`) and `MetadataExtractor` (`metadata.go:28`, file-tag reader, not a network source).
- Construction is NOT via a registry: a hard-coded `switch src.ID` in `internal/metafetch/service_search.go:~300-338` builds each client, falling back from the config credentials map to flat config fields. Adding a provider means editing that switch plus `providerhttp.defaultLimits` (`providerhttp.go:60`) and `provider_keys.go:21-26` constants.
- Resilience stack, all keyed by provider id, all package-global singletons: `providerhttp.Client(provider)` token-bucket + retry (RPS/Burst/MaxRetries/Timeout, `providerhttp.go:40,79`; fallback 2 RPS); `ThrottleRegistry` with persisted holds by failure class (daily-quota 4h, rate-limit 15m, auth 6h, 5xx 30m, transport 5m; `throttle.go:60-66`, global `defaultThrottles` `throttle_registry.go:69`, store via `AttachStore` :87); `dailyquota.For(provider)` (`dailyquota.go:333`, Google Books budget `google_quota.go:40-72`); `CircuitBreaker` + `ProtectedSource` (`circuitbreaker.go:32,138`, wrapped by `NewChainSource` :172); `ClassifyProviderError`/`IsNotFound` (`provider_error.go`).
- Audiobook coupling is in the *payload* (`BookMetadata`), the *Audible/Audnexus* clients and the ASIN/series fields, not in the call shape. A Sonarr/Radarr-like app would need a different result type, so a reusable core must make the result type generic or define per-app source interfaces that reuse `providerhttp`, `ThrottleRegistry`, `dailyquota`, `CircuitBreaker` (those four are already independent of `BookMetadata`; the breaker wraps only the `MetadataSource` interface).
- Metadata rate-limit config: `config.MetadataSource.RateLimit` tier multiplier (`config.go:165-184`) applied by `applyProviderLimits` (`service_search.go:162-180`).

## 4. Download clients

- Concrete only. `deluge.Client` methods: Login, ListTorrents, ListTorrentsByLabel, ListLabels, GetTorrent, MoveStorage, Connected, GetTorrentDetail(+Context), ListTorrentDetails(+ByLabel), RemoveTorrent (`internal/deluge/client.go:259-483`). No interface; 4 non-deluge files reference `*deluge.Client`/`TorrentStatus`.
- A vestigial interface exists: `plugin.DownloadClient` = `Plugin` + `TestConnection() error`, `ListTorrents() ([]TorrentInfo, error)`, `MoveStorage(hash,newPath string) error` (`internal/plugin/plugin.go:58-63`). Its own doc says "VESTIGIAL... zero implementors... NOT the seam" (:51-57); it uses the legacy `internal/plugin` base, while the real deluge plugin uses `pkg/plugin/sdk` (different framework). It also lacks add-torrent/download-completed/queue semantics that Sonarr/Radarr need (no AddTorrent, no history/status polling by category).
- Callbacks are free functions with an injected store (`NotifyDelugeMoveStorage`, `NotifyDelugeAfterOrganize`, `NotifyDelugeAfterVersionSwap`, `internal/server/deluge_integration.go:39-51`), i.e. the "notify download client after organize" step is hard-wired to Deluge.
- Indexers (Torznab/Newznab/Prowlarr), usenet: no code. qBittorrent/SABnzbd were deliberately removed (`removed_keys.go:30-35`).

## 5. Media servers / notifications / plugin frameworks

Two parallel plugin frameworks coexist:

| Framework | Base interface | Implementors | Notes |
|---|---|---|---|
| `internal/plugin` (legacy, 939 LOC incl. tests; 477 non-test) | `Plugin`: ID, Name, Version, Capabilities, Init(ctx,Deps), Shutdown, HealthCheck (`plugin.go:25-33`); `Capability` strings `metadata_source`, `download_client`, `media_player`, `notifier`, `event_subscriber` (:16-22); `Registry` with `Register`, `Global`, `ByCapability`, `InitAll(Scoped)`, `ShutdownAll`, `HealthCheckAll` (`registry.go:33-163`) | only `internal/plugins/webhook` | `Deps` = EventBus, Config map[string]string, Logger, Router (`plugin.go:44-49`); global singleton registry (`registry.go:45`) |
| `pkg/plugin/sdk` (public SDK) | `Plugin`: ID, Name, Version, `Register(r Registry) error` where `Registry` = `RegisterOp`, `EnqueueOp` (`sdk/plugin.go:21-30`, `sdk/registration.go`) | acoustid, dedup, deluge, itunes, maintenance, metafetch (all under `internal/plugins`) | op-centric (operations/jobs); coarse permission `Capability` consts include `CapNetworkOpenAI/Audible/OpenLibrary/GoogleBooks/ITunes` (`sdk/capability.go:18-23`), aliased from `internal/operations/registry` |

Plus a third wiring mechanism: `serviceregistry.Register(ServiceDef)` in each plugin's `register.go` (43 call sites repo-wide; e.g. `internal/plugins/deluge/register.go:24`).

No media-server clients: no Plex, Jellyfin, Emby, outbound ABS. `plugin.MediaPlayer` (`SyncLibrary`, `GetPlaybackState`, `UpdatePlaybackState`, `plugin.go:76-81`) and `plugin.Notifier` (`Notify`, `SupportedEvents`, :98-102) have zero implementors (grep: no references outside `internal/plugin`).

Event type constants in `internal/plugin/events.go:15-28` are audiobook-flavoured (`book.imported`, `dedup.merged`, `cover.changed`, `read_status.changed`). `EventBus` is notification-only, handlers in goroutines (`events.go:55-80`).

## 6. Conclusion: capability contracts

Already in reusable shape (interface exists, more than one implementor or clear independent seam):
1. `mail.Sender` (1 impl + test fake; fully generic).
2. `oauth.Provider` (2 impls; generic).
3. `metadata.MetadataSource` + `ContextualSearch` + `ProviderIdentified` (6 impls; generic call shape, audiobook result type).
4. Resilience primitives independent of the result type: `providerhttp`, `ThrottleRegistry` / `ThrottleStore` (`throttle.go:224`), `dailyquota`, `CircuitBreaker`.
5. `aidispatch` endpoint/capability router (generic mechanism; capability IDs need per-app registration; secret resolution is OpenAI-key-only today).
6. `ai.MetadataCandidateScorer` (3 tiers: embedding, LLM, heuristic) - generic scoring contract but typed on audiobook Query/Candidate.
7. Plugin lifecycle (`internal/plugin.Plugin`, `pkg/plugin/sdk.Plugin`) and `serviceregistry` DI container (see file 12).

Concrete-only (no usable abstraction today):
1. Download client (Deluge): interface is vestigial and does not match Sonarr/Radarr needs; no indexer abstraction at all.
2. OpenAI chat/batch client (`*OpenAIParser`): consumers define their own subsets; no provider-neutral LLM interface (`Complete(ctx, messages)`-style) exists.
3. Whisper/transcribe pool (duplicate of aidispatch cooldown logic).
4. AcoustID, Open Library dumps, iTunes, Cloudflare Access, mTLS, GitHub updater.
5. Media-server sync (interface only, no impl); notifier (interface only, 1 webhook subscriber that uses a different capability).

Cross-cutting observations: provider construction is switch-based, not registry-based (`service_search.go`); most resilience state is process-global keyed by provider string (collision risk if two apps embed in one binary and both name a provider "audible"/"openlibrary"); secrets come from three places (flat config fields, per-source `Credentials` maps, env-bound viper keys) with the encrypted-settings table (`database/settings.go:39`, `IsSecret`) as the persisted store.
