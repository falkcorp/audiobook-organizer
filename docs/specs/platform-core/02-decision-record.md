<!-- file: docs/specs/platform-core/02-decision-record.md -->
<!-- version: 1.0.0 -->
<!-- guid: cd524ddf-51e6-46ad-b7fe-f6fc8b6d9dc9 -->
<!-- last-edited: 2026-11-04 -->

# 02 — Platform-core decision record (supersedes 2026-09-18)

## Status

**Accepted for planning** (2026-11-04). Supersedes
[`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`](../../architecture/2026-09-18-pluggable-media-platform-decision-record.md),
which was "Proposed" and never implemented (`raw/08-prior-art.md` §3: 6 of its 13 decisions are
reflected in code, 4 partially, 3 not at all). The 2026-09-18 evaluation and adversarial review
remain **inputs**; their eight "non-negotiable gates" are inherited as acceptance criteria (§2.4).

No application behaviour changes are made by this record. Implementation happens only through the
plan in [`docs/plans/platform-core/implementation-plan.md`](../../plans/platform-core/implementation-plan.md).

## 2.1 What changed and why

The 2026-09-18 record assumed **one host binary with compiled-in media modules**. The owner's goal
(2026-11-03) is a **core repository** that several **separate app repositories** import —
audiobook-organizer, a Sonarr-like TV app, a Radarr-like movies app — each shipping its own binary,
with "all apps in one binary" kept possible but lowest priority. That overturns the single-host
assumption: the unit of reuse is now a **Go module with a public SDK**, not a module catalog inside
one program. Everything else is re-examined against that.

## 2.2 The 13 decisions of 2026-09-18

| # | 2026-09-18 decision | Verdict | Reason |
|---|---|---|---|
| 1 | Host stays Go + bundled React/TS UI | **Kept** | Matches the stack and the goal; no evidence for a rewrite. Each app is Go + React; the React shell is a core npm package (D6). |
| 2 | Media domains are compiled-in modules in one host, runtime-selectable | **Changed** | Modules are still compiled in and still have typed manifests, but they live in **app repos** and are composed per binary by `app.New(core, modules...)` (§03 §3.7). "One host" is replaced by "one core module, many app binaries". |
| 3 | Integrations are capability adapters with narrow contracts; host owns secrets/health/permissions | **Kept** | Confirmed by evidence: the only reusable shapes today are `metadata.MetadataSource`, `mail.Sender`, `oauth.Provider`; download clients are concrete-only and `plugin.DownloadClient` has zero implementors (`raw/11-integrations.md` §1, §4). Contracts are in `core/integrations/*` (§05-ai … §05-notify). |
| 4 | Go shared-object (`.so`) plugins rejected | **Kept** | Still Linux/FreeBSD/macOS only, exact-toolchain match, no UI story (`raw/08-prior-art.md` §2 row 9). Nothing in the repo uses `plugin.Open`. |
| 5 | External code deferred; out of process, language-neutral, permissioned | **Kept (deferred)** | Not needed for the three first-party apps. The *arr facade (D2) is the only cross-process API commitment, and it is an inbound HTTP API, not an extension protocol. |
| 6 | Core owns platform concerns; modules own media data; no universal nullable media table | **Kept** | The TV/Movies research shows the *acquisition* machinery is media-neutral but the *entities* are not (`raw/16-arr-domain-research.md` "Implications"). Core gets a polymorphic `EntityRef{Module, Kind, ID}`, never a shared media row. |
| 7 | Books and audiobooks link through shared works | **Changed → deferred to the ebooks app** | No ebook module exists or is planned in this operation (D5 gives ebooks a note only). The existing `Work` entity stays inside the `books` module. If an ebook app appears, linking is designed then, behind core's `EntityRef`. |
| 8 | Browser navigation comes from enabled module descriptors; first-party bundles compiled in; no remote JS | **Kept, made concrete** | Becomes the `@mediacore/shell` extension API (§07): modules register pages/nav/settings at build time; the server reports enabled modules; the shell intersects the two. No remote JS. |
| 9 | Ebooks are the first new module; TV/movies wait for acquisition contracts | **Changed** | The owner's priority is TV and Movies replacements. Ebooks get a short note (§10). TV/Movies still wait for the acquisition contracts — which are now specified (§05-download, §05-indexer, §10 `acquire`). |
| 10 | Module enablement is restart-bound | **Kept** | Gin/`ServeMux` routes cannot be unmounted and the executor has no per-module drain (`raw/08-prior-art.md` §2 row 1). Same rule in the new kernel (§03 §3.7). |
| 11 | Event bus is notification-only; correctness uses durable ops or an outbox | **Kept, outbox specified** | The bus has no ack/retry/order (`raw/12-cross-cutting.md` §5 — three unrelated buses exist). Core provides one `events` bus (notify-only) and a durable same-batch `outbox` (§05-events). |
| 12 | Audiobooks and ebooks link at Work, not Edition | **Dropped** | Moot for this operation (see 7, 9). Re-decide with the ebook app. |
| 13 | Existing audiobook Pebble keys stay in place during extraction; new modules use namespaced keys | **Kept, mechanism specified** | ABO's 258 key families (256 already `name:`-prefixed, 2 bare; `raw/05-storage.md` §3) are **claimed** by the `books` module as legacy prefixes; new keys use `m/<module>/`. Small generic families (users, roles, sessions, API keys) are adopted into core keyspaces by one atomic, backed-up migration; large ones are read in place behind adapters. Full rename is not planned (§05-storage, §09). |

## 2.3 New decisions

| # | Decision | Source |
|---|---|---|
| N1 | Core is a nested Go module `github.com/falkcorp/mediacore` at `core/`, wired by `require` + `replace`; **no `go.work`**. The path is the post-split path. | D3; §03 §3.2; `raw/00-baseline-build.md` |
| N2 | Core moves to its own repository only when the measurable criteria in §12 hold. | D3 |
| N3 | Native API under `/api/core/v1` and `/api/<module>/v1`; an optional per-app *arr-v3 facade through `core/arrfacade`; no commitment to full Sonarr/Radarr parity or database import. | D2 |
| N4 | Composition is explicit: `app.New(opts, modules...)`. No `init()` registration and no process-global registries in core (rule R5). `internal/serviceregistry`, `internal/plugin` and `pkg/plugin/sdk` are retired in favour of `module.Registrar`. | §2.4 hypothesis verified, `raw/07-plugin-registry-sdk.md` |
| N5 | Multi-user, single-tenant. Permissions are namespaced scopes `<module>:<action>`; roles are data; modules declare their permissions; admin is a permission (`core:admin`), never a role-name check. No tenant IDs. | D7; `raw/10-auth-rbac.md` G1–G4 |
| N6 | UI = core React shell package + per-module UI contributions registered at build time; one shell per binary. | D6 |
| N7 | Core's HTTP framework is standard-library `net/http` (`ServeMux` patterns). ABO's gin router is mounted under the `books` module as an `http.Handler` during migration and retired route-group by route-group. | §05-http-api; D8 "prefer stdlib where equivalent" |
| N8 | The task system's public API makes parallelism a required, typed property (`tasks.Parallelism` has no valid zero value); a sequential run must state why. | §2.7; §05-tasks |
| N9 | One task system: the v3 authoring model (`pkg/ops`) on the production v2 executor, cleaned of audiobook types, becomes `core/tasks`; v1 rows, `maintenance.MaintenanceJob`, and the v2/v3 duplication are retired with adapters. | `raw/06-tasks-ops.md` §0, §7 |
| N10 | Every secret is encrypted at rest through `core/secrets`; config holds references, never values. | `raw/10-auth-rbac.md` G12 |
| N11 | Toolchain: latest stable Go (go1.27.3 at 2026-11-03) and latest stable frontend tooling, exceptions recorded in §13. | D8 |
| N12 | Open-source only; no paid tier, licence or SaaS dependency anywhere in the design. | D9 |

## 2.4 Inherited acceptance gates (from the 2026-09-18 adversarial review)

Each is mapped to the spec section that satisfies it and the plan wave that proves it.

| Gate | Satisfied by |
|---|---|
| G1 a disabled module contributes no route, scheduled op, worker, nav entry or filesystem mutation after restart | §03 §3.7, §06 (kernel enablement), `coretest` disabled-module test |
| G2 a failed module migration prevents startup without corrupting data | §05-storage (migrations run in a batch per step; boot halts on error; checkpoint before each module) |
| G3 the client rejects unknown or version-incompatible module descriptors | §07 (shell intersects server manifest with compile-time registry; version check) |
| G4 no correctness-critical transition depends solely on the in-memory bus | §05-events (outbox), rule enforced in review checklist |
| G5 the existing audiobook DB opens unchanged and stays readable by the previous release | §05-storage legacy claims; §09 compatibility windows and rollback per stage |
| G6 a dummy second module needs no edit to audiobook packages | §06 "hello" module; plan acceptance test |
| G7 the host is testable with the audiobook module omitted | `coretest` harness boots core with zero modules |
| G8 resource/write-set identities are module-namespaced | §05-tasks `Resource` = `{Module, Kind, ID}` |

## 2.5 Open questions of 2026-09-18

| Q | Status |
|---|---|
| Q1 tenancy | **Resolved by D7**: multi-user, single-tenant (N5). |
| Q2 ebook formats/readers for a first ebook milestone | **Open, out of scope**; recommendation in §10 (EPUB + PDF, OPDS catalog feed, reuse `library` and `metadata`). |
| Q3 meaning of "replace Sonarr/Radarr" | **Resolved by D2**: personal-workflow replacement with a native API plus an optional *arr-v3 facade for ecosystem clients (N3). |
| Q4 one storage-root abstraction vs per-module drivers | **Answered in §05-library**: one core `library.Root` abstraction; modules cannot bring their own driver now; the `RootFS` interface leaves room for a remote driver later. |
| Q5 config/endpoint compatibility window for the ABO extraction | **Answered in §09**: every legacy path, key, permission atom and config key gets an alias for two minor releases after its replacement ships, with a deprecation header/log, then 410/refusal. |
