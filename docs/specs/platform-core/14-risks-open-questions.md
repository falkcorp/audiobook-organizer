<!-- file: docs/specs/platform-core/14-risks-open-questions.md -->
<!-- version: 1.0.0 -->
<!-- guid: e270beb6-8eea-48b1-8397-4ef4ec709956 -->
<!-- last-edited: 2026-11-04 -->

# 14 — Risks, migration hazards, open questions

Every item has an owner section and a recommendation. "Owner" means the repository owner must decide;
everything else is resolved inside the named spec file during review.

## 14.1 Top risks

| # | Risk | Likelihood / impact | Evidence | Mitigation (where specified) |
|---|---|---|---|---|
| K1 | **Scale of the books-module residue.** ~620k LOC and 154 packages stay in ABO; 333 `Store` methods remain books-owned; 114 + ~22 test files use full-store mocks. The extraction can stall halfway with two half-systems. | High / High | [04](04-package-mapping.md), `raw/04-database-store.md` | Strangler stages each end shippable ([09](09-abo-on-core.md)); adapters keep old call sites compiling; ratchet tests stop regressions; the plan sequences the keystone (`database`, `config`, `server` wiring) first. |
| K2 | **Data compatibility during adoption migrations** (users, roles, sessions, API keys, settings copied into `c/...`). A rollback to the previous release must still open and read the DB (gate G5). | Medium / Critical | [05-storage](05-storage.md), [09](09-abo-on-core.md) | Dual-write to legacy keys for the window; compat-level guard (`c/compat/required_level` + sidecar + `--print-compat-level`); pre-migration checkpoint; adoption over the batch cap becomes a resumable, checkpointed data migration with dry-run. |
| K3 | **Security regressions while two auth stacks coexist** (gin `RequireAuth`/`s.perm` and core `httpapi`+`rbac`). | Medium / High | [05-rbac](05-rbac.md), [05-http-api](05-http-api.md) | Permission-equivalence test: allow/deny identical across all 572 routes for every seed role before and after each move ([09](09-abo-on-core.md)); fail-closed registration (R6); live defects hot-fixed independently (`todo.d/2026-11-04_platform_core_followups.md`). |
| K4 | **Toolchain churn:** several "latest" releases are days old (bleve v2.6.2, @babel/core 8, vitest 5.1, plugin-react 6.2.1); TypeScript 7 has no JS API so lint and `openapi-typescript` stay on the TS 6 alias; react-router 8 removes `react-router-dom` (121 ABO files). | High / Medium | [13 §13.7](13-toolchain.md) | 14-day release-age rule; exceptions E1–E4 recorded with exit conditions; react-router migration rides with the shell extraction. |
| K5 | **Single-process failure domain and module trust.** Modules are compiled-in, trusted code; keyspaces are organisation, not access control. | Certain / Medium | 2026-09-18 adversarial review residual risks (`raw/08-prior-art.md` §2) | Accepted. R8 prevents accidental cross-module writes; manifests cannot grant `core:*` ([05-rbac](05-rbac.md)); no third-party in-process code (decision 5). |
| K6 | **"Small core" grows into a god package.** | Medium / Medium | adversarial review | Promotion rule (03 §3.4): platform concern or ≥2 consumers; API-stability gate measures the SDK surface ([12](12-repo-split.md)). |
| K7 | **Replace directive and `go install`:** `go install github.com/falkcorp/audiobook-organizer@version` fails while the `replace` exists. | Low / Low | 03 §3.2 item 5 | Not used by the release path; documented in the README of the release. Ends at the split. |
| K8 | **Stale transitive `genproto`** reappears if `cockroachdb/errors` changes or another dependency pins the monolithic module. | Low / Medium | `raw/00-baseline-build.md` E7 | The `exclude` is re-verified by a CI step running `go mod graph`; the build never depends on `go.work`. |
| K9 | **Licence mismatch with D9.** The repo is PolyForm Noncommercial 1.0.0 (`LICENSE`), which is source-available, not OSI open source; core's history inherits it. | Certain / Medium | `LICENSE:1`, [12](12-repo-split.md) open item 1 | Owner decision Q-A below, before the split. |
| K10 | **Production unknowns** (key counts per family, session counts, op-history size) drive whether adoption is atomic or online. | Certain / Low | [05-storage](05-storage.md), [09](09-abo-on-core.md) | Both branches designed; read-only census commands listed in 09 for the owner to run. |

## 14.2 Migration hazards (each has a guard)

| Hazard | Guard |
|---|---|
| Core silently requires the app module via `go mod tidy` (`raw/00-baseline-build.md` E2) | `archtest.TestNoAppRequire` (R1a) |
| Repo-walking ratchet tests moved into core scan nothing | Per-move test classification step in every move brief; ratchets stay in the root |
| Root `./...` does not reach `core/` | `GO_MODULE_DIRS := . core` in every Go-running target and CI job |
| Layering guard fails on the first move | Guard redesign (03 §3.5.1) is the first code task |
| Library-scale loop added without a worker pool | R7 (type + AST check) |
| Route added without permission | R6 (registration error + `archtest.RoutesHavePermissions`) |
| Two writers of one key family across owners | R8 claims + boot overlap check |
| Event bus used for correctness | Outbox ([05-events](05-events.md)); review checklist |
| Restore into a live store | Staged restore applied at next boot ([05-backup](05-backup.md)) |

## 14.3 Questions for the owner (with recommendations)

| ID | Question | Recommendation | Raised in |
|---|---|---|---|
| Q-A | Licence of the core repo: keep PolyForm Noncommercial (source-available) or relicense core under an OSI licence (Apache-2.0 or MIT) to satisfy D9 "open source only"? | **Relicense core as Apache-2.0** before the split (you are the copyright holder of the history; confirm no third-party contributions need consent). ABO may keep its licence. | [12](12-repo-split.md) |
| Q-B | Public or private core repository? | **Public** (free proxy/sum-db, free npm publishing, unlimited Actions minutes for public repos). | [12](12-repo-split.md) |
| Q-C | The 15 v2 cron strings that never ran (e.g. `deluge.protected-paths-sync` every 30 min): enable as real schedules or delete? | **Delete the declarations** (spec default); re-add individually as real `Schedule`s if wanted, because enabling 15 never-run jobs at once is an untested behaviour change. | [05-tasks](05-tasks.md) |
| Q-D | Session copy during rollback window: mirror raw sessions to legacy keys (no re-login on rollback) or not (one re-login after a rollback)? | **No mirror** — session tokens are then stored only hashed; the cost is one re-login, only if you roll back. | [05-authn](05-authn.md), [09](09-abo-on-core.md) |
| Q-E | Plaintext secrets in `config_blob`: keep through the rollback window or scrub as soon as they are encrypted? | **Scrub at once** and make the previous release read the encrypted copy (it already decrypts the 4 encrypted secrets); a secret kept in plaintext for two releases is a longer exposure than a rollback inconvenience. | [09](09-abo-on-core.md) O1, [05-config](05-config.md) |
| Q-F | CLI flags baked into compose files: two-minor window or keep until core 1.0? | **Keep until 1.0** for flags/env names that appear in shipped deploy files; two minors for everything else. | [09](09-abo-on-core.md) O7 |
| Q-G | Readiness thresholds for the split (S1: 3 consecutive API-stable checkpoints over 6 weeks; S5: coverage 70 % core / 60 % SDK)? | Accept as proposed; tune before wave 0 wires the jobs. | [12](12-repo-split.md) |
| Q-H | Core module name `github.com/falkcorp/mediacore`. | Accept; renaming is cheap only before the first core package lands. | [03](03-architecture.md) |

## 14.4 Old decision-record questions

Q1 resolved by D7, Q3 by D2; Q2 → [10 §ebooks](10-tv-movies-proof.md) (recommendation EPUB + PDF with an
OPDS feed, reusing core); Q4 → [05-library](05-library.md) (one root abstraction; `os.Root`-confined; room
for a remote driver later); Q5 → [09](09-abo-on-core.md) (31-row compatibility ledger, two-minor + 30-day
windows with zero-use evidence).

## 14.5 Remaining open items by file

These are design details each section resolves in review; none blocks the plan. Counts are the number of
items in each file's "Open items" section at this version: 04 (6), 05-ai (4), 05-authn (5), 05-backup (4),
05-config (8), 05-download (3), 05-events (5), 05-health (4), 05-http-api (5), 05-indexer (3),
05-library (4), 05-logging (2), 05-metadata (11), 05-metrics (3), 05-notify (3), 05-rbac (5),
05-realtime (2), 05-storage (6), 05-tasks (9), 05-updater (4), 06 (8), 07 (8), 08 (11), 09 (9), 10 (6),
12 (4).
