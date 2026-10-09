<!-- file: docs/proposals/2026-10-holistic/tasks/06/06-README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4e8b2d71-5a39-4c06-b1f4-7d2a9c3e6f58 -->
<!-- last-edited: 2026-10-09 -->

# 06: bleeding-edge Go and Node, task briefs

Source: `docs/proposals/2026-10-holistic/06-bleeding-edge-go-node.md` (v1.3.0) and its appendices in `06-bleeding-edge/`. Each brief follows `../00-TEMPLATE.md` and can be executed by an agent with no conversation context.

## Briefs (15)

| Id | Title | Wave | Model | Size | Depends on |
|---|---|---|---|---|---|
| [06-P1](06-P1.md) | Go 1.27.2 across the 18 pinned files | 0 | sonnet | S | 07 C1 |
| [06-P2](06-P2.md) | Drift check covers Woodpecker, `ci_remote.py`, workflow patch level | 0 | sonnet | S | P1; 07 C3 (on `ci.yml`) |
| [06-P3](06-P3.md) | pprof tag in every deploy, committed Makefile owns build flags | 3 (roadmap) | sonnet | S | 07 C3 |
| [06-P4](06-P4.md) | Flight recorder with watchdog and search-stall hooks | 3 (roadmap) | **opus** | M | none hard (P3 for one test) |
| [06-P5](06-P5.md) | go fix batch 1 (ten simple analyzers, 80 files) | F | sonnet | M | P1; 01 T1-T7; 02 PR 9a |
| [06-P6a](06-P6a.md) | go fix batch 2, `internal/database` (33) | F | sonnet | M | P5; 01 T1 |
| [06-P6b](06-P6b.md) | go fix batch 2, `internal/server` (37) | F | sonnet | M | P6a; 01 T2; 02 PR 16/17/4 |
| [06-P6c](06-P6c.md) | go fix batch 2, `internal/plugins` (61) | F | sonnet | L | P6b; 01 T5b; 02 PR 8; 04 P2/P5/P12 |
| [06-P6d](06-P6d.md) | go fix batch 2, metadata, matching and AI packages (33) | F | sonnet | M | P6c; 01 T3a/T3b/T7 |
| [06-P6e](06-P6e.md) | go fix batch 2, scanning, operations and remaining packages (56) | F | sonnet | M | P6d; 01 T4a/T5a/T6a/T6b |
| [06-P7](06-P7.md) | go fix batch 3, `newexpr` (67 test files) and orphaned helpers | F | sonnet | M | P6e |
| [06-P8](06-P8.md) | go fix batch 4, `waitgroupgo` and `testingcontext` (29) | F | sonnet | M | P7 |
| [06-P9](06-P9.md) | TypeScript 7 typecheck (`tsc`), TS 6 kept as `tsc6` for lint | 3 (roadmap) | sonnet | S | none hard |
| [06-P11](06-P11.md) | Vitest 5, then `fsModuleCache` | 3 (roadmap) | sonnet | M | P9 |
| [06-P12](06-P12.md) | PGO for deploy builds, trimpath profile | 3 (roadmap) | sonnet | S | P3 deployed; owner-supplied profiles; 07 C3 |

P6 is five briefs (P6a to P6e), so the folder holds 15 briefs plus this README.

## Not briefed here (listed, per the task)

| Item | Wave | Source pointer |
|---|---|---|
| P10 `feat(web): react-router 8` | 3 (roadmap §5 'Frontend'; after 03 PR 11, before 05 PR 10) | 06 §3.2 S6, §4 row P10; file list `06-bleeding-edge/react-router-files.md` (95 files) |
| P13+ `refactor(web): useTransition for loading flags` series (batches of 10 files or fewer) | 3, open-ended, after 07 F2 | 06 §2.1 F14/F15, §4 row P13+; `docs/react-compiler-adoption.md` |
| P14+ `test(go): synctest for sleep-based tests` series (one package per PR) | after freeze window F, open-ended | 06 §2.1 F21, §4 row P14+; `06-bleeding-edge/synctest-candidates.md` |

## Notes for the coordinator

1. **Waves.** The task asked for waves 0, 1, 2 and F. In the roadmap (`08-integrated-roadmap.md` §5) only P1 and P2 are wave 0 and P5 to P8 are freeze window F. There is no 06 PR in wave 1 or 2. P3, P4, P12 (Observability row), P9 and P11 (Frontend row) are listed under **wave 3**; each brief says so and notes it may be pulled forward when it has no code dependency (P3, P4, P9).
2. **P1 versus the 7 `ci.yml` lines.** `ci.yml` has no `1.27.1` string (it passes a floating `'1.27'` at lines 47, 125, 189, 265, 338, 464, 529), so it is not among the 18 pinned files. Those lines belong to P2, as in the source doc.
3. **P2 goes beyond the source doc, on purpose.** `check_toolchain_versions.py` today requires every workflow `go-version:` to be the floating minor, so changing only `ci.yml` to `'1.27.2'` would fail the existing check. The brief applies the full-pin rule to all 14 literals in 8 workflows (the 7 in `ci.yml` plus `binary-smoke`, `codeql`, `e2e`, `frontend-ci`, `nightly`, `security`, `vulnerability-scan`) and carries a Decision flag with a fallback if the owner prefers floating minors. It also requires the agent to confirm that the `falkcorp/github-common` reusable workflows pass the value straight to setup-go.
4. **P1 also fixes a latent test trap.** `scripts/tests/test_check_toolchain_versions.py` mutates `.envrc` and `Dockerfile.build-cgo` to `1.27.2` as the "wrong" value; after the bump those mutations would be no-ops. The brief has the tests derive the pin from the Makefile.
5. **Model split.** 14 sonnet, 1 opus (P4). P4 is opus because it adds a runtime component with concurrency invariants (one recorder, one `WriteTo` at a time, a snapshot path that must not block the watchdog goroutine) across the registry, server lifecycle and a new handler, and may need re-targeting onto ops v3. P3 stays sonnet: every edit is specified, verification is a `make -n` diff, a grep and a small tagged test.
6. **P3 owner hand-off.** The private `Makefile.local` is not in the repo. The brief makes the agent leave it alone and put a removal list (build-linux-debug, inline `go build` lines, `-tags`/`-gcflags`/`-ldflags`/`-trimpath` text, any `GO_BUILD_*`/`GO_LDFLAGS` assignment) in the PR body for the owner.
7. **P4 route file.** The source named `wire_handlers.go` for route registration; the `/diagnostics/*` routes are actually declared in `internal/server/wire_media_routes.go:68-71` (the handler is constructed in `wire_handlers.go:509`). The brief lists both.
8. **P6 slices.** The appendix gives one 220-file P6 list and says it "can be split by top-level package". The five slices (database 33, server 37, plugins 61, metadata and AI 33, the rest 56) were computed from that list and sum to 220. All go fix briefs carry the rule: regenerate with `go fix -diff` at the PR base, never rebase an old diff, run only in freeze window F, `omitzero` excluded (D40, D50).

## Unverified at writing time

- Docker Hub publication of `golang:1.27.2-alpine` and `-bookworm` and their digests (P1 step 2).
- Whether `falkcorp/github-common` reusable workflows accept a patch-level `go-version` (P2 step 1).
- The `fsModuleCache` config key and cache location in Vitest 5 (P11 step 4).
- TS 7 native binaries on `node:26-alpine` (musl) and linux/arm64, and whether both TS packages link a colliding `tsserver` bin (P9 steps 4 and 7).
- The `runtime/trace.FlightRecorder` method names and `go fix` flag syntax (`-tags`, multiple analyzer flags) on the pinned toolchain: the briefs tell the agent to check `go doc` / `go help fix` first.
- The contents of the owner's private `Makefile.local` (read only by the original analyst).
