<!-- file: docs/specs/platform-core/02-decision-record.md -->
<!-- version: 1.7.1 -->
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
| 6 | Core owns platform concerns; modules own media data; no universal nullable media table | **Kept** | The TV/Movies research shows the *acquisition* machinery is media-neutral but the *entities* are not (`raw/16-arr-domain-research.md` "Implications"). Core gets one polymorphic reference, `entity.Ref{Module, Kind, ID}` (03 §3.3), never a shared media row. |
| 7 | Books and audiobooks link through shared works | **Changed → deferred to the ebooks app** | No ebook module exists or is planned in this operation (D5 gives ebooks a note only). The existing `Work` entity stays inside the `books` module. If an ebook app appears, linking is designed then, behind core's `entity.Ref`. |
| 8 | Browser navigation comes from enabled module descriptors; first-party bundles compiled in; no remote JS | **Kept, made concrete** | Becomes the `@mediacore/shell` extension API (§07): modules register pages/nav/settings at build time; the server reports enabled modules; the shell intersects the two. No remote JS. |
| 9 | Ebooks are the first new module; TV/movies wait for acquisition contracts | **Changed** | The owner's priority is TV and Movies replacements. Ebooks get a short note (§10). TV/Movies still wait for the acquisition contracts — which are now specified (§05-download, §05-indexer, §10 `acquire`) as contracts only: `acquire`, `integrations/indexer` and the download grab/monitor pipeline are **deferred-build** (03 §3.4), built when the first TV/Movies app starts. |
| 10 | Module enablement is restart-bound | **Kept** | Gin/`ServeMux` routes cannot be unmounted and the executor has no per-module drain (`raw/08-prior-art.md` §2 row 1). Same rule in the new kernel (§03 §3.7). |
| 11 | Event bus is notification-only; correctness uses durable ops or an outbox | **Kept, outbox specified** | The bus has no ack/retry/order (`raw/12-cross-cutting.md` §5 — three unrelated buses exist). Core provides one `events` bus (notify-only) and a durable same-batch `outbox` (§05-events). |
| 12 | Audiobooks and ebooks link at Work, not Edition | **Dropped** | Moot for this operation (see 7, 9). Re-decide with the ebook app. |
| 13 | Existing audiobook Pebble keys stay in place during extraction; new modules use namespaced keys | **Kept, mechanism specified (extended by R3-1, amended by R4-1, R4-2, R5-1 and R5-6, 2026-11-04)** | ABO's 258 key families (256 already `name:`-prefixed, 2 bare; `raw/05-storage.md` §3) are **claimed** by the `books` module as legacy prefixes; new keys use `m/<module>/` and `c/<subsystem>/`. **During the extraction (S0–S10) every family has exactly one stored format.** **Amendment R4-1 (refines R3-1): core holds no audiobook key name or row format** (03 R1). Each core service that persists state an app may already hold (the one list is 03 §3.3 "Store interfaces"; SIMP5-8) defines a **store interface** in the SDK and ships a **native implementation** over its own `c/<subsystem>/` keyspace, which new apps and `hello` use; the `books` module supplies a **legacy implementation** of the same interface, written in ABO over ABO's existing keys and row formats, on `module.Manifest.LegacyStores` (03 §3.3 "Store interfaces"). So the families core services use during the extraction (03 §3.7 names them) are read and written only by ABO code, and core needs **no storage grant** for them; grants become part of the deferred cutover (N15). There is no copy into `c/…`, no mirror write, no copy of a legacy field in a side record, no rebuild at boot and no hand-over of a claim between owners. Native-only data (namespaced scopes, hashed sessions, native one-time tokens) is stored by the legacy implementation in a **books-owned extension family that older builds never read**, never additively in a legacy row. R3-1's premise that an extra string in a role row grants nothing to an older build was false: HEAD matches most permissions by exact string (`internal/server/middleware/auth.go:430-455` @748fd787a), but its worker confinement is a "nothing else" test (`auth.go:271-297`), so a namespaced string beside `fingerprint.worker` would un-confine a custom worker role on rollback (SEC4-3). **Amendment R5-6:** each extension record carries a fingerprint (`legacy_ref.fp`) of the **security-relevant** fields of the legacy row it extends, never of per-request fields HEAD rewrites on every use (an API key's last-use time); if an older build changed those fields while rolled back, a record holding a Widening field is suspended until an admin re-confirms it (a record without one is re-stamped by its next write), and a revocation the older build made stays a revocation on roll-forward, never re-enableable, so a roll-forward never widens anything silently. Sessions are hashed in that extension family, so a rollback costs one re-login (14 Q-D). New API keys always carry explicit, non-empty scopes, because an empty scope list means "every role permission" to HEAD (`auth.go:405-408`); a key with no legacy-equivalent scope is written to its legacy row with a scope that grants nothing on an older build (ABO: `["none"]`, 05-authn §4.4), never an empty list (R5-6). **A moved secret never weakens a gate (R4-2, amended by R5-1):** only secrets HEAD stores in **plaintext** move. The four HEAD already stores encrypted in its own secret rows — OpenAI key, Google Books key, Hardcover token, Basic password (`internal/config/persistence.go:1955-1958` blanks them in the blob, `:1989-2003` writes them as encrypted rows) — are **not moved** during the extraction: the books legacy config store reads HEAD's encrypted rows, so a rollback never strands the Basic gate (OPS5-2). The first core write of a plaintext secret stores it encrypted in `secrets` and replaces every legacy copy — the legacy row **and** the second `config.yaml` copy — with an unguessable, never-matching sentinel, not an empty value (HEAD's Basic gate admits an empty presented password when the stored one is empty, `internal/server/middleware/basicauth.go:81-87`). After a rollback the older build therefore fails closed on every moved secret until the operator re-enters it, which the release note states (14 Q-E); on roll-forward the encrypted copy wins unless the legacy value differs from the sentinel, in which case the re-entered value is re-imported and re-sentinelled. Two further rules keep the Basic gate at least as strong as at HEAD (R5-1): the books legacy config store preserves HEAD's precedence for legacy keys until the cutover — a value persisted through the UI wins over an environment default exactly as at HEAD, so the shipped compose file's always-set `BASIC_AUTH_ENABLED=${BASIC_AUTH_ENABLED:-false}` (`docker-compose.yml:20`) cannot switch off a gate enabled in the UI (OPS5-1) — and the core Basic gate (from S3) refuses every non-exempt request when it is enabled with an unset, empty or unreadable password (fail-closed, SEC5-4). The store interfaces are SDK and frozen by the API-stability gate (12 SC1; the trade is 14 K23). The previous release therefore reads every row the new build wrote, and no rollback floor exists during the extraction (N13, superseded); a rollback's costs and consequences are the one table of 09 §9.2.1. Moving a family to a native keyspace is a **cutover**, outside the extraction (N15). Full rename of the 258 families is not planned (§05-storage, §09). |

## 2.3 New decisions

| # | Decision | Source |
|---|---|---|
| N1 | Core is a nested Go module `github.com/falkcorp/mediacore` at `core/`, wired by `require` + `replace`; the build never depends on `go.work`. An `exclude` of the stale monolithic genproto in **`core/go.mod`** (not the root) makes an uncommitted developer `go.work` (`go work init . ./core`) safe and keeps the root `go.mod` installable after the split. The path is the post-split path. | D3; §03 §3.2; `raw/00-baseline-build.md` E1–E10; round-1 modules-build review X2–X4 |
| N2 | Core moves to its own repository only when the measurable criteria in §12 hold. | D3 |
| N3 | Native API under `/api/core/v1` and `/api/<module>/v1`; an optional per-app *arr-v3 facade through `core/arrfacade` (contract specified, build deferred to the first TV/Movies app, 03 §3.4); no commitment to full Sonarr/Radarr parity or database import. | D2 |
| N4 | Composition is explicit: `app.New(opts, modules...)`. No `init()` registration and no process-global registries in core (rule R5, which binds core only: an app may keep per-`App` façades over core handles, as ABO does for `config.AppConfig` and its logger shim). `internal/serviceregistry`, `internal/plugin` and `pkg/plugin/sdk` are retired in favour of `module.Registrar`. | §2.4 hypothesis verified, `raw/07-plugin-registry-sdk.md` |
| N5 | Multi-user, single-tenant. Permissions are namespaced scopes `<module>:<action>`; roles are data; modules declare their permissions; admin is a permission (`core:admin`), never a role-name check. No tenant IDs. | D7; `raw/10-auth-rbac.md` G1–G4 |
| N6 | UI = core React shell package + per-module UI contributions registered at build time; one shell per binary. | D6 |
| N7 | Core's HTTP framework is standard-library `net/http` (`ServeMux` patterns); core never imports gin. ABO's gin router is mounted through `httpapi.MountLegacy` **permanently**, behind core's perimeter middleware with gin's own auth chain unchanged inside; the auth engine swap happens in exactly one stage (09 S4). There is no gin-retirement stage: a books route group moves to native `httpapi` only when it is rewritten for its own reasons. Trade: ABO keeps gin and two routing stacks indefinitely; core is unaffected. | §05-http-api; D8 "prefer stdlib where equivalent"; round-1 SIMP-2 |
| N8 | The task system's public API makes parallelism a required, typed property (`tasks.Parallelism` has no valid zero value); a sequential run must state why. | §2.7; §05-tasks |
| N9 | One task system: the v3 authoring model (`pkg/ops`) on the production v2 executor, cleaned of audiobook types, becomes `core/tasks`; v1 rows, `maintenance.MaintenanceJob`, and the v2/v3 duplication are retired with adapters. | `raw/06-tasks-ops.md` §0, §7 |
| N10 | Every secret is encrypted at rest through `core/secrets`; config holds references, never values. | `raw/10-auth-rbac.md` G12 |
| N11 | Toolchain: latest stable Go (go1.27.3 at 2026-11-03) and latest stable frontend tooling, exceptions recorded in §13. | D8 |
| N12 | Open-source only; no paid tier, licence or SaaS dependency anywhere in the design. | D9 |
| N13 | ~~The rollback floor is the Teach build.~~ **Superseded by R3-1 (2026-11-04); see decision 13 and N15.** Round 2 made the first build of every adopt/expand sequence a Teach build that carried legacy-side guards and was stamped as the minimum rollback target. R3-1 removes the dual-format machinery those guards protected (adoption copies, mirrors, side records, mirror-disabled keys, claim hand-overs), so there are no Teach builds and no adopt-step `Floor` during the extraction: a rollback lands on the previous release, which reads every row because each family keeps one stored format. Its costs and consequences are listed once, in the table of 09 §9.2.1 (OPS4-11). The live security hotfixes — the five early fixes of 05-authn §11.1, the single list (SEC5-11) — still ship independently and early, as ordinary fixes, not as a floor build. | Round-2 resolution R2-1; superseded by round-3 resolution R3-1 (`spec-round-3-dispositions.md`; SEC3-1, SEC3-2, SEC3-3, DATA3-2, DATA3-3, COR3-1) |
| N14 | **Legacy migrations run first; the stepping stone is honest.** Books' existing legacy migrations run in a boot phase before every core and module migration, on books' own claims; no core grant exists over a books family during the extraction (R4-1), so they need no grant of their own. Two releases are named and never confused (OPS4-5): the **floor release** is the first release that deletes those steps' code — and every code path they call (the 05-tasks T8 deletion) — and declares books' upgrade floor (`UpgradeFloor{Version: 66}`); the **stepping-stone release** is the release the floor release's manifest names to install first (`InstallFirst`, set to the newest production tag that exists when the deleting PR merges), which carries steps 1..66 and the code they call and contains no deletion. The code is kept in every release before the floor release. The requirement is a property of the **store**, not of the binary being replaced: the floor release's manifest carries what the store must already have applied (books' legacy migration versions) together with the stepping-stone release; every guard-aware boot records the store's applied steps in a sidecar next to the database; an updater from S0 on compares that sidecar with the manifest before applying and offers the stepping stone instead, and boot refuses a store that falls short with a message naming it (05-updater, 05-storage). A pre-S0 updater swaps any newer binary unchecked (`internal/updater/updater.go:214-280`), so an install that auto-updates from a pre-S0 release meets the boot refusal; the floor release's release note and its last log line name the stepping stone, and a store without a sidecar gets one from one boot of any release from S0 on (DATA4-18). "Passing through" therefore means the store has applied those steps, not merely that the stepping-stone binary ran once. Every release other than the stepping stone may be skipped. Data migrations whose code a release deletes join the requirement with the first cutover (05-storage §5S.3.13). | Round-2 resolution R2-4; OPS3-7; OPS4-5, DATA4-18; 03 §3.7; 09; 05-updater |
| N15 | **Cutover (deferred).** Moving a family from its legacy keys to a native core keyspace, and deleting the legacy rows, is **not part of the extraction**. It is a later, per-family, **one-way** step, decided on measured data (key counts, row sizes, the native features that need a different shape), run behind the mandatory pre-migration checkpoint and the single per-owner level guard (05-storage). Its rollback floor is the cutover release itself: once it ran, an older build is refused by the level guard and only a restore of the pre-cutover checkpoint or backup goes back. The cutover is the only step in which core reads books' rows, so the storage grants (`Grant`, `GrantMode`) land with it as its deferred-build members (05-storage §5S.3.13; R4-1). This is what 2026-09-18 decision 13 already said; R3-1 restores it after rounds 1–2 had pulled an adoption window into the extraction. | Round-3 resolution R3-1; 05-storage; 09 |

## 2.4 Inherited acceptance gates (from the 2026-09-18 adversarial review)

Each is mapped to the spec section that satisfies it and the 09 stage that proves it.

| Gate | Satisfied by |
|---|---|
| G1 a disabled module contributes no route, scheduled op, worker, nav entry or filesystem mutation after restart | §03 §3.7, §06 (kernel enablement), `coretest` disabled-module test |
| G2 a failed module migration prevents startup without corrupting data | §05-storage (each migration step commits in one batch, except books' existing legacy steps 1–66, which are non-atomic, idempotent `LegacyMigration`s behind the mandatory pre-migration checkpoint; boot halts on error with the step uncommitted; one pre-migration checkpoint per boot) |
| G3 the client rejects unknown or version-incompatible module descriptors | §07 (shell intersects server manifest with compile-time registry; version check) |
| G4 no correctness-critical transition depends solely on the in-memory bus | §05-events (outbox), rule enforced in review checklist |
| G5 the existing audiobook DB opens unchanged and stays readable by the previous release | §05-storage legacy claims and the store interfaces of 03 §3.3, whose ABO implementations are books code over ABO's keys: during the extraction each family keeps one stored format (decision 13), so the previous release reads every row the new build wrote; native-only data lives in books' extension family, which older builds never read, and a record holding a Widening field is suspended when an older build changed its legacy row's security-relevant fields (R5-6); the per-owner level guard covers real format changes (books' own migrations, a later cutover, N15); §09 rollback per stage, whose costs and consequences are the one table of 09 §9.2.1 (fail-closed: a moved plaintext secret leaves a never-matching sentinel, R4-2; the secrets HEAD already encrypts are not moved, R5-1); every release other than the stepping-stone release may be skipped (N14) |
| G6 a dummy second module needs no edit to audiobook packages | §06 "hello" module; plan acceptance test |
| G7 the host is testable with the audiobook module omitted | `coretest` harness boots core with zero modules |
| G8 resource/write-set identities are module-namespaced | §05-tasks `Resource` = `entity.Ref{Module, Kind, ID}` (03 §3.3) |

## 2.5 Open questions of 2026-09-18

| Q | Status |
|---|---|
| Q1 tenancy | **Resolved by D7**: multi-user, single-tenant (N5). |
| Q2 ebook formats/readers for a first ebook milestone | **Open, out of scope**; recommendation in §10 (EPUB + PDF, OPDS catalog feed, reuse `library` and `metadata`). |
| Q3 meaning of "replace Sonarr/Radarr" | **Resolved by D2**: personal-workflow replacement with a native API plus an optional *arr-v3 facade for ecosystem clients (N3). |
| Q4 one storage-root abstraction vs per-module drivers | **Answered in §05-library**: one core `library.Root` abstraction; modules cannot bring their own driver now; the `RootFS` interface leaves room for a remote driver later. |
| Q5 config/endpoint compatibility window for the ABO extraction | **Answered in §09**, which defines the window **once** (09 §9.2.1) and `compat` implements it: a legacy path, key, permission atom or config key keeps an alias for at least the later of two minor releases and 30 days after its replacement ships **and** until its legacy-use counter has been zero for the required period; it is removed only by a contract PR, never by a clock. Env names that are the only source of a required setting in a shipped deploy file are permanent. Books routes on the permanent legacy mount (N7) have no window at all. |

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- COR-17/SIMP-16 — decision 6, decision 7 and G8 use `entity.Ref`.
- SIMP-1 — decision 9 and N3: acquisition, indexer and facade are contracts with deferred builds.
- SIMP-2 — N7 rewritten: permanent `MountLegacy`, no gin retirement, auth swap in one stage.
- SIMP-3 — N4: R5 binds core only; per-App façades allowed.
- SIMP-7 — Q5 points at the single window in 09 §9.2.1; no clock-based removal.
- MOD-3/MOD-4 — N1: the genproto exclude lives in `core/go.mod`.
- DATA-1, DATA-3, SEC-1, SEC-19 — decision 13: identity-family adoption model; settings families stay books legacy; config overrides under `c/config/`. (Adoption model superseded in round 3 by R3-1.)
- DATA-8 — G2: books' legacy migrations 1–66 are the non-atomic exception.
- OPS-2/OPS-3 — G5 and decision 13: skipped releases; contract steps check the stamped level.
- OPS-5 — Q5: env names that are the sole source of a required setting in a shipped deploy file are permanent.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1 (SEC2-1, SEC2-2, DATA2-3, DATA2-5) — new decision N13: the Teach build ships the legacy-side guards and is the rollback floor; G5 and decision 13 bound "previous release" by it. (Superseded in round 3 by R3-1.)
- R2-2 (SEC2-3, DATA2-4) — decision 13: every revocation, also after a restore, writes the authoritative legacy row in the same batch. (Superseded in round 3 by R3-1.)
- R2-4 (DATA2-2, OPS2-11) — new decision N14: books legacy migrations before core claims; minimum-upgrade-from release (`min_upgrade_from`); decision 13 and G5 no longer claim every release may be skipped.
- MOD2-21 — §2.4 maps gates to 09 stages (no "wave").

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): N13: the adopt step stamps the Teach build's level as its `Migration.Floor`; contracts raise levels with `RaisesLevel`; the Teach build's guards include the integration-URL bound (lead decision). Decision 13: contracts check adopt-step applied records (`Adopts`), not the stamped level. (Superseded in round 3 by R3-1.)

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 (COR3-1, DATA3-2, DATA3-3, SEC3-1, SEC3-2) — decision 13 extended: one stored format per family during the extraction; legacy-format adapters behind explicit, logged storage grants; native-only data additive in legacy rows (exact-match permission check, `auth.go:430-455`) or in core-only families; new API keys never have empty scopes (`auth.go:405-408`); secrets moved to the encrypted store on first write and blanked in the blob (rollback = re-enter them). N13 marked superseded; live hotfixes stay independent early fixes. G5 rewritten.
- R3-1 — new decision N15: cutover to native keyspaces is deferred, per family, one-way, checkpointed, behind the per-owner level guard; its floor is the cutover release.
- SEC3-1 — superseded by R3-1: no legacy role mirror, so N13's dominance sentence is removed with N13.
- SEC3-3 — superseded by R3-1: no Teach build and no "pre-adoption backup" route past a floor; N13's trade sentence is removed.
- OPS3-7 — N14: the minimum-upgrade-from requirement is a property of the store (applied legacy migrations and data migrations whose code the release deletes), recorded in a sidecar the updater compares before applying; "passing through" means the store applied those steps.

Round 4 (dispositions in `docs/plans/platform-core/reviews/spec-round-4-dispositions.md`; R4-1 refines R3-1):

- SIMP4-2, SIMP4-3, COR4-4, DATA4-2 (R4-1) — decision 13 amended: store interfaces in the SDK, native `c/<subsystem>/` implementations, books-supplied legacy implementations; core holds no ABO key name or row format and no storage grant during the extraction (grants move to the cutover, N15).
- SEC4-3 (R4-1) — decision 13: native-only data lives in books' extension family, never additively in legacy rows; R3-1's exact-match premise corrected (worker confinement is a "nothing else" test, `auth.go:271-297`); extension records carry a legacy-row hash and are suspended until an admin re-confirms.
- DATA4-8, SIMP4-9 — decision 13: "no copy of a legacy field in a side record"; extension records keyed to legacy rows are suspended when their row changed.
- SEC4-1, DATA4-1, OPS4-1, DATA4-3 (R4-2) — decision 13: a moved secret's legacy row and `config.yaml` copy get a never-matching sentinel; rollback fails closed; roll-forward re-imports a re-entered value.
- DATA4-20 — decision 13 names the families core services use (config blob and secret rows, clock and pause keys, system log rows), not `setting:`/`preference:`/`pref:` or "activity".
- OPS4-5 — N14: "floor release" and "stepping-stone release" named distinctly; `InstallFirst` = newest production tag when the deleting PR merges.
- DATA4-18 — N14: updater refusal scoped to S0+ updaters; what pre-S0 auto-update installs see; sidecar from one boot of any S0+ release.
- SIMP4-4 — N14: data-migration requirements join with the first cutover.
- OPS4-11 — N13 and G5: rollback costs are the one table of 09 §9.2.1.
- Lead decision (R4-1) — decision 13: legacy store implementations are supplied on `module.Manifest.LegacyStores`, not `app.Options`.

Integration pass (round 4):

- integration pass (round 4): decision 13 drops the ABO key name `syslog:`; N15's deferred cutover members are `Grant` and `GrantMode` (`Exclusive` is deleted, not deferred; 05-storage §5S.3.13).

Round 5 (dispositions in `docs/plans/platform-core/reviews/spec-round-5-dispositions.md`; resolutions R5-1…R5-7):

- R5-1 (OPS5-1, OPS5-2, SEC5-4) — decision 13 amended: only plaintext secrets move; the four HEAD already stores encrypted (`persistence.go:1955-1958`, `:1989-2003`) are not moved, so a rollback never strands the Basic gate; HEAD's precedence for legacy keys holds until cutover (compose's `BASIC_AUTH_ENABLED`, `docker-compose.yml:20`); the core Basic gate refuses "enabled with no password"; G5 says "plaintext".
- R5-6 (DATA5-2, DATA5-3, SEC5-1, SEC5-2) — decision 13 amended: `legacy_ref.fp` covers security-relevant fields only; older-build revocations are monotone; a key with no legacy-equivalent scope gets a grants-nothing legacy scope, never an empty list; G5 follows.
- DATA5-4 — decision 13 and G5: only a record holding a Widening field is suspended; others are re-stamped by their next write.
- SIMP5-8 — decision 13 cites 03 §3.3 "Store interfaces" and 03 §3.7 instead of restating the store and family lists.
- SEC5-11 — N13 cites the five early fixes of 05-authn §11.1 as the single hotfix list instead of restating four.
- integration pass (round 5): decision 13 — a key with no legacy-equivalent scope gets ABO's `["none"]` (05-authn §4.4), named beside "a scope that grants nothing".
