<!-- file: docs/handoffs/2026-08-14-task-board.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8d2f6a41-3c97-4e58-b0d6-1a75c9e28f30 -->
<!-- last-edited: 2026-08-14 -->

# Task board — 2026-08-14

Snapshot of the 113-task breakdown (waves T/A/B/C/D/E/F/G/H) as of
2026-08-14 ~16:00 EDT. **22 PRs merged today** (#2421–#2439 plus reruns),
1 in CI (#2440), plus several production actions executed live. Owner
decisions D-0…D-10 were all answered today; the outcomes are inlined below.

## Wave 0 — Hygiene

| Task | Description | Status |
|---|---|---|
| T001 | Prune TODO.md of finished/superseded entries | ✅ merged #2422 |
| T002 | Remove stray SQL migration | ✅ done (D-0) |
| T003 | Peer-session check each wave | 🔁 ongoing rule |

## Wave A — Ops / prod verification

| Task | Description | Status |
|---|---|---|
| A01 | CI green before deploy | ✅ done |
| A02 | Deploy to production (debug build) | ✅ done 10:01 |
| A03 | Verify dry-run default (#2419) via composer-scan probe | 🟡 scan running (~3h, 492K files); PREVIEW verdict at completion |
| A04 | Verify op-ID audit trail (#2414) | 🟡 deferred to tonight's maintenance window |
| A05 | Author 46627 repair verification | ⬜ owner, unblocked |
| A06 | Post-deploy warmup row/key counts | ⬜ owner, unblocked |
| A07 | Aggregate-recompute attribution sample | ⬜ needs scan/organize activity |
| A08 | Local `internal/server` hang control | ✅ done — filesystem write-cost, not a code hang |

## Wave B — ABS compatibility

| Task | Description | Status |
|---|---|---|
| B00 | Phase-0 ground-truth doc (gates all B) | ✅ merged #2421 (+#2423) |
| B06 | Chapters end-to-end check | ✅ merged #2433 — verified live; stored chapters already on ~72% of sampled single-file items; **E02 = residue job** |
| B10–B13 | Collections: store → CRUD → ABS → routing | ⬜ ready, serial |
| B20–B24 | Series/author detail + list fixes | ⬜ ready |
| B30 | Ignored-query-params sweep | ⬜ ready |
| B40–B42 | Conformance value gate + fixtures | ⬜ ready |
| B50 | Phase-3 decisions doc | ⬜ ready |

## Wave C — Data integrity

| Task | Description | Status |
|---|---|---|
| C110 | `version_group_id` filter field (D-1 ✅) | ✅ merged #2436 |
| C111 | nil `is_primary_version` semantics (D-2) | 🟡 census merged #2438 — **nil = 5,702, all ungrouped** ("22,552 nils" was the explicit-false set); code-unify half open (nil=true + backfill) |
| C112–C113 | Bare-param guard extensions / declared schema | ⬜ ready / design |
| C210–C213 | Delete-guard family | ⬜ ready (C211 ✅ closed by peer #2412) |
| C310–C314 | Version-group integrity | ⬜ C310 gates; C314's exact 41-book population identified by the C111 census |
| C410–C414 | Author-table hygiene | ⬜ ready / gated on C410 |
| C510 | opstate params sweep | ⬜ ready |
| C511 | Resume-fallback metric | ✅ merged #2429 |
| C512–C514 | Dry-run declaration / ctxOpID audit / channel drops | ⬜ design / gated A04 / ready |
| C610 | Dangling-SeriesID copy guard | 🟢 **PR #2440 in CI** (3 copy paths guarded; repair op = follow-up) |
| C710 | Fuzzy-query case fix | ✅ merged #2424 |
| C711–C714 | Keyword analyzer / stopwords / track names / metrics | ⬜ ready |
| C715 | Coverage gate: sets not counts | ✅ merged #2434 — next boot deletes the 3,953 stale trash docs |
| C716 | 3,954-book API-vs-store gap | ✅ merged #2430 — 3,953 was the pre-#2408 instrument + 2 quarantined + 0 unexplained |
| C717 | Search-result cache | 🔒 design-gated |
| C810–C813 | Data-shape repairs | 🔒 design / owner / ready |
| C814 | Soft-deleted total count | ✅ merged #2425 |
| C815–C816 | Orphan-VG pagination / memdb mutex | ⬜ ready |
| C817 | DeleteBookFilesForBook memdb sync | ✅ merged #2427 |
| C818–C819 | Aggregates coalescing / row-count bookkeeping | 🔒 need A07 / A06 |
| C910–C911 | Dedup backlog / merge-cache-evict | ⬜ ready |
| CA10–CA11 | SSRF alerts / zipslip batch | ⬜ ready |
| CA12 | Log-injection sweep (D-7 ✅) | 🟡 wave 1 merged #2439 — conduit barriers + root cause (sanitizer comment described deleted code; 322 alerts); fan-out awaits CodeQL recognition verdict |
| CA13 | OpenAI key validation | ⬜ ready |
| CA14 | Security-sweep status column | ✅ merged #2426 (41/41 verified) |
| CA15 | Secrets/systemd batch (D-10: deferred, LAN trusted) | ⬜ owner-at-keyboard |

## Waves D/E — Config & gated prod ops

| Task | Description | Status |
|---|---|---|
| D110 | Scheduled.* zeros repair (D-4 ✅) | ✅ done live — 4 intervals restored; interval-0 is by-design for 2 jobs; ticker check at next boot (D112) |
| D111 | Stored zeros must not shadow defaults | ⬜ design |
| D112 | Scheduled-tasks residual verification | 🟡 at post-scan redeploy |
| D113 | wipeActivity preview | ⬜ ready |
| E01 | BookSig sidecar migrate | ⬜ queued post-scan (dry-run first) |
| E02 | Chapters backfill | ⬜ queued post-scan — residue job per B06 |
| E03 | Search-index boot repair confirmation | 🟡 at post-scan redeploy (needs #2434's boot) |
| E04 | Title-match false-series repair | ⬜ build + dry-run |
| E05 | review_apply_enabled flip + canary | ✅ **done + verified** — flag live, canary combine applied exactly per card (31 files → 1 book); 354 items now live-appliable |
| E06 | probe-directory-books apply | ⬜ queued post-scan |
| E07 | iTunes PID-repair | 🟡 boot backfill already registered 86,732 track mappings post-#2367; residual verification pending |
| E08 | Review-screen tag repair | 🟡 preview done — per-book derivation impossible; recommendation: library-wide bulk-write-back in the nightly window (owner pick pending) |
| E09 | Prometheus metrics bearer token | ⬜ script staged on the server; owner runs it |

## Waves F/G/H — iTunes, frontend, CI

| Task | Description | Status |
|---|---|---|
| F110 | Playlist-PID coverage measure | ✅ merged #2435 — 88.5% via ExternalIDMapping; album-PID instrument (14%) documented as wrong tool |
| F111–F113 | Playlist import / smart criteria / ITL defect | ⏸️ **parked — owner's lowest priority** |
| F114 | Progress propagation investigation | ⬜ ready |
| F115 | Dead file-level playlist path | ✅ merged #2428 |
| F116 | 2-way-sync guard scope | 🔒 owner decision |
| G110 | Restore library sort | ⬜ ready |
| G111 | Clickable metadata links | ⬜ unblocked by C110 |
| G112–G117 | Frontend batch | ⬜ ready / owner-gated |
| G118 | Empty-FieldFilter trace + 400 toast | ✅ merged #2432 — no live UI path can send an empty value; 400s now surface the server message |
| G119 | MUI upgrade ladder | 🔒 D-8 resolved as fast-checks-only; ladder still needs its own gate decision |
| H110 | internal/database CI stall | 🟡 **evidence finally captured** (Coverage Floor stall log: cumulative slowness + leaked nutsdb goroutines, NOT a deadlock); proactive half open |
| H111 | tmpfs test target (D-9 ✅) | ✅ merged #2437 — `make test-fast`, measured 54.1s → 5.3s |
| H112 | Required checks (D-8 ✅) | ✅ **live** — Minimal CI Summary + changelog + fragment-headers required on main (~3-4 min); E2E advisory (12 min measured) |
| H113 | E2E port isolation per worktree | 🟡 in progress — per-worktree port/tmp derivation wired; identity assertion + bind-failure exit remaining |
| H114 | Leak-scanner heuristic | ✅ merged #2431 |
| H115–H116 | e2e debts / coverage-floor items | ⬜ ready / gated H110 |

## Production state notes (scrubbed)

- Review-apply is ON with one verified canary; scheduled intervals repaired;
  branch protection with fast required checks is live.
- The composer-tag scan (A03 probe) has been running since 10:03 over 492,157
  files; the redeploy queue (E03 boot cleanup, D112 ticker check) waits for it.
- Metrics scraping remains down pending the operator running the staged
  Prometheus token script.
