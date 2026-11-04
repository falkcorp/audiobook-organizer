<!-- file: docs/plans/platform-core/raw/08-prior-art.md -->
<!-- version: 1.0.0 -->
<!-- guid: d3db8f95-77d7-47e9-87a6-ee8890854613 -->
<!-- last-edited: 2026-11-03 -->

# 08 - Prior art digest (pluggable-media-platform docs, layering, decisions)

Evidence commit: `748fd787a`. Doc anchors are `docs/...:line`; code anchors are `path:line`.

Sources digested:

| File | Lines | Status |
|---|---|---|
| `docs/architecture/2026-09-18-pluggable-media-platform-evaluation.md` | 438 | v1.1.0, 2026-09-18; recommendation incl. adversarial corrections |
| `docs/architecture/2026-09-18-pluggable-media-platform-adversarial-review.md` | 286 | v1.0.0, 2026-09-18 |
| `docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md` | 68 | **"Proposed"** (`decision-record.md:11-14`: "No application behavior changes are approved by this document.") |
| `docs/architecture/layering.md` | 45 | enforced by `internal/arch/layering_test.go` |
| `docs/architecture/server-plugin-registry-{design,deferred-work}.md` | 585 / 598 | digested in file 07 |
| `docs/decisions/*.md` | 71 files | operational decision logs, 2026-10-30 .. 2026-10-31 |

## 1. Evaluation digest

**Recommendation** (`evaluation.md:~14-21`): keep Go; evolve into a **single-binary, compile-time modular media platform**; extract audiobooks as the first module, add ebooks as the second; no Go `.so` plugins; do not start by replacing Sonarr/Radarr wholesale.

**Three boundaries** the code conflates under "plugin": (1) *platform modules* (first-party compiled-in media domains owning model, API, ops, UI contributions); (2) *integrations* (compiled-in adapters: metadata, download clients, indexers, media servers, notifications, import sources) behind small typed capability contracts, configured per module; (3) *external extensions* (later, out of process, versioned API, never linked in).

**Current-state foundations table** (`evaluation.md:~33-44`): single deployable artifact; `internal/serviceregistry` (composition); `internal/operations/registry` (durable async work, "retain as the common job system for every module"); `internal/plugin.EventBus` (replace book-only event names with versioned cross-domain events); `internal/plugin.Registry` (retire or narrow into integrations runtime); `pkg/plugin/sdk` (fold into canonical SDK); static React shell (`App.tsx`, `Sidebar.tsx`).

**Options**: A compiled-in modules + versioned internal SDK (recommended); B Go `.so` plugins (reject: platform support, exact toolchain match, shared memory, no UI/migration answer); C external-process plugins from day one (right eventual boundary, defer).

**Platform core scope** (`evaluation.md:~118-132`): auth/users/roles/API keys/tenancy/audit; config, encrypted secrets, module enablement, schema versioning, backups; service container, module catalog, lifecycle supervisor, health, logging, metrics; HTTP middleware, API envelopes, SSE, webhooks, shared error model; durable operations, scheduling, resource/write-set locking, event delivery, notifications, activity history; storage roots, path mapping, safe moves/copies, hashing, quarantine, disk checks, import observation; generic people/works/external ids/tags/collections only where cross-media. **Core must not own a universal nullable `MediaItem`.**

**Module contract** (design target, not an interface to copy): `Module{Descriptor(); Register(Registrar)}`, `ModuleDescriptor{ID, Version, DisplayName, Category, DependsOn, Navigation, Settings}`, `Registrar{RegisterService, RegisterRoute, RegisterOperation, RegisterMigration, RegisterEventSchema, RegisterCapabilityRequest}`; host validates duplicate IDs/routes, cycles, requested capabilities, migration order, enablement. Disabled = routes and recurring jobs absent, data retained. Use an **explicit builtin list** (`platform.Builtins`) not `init()` discovery.

**Integration contract**: capability names `metadata.lookup`, `indexer.search`, `download.enqueue`, `download.history`, `media.sync`, `notification.send`; host owns secrets/health/rate limits/per-module enablement; download client interface must not get the DB or a filesystem root; Deluge is the extraction candidate.

**Data**: small shared bibliographic graph (Work, contributors, external ids, tags, module links), not a shared media table; Pebble keys explicitly namespaced (`catalog:work:<id>`, `audiobooks:item:<id>`, `ebooks:item:<id>`); no `Edition` yet.

**API/nav**: shell calls `GET /api/v1/platform/modules`, intersects with a compile-time TS registry of trusted components, module APIs under `/api/v1/modules/{module-id}`, paths `/library/audiobooks`, `/screen/tv`...

**Migration stages 0-7** (`evaluation.md:~290-305`): 0 consolidate vocabulary; 1 platform kernel (catalog, descriptor, restart-bound enablement, nav manifest, namespaced settings); 2 audiobooks as reference module; 3 ebooks; 4 comics; 5 shared automation integrations (extract Deluge etc.); 6 TV then movies; 7 external integrations. "First implementation plan should cover stages 0-1 only." Estimates: stages 0-2 12-20 weeks; stage 3 8-16; stage 4 6-12; stages 5-6 9-18 engineer-months.

**Guardrails**: no everything-schema; do not rename everything "plugin"; enabled must not mean merely hidden; forward-only per-module migrations; do not rewrite existing audiobook keys; do not use the event bus for correctness.

## 2. Adversarial review - key objections and outcomes

Outcome: central recommendation survives; five corrections (`adversarial-review.md:~16-27`): (1) enablement restart-bound, not hot; (2) event bus cannot carry correctness; (3) link at Work not Edition; (4) adapt legacy keys in place; (5) widen estimates.

| # | Attack | Evidence cited | Verdict |
|---|---|---|---|
| 1 | "Disable means disabled": toggle a plugin at runtime | `plugins_init.go` calls `InitAllScoped` once; `Registry.Enable/Disable` only flip a bool; Gin routes cannot be unregistered; container has no per-service remove; `plugins` group included before runtime registry init | Hot enable/disable not safe; persist desired state, apply at restart |
| 2 | Event loss / partial failure | `EventBus.Publish` goroutine per subscriber, recover panics, no offset/ack/retry/order/replay | Bus = notifications only; correctness needs durable ops, outbox or sync API |
| 3 | Bibliographic counterexamples (abridged vs unabridged, translations, dramatizations, EPUB/PDF, ASIN vs ISBN) | `database.Work` spans editions; no cross-format edition identity | Work is the only defensible link; Edition/Expression deferred |
| 4 | Build an independent plugin against the public SDK | `pkg/plugin/sdk/capability.go` aliases `internal/operations/registry.Capability`; lifecycle/routes in `internal/plugin`; construction in `serviceregistry` | Current SDK is an in-repo operation facade, not a platform SDK; define version-neutral DTOs first |
| 5 | Omit the audiobook module from the build | 843 files reference `database.Book`, 197 `database.Store`; `internal/server` 630 files, `internal/database` 384; static `App.tsx`/`Sidebar.tsx` | Architecture survives, schedule does not: strangler migration, adapters for several releases |
| 6 | UI injection / version skew | React imports are explicit; JSON can select but not create components | Client needs its own compile-time registry keyed by module ID; unknown/mismatched ids -> diagnostic; no remote JS |
| 7 | Rename `book:*` to `audiobooks:*` | many hand-maintained Pebble indexes/sidecars; documented stale-index/data-loss regressions | New modules namespaced; audiobook module wraps legacy keyspace; migration needs dual-read/write or verified offline |
| 8 | Sonarr/Radarr parity | no release-candidate model, quality/custom-format engine, episodic wanted state | Goal survives only if scoped; personal-workflow replacement plausible; parity multi-year |
| 9 | Is rejecting Go `.so` bias? | Go `plugin` docs: Linux/FreeBSD/macOS only, race detector, toolchain match | Rejection survives strongly |

**Eight non-negotiable gates** before going beyond the kernel (`adversarial-review.md:~222-240`): (1) disabled module contributes no route/scheduled op/worker/nav/filesystem mutation after restart; (2) failed module migration prevents startup without corrupting data; (3) client rejects unknown/version-incompatible descriptors; (4) no correctness-critical transition depends solely on the in-memory bus; (5) existing audiobook DB opens unchanged and stays readable by the previous release; (6) a dummy second module needs no edit to audiobook packages; (7) host testable with audiobook module omitted from the startup graph; (8) resource/write-set identities are module-namespaced.

**Residual risks**: "small core" becoming a god package (promotion into core needs two working consumers); module UI code still present in binary (behavior isolation, not sandbox); Pebble namespaces are organisation not access control; transport-neutrality only at module boundaries; single process is a shared failure domain. Final: "modular monolith with trusted first-party modules, not a general plugin platform yet"; if a dummy module cannot be omitted cleanly, stop before ebooks.

## 3. Decision record: 13 decisions and status at HEAD

(Record is status "Proposed". "Reflected?" = does the code at `748fd787a` implement it.)

| # | Decision (one line) | Reflected in code at HEAD? | Evidence |
|---|---|---|---|
| 1 | Host stays Go + bundled React/TS UI; no language rewrite | **Yes** (that is the stack) | `go.mod`; `web/src/`; `web_embed.go`; Makefile embeds `web/dist` (CLAUDE.md) |
| 2 | First-party media domains are compiled-in modules with a typed manifest, runtime-selectable by enablement | **No**: no `Module`/`ModuleDescriptor`/builtin list; the only "module" notion is the `plugins` service group and `config.AppConfig.Plugins` enable flags | grep `ModuleDescriptor`, `platform/modules` over `internal pkg cmd web/src`: no hits; `internal/server/plugins_init.go:25-47` |
| 3 | Integrations are capability adapters with narrow contracts, host-owned secrets/health/permissions | **Partial / aspirational**: `internal/plugin.Capability` consts exist but the typed interfaces are vestigial with zero implementors; the real integrations (deluge, itunes, metafetch) are `sdk.Plugin` op bundles plus bespoke clients, not capability adapters; no indexer/media-server contract | `internal/plugin/plugin.go:17-21,52-57`; `internal/plugins/deluge/plugin.go:45` |
| 4 | Go shared-object plugins rejected | **Yes** (nothing uses `plugin.Open`) | grep `plugin.Open(` over repo: none |
| 5 | External code deferred; must be out of process, language-neutral, permissioned | **Yes by absence**; the only out-of-process component is first-party: the fingerprint worker lease protocol, not a plugin protocol | `internal/fingerprint/workerapi`, `internal/server/handlers/fpworker/handler.go:7-17` |
| 6 | Core owns platform concerns, modules own media-specific data/logic; no generic nullable media-item table | **Partly**: no universal `MediaItem` (reject honoured; `MediaItemID` hits are ABS wire DTOs only, `internal/server/handlers/abs/dto_library.go:335`); but there is no core/module split: one audiobook-shaped `database.Store` and `Book` carry everything | `internal/database/store.go` (`Work`, `Book`); adversarial review workflow 5 numbers |
| 7 | Books and audiobooks link initially through shared works; links explicit, provenance-backed, reviewable | **Partial**: a `Work` entity and `Book.WorkID` exist, with a `WorkService`; no ebook side, no module-link/provenance/review workflow | `internal/database/store.go:212,469`; `internal/database/iface_catalog.go:95`; `internal/work/service.go`; key family `work:` `keyfamilies.go:91` |
| 8 | Browser nav comes from enabled module descriptors; first-party bundles compiled in; no remote JS | **No**: nav is static arrays | `web/src/components/layout/Sidebar.tsx`, `web/src/App.tsx`; no `/platform/modules` route |
| 9 | Ebooks are the first new module; TV/movies wait for shared acquisition contracts | **No ebook module exists** (only incidental mentions of the word in matching code); no acquisition/indexer/download contracts beyond Deluge ops | grep over `internal` |
| 10 | Module enablement restart-bound initially | **Effectively yes for the existing plugin flags** (enable only flips a bool, init once) but nothing persists "module" enablement | `internal/plugin/registry.go:69-81`; `plugins_init.go:25-47` |
| 11 | Event bus notification-only; correctness uses durable ops or outbox | **Yes in behaviour**: bus is async best-effort; durable work is the ops system (opv2/opv3). No generic outbox (the one write-back outbox was "never-wired" and is being cleaned up: `internal/server/itunes_writeback_cleanup.go:19-23`) | `internal/plugin/events.go:85-103`; `internal/database/keyfamilies.go:242` |
| 12 | Audiobooks and ebooks link at Work, not Edition | **Yes (no Edition entity exists)** | grep `type Edition`/`type Expression` over `internal`: none; `Work` comment `store.go:468` |
| 13 | Existing audiobook Pebble keys remain during extraction; new modules use namespaced keys | **Yes for existing keys** (`book:`, `work:`, `author:` ... unchanged, catalogued in `keyfamilies.go`); no new module-namespaced keys exist since no new module does | `internal/database/keyfamilies.go:91-99` |

Score: reflected 6 (1, 4, 5, 11, 12, 13; 5, 12 and 13 hold partly by absence of the thing they forbid), partial 4 (3, 6, 7, 10), not reflected 3 (2, 8, 9). All gates of the review (section 2) are unproven because the module boundary does not exist yet.

## 4. Decision record: 5 open questions (status at HEAD)

1. **Single-user/home-server vs per-user/tenant scoping from the start?** UNRESOLVED in the record. Code state: multi-user with RBAC exists today (`database.User` `store.go:734`, `Role` `store.go:968`, `auth.Permission`, per-user preferences/progress, per-user search terms: see `docs/decisions/2026-10-30-select-all-per-user-filters.md`), but there is no tenant/library scoping above users (not found).
2. **Which ebook formats/reader integrations for the first useful ebook milestone?** UNRESOLVED; no ebook code.
3. **Does "replace Sonarr/Radarr" mean personal workflow coverage or API/config compatibility with their ecosystems?** UNRESOLVED in the record; the evaluation assumes personal-workflow scope and flags parity as a product decision. The code already emulates one external API: an Audiobookshelf-compatible surface (`internal/server/handlers/abs`, `internal/syncapi`) - a precedent for compat endpoints.
4. **Single shared storage-root abstraction vs per-module storage drivers?** UNRESOLVED. Code state: a single library-root/import-path concept (`internal/libroot`, `internal/pathutil`, `internal/fileops`) with remote "Mac" lanes for audio decode (`pkg/ops/lanes.go:15`); no pluggable storage driver.
5. **Which existing configuration formats/endpoints need a compat window when audiobooks is extracted?** UNRESOLVED; a shim ledger already exists for refactors (`docs/compat-surfaces.md`) and v1 routes are being retired with `410 Gone` for one release (`docs/decisions/2026-10-30-bulk-fetch-gate.md`).

## 5. Alternatives table (decision record)

Keep adding to the audiobook model (rejected); universal media schema first (rejected); Go `.so` (rejected); external-process plugins first (deferred); TV/movie parity first (deferred, "substantially larger than ebook/comic library management").

## 6. `docs/architecture/layering.md` digest

`internal/arch/layering_test.go` (`TestLayering`) enforces "a package may import only packages whose layer is <= its own" in `go test ./...` (`layering.md:8`). Layers (`layering.md:12-20`): 0 leaf (`util`, `pathutil`, `personname`, `titleutil`, `authorname`, `audioext`, `httputil`, `logger`, `logging`, `metrics`, `cache`, `models`, `seqnum`, `querygrammar`, `keyedmutex`, `matcher`, `fingerprint`); 1 `config`; 2 storage (`database`, `openlibrary`, `search`); 3 domain (`merge`, `versionprimary`, `versions`, `dedup`, `metadata`, `organizer`, `scanner`, `itunes`, `audiobooks`, `metafetch`, `reconcile`, `repairs`, `undo`, `writeback`, ...); 4 jobs (`operations/registry`, `scheduler`, `plugins/*`, `maintenance/jobs`); 5 transport (`server/**`, `realtime`, `syncapi`); 6 entry (`cmd/*`, `tools/cmd/*`, root `main`). Authoritative map is `layerOf` in the test (every package must have an entry); an `allowed` map lists known wrong-way edges and may only shrink; `layerOverride` today has `matcher` and `fingerprint`. Only default build tags are checked.

Relevance to platform-core: (a) the existing layering already separates a "storage" layer (2) and "jobs" layer (4) but both are audiobook-shaped; a core would need new layers below 3 that must not import `database.Book`; (b) layer-4 `plugins/*` and `operations/registry` are in the same layer so the registry cannot call into layer-3 domain code except via injected callbacks (`walkresume`, `Deps` closures); (c) the module list `internal/opscatalog` is layer 4 (`layering_test.go:221`).

## 7. `docs/decisions/` index and relevant digests

71 files (`ls docs/decisions | wc -l`), all dated 2026-10-30 / 2026-10-31; none is about plugins or the platform vision directly. Grouped by theme (filename stems):

* **Ops / jobs / scheduling (relevant)**: `boot-ops-resumable-checkpoint`, `resume-restart-checkpoint-guard`, `finish-without-resuming`, `ops-dirtyset-compare-and-delete`, `opsv3-undecodable-census-total`, `opv2-waiting-deps-index`, `opchange-verify-resumable`, `dry-run-flag-both-spellings`, `lsh-backfill-cancel-guard`, `dedup-cpu-parallel-buckets`, `throttle-reset-op`, `throttle-panel`, `author-reclassify-route-undo`, `author-merge-scan-standdown`, `claim-lock-race`, `retired-versions-apply-preconditions`, `revert-v3-writer-record-only-changes`, `repairs-visibility-aggregates`, `reports-dir-prune`, `purge-now-counts-once-per-day`, `tombstone-sweep-guard`, `batch-apply-structural-refusal-backoff`, `maint-test-budget`.
* **Auth / sessions (relevant)**: `web-sessions-credential-epoch`, `admin-routes-auth-off`, `masksecret-length-independent`.
* **API / events / journal (relevant)**: `bulk-fetch-gate`, `activity-writer-one-batch-path`, `activity-filters-silently-ignored`, `journal-direct-writers`, `naming-pattern-tokens`, `authors-series-pagination`, `select-all-per-user-filters`, `reset-to-defaults-from-viper`.
* **Storage / indexes (relevant)**: `author-books-index`, `titledir-memstore-index`, `memdb-sort-bounded-topk`, `bookfile-acoustid-seg-keys`, `bookfile-hash-writers-recompute`, `retired-fpidx-holder`, `retired-bookfiles-backoff-outside-stripe`, `book-edit-join-stripe`, `merge-undo-reverted-repair-lock`.
* **Audiobook-domain only (not relevant to core)**: deluge-*, itl-bookmarkable, narrator-fold-match, read-status-alternation, search-*, franchise-matchall-prefilter, whisper-midrequest-identity, transcribe-flakes, transcription-mismatch-detail, transcode-no-hardcoded-paths, scanner-trash-guard, ingest-respects-unfinished-swap, copy-organize-sync-identity, ai-parse-confidence, min-score-slider-findings, dedup-*, fixture/test-flake notes.

Digests of the relevant ones (decision, with the platform lesson):

| Doc | Decision | Platform-core lesson |
|---|---|---|
| `boot-ops-resumable-checkpoint` | Four boot-enqueued full-library walks (timeouts 30m/60m/120m/6h) get a cursor checkpoint and **yield before the deadline** (`ErrContinueInPlace`, same op id re-queued with fresh timeout); rejected longer timeouts and per-plugin concurrency caps. Checkpoint is keyed by op id and only merged back on a re-queue of the same row; a `timed_out` run is never re-queued | any long-job framework needs a "yield and continue in place" primitive plus idempotent items; timeout is terminal |
| `resume-restart-checkpoint-guard` | A static AST test (`internal/arch/resume_restart_guard_test.go`) requires every `ResumeRestart` def to reference `.Checkpoint`; allowlist 21 entries | resume-policy contracts need guard tests, not convention |
| `finish-without-resuming` | New route `POST /api/v1/operations/v2/:id/finish`, `Registry.FinishWithoutResuming`, `RunRecord.FinishOnly`, runner `finishOnly` mode that runs `Finish` without starting workers; v3 has only `cancel` and `approve` routes | v3 decision states (`awaiting_decision`) are still driven through v1 routes |
| `ops-dirtyset-compare-and-delete` | Generic `CompareAndDeleteRaw` + `MarkDirty(prefix, id)` in `internal/database/dirty_set.go`, using the store's striped mutexes; identification dirty markers now delegate to it | dirty-set source (incremental work queue) is a reusable core primitive |
| `opsv3-undecodable-census-total` | Corrupt v3 run records are hidden from census and `Total`, with a warning, instead of 500 | persisted run records must tolerate undecodable rows |
| `opv2-waiting-deps-index` | `opv2:wait:` index staged from the single `stageOpRow` writer (AST ratchet `TestOpsV2RowWritesGoThroughStageOpRow`) | single-writer + ratchet tests for index families |
| `opchange-verify-resumable` | the boot-time `opchange-book-index-rebuild ensure` verify becomes incremental/resumable (cursor) because the journal never shrinks | boot ops must be incremental |
| `dry-run-flag-both-spellings` | 83 ops with `Preview`; 73 already accepted both `dry_run` and `dryRun`, 10 fixed; disagreeing bodies are refused; owner rule 2026-09-25: writers run as preview unless `dry_run=false` | API param contract (`opmode`) belongs in core |
| `lsh-backfill-cancel-guard` | `RunItems` cancellation is dispatcher-only; fix applied inside the op (per-item `ctx.Err()` guard), general fix deferred | shared-executor semantics matter to every op (see file 06 section 2) |
| `dedup-cpu-parallel-buckets` | errgroup + `SetLimit(NumCPU)` over disjoint buckets, serial merge in first-seen order | the CLAUDE.md concurrency rule's worked example |
| `web-sessions-credential-epoch` | `Session.CredentialEpoch` stamped at sign-in, `RequireAuth` refuses a mismatch; password change keeps the caller's session via `keepSessionID`, cleanup revokes only sessions below the new epoch; same treatment as ABS sessions | session/credential epoch design is core RBAC material |
| `admin-routes-auth-off` | with `EnableAuth=false`, read-only admin introspection opens via `adminRole()`; routes that delete/rewrite data or touch credentials keep `RequireAdmin()` (401) | auth-off mode semantics must be explicit per route class |
| `bulk-fetch-gate` | endpoint retired with `410 Gone` for one release rather than gated | route retirement convention |
| `activity-writer-one-batch-path` | dead batch branch in `Writer.sendEntry` removed; batching only via `LogBatch` | activity log is a core service |
| `journal-direct-writers` | classification check on operation-change journal writers: 20 files/48 call sites outside `database` and `repairs` write the journal directly; guarded by `internal/arch/journal_direct_writers_test.go` | change-journal/undo is core-adjacent but its change types are audiobook-specific |

## 8. Cross-cutting takeaways for the spec

1. The prior art already chose: compiled-in, Go, no `.so`, external process later; **platform core must be a library of compiled-in packages** which matches the owner's stated goal.
2. The prior art's own kernel (descriptor/manifest/registrar/builtin list/module catalog API) has **not been built at all**; a platform-core spec can adopt it wholesale or deliberately narrow it.
3. The adversarial gates (esp. 6 "dummy second module needs no edit to audiobook packages", 7 "host testable with audiobook module omitted", 8 "module-namespaced write-set identities") are the acceptance criteria any core extraction should inherit. Note `OperationDef.Writes/Reads []Resource` and `ConcurrencyKey` are global strings today (file 06 section 1.1), so gate 8 is currently unmet.
4. The evaluation's concern about three overlapping plugin systems is confirmed and quantified in file 07.
5. Open question 5 (compat window) is partly answered by existing practice: `docs/compat-surfaces.md`, `410 Gone` retirements, and the opv2/opv3 dual-write mirror kept "until 05-PR16" (`internal/database/pebble_store_ops_v3.go:28-45`).
