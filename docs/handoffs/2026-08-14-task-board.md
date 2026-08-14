<!-- file: docs/handoffs/2026-08-14-task-board.md -->
<!-- version: 1.1.0 -->
<!-- guid: 8d2f6a41-3c97-4e58-b0d6-1a75c9e28f30 -->
<!-- last-edited: 2026-08-14 -->

# Task board — 2026-08-14

Snapshot of the 113-task breakdown (waves T/A/B/C/D/E/F/G/H) as of
2026-08-14 ~17:45 EDT. **29 PRs merged today** (#2421–#2447), 0 open, and
**everything is deployed**: the 13:40 EDT production deploy was built from
main with all 29 included, and the boot came up clean (no search-index
divergence). Owner decisions D-0…D-10 were all answered today; the outcomes
are inlined below.

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
| C111 | nil `is_primary_version` semantics (D-2) | 🟡 census merged #2438; normalize-primary-flags maintenance job (nil→explicit true for 5,702 ungrouped + the 41 false/no-VG = C314) built, PR imminent |
| C112–C113 | Bare-param guard extensions / declared schema | ⬜ ready / design |
| C210–C213 | Delete-guard family | ⬜ ready (C211 ✅ closed by peer #2412) |
| C310–C314 | Version-group integrity | ⬜ C310 gates; C314's exact 41-book population identified by the C111 census |
| C410–C414 | Author-table hygiene | ⬜ ready / gated on C410 |
| C510 | opstate params sweep | ✅ merged #2446 — retention job now clears state for gone/terminal ops; keeps running/queued/interrupted/unknown |
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
| C815–C816 | Orphan-VG pagination / memdb mutex | ✅ merged #2443 / #2444 (mutex hoist benched 1.35–1.83 → 1.21–1.25 ms/op) |
| C817 | DeleteBookFilesForBook memdb sync | ✅ merged #2427 |
| C818–C819 | Aggregates coalescing / row-count bookkeeping | 🔒 need A07 / A06 |
| C910–C911 | Dedup backlog / merge-cache-evict | ⬜ ready |
| CA10–CA11 | SSRF alerts / zipslip batch | ⬜ ready |
| CA12 | Log-injection sweep (D-7 ✅) | 🟡 waves 1+1.1 merged #2439/#2445 — recognition verdict came back **1/322 closed**: both sanitizers had a clean-string fast-path returning BEFORE the ReplaceAll barrier (CodeQL barriers are path-sensitive). Fixed; next main analysis is the fan-out gate |
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
| E01 | BookSig sidecar migrate | 🟡 dry-run done: examined 63,841, **would migrate 26,159**, 0 errors — instrument gate (≈27K) PASSED; canary apply (limit 100) is next |
| E02 | Chapters backfill | 🟡 cohort dry-run done: 77 examined, would-persist 0 (47 already have chapters from the B06 pass, 17 multi-file by design, 12 markerless) — cohort complete; whole-library residue (~14.6K) is the remaining step |
| E03 | Search-index boot repair confirmation | ✅ verified — boot gate deleted **3,983** stale docs in 1m29s (3,953 trash + 30 from the E05 canary merge); index now equals the library and the 13:40 boot showed no divergence |
| E04 | Title-match false-series repair | ⬜ build + dry-run |
| E05 | review_apply_enabled flip + canary | ✅ **done + verified** — flag live, canary combine applied exactly per card (31 files → 1 book); 354 items now live-appliable |
| E06 | probe-directory-books apply | ✅ applied — examined 1,026: **434 actioned** (exactly the approved dry-run count), 592 stay in review, 0 errors |
| E07 | iTunes PID-repair | ✅ resolved, no apply needed — live census shows only **2 duplicate PIDs** (both ambiguous, one same-content pair split across the two trees; hand review); the recorded 8,984 was stale pre-repair state. Side catch: the endpoint read dry_run from the query only and silently ignored a JSON body — fixed in #2447 |
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
