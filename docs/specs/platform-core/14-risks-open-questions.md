<!-- file: docs/specs/platform-core/14-risks-open-questions.md -->
<!-- version: 1.4.0 -->
<!-- guid: e270beb6-8eea-48b1-8397-4ef4ec709956 -->
<!-- last-edited: 2026-11-04 -->

# 14 — Risks, migration hazards, open questions

Every item has an owner section and a recommendation. "Owner" means the repository owner must decide;
everything else is resolved inside the named spec file during review. Review findings are cited by
ID (`docs/plans/platform-core/reviews/spec-round-{1,2,3}-*.md`; dispositions in
`spec-round-{1,2,3}-dispositions.md`; round-2 and round-3 resolutions are cited as R2-n and R3-n).
Round 3's R3-1 replaced the dual-format rollback machinery of rounds 1–2 (adoption copies, mirrors,
side records, Teach-build floors); risks that existed only for that machinery are withdrawn below, not
kept as history.

## 14.1 Top risks

| # | Risk | Likelihood / impact | Evidence | Mitigation (where specified) |
|---|---|---|---|---|
| K1 | **Scale of the books-module residue.** ~620k LOC and 160 packages stay in ABO (04 §8 note 6: 55 leave, 2 new paths); 333 `Store` methods remain books-owned; 114 + ~22 test files use full-store mocks. The extraction can stall halfway with two half-systems. | High / High | [04](04-package-mapping.md), `raw/04-database-store.md` | Strangler stages each end shippable ([09](09-abo-on-core.md)); adapters keep old call sites compiling; ratchet tests stop regressions; the plan sequences the keystone (`database`, `config`, `server` wiring) first. Scope is smaller than v1.0.0 assumed: gin retirement and ABO-global removal are no longer stages (02 N7, 03 R5). |
| K2 | **Data compatibility during the extraction.** Core services read and write ABO's existing generic families (users, roles, API keys, settings and the config blob, ops history, activity) while a rollback to the previous release must still open and read the DB (gate G5). | Medium / Critical | [05-storage](05-storage.md), [09](09-abo-on-core.md); DATA-1, DATA-3, SEC-1; R3-1 | One stored format per family (02 decision 13): legacy-format adapters over the existing keys, reached through explicit, logged storage grants; no copy into `c/…`, no mirror, no rebuild at boot, no claim hand-over, so the previous release reads every row the new build wrote. Native-only data is additive where the legacy format tolerates it — namespaced scopes in role/key lists grant nothing to an older build, which matches permissions by exact string (`internal/server/middleware/auth.go:430-455` @748fd787a) — or in core-only families older builds never read. New API keys never have empty scopes (empty = every role permission today, `auth.go:405-408`). One store-level compatibility guard (per-owner level + sidecar + flag + rollback-script check, owned by 05-storage) for real format changes; pre-migration checkpoint; moving a family to a native keyspace is a later cutover (02 N15). |
| K3 | **Two auth stacks, permanently.** ABO's gin router stays mounted through `httpapi.MountLegacy` with gin's own auth chain inside (02 N7), next to core `httpapi`+`rbac`. What v1.0.0 treated as a transition is now the steady state. | Medium / High | [05-rbac](05-rbac.md), [05-http-api](05-http-api.md); SIMP-2, SEC-13, OPS-11 | Core perimeter middleware (peer allowlist, recovery, headers, CSRF, rate limit) wraps the legacy mount; the auth engine swap happens in exactly one stage (09 S4); the permission-equivalence test (allow/deny identical across all 572 routes for every seed role) becomes a **permanent** CI test; every legacy route is in a reviewed manifest with a permission (03 R6); fail-closed registration; live defects hot-fixed independently (`todo.d/2026-11-04_platform_core_followups.md`). |
| K4 | **Toolchain churn:** several "latest" releases are days old (bleve v2.6.2, @babel/core 8, vitest 5.1, plugin-react 6.2.1); TypeScript 7 has no JS API so lint and `openapi-typescript` stay on the TS 6 alias; react-router 8 removes `react-router-dom` (122 ABO files: 52 source, 70 test; 07 §7.3.3's grep). | High / Medium | [13 §13.7](13-toolchain.md) | 14-day release-age rule, owned by 13 §13.6 and binding on every version target in the spec (FE-21); exceptions E1–E4 recorded with exit conditions; the react-router 8 migration is a separate ABO series that **precedes** shell adoption (13 E4, FE-2, SIMP-25). |
| K5 | **Single-process failure domain and module trust.** Modules are compiled-in, trusted code; keyspaces are organisation, not access control. | Certain / Medium | 2026-09-18 adversarial review residual risks (`raw/08-prior-art.md` §2) | Accepted. R8 prevents accidental cross-module writes; manifests cannot grant `core:*` ([05-rbac](05-rbac.md)); no third-party in-process code (decision 5). |
| K6 | **"Small core" grows into a god package.** | Medium / Medium | adversarial review; SIMP-1 | Non-circular promotion rule (03 §3.4): a package is built only if ABO consumes it during the extraction or core itself needs it; everything else is **deferred-build** (`acquire`, `arrfacade`, `integrations/indexer`, download grab/monitor). The Build column of 03 §3.3 is re-checked at every stage gate; the API-stability gate measures the SDK surface ([12](12-repo-split.md)). |
| K7 | **Replace directive and `go install`:** `go install github.com/falkcorp/audiobook-organizer@version` fails while the root `go.mod` carries a `replace` (or an `exclude`; `cmd/go` refuses either, MOD-3 X4). | Low / Low | 03 §3.2 item 5 | Not used by the release path; documented in the README of the release. The genproto exclude lives in `core/go.mod`, so the root becomes installable as soon as the split drops the `replace` (12 §12.8.2 step 7). |
| K8 | **Stale transitive `genproto`** reappears if `cockroachdb/errors` changes or another dependency pins the monolithic module, or the exclude outlives its reason. | Low / Medium | `raw/00-baseline-build.md` E7; MOD-4 X2 | Exclude in `core/go.mod` from S0 (applies to every workspace that includes core); `core/scripts/ci/check_genproto_exclude.py` runs in a temp copy and fails unless the line is present, the workspaces build with it, and without it at least one applicable probe's `go mod graph` shows the requirer **and** that probe's build fails with `ambiguous import` (stale otherwise; the live graph never lists an excluded version, MOD2-5 X10/X16); core mode runs in `make -C core exclude-check`, root mode in the root job `genproto-exclude-root` (MOD3-12); the build never depends on `go.work` (03 §3.2 item 1). |
| K9 | **Licence mismatch with D9.** The repo is PolyForm Noncommercial 1.0.0 (`LICENSE`), which is source-available, not OSI open source; core's history inherits it. | Certain / Medium | `LICENSE:1`, [12](12-repo-split.md) open item 1 | Owner decision Q-A below, before the split. |
| K10 | **Production unknowns** (key counts per family, session counts, op-history size, row sizes) decide whether, and how, a family is ever cut over to a native keyspace, and whether that cutover fits the batch cap or takes the resumable path. | Certain / Low | [05-storage](05-storage.md), [09](09-abo-on-core.md) | Cutover is outside the extraction and decided on measured data (02 N15); both branches (in-batch, resumable checkpointed data migration with dry run) are designed; read-only census commands are listed in 09 for the owner to run. |
| K11 | **The first core stage (S0) breaks the Docker and web builds.** Both Dockerfiles copy only `go.mod go.sum` before `go mod download` (`Dockerfile:54-55`, `Dockerfile.build-cgo:52-53`), which fails once the root `replace`s `./core` (MOD-1 X1); eleven-plus `cd web && npm ci` entry points break once `web/` depends on built `@mediacore/*` (MOD-9, FE-13). Prereleases do not build images, so only the production release would find out. | High (if unmitigated) / High | MOD-1, OPS-1, MOD-9, FE-13, MOD3-13; `.github/workflows/prerelease.yml:48` (`docker-enabled: false`) | S0 updates both Dockerfiles' Go stage (copy `core/go.mod`, `core/go.sum`) and `.dockerignore`, and adds a PR job that builds the image; the Docker frontend stage and every `npm ci` entry point (one `scripts/ci/build_web.sh` behind `make web-deps`) change in the UI-0 PR that creates `core/web`, because before it there is no `core/web/package.json` to copy (03 §3.2 item 4; 09 S0 and UI-0 own the lists and the done-checks). |
| K12 | **Releases are skipped.** Installs jump from release N to the latest (the updater fetches the latest release, Docker users pull `:latest`, archives from old releases are restored), so a build can meet a store that never ran an intermediate release's steps. | Medium / Critical | OPS-2, OPS-3, OPS3-7; `internal/updater/updater.go:295` (fetches `releases/latest`) | During the extraction no step rewrites or deletes a legacy family (02 decision 13), so a skipped release cannot strand data in a format the running build does not read. The steps that do change formats — books' legacy migrations — are bounded below by the minimum-upgrade-from release, expressed as what the **store** must have applied and checked by the updater against the store's sidecar before applying (K18, 02 N14). A later cutover (02 N15) declares its own store requirement the same way. Compatibility levels are defined by reader capability (09 §9.2.1, §9.2.2). |
| K13 | **The one-format model has deliberate costs.** A rollback costs every user one re-login (sessions are hashed in a core-only family no older build reads) and re-entering every secret the new build moved from the config blob to the encrypted store (the blob field is blanked). Native-only grants (namespaced scopes in legacy role and key lists) are invisible to an older build, so after a rollback a role or key holds only its legacy atoms. A native feature that needs a different stored shape waits for that family's cutover. | Certain / Medium | DATA-3, DATA-4, DATA-5, SEC-1, SEC-19, OPS-19; R3-1 | Accepted trades of R3-1 (owner Q-D and Q-E recommendations). Every narrowing on rollback is fail-closed (exact-match permission check, `internal/server/middleware/auth.go:430-455`); the release note of the first build that moves secrets states which ones must be re-entered; rollback runbooks state the re-login. |
| K14 | **Dependency skew between the binary and core CI**, and its twin, **tidy coupling**: the root build takes the MVS maximum over the root and `core/go.mod`, core CI tests only core's requirements, and a core-only `go mod tidy` leaves the root without the `go.sum` entries core now needs (MOD2-6 X11). | Medium / Medium | MOD-11, MOD2-6 | Dependabot gomod for `/` and `/core` in one group, verified to yield one PR; `scripts/ci/check_module_skew.py` (12 SC4(c), 03 §3.2 item 6) with `--pin` for pinned modules (13 §13.5.2), blocking in root `make ci` from S0, and the Dependabot group covering security updates too (MOD3-16); a PR that changes `core/go.mod` carries the root tidy result, enforced by per-module `tidy-check` and by `core-ci.yml` building the root (03 §3.2 item 4). |
| K15 | **A dropped legacy env name silently points ABO at a new, empty database** (`DATABASE_PATH` is the only DB-path source in the shipped unit and plist), or turns basic auth off for compose users. | Low after mitigation / Critical | OPS-5 | Env names that are the only source of a required setting in a shipped deploy file are **permanent** (compat ledger "permanent" class); boot refuses to start on an empty store when a legacy DB path is set but unread (09, 05-config). |
| K16 | **The live restore path stays broken until backup's stage.** Today's restore extracts over the live store under a running engine; the staged restore arrives only with backup's stage. | Medium / Critical | OPS-15 | Staged restore pulled forward (or live-target extraction disabled with an offline runbook) (09 §9.5, 05-backup); the revocation journal is re-applied after every restore (05-backup); "staged restore shipped" is a precondition of any later cutover (02 N15). |
| K18 | **The stepping stone is real.** Books' legacy migrations 59–66 write families that core reads later and call code that 05-tasks T8 deletes, so an install whose store has not applied what the declared **minimum-upgrade-from release** requires cannot jump straight to a build that no longer carries that code. The updater and Docker `:latest` users would otherwise meet a refusal. | Medium / High | DATA2-2, OPS2-11, OPS3-7; R2-4 | Legacy migrations run before core claims are enforced, under a logged legacy-migration grant, and their code is kept until the minimum-upgrade-from release (02 N14). The requirement is checked on the **store**, not on the running binary: release manifests name what the store must have applied and the release to install first; every guard-aware boot writes the store's applied steps to a sidecar; the updater compares the two before applying and offers the stepping-stone release instead (05-updater); boot refuses a store that falls short with a message naming the release to install (09, 05-storage). |
| K19 | *(Withdrawn in round 3, R3-1.)* The rollback-floor cost it recorded existed only for Teach builds, which R3-1 removes; the costs that remain are K13's. | — | SEC3-1, SEC3-3, OPS3-5; R3-1 | — |
| K20 | **Gates that gate nothing.** Main merges without CI gating (`auto-revert.yml`), `prerelease.yml` cuts an RC on every push and auto-promotes a production minor every 10 RCs, so the extraction's gates (ratchets, ledger, stage-visibility, Docker build, rollback rehearsal) would run after the code ships. Merge tooling can bypass a required check with admin merges (`--admin`). | High (if unmitigated) / Critical | OPS2-1, OPS3-3, OPS3-4, OPS3-5, OPS3-6; R2-6, R3-2; `.github/workflows/auto-revert.yml:8-11`, `prerelease.yml:59-131` | **One requirement (R3-2):** every PR to main passes `make ci`, including the extraction gates, before it merges. A release can then only contain commits that passed, so promotion needs no gate of its own (09 drops the promotion-time mechanics). How the requirement is enforced is owner question Q-I. W (a) counts production minors **and** 30 days (09 §9.2.1). |
| K17 | **Merging single-app installs into one store needs a new core contract** (DATA-30). Two installs each hold `c/auth/`, `c/rbac/`, `c/tasks/`, `c/config/`, `c/secrets/` under different keyrings, plus their own `c/storage/install`, bookkeeping, levels and `Sequence` high-water marks, so "copy the keyspaces" collides. | Low (only when a multi-app suite merges existing installs) / High | DATA-30; [05-storage](05-storage.md) §5S.11 item 6; [11](11-multi-app.md) §11.5 row 4 | A core **storage export/import** capability (per keyspace, version and level check, ID-remap hooks for users, roles and keys, keyring merge, sequence rebase; module keyspaces copy as-is). It changes a core contract, is not built until multi-app exists (lowest priority), and needs no change now: per-owner levels already keep one app's raise from blocking another's rollback. |

## 14.2 Migration hazards (each has a guard)

| Hazard | Guard |
|---|---|
| Core silently requires the app module via `go mod tidy` (`raw/00-baseline-build.md` E2) — including through a test file | `archtest.TestNoAppRequire` (R1a); `TestCoreImportsOnlyCore` over `go list -deps -test` (R1b, MOD-16) |
| ABO rewired onto a core `internal/` or `kernel/` package it may not import (stops compiling), or a package moved before its replacement surface exists | Visibility rule (03 §3.4) and R2: a package with an ABO importer moves to a public path or stays in ABO (MOD-2); the committed stage-visibility gate reads the non-test imports of every root package, including `cmd/*` and the root `main`, in root `make ci` (03 §3.5.1 item 4; R3-3, MOD3-1, MOD3-2) |
| `examples/hello` imports core internals (the `internal/` rule is path-based) | `archtest.SDKOnly` (03 R2, MOD-7) |
| Repo-walking ratchet tests moved into core scan nothing | Per-move test classification step in every move brief; ratchets stay in the root |
| Root `./...` does not reach `core/`; `make ci` steps and CI jobs silently skip core or fail on moved packages | `GO_MODULE_DIRS := . core` for per-module targets plus the per-target classification and per-stage checklist in 09 S0 (03 §3.2 item 4, MOD-8) |
| Docker module-download layer lacks `core/go.mod`; web builds lack built `@mediacore/*` | S0 Dockerfile and entry-point changes plus a PR image job (K11) |
| Checkpoint tags take over `git describe` and the reported version | Checkpoints are `refs/core-checkpoints/v0.Y.Z`, not tags (12 §12.4.1, MOD-5) |
| `version.Version` drifts from checkpoints/tags | `scripts/ci/check_core_version.py`; release step 1 (12 §12.4.3, MOD-12) |
| Layering guard fails on the first move, or passes with a loader that sees nothing | Guard redesign (03 §3.5.1) is the first code task; ratio floor; in-module control `. → cmd`; classifier control (MOD-14); core guard's control staged S0 → S1 → S2 (MOD3-8) |
| Core imports hide wrong-way edges in ABO (every core package legal from every ABO layer) | Core layers mapped onto ABO's scale (L0–L5 → −1, L6 → transport 5, L7–L9 → entry 6), mapped layers feed the computed layer, `loadGraph` keeps mediacore imports, `allowed` edges re-evaluated when their target moves (03 §3.5.1, MOD2-7, MOD3-5) |
| A core dependency change breaks the root build (`missing go.sum entry`) | Tidy-coupling rule, per-module `tidy-check`, `core-ci.yml` builds the root on `core/go.mod` changes (K14, MOD2-6) |
| Rollback widens access | One stored format per family, so no mirrored legacy row exists to widen (02 decision 13): native scopes stored additively grant nothing to an older build (exact-match check, `internal/server/middleware/auth.go:430-455`), new API keys never carry empty scopes (`auth.go:405-408`); the live security hotfixes of `todo.d/2026-11-04_platform_core_followups.md` ship independently and early (K13, R3-1) |
| An old install skips the books legacy migrations, or a release deletes code they call | Legacy migrations before claims; minimum-upgrade-from requirement checked on the store's applied steps by the updater and at boot (02 N14, K18) |
| A gate runs only after the commit is already in an RC or an auto-promoted production release | Every PR to main passes `make ci` including the extraction gates before merge (R3-2, K20); enforcement is Q-I |
| Library-scale loop added without a worker pool | R7 (type + AST check) |
| Route added without permission, including on the legacy mount | R6 (registration error + `archtest.RoutesHavePermissions` over the router and the legacy manifest) |
| Two writers of one key family across owners | R8 claims + boot overlap check |
| Event bus used for correctness | Outbox ([05-events](05-events.md)); review checklist |
| A core service writes a legacy family in a shape the previous release cannot read | Legacy-format adapters write the stored format; every storage grant is explicit and logged at boot; native-only data is additive or in core-only families; 09's rollback rehearsal boots the previous release on a store the new build wrote (02 decision 13, K2) |
| Rollback widens an API key | New keys always carry explicit, non-empty scopes, and native-only scopes are ignored by older builds, so a key can only narrow on rollback (K13, SEC-1, R3-1) |
| Books' legacy reads/writes outside its claims go unnoticed in shadow mode | Violations are persisted (`c/storage/claimviol/`), so 09 S2b's "zero persisted violations ≥ 14 days" survives restarts (05-storage §5S.9.4) |
| Restore into a live store | Staged restore applied at next boot ([05-backup](05-backup.md)); until it ships, live-target extraction is disabled (K16) |

## 14.3 Questions for the owner (with recommendations)

| ID | Question | Recommendation | Raised in |
|---|---|---|---|
| Q-A | Licence of the core repo: keep PolyForm Noncommercial (source-available) or relicense core under an OSI licence (Apache-2.0 or MIT) to satisfy D9 "open source only"? | **Relicense core as Apache-2.0** before the split (you are the copyright holder of the history; confirm no third-party contributions need consent). ABO may keep its licence. | [12](12-repo-split.md) |
| Q-B | Public or private core repository? | **Public** (free proxy/sum-db, free npm publishing, unlimited Actions minutes for public repos). | [12](12-repo-split.md) |
| Q-C | The 15 v2 cron strings that never ran (e.g. `deluge.protected-paths-sync` every 30 min): enable as real schedules or delete? | **Delete the declarations** (spec default); re-add individually as real `Schedule`s if wanted, because enabling 15 never-run jobs at once is an untested behaviour change. | [05-tasks](05-tasks.md) |
| Q-F | CLI flags and env names baked into shipped deploy files (unit, plist, compose, Dockerfile, example config) that are **not** the only source of a required setting: two-minor window or keep until core 1.0? (The sole-source case is decided, §14.3.1.) | **Keep until 1.0**, not usage-gated; two minors for everything else. | [09](09-abo-on-core.md) O7; OPS-5 |
| Q-G | Readiness thresholds for the split (SC1: 3 consecutive API-stable checkpoints over 6 weeks; SC5: coverage 70 % core / 60 % SDK)? They are readiness reports, not merge gates (12 §12.3.11). | Accept as proposed; tune before S0 wires the readiness report. | [12](12-repo-split.md) |
| Q-H | Core module name `github.com/falkcorp/mediacore`. | Accept; renaming is cheap only before the first core package lands. | [03](03-architecture.md) |
| Q-I | **How is the release-gating requirement enforced?** The requirement itself is decided (R3-2): every PR to main passes `make ci`, including the extraction gates (ratchets, compat ledger checks, the stage-visibility gate, the module-skew check, the Docker image build), before it merges; promotion of an RC needs no gate of its own, because a release then only contains commits that passed. The options: **(a)** a ruleset or branch protection on `main` that requires the check, with **no bypass actors** (`enforce_admins: true`), and merge tooling (the `/parallel-sweep` coordinator, CLAUDE.md's Quick Fix flow) that waits for the check and never merges with `--admin` past it; **(b)** keep server-side merges ungated and rely on the merge tooling running `make ci` locally before every merge, with `auto-revert.yml` as the backstop. | **(a).** Reason: (b) holds only while every merge path runs the local gate, and today two do not (CLAUDE.md's Quick Fix flow merges right after `gh pr create`; the sweep merges with `--admin`, OPS3-3), and `prerelease.yml` cuts an RC on every push, so a red commit reaches RC installs before the backstop reverts it. Cost: every merge to main waits for CI. | [09](09-abo-on-core.md) §9.2.1; OPS2-1, OPS3-3, OPS3-4, MOD3-2; R2-6, R3-2 |

### 14.3.1 Decided in round 1 (adopted recommendation; the owner may override)

These were owner questions in v1.0.0. Round 1 found that leaving them open let spec files contradict
each other, so the lead adopted the recommendation and the files now implement it. Each stays
overridable by the owner; overriding one reopens the files named. Q-D and Q-E were restated in round 3
by R3-1: the same direction (no legacy-readable session copy; secrets out of the blob), with the
mirroring removed and the rollback cost stated instead.

| ID | Former question | Decision | Implemented in |
|---|---|---|---|
| Q-D | Sessions across a rollback: keep a copy an older build can read (no re-login on rollback) or not? | **No legacy-readable copy** (SEC-19; R3-1). The new build stores sessions only hashed, in a core-only family that no older build reads, so a rollback costs every user one re-login. A legacy token presented to the new build is looked up in `sess:` with epoch, status and revocation checks; a hit issues a hashed native session, sets the cookie and deletes the legacy row; that fallback code is removed 7 days after the session stage. | [05-authn](05-authn.md), [09](09-abo-on-core.md) S4 |
| Q-E | Plaintext secrets in `config_blob`: keep them readable by an older build through the rollback window, or move them as soon as the new build owns them? | **Move on first write** (SEC-20; R3-1). The first time the new build writes a secret, it stores it encrypted in `secrets` (one keyring, 05-config) and blanks the field in the blob; a secret the new build has not written stays where it is. A rollback therefore requires re-entering the moved secrets, and the release note of the first build that moves them says so. No legacy-format re-encryption and no mirror writer exist. (OPS-9's precondition — the previous build must read every scrubbed secret — is replaced by this stated cost.) | [05-config](05-config.md), [09](09-abo-on-core.md) S3 |
| Q-F (part) | Env names that are the **only** source of a required setting in a shipped deploy file (e.g. `DATABASE_PATH`) | **Permanent** (OPS-5): compat ledger "permanent" class; boot refuses to start on an empty store when a legacy DB path is set but unread. | [09](09-abo-on-core.md), [05-config](05-config.md) |

## 14.4 Old decision-record questions

Q1 resolved by D7, Q3 by D2; Q2 → [10 §ebooks](10-tv-movies-proof.md) (recommendation EPUB + PDF with an
OPDS feed, reusing core); Q4 → [05-library](05-library.md) (one root abstraction; `os.Root`-confined; room
for a remote driver later); Q5 → [09](09-abo-on-core.md) §9.2.1 (the single compatibility window, used by
every alias kind; removal only by a contract PR, never by a clock; 02 §2.5).

## 14.5 Remaining open items by file

These are design details each section resolves in review; none blocks the plan. Each file's "Open
items" section is authoritative. Census re-taken in the **round-3 revision** (2026-11-04) from the working
tree with the round-1 method unchanged: an item is a numbered list entry or an ID'd table row (`O1`, `1`,
…) under a heading containing "Open items"; entries the file itself marks closed or resolved ("(Closed …)",
"(closed by …)", "**Resolved**") are counted separately. Files without an "Open items" section (01, 02,
03, 11, 13, 15, README) and this file are not listed. **114** items remain open across 26 files
(round-2 integration pass: 115. Change since: 05-tasks has 7 open and 1 closed in place, was 8 open).
The snapshot was taken while the other round-3 revisers were still editing their files; the round-3
integration pass re-takes it once every file has settled.

| File | Open | Closed in place |
|---|---|---|
| [04-package-mapping](04-package-mapping.md) | 5 | 0 |
| [05-ai](05-ai.md) | 4 | 0 |
| [05-authn](05-authn.md) | 4 | 0 |
| [05-backup](05-backup.md) | 3 | 0 |
| [05-config](05-config.md) | 7 | 0 |
| [05-download](05-download.md) | 3 | 0 |
| [05-events](05-events.md) | 2 | 3 |
| [05-health](05-health.md) | 3 | 2 |
| [05-http-api](05-http-api.md) | 4 | 1 |
| [05-indexer](05-indexer.md) | 3 | 0 |
| [05-library](05-library.md) | 4 | 0 |
| [05-logging](05-logging.md) | 2 | 0 |
| [05-metadata](05-metadata.md) | 6 | 0 |
| [05-metrics](05-metrics.md) | 3 | 0 |
| [05-notify](05-notify.md) | 2 | 0 |
| [05-rbac](05-rbac.md) | 6 | 1 |
| [05-realtime](05-realtime.md) | 2 | 0 |
| [05-storage](05-storage.md) | 6 | 0 |
| [05-tasks](05-tasks.md) | 7 | 1 |
| [05-updater](05-updater.md) | 6 | 1 |
| [06-app-sdk](06-app-sdk.md) | 6 | 1 |
| [07-ui-shell](07-ui-shell.md) | 4 | 0 |
| [08-arr-facade](08-arr-facade.md) | 8 | 0 |
| [09-abo-on-core](09-abo-on-core.md) | 6 | 4 |
| [10-tv-movies-proof](10-tv-movies-proof.md) | 5 | 0 |
| [12-repo-split](12-repo-split.md) | 3 | 0 |

## Review history

Round 1 (dispositions in `docs/plans/platform-core/reviews/spec-round-1-dispositions.md`):

- SEC-19 — Q-D moved to §14.3.1 as decided (no raw-session mirror).
- SEC-20, OPS-9 — Q-E moved to §14.3.1 as decided (one keyring, scrub at S3), with OPS-9's precondition. (Superseded in round 3 by R3-1/R3-2.)
- OPS-5 — Q-F split: sole-source env names decided permanent; K15 added; residual Q-F kept.
- MOD-1, OPS-1, MOD-9, FE-13 — K11 (Docker and web build break) added; hazards row.
- OPS-2, OPS-3 — K12 (skipped releases) added; hazards row.
- DATA-3, DATA-4, DATA-5, SEC-1, OPS-19 — K2 rewritten for the identity adoption model; K13 (its costs) added; hazards row. (Superseded in round 3 by R3-1/R3-2.)
- DATA-1 — K2: settings families not adopted; `c/config/`.
- OPS-15 — K16 added; "Restore into a live store" hazard updated.
- SIMP-1 — K6 mitigation: non-circular rule, Build column re-checked at stage gates.
- SIMP-2, SEC-13, OPS-11 — K3: permanent legacy mount, one-stage auth swap, permanent equivalence test.
- SIMP-3 — K1 notes reduced scope.
- SIMP-7 — §14.4 Q5 points at the single window in 09 §9.2.1.
- SIMP-18 — K2: one store-level compatibility guard.
- SIMP-25, FE-2, FE-21 — K4: react-router migration precedes the shell; 13 owns the age rule.
- MOD-2, MOD-5, MOD-7, MOD-8, MOD-12, MOD-14, MOD-16 — hazards rows added or updated.
- MOD-3, MOD-4 — K7 and K8 updated for the exclude in `core/go.mod`.
- MOD-11 — K14 added.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: R-G request (DATA-30) — K17 added: store merge needs a core storage export/import contract (deferred, lowest priority).
- integration pass: Post-revision state — K1 package count (160 remain); K12 and hazards: `Adopts` check, `RequiresServed` defers instead of failing boot; K13 and hazards: mirror-disabled keys stay active natively while `Fresh`; shadow-mode claim violations persisted. (Superseded in round 3 by R3-1/R3-2.)
- integration pass: §14.5 re-counted per file (118 open items in 26 files) with the method stated there.

Round 2 (dispositions in `docs/plans/platform-core/reviews/spec-round-2-dispositions.md`):

- R2-1 (SEC2-1, SEC2-2, DATA2-3, DATA2-5) — K2, K13 point at the Teach-build rollback floor; K19 added (its cost); hazards row. (Superseded in round 3 by R3-1/R3-2.)
- R2-2 (SEC2-3, DATA2-4) — K2: every revocation, also after a restore, reaches the legacy row in the same batch. (Superseded in round 3 by R3-1/R3-2.)
- R2-4 (DATA2-2, OPS2-11) — K18 added (minimum-upgrade-from release, `min_upgrade_from`); K12 bounded by it; hazards row.
- R2-6 (OPS2-1) — Q-I added (required checks via branch protection vs RC-promotion gating; recommendation: required checks on gated paths plus promotion gating); K20 added; hazards row. (Superseded in round 3 by R3-1/R3-2.)
- MOD2-5 — K8: redesigned exclude check (temp copy; present, green with it, edge plus `ambiguous import` without it).
- MOD2-6 — K14: tidy coupling, one-PR Dependabot, `--pin`; hazards row.
- MOD2-7 — hazards row: core layers mapped onto ABO's scale.
- MOD2-8, MOD2-21 — Q-G: SC1/SC5, readiness report not merge gate, "before S0"; K11 "first core stage".
- FE2-19 — K4: 122 files (52 source, 70 test).
- §14.5 re-counted with the round-1 method (117 open items in 26 files).

Integration pass (round 2, 2026-11-04):

- integration pass (round 2): §14.5 re-counted after the integration pass: 115 open items in 26 files.

Round 3 (dispositions in `docs/plans/platform-core/reviews/spec-round-3-dispositions.md`):

- R3-1 (COR3-1, DATA3-2, DATA3-3, SEC3-1, SEC3-2) — K2 rewritten for one stored format per family (legacy-format adapters, storage grants, additive native data, never-empty key scopes); K10 now about cutover decisions; K12 no longer depends on `Adopts`/contract deletes; K13 rewritten as the one-format model's costs (re-login, re-enter moved secrets, native grants invisible after rollback); K16 drops the contract-step precondition; hazards rows "Rollback widens access", "Rollback widens an API key" and the new "legacy family written in an unreadable shape" row replace the Teach-build and mirror rows; Q-D and Q-E restated (core-only hashed sessions; secrets moved on first write, rollback = re-enter them).
- R3-2 (OPS3-3, OPS3-4, OPS3-5, OPS3-6) — K20 and Q-I: release gating is one requirement (every PR passes `make ci` with the extraction gates before merge); Q-I asks only how it is enforced (ruleset with no bypass actors and merge tooling that never uses `--admin`, recommended, vs. local gates plus auto-revert); hazards row updated.
- SEC3-1, SEC3-3, OPS3-5 — K19 withdrawn: the Teach-build rollback floor it priced no longer exists.
- MOD3-2 — Q-I: the stage-visibility gate is among the gates of the requirement; hazards row names the committed generator in root `make ci`.
- R3-3 (MOD3-1) — hazards row: a package moved before its replacement surface exists; the gate reads non-test imports of every package, including `cmd/*` and the root `main`.
- OPS3-7 — K12, K18 and hazards row: the minimum-upgrade-from requirement is what the store has applied, compared by the updater with the store's sidecar before applying.
- MOD3-5, MOD3-8, MOD3-12, MOD3-13, MOD3-16 — K8 (pass rule, two targets), K11 (Docker frontend stage and npm entry points change at UI-0), K14 (skew check blocking, security updates grouped) and the layering hazards rows follow 03 v1.6.0.
- §14.5 re-counted with the round-1 method (114 open items in 26 files; snapshot during the round-3 revision).

