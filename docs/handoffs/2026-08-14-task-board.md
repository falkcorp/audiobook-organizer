<!-- file: docs/handoffs/2026-08-14-task-board.md -->
<!-- version: 1.2.0 -->
<!-- guid: 8d2f6a41-3c97-4e58-b0d6-1a75c9e28f30 -->
<!-- last-edited: 2026-08-14 -->

# Task board — 2026-08-14

Snapshot of the 113-task breakdown (waves T/A/B/C/D/E/F/G/H) as of
2026-08-14 ~21:30 EDT. **33 PRs merged today** (#2421–#2453), 1 in CI
(#2454). Third production deploy at 16:19 EDT; the entire E-wave except the
E08 full run is now EXECUTED AND VERIFIED on prod. Owner decisions D-0…D-10
were all answered today; the outcomes are inlined below.

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
| A03 | Verify dry-run default (#2419) via composer-scan probe | ✅ verified — scan completed 13:04 EDT as a **preview** (492,157/492,157 files, 3h01m; the persisted 90-byte params carried the advertised dry_run:true) |
| A04 | Verify op-ID audit trail (#2414) | 🔴 probe ran, INSTRUMENT WRONG — the designated `temp-file-cleanup` records deletions to the ACTIVITY feed only, never `operation_changes`, so it cannot verify #2414 even when it deletes (fragment filed; pick an op that calls CreateOperationChange) |
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
| C111 | nil `is_primary_version` semantics (D-2) | ✅ **CLOSED ON PROD** — job merged #2449; dry-run matched the census EXACTLY (5,702 + 41 + 0 grouped-nils), apply wrote 5,743 with 0 errors, re-dry-run 0/0/0. C314's 41 fixed in the same pass |
| C112–C113 | Bare-param guard extensions / declared schema | ⬜ ready / design |
| C210–C213 | Delete-guard family | ⬜ ready (C211 ✅ closed by peer #2412) |
| C310–C314 | Version-group integrity | ⬜ C310 gates; C314's exact 41-book population identified by the C111 census |
| C410–C414 | Author-table hygiene | ⬜ ready / gated on C410 |
| C510 | opstate params sweep | ✅ merged #2446 |
| C511 | Resume-fallback metric | ✅ merged #2429 |
| C512–C514 | Dry-run declaration / ctxOpID audit / channel drops | ⬜ design / gated A04 / ready |
| C610 | Dangling-SeriesID copy guard | ✅ merged #2440 (repair op for the ~12K existing refs = follow-up) |
| C710 | Fuzzy-query case fix | ✅ merged #2424 |
| C711–C714 | Keyword analyzer / stopwords / track names / metrics | ⬜ ready |
| C715 | Coverage gate: sets not counts | ✅ merged #2434 — next boot deletes the 3,953 stale trash docs |
| C716 | 3,954-book API-vs-store gap | ✅ merged #2430 — 3,953 was the pre-#2408 instrument + 2 quarantined + 0 unexplained |
| C717 | Search-result cache | 🔒 design-gated |
| C810–C813 | Data-shape repairs | 🔒 design / owner / ready |
| C814 | Soft-deleted total count | ✅ merged #2425 |
| C815–C816 | Orphan-VG pagination / memdb mutex | ✅ merged #2443 / #2444; the 7 REMAINING offset walkers (5 jobs + 2 Pebble pagers) collapsed in #2452 |
| C817 | DeleteBookFilesForBook memdb sync | ✅ merged #2427 |
| C818–C819 | Aggregates coalescing / row-count bookkeeping | 🔒 need A07 / A06 |
| C910–C911 | Dedup backlog / merge-cache-evict | ⬜ ready |
| CA10–CA11 | SSRF alerts / zipslip batch | ⬜ ready |
| CA12 | Log-injection sweep (D-7 ✅) | 🟡 waves 1+1.1 merged; post-#2445 verdict: **316 still open — the conduit's own alerts survive** because CodeQL weak-updates variadic `[]any` element writes (no code shape fixes it). Wave 2 = model-as-data sanitizer rows (fragment, with extensible-name validation caveat). Bonus find: ALL 16 existing sanitizer-model rows were inert since the module rename (jdfalk→falkcorp) — fix in #2454 |
| CA13 | OpenAI key validation | ⬜ ready |
| CA14 | Security-sweep status column | ✅ merged #2426 (41/41 verified) |
| CA15 | Secrets/systemd batch (D-10: deferred, LAN trusted) | ⬜ owner-at-keyboard |

## Waves D/E — Config & gated prod ops

| Task | Description | Status |
|---|---|---|
| D110 | Scheduled.* zeros repair (D-4 ✅) | ✅ done live — 4 intervals restored; interval-0 is by-design for 2 jobs; ticker check at next boot (D112) |
| D111 | Stored zeros must not shadow defaults | ⬜ design |
| D112 | Scheduled-tasks residual verification | ✅ verified — dedup_refresh 6h ticker started at the 13:09 boot |
| D113 | wipeActivity preview | ⬜ ready |
| E01 | BookSig sidecar migrate | ✅ **CLOSED ON PROD** — canary (limit 100, 0 errors) → full apply **migrated 26,027/26,027, 0 errors** (~559 MB inline signatures → sidecars) → re-dry-run **0 candidates of 63,841**. `phase_mb[books]` drop check at next boot |
| E02 | Chapters backfill | 🟡 cohort complete; whole-library dry-run RUNNING (ffprobe over ~14.6K candidates) — apply follows its numbers |
| E03 | Search-index boot repair confirmation | ✅ verified — boot gate deleted **3,983** stale docs in 1m29s (3,953 trash + 30 from the E05 canary merge); index now equals the library and the 13:40 boot showed no divergence |
| E04 | Title-match false-series repair | ⬜ build + dry-run |
| E05 | review_apply_enabled flip + canary | ✅ **done + verified** — flag live, canary combine applied exactly per card (31 files → 1 book); 354 items now live-appliable |
| E06 | probe-directory-books apply | ✅ applied — examined 1,026: **434 actioned** (exactly the approved dry-run count), 592 stay in review, 0 errors |
| E07 | iTunes PID-repair | ✅ resolved, no apply needed — live census shows only **2 duplicate PIDs** (both ambiguous, one same-content pair split across the two trees; hand review); the recorded 8,984 was stale pre-repair state. Side catch: the endpoint read dry_run from the query only and silently ignored a JSON body — fixed in #2447 |
| E08 | Review-screen tag repair | 🟡 owner approved canary+full; canary (100 books) ran clean on TAGS but exposed a real bug: the tag rewrite replaced files as mode 0600, zeroing ACL masks — every rewritten file went share-unreadable. Root cause fixed (#2451: OpenFile's mode arg only applies at CREATE), `fix-file-modes` repaired 1,547 files; **124 residue files expose a second defect** (book rows carrying stale paths — fragment). FULL RUN DEFERRED on measurement: ~35 s/book serial + unconditional writes = weeks, not a night; prerequisites filed (diff-skip + in-op parallelism) |
| E09 | Prometheus metrics bearer token | ✅ **DONE** — scrape `health=up` since 14:02 EDT. The config block pre-existed; the whole fix was a valid key in the token file (minted via API straight to a server-side file). The staged script's yml editor had a guaranteed ValueError (dead indent block) — patched on-server |

## Waves F/G/H — iTunes, frontend, CI

| Task | Description | Status |
|---|---|---|
| F110 | Playlist-PID coverage measure | ✅ merged #2435 — 88.5% via ExternalIDMapping; album-PID instrument (14%) documented as wrong tool |
| F111–F113 | Playlist import / smart criteria / ITL defect | ⏸️ **parked — owner's lowest priority** |
| F114 | Progress propagation investigation | ⬜ ready |
| F115 | Dead file-level playlist path | ✅ merged #2428 |
| F116 | 2-way-sync guard scope | 🔒 owner decision |
| G110 | Restore library sort | ✅ merged #2453 — grid-view Sort control restored, server-side keys, 2 mutations verified |
| G111 | Clickable metadata links | ⬜ unblocked by C110 |
| G112–G117 | Frontend batch | ⬜ ready / owner-gated |
| G118 | Empty-FieldFilter trace + 400 toast | ✅ merged #2432 — no live UI path can send an empty value; 400s now surface the server message |
| G119 | MUI upgrade ladder | 🔒 D-8 resolved as fast-checks-only; ladder still needs its own gate decision |
| H110 | internal/database CI stall | 🟡 **evidence finally captured** (Coverage Floor stall log: cumulative slowness + leaked nutsdb goroutines, NOT a deadlock); proactive half open |
| H111 | tmpfs test target (D-9 ✅) | ✅ merged #2437 — `make test-fast`, measured 54.1s → 5.3s |
| H112 | Required checks (D-8 ✅) | ✅ **live** — Minimal CI Summary + changelog + fragment-headers required on main (~3-4 min); E2E advisory (12 min measured) |
| H113 | E2E port isolation per worktree | ✅ merged #2442 — per-worktree port/tmp, served-bundle identity assertion, bind-failure exit |
| H114 | Leak-scanner heuristic | ✅ merged #2431 |
| H115–H116 | e2e debts / coverage-floor items | ⬜ ready / gated H110 |

## Production state notes (scrubbed)

- Review-apply is ON with one verified canary; scheduled intervals repaired;
  branch protection with fast required checks is live.
- Production runs the 13:40 EDT build with all 29 of today's merges. Boot
  checks all passed: search index reconciled (3,983 stale docs deleted, now
  exact), dedup-refresh ticker live, config snapshot survived the restart.
- Remaining gated prod steps: BookSig canary apply (E01), whole-library
  chapters apply (E02), the audit-trail check tonight (A04), and the CodeQL
  fan-out once the next analysis lands.
- Metrics scraping remains down pending the operator running the staged
  Prometheus token script.
