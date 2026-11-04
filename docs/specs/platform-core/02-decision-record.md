<!-- file: docs/specs/platform-core/02-decision-record.md -->
<!-- version: 1.14.0 -->
<!-- guid: cd524ddf-51e6-46ad-b7fe-f6fc8b6d9dc9 -->
<!-- last-edited: 2026-11-04 -->

# 02 — Platform-core decision record (supersedes 2026-09-18)

## Status

**Accepted for planning** (2026-11-04). Supersedes
[`docs/architecture/2026-09-18-pluggable-media-platform-decision-record.md`](../../architecture/2026-09-18-pluggable-media-platform-decision-record.md),
which was "Proposed" and never implemented (`raw/08-prior-art.md` §3: 6 of its 13 decisions are
reflected in code, 4 partially, 3 not at all). The 2026-09-18 evaluation and adversarial review
remain **inputs**; their eight "non-negotiable gates" are inherited as acceptance criteria (§2.4).
The independent advisor review (phase 4) and review rounds 9 and 10 amended decisions 9 and 13 and N3, N5, N7,
N9, N10, N12, N14 and N15 and the answer to Q5 (binding resolutions A-1…A-17, R9-n and R10-n in
`docs/plans/platform-core/reviews/`). **The owner decisions D10–D12 of 2026-11-04 (§2.6) override every earlier
resolution** that kept backward compatibility, HEAD's stored representation, binary-swap rollback past a migration
or a soak: decision 13, N9, N14, N15, G5 and Q5 are restated below in their D10–D12 form, N13 is deleted, and the
representation-neutral amendments (A-2) to N5 and N10 are removed.

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
| 9 | Ebooks are the first new module; TV/movies wait for acquisition contracts | **Changed (amended by A-15, 2026-11-04)** | The owner's priority is TV and Movies replacements. Ebooks get a short note (§10). TV/Movies still wait for the acquisition contracts — which are now sketched (§05-download, §05-indexer, §10 `acquire`): `acquire`, `integrations/indexer` and the download grab/monitor pipeline are **deferred-build** (03 §3.4), built when the first TV/Movies app starts. **Amendment A-15 (advisor, ADV-S9):** those deferred contracts are **design sketches (non-normative)**, which the SDK freezes none of; D5's proof depth holds, because 10 still shows that the frozen contracts fit TV and Movies. |
| 10 | Module enablement is restart-bound | **Kept** | Gin/`ServeMux` routes cannot be unmounted and the executor has no per-module drain (`raw/08-prior-art.md` §2 row 1). Same rule in the new kernel (§03 §3.7). |
| 11 | Event bus is notification-only; correctness uses durable ops or an outbox | **Kept, outbox specified** | The bus has no ack/retry/order (`raw/12-cross-cutting.md` §5 — three unrelated buses exist). Core provides one `events` bus (notify-only) and a durable same-batch `outbox` (§05-events). |
| 12 | Audiobooks and ebooks link at Work, not Edition | **Dropped** | Moot for this operation (see 7, 9). Re-decide with the ebook app. |
| 13 | Existing audiobook Pebble keys stay in place during extraction; new modules use namespaced keys | **Changed (D10, 2026-11-04; replaces the R3-1, R4-1, R5-1, R5-6, R6-3, A-2, A-3, R9-1, R10-4 and R10-5 amendments)** | ABO's 258 key families (256 already `name:`-prefixed, 2 bare; `raw/05-storage.md` §3) are **claimed** by the `books` module as legacy prefixes; new keys use `m/<module>/` and `c/<subsystem>/`. **Books' own domain families** (about 240) stay in place permanently, because renaming them buys nothing. **The core-service families** — users, OAuth identities, the invite allow-list, API keys, roles, task runs, clocks and markers, the config blob and secret rows, system log rows, import roots, AI jobs, integration instances, the webhook rule and the pending dirty set (03 §3.7) — **move once, one-way, in R2** into their subsystem's native `c/<subsystem>/` keyspace (03 §3.8 M2–M6). Core holds no audiobook key name or row format (03 R1): every core service persists through its store interface's **native implementation only**, and ABO's HEAD rows reach it through the subsystem's **importer**, which a books migration step calls after decoding the rows, deleting them in the same batch (03 §3.3 "Store interfaces and importers"). There is no legacy store implementation, no `legacy` package, no storage grant and no HEAD lock held across core's commit. Every ID that books' rows reference is kept; a row that does not decode is quarantined and reported, never dropped. Sessions and one-time tokens are reset, throttle and quota holds, indexes and caches are rebuilt, and API keys, the Audiobookshelf phone apps' sessions and task-run and log history survive (D12 F2). The hardened design (hashed sessions and one-time tokens, `mck_` keys, native scopes, encrypted secrets, operator-locked config with one fail-closed rule for the auth and Basic-gate keys) applies to ABO from R2, as to every native store. Storage-enforced `CheckedWrites` cover every native keyspace; claims are strict from R1 with a shadow emergency dial (03 §3.3 `storage`). Older binaries refuse a migrated store through the per-owner levels and books' projection onto HEAD's format stamp (03 §3.7); rollback is an offline restore of the pre-migration checkpoint or an off-host archive plus the previous binary (03 §3.8). |

## 2.3 New decisions

| # | Decision | Source |
|---|---|---|
| N1 | Core is a nested Go module `github.com/falkcorp/mediacore` at `core/`, wired by `require` + `replace`; the build never depends on `go.work`. An `exclude` of the stale monolithic genproto in **`core/go.mod`** (not the root) makes an uncommitted developer `go.work` (`go work init . ./core`) safe and keeps the root `go.mod` installable after the split. The path is the post-split path. | D3; §03 §3.2; `raw/00-baseline-build.md` E1–E10; round-1 modules-build review X2–X4 |
| N2 | Core moves to its own repository only when the measurable criteria in §12 hold. | D3 |
| N3 | Native API under `/api/core/v1` and `/api/<module>/v1`; an optional per-app *arr-v3 facade through `core/arrfacade` (a non-normative design sketch, build deferred to the first TV/Movies app, 03 §3.4; amended by A-15); no commitment to full Sonarr/Radarr parity or database import. | D2 |
| N4 | Composition is explicit: `app.New(opts, modules...)`. No `init()` registration and no process-global registries in core (rule R5, which binds core only: an app may keep per-`App` façades over core handles, as ABO does for `config.AppConfig` and its logger shim). `internal/serviceregistry`, `internal/plugin` and `pkg/plugin/sdk` are retired in favour of `module.Registrar`. | §2.4 hypothesis verified, `raw/07-plugin-registry-sdk.md` |
| N5 | Multi-user, single-tenant. Permissions are namespaced scopes `<module>:<action>`; roles are data; modules declare their permissions; admin is a permission (`core:admin`), never a role-name check. No tenant IDs. ABO's stored permission atoms become native scopes in R2, through one books-owned atom → scope table (03 §3.3 `rbac`). **Amendment A-2 (legacy atoms stored through the extraction): removed by D10.** | D7; `raw/10-auth-rbac.md` G1–G4 |
| N6 | UI = core React shell package + per-module UI contributions registered at build time; one shell per binary. | D6 |
| N7 | Core's HTTP framework is standard-library `net/http` (`ServeMux` patterns); core never imports gin. ABO's gin router is mounted through `httpapi.MountLegacy` **permanently**, behind core's perimeter middleware with gin's own auth chain unchanged inside; the auth engine swap happens in exactly one PR, the R2 identity flip F4 (03 §3.8). There is no gin-retirement stage: a books route group moves to native `httpapi` only when it is rewritten for its own reasons. Trade: ABO keeps gin and two routing stacks indefinitely; core is unaffected. **Amendment A-1 (advisor, ADV-1):** HEAD's gin routes are not all distinct `ServeMux` patterns, so the legacy manifest registers each connected set of same-owner conflicting routes as one generalised pattern when every member carries the same requirements, and fails generation otherwise (03 R6, 05-http-api §4.8). | §05-http-api; D8 "prefer stdlib where equivalent"; round-1 SIMP-2 |
| N8 | The task system's public API makes parallelism a required, typed property (`tasks.Parallelism` has no valid zero value); a sequential run must state why. | §2.7; §05-tasks |
| N9 | One task system: the v3 authoring model (`pkg/ops`) and the production v2 executor's semantics become `core/tasks`, with core's executor **re-implemented** against `tasks.RunStore`/`RunState`. **Restated by D10:** every ABO definition is converted to a native definition before the R2 tasks flip, and that flip deletes HEAD's engine (no opsbridge, no batch releases); stored kinds are renamed once by books' kind-rename map during the tasks migration (03 §3.8 M4). Every metric family has exactly one owner (R9-3): the PR that gives a family a core emitter deletes its ABO emitter, and core's perimeter records every HTTP request, the gin mount's included, with `otelgin` removed in R1 (03 §3.6). Metric names may change, and the dashboards and rules change in the same PR. Which files core re-implements rather than moves is planning information in 04 §8.6, not a CI rule (**amended by A-4**, advisor ADV-S3); v1 rows, `maintenance.MaintenanceJob`, and the v2/v3 duplication are retired. | `raw/06-tasks-ops.md` §0, §7 |
| N10 | Every secret is encrypted at rest through `core/secrets`; config holds references, never values. ABO's plaintext blob secrets and HEAD's four encrypted secret rows are re-encrypted into `c/secrets/` in R2 (03 §3.8 M2). **Amendment A-2 (plaintext blob secrets kept through the extraction): removed by D10.** | `raw/10-auth-rbac.md` G12 |
| N11 | Toolchain: latest stable Go (go1.27.3 at 2026-11-03) and latest stable frontend tooling, exceptions recorded in §13. | D8 |
| N12 | Nothing paid: no paid tier, licence fee or SaaS dependency anywhere in the design. **Amendment ADV-9 (advisor, A-16):** D9 is "nothing paid", not "OSI open source only"; the repo's PolyForm Noncommercial 1.0.0 licence (`LICENSE:1`) costs nothing, so it meets D9, and relicensing core is an optional reuse decision (14 Q-A). | D9 |
| N14 | **Legacy migrations run first; one upgrade floor, in R3 (restated by D10, D11).** Books' existing legacy migrations 1..66 run in a boot phase before every core and module migration, on books' own claims, and are kept until R3. R3 deletes them, together with the HEAD-format readers and the R2 cutover steps, and declares books' one upgrade floor, `UpgradeFloor{InstallFirst: R2}`. A store that has not run R2's level step is refused at boot, before any write, with exit 78 and a last log line naming R2; `--print-upgrade-floor` prints the requirement. There is no applied-steps sidecar, no stepping-stone release distinct from R2, and no pre-swap floor check in the updater or the deploy recipe. An empty store is exempt (03 §3.7). | Round-2 resolution R2-4; D10, D11; 03 §3.7; 05-updater |
| N15 | **The cutover happens inside the extraction, in R2 (restated by D10–D12).** Moving the core-service families from ABO's HEAD rows to native core keyspaces is R2's one ordered list of `BootBlocking`, resumable migration steps (03 §3.8 M1–M8), behind the mandatory pre-migration checkpoint and the per-owner level guard. Books runs each step: it decodes its HEAD rows and calls the subsystem's importer, so core reads no app row and needs **no storage grant**. The hardening that the former H1–H3 cutovers would have done after the extraction lands directly in R2: hashed sessions and one-time tokens (web sessions and one-time tokens are reset, not rehashed), `mck_` keys beside the kept `abk_` hashes, native scopes with the ambient rule for unconfined principals, encrypted secrets, and operator precedence with the fail-closed rule for the auth and Basic-gate keys. The level step raises `books` and `core`, so R0 and R1 binaries refuse an R2 store. The pre-migration checkpoint holds plaintext secrets and raw tokens and is deleted automatically after 7 days (D12 F4); the no-raw-token and no-plaintext-secret canaries cover the live DB directory with that checkpoint excepted. | D10–D12; 05-storage; 09 |

## 2.4 Inherited acceptance gates (from the 2026-09-18 adversarial review)

Each is mapped to the spec section that satisfies it; 09 names the release that proves it.

| Gate | Satisfied by |
|---|---|
| G1 a disabled module contributes no route, scheduled op, worker, nav entry or filesystem mutation after restart | §03 §3.7, §06 (kernel enablement), `coretest` disabled-module test |
| G2 a failed module migration prevents startup without corrupting data | §05-storage (each migration step commits in one batch, except books' existing legacy steps 1–66, which are non-atomic, idempotent `LegacyMigration`s behind the mandatory pre-migration checkpoint; the R2 cutover steps commit one batch per chunk and resume after a crash; boot halts on error with the step uncommitted; one pre-migration checkpoint per boot) |
| G3 the client rejects unknown or version-incompatible module descriptors | §07 (shell intersects server manifest with compile-time registry; version check) |
| G4 no correctness-critical transition depends solely on the in-memory bus | §05-events (outbox), rule enforced in review checklist |
| G5′ (replaces G5, D10) the existing audiobook DB is migrated forward without loss, every older build refuses the migrated store, and rollback restores the pre-migration copy plus the previous binary | 03 §3.7 and §3.8: R1 changes no user data (the R0 binary opens an R1 store); R2's steps keep every ID, quarantine what they cannot decode, and are checked per family (counts, IDs, references) on a fixture corpus written by the R0 binary; the level step makes R0 and R1 refuse the store; rollback is `make rollback-restore` with the checkpoint or an off-host archive, keyring and state files included; R3's floor refuses a store that skipped R2 (N14) |
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
| Q5 config/endpoint compatibility window for the ABO extraction | **Answered by D10: there is no window.** Renamed config keys, env names, metric names and HTTP paths change in the R2 flip of their subsystem, with the dashboards, rules, scripts and skills in the same PR, and the R2 release note lists them. A set retired name of a `ProtectAdmin`-or-higher or secret key refuses boot, and storage refuses to create a store while a retired DB-path name is set. The one frozen external contract is the Audiobookshelf-compatible API (D12 F3). Books routes on the permanent legacy mount (N7) keep their paths unless an R2 flip replaces them. |

## 2.6 Owner decisions D10–D12 (2026-11-04)

These are owner decisions like D1–D9 and are fixed inputs. They override every earlier resolution (A-n, Rn-n,
FIN12-n) that conflicts with them.

| # | Decision | Consequences in this spec |
|---|---|---|
| D10 | **No backward compatibility.** Data formats, storage layouts, key names, HTTP paths, metric names, config keys, env names, CLI flags, the webhook format, the OAuth callback path and the fingerprint-worker API may change. Dashboards, rules, scripts and skills are updated in the same PR as the rename. A thing stays unchanged only where that is cheaper; no compatibility machinery is built (no aliases, windows, Gone steps, compatibility ledger, `legacy` package, legacy stores, HEAD-parity goldens, legacy env decoding or lock parity). | Decision 13, N9, N14, N15, G5′, Q5 above; 03 §3.3, §3.6, §3.8. The webhook format, the OAuth callback path, the fingerprint-worker API, `/health`, `/metrics` and the CLI flags stay unchanged because that is cheaper (03 §3.8 "Kept contracts"). |
| D11 | **No soaks.** At most an optional 1-day smoke run, the owner's choice. The shortest path to the finished extraction wins. Trunk-based: code lands dark, one flip PR per subsystem, and every merge passes `make ci` and e2e. No `Stage-Release:` trailer, soak ledger or soak gate. | Four releases R0–R3 (03 §3.8); safety is the test suites, the pre-migration checkpoint, the off-host archive and restore. |
| D12 | **Cutover scope** (owner answers F1–F4). **F1:** automatic promotion and auto-update are paused from the first R2 flip until R2 ships, so R2 is one cutover release. **F2:** at the R2 cutover API keys (`abk_`) and the Audiobookshelf phone apps' sessions and tokens survive; web sessions and one-time tokens are reset (users sign in once); task-run and log history is kept, converted; throttle and quota holds, indexes and caches are reset or rebuilt. **F3:** the only frozen external contract is the Audiobookshelf-compatible API used by the phone apps (its unprefixed root routes, its JWT secret's value and its auth semantics), pinned by its conformance suite; webhooks are not part of it; everything else may change. **F4:** the pre-migration on-host checkpoint, which holds plaintext secrets and raw tokens, is deleted automatically after 7 days. | 03 §3.7, §3.8 (M3–M6, "What survives R2", "Kept contracts", "Rollback"). |

Kept with D10–D12: the six early security fixes of 05-authn §11.1 ship first, in R0, and the tests of those whose
code R2 deletes are ported as property tests onto core auth; data is migrated forward one-way by in-binary
migrations, never dropped, with undecodable rows quarantined and reported; rollback is an offline restore of the
checkpoint or an off-host archive (keyring and state files included) plus the previous binary; an older binary
refuses a migrated store. D1–D9 are unchanged, and the deferred-build sketches stay deferred.

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

Round 6 (dispositions in `docs/plans/platform-core/reviews/spec-round-6-dispositions.md`; resolutions R6-1…R6-7):

- SIMP6-2, SIMP6-3 (R6-3) — decision 13 amended: the store interfaces' native contract is frozen; legacy-only identifiers live in the `legacy` package outside the API promise, so cutover removals are not breaking.
- SEC6-1 (R6-6) — decision 13 amended: an operator env value or flag that enables auth always takes effect, the legacy `ENABLE_AUTH=true` included; a disabling value keeps HEAD's precedence.
- SEC6-3 (R6-5) — N13: six early fixes; the sixth makes every `apikey:` writer take HEAD's last-use lock.
- OPS6-1 — N14: the S0 updater hotfix and the `deploy` recipe compare the new binary's `--print-upgrade-floor` with the store's sidecar; the S8 updater also checks the signed manifest.

Round 7 (dispositions in `docs/plans/platform-core/reviews/spec-round-7-dispositions.md`; resolutions R7-1…R7-8):

- R7-1 (SEC7-1) — decision 13 amended: the legacy config store writes the narrowing `enable_auth: true` (and the Basic gate's `true` with a stored password) at P4d step 1, so operator-enabled auth survives a rollback.
- SEC7-13 — decision 13: "operator env value or operator-file value"; no auth flag exists at HEAD.
- COR7-5 — decision 13: `legacy` identifiers are deleted in the first release after the cutover that no longer sets the member.
- SIMP7-8, R7-2 — decision 13 names `legacy.LibraryRootSource` and `legacy.Maintenance`.
- R7-8 (MOD7-1) — N9: core's executor is a re-implementation, not the v2 executor "cleaned"; ABO's registry files stay as opsbridge until T8.

Round 8 (dispositions in `docs/plans/platform-core/reviews/spec-round-8-dispositions.md`; resolutions R8-1…R8-6):

- R8-6 (MOD8-1, MOD8-2) — N9: the move rule admits 04 §2.2's enumerated mechanical rewrites (logger cut, store-interface cut); `childop` and `opsmetrics` stay as opsbridge until their last ABO user is native, `opsmetrics`' successor in `internal/executor`.
- R8-4 (SEC8-1, SEC8-6) — decision 13 amended: the Basic-gate narrowing applies whenever the gate is enabled (fail-closed without a readable password; release note), `enable_rate_limit` narrows, canonical-only operator values are reported through `operator-names.json` and `canonical_only_operator_values`.
- SIMP8-4 — decision 13: `legacy.Maintenance` is deleted in the S4 r2 PR.

Advisor (dispositions in `docs/plans/platform-core/reviews/spec-advisor-dispositions.md`; resolutions A-1…A-17):

- A-1 (ADV-1) — N7 amended: same-owner conflicting legacy routes register as one generalised pattern.
- A-2 (ADV-3, ADV-S1) — decision 13 amended: representation-neutral extraction; the extension family, `legacy_ref.fp`, suspension, `["none"]`, the moved-secret sentinels and the R6-6/R7-1/R8-4 amendments are superseded; rollback costs nothing for identity or secrets. N5, N10 and G5 amended accordingly; N15 names the identity, secrets and auth-precedence hardening cutovers.
- A-3 (ADV-S2) — decision 13 amended: no `CheckedWrites` on ABO's legacy families; HEAD's lock discipline kept.
- A-4 (ADV-S3) — N9 amended: the move/re-implementation taxonomy is planning information in 04 §8.6, not a CI rule.
- A-6 (ADV-S5) — N14 amended: the S8 updater is the moved S0-hotfixed updater; signed manifests are deferred-build.
- A-14 (ADV-S8) — §2.5 Q5 amended: no persisted usage gate; dates, release notes and an owner check of the process-local counter.
- A-15 (ADV-S9) — decision 9 and N3 amended: deferred contracts are non-normative design sketches.
- ADV-8 — decision 13: the `config.yaml` sentinel clause deleted; no moved secret has a file copy at HEAD; core writes only keys HEAD's `SaveConfigToFile` manages.
- ADV-9 — N12 restated: D9 is "nothing paid"; PolyForm Noncommercial meets it; relicensing is optional (14 Q-A).
- Status — the advisor's amended decisions are listed under "Status".
- integration pass (advisor): decision 13 amendment A-3: a legacy writer may stage a voluntary `Expect`/`GetForUpdate` inside a `Serialized` legacy claim (lead ruling).

Round 9 (dispositions in `docs/plans/platform-core/reviews/spec-round-9-dispositions.md`; resolutions R9-1…R9-11):

- R9-1 (COR9-1, DATA9-1, DATA9-2) — decision 13 amended: `legacy.WriteLock` and `storage.CommitLocked` hold HEAD's mutexes across core's commit; one instance per legacy store family.
- R9-3 (MOD9-1) — N9 amended: one owner per metric family; T0+T1 deletes ABO's `opsmetrics` emission of the executor's families.
- R9-5 (SEC9-2) — N15: H3 moves only auth-enable precedence.
- R9-11 (SEC9-3, DATA9-8) — N15 amended: every owner raised; checkpoint and old Pebble files deleted by forced compaction; archives keep the old representation.
- Status — round 9's amendments listed.
- integration pass (round 9): decision 13: the lock list follows 05-storage v1.11.0 (identity mutexes behind 05-authn's lock names, `opsMu`, `configBlobSaveMu`; not `counterMu`), `WriteLocks(names []string) []storage.Lock` and `storage.CommitLocked(ctx, b, d, stage, locks...)` taking each distinct lock once in `LockRank` order.

Round 10 (dispositions in `docs/plans/platform-core/reviews/spec-round-10-dispositions.md`; resolutions R10-1…R10-11):

- R10-4, R10-5 (COR10-2, COR10-5, DATA10-1, DATA10-2, DATA10-3) — decision 13 amended: the hold is a mark on the batch (`storage.LocksHeld(b, l)`), one total lock order, the post-commit gap-fill write inside the hold, the kernel resolves the lock names.
- R10-3 (MOD10-7) — N9 amended: `ops_abandoned_calls` moves in one PR; otelgin stays the HEAD-route recorder, one owner per series.
- R10-2, R10-11, SEC10-7, DATA10-7 — N15 amended: every pre-cutover checkpoint deleted; H1 deletes live tokens and keeps worker confinement; "re-issue keys" dropped; the first cutover raising `core` lifts `{core, 0}`.
- (all) — introduction names the round-10 amendments.

Owner decisions D10–D12:

- D10–D12 revision (2026-11-04): new §2.6 records D10, D11 and D12 (F1–F4) as owner decisions. Status rewritten. Decision 13 rewritten: books' domain families stay, the core-service families move once in R2 through importers; the R3-1 to R10-5 amendments (legacy store implementations, representation-neutral extraction, HEAD lock discipline, `legacy` package, `CommitLocked`) deleted. N5 and N10: the representation-neutral A-2 amendments marked removed by D10. N7: the auth swap is the R2 identity flip. N9 restated (no opsbridge; one HTTP recorder; names may change). N13 deleted. N14: one floor in R3, no stepping stone or sidecar. N15: the cutover is inside the extraction, in R2, no grants; H1–H3 folded in. G2 names the resumable cutover steps; G5 replaced by G5′. Q5: no window.
