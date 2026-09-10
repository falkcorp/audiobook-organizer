<!-- file: docs/agent-tasks/todo-completion-2026-09/state/RAW-RESULTS.md -->
<!-- version: 1.8.0 -->
<!-- guid: 9b4e6d21-7f3a-4c58-a1d2-5e8f0b9c3d74 -->
<!-- last-edited: 2026-09-10 -->

# Raw results ledger — burndown re-evaluation 2026-09-10

Crash-safe record of every subagent's final report, written as each one completes.
The JSON files under `wave1/` and `wave3/` are the agents' actual deliverables; this
file carries the summaries that otherwise live only in the coordinator's context.
Baseline: HEAD `42d187168` (== `origin/main`), previous reconciliation `d2fcef16a`
(2026-09-02), 399 commits / 138 merged PRs in between.

## Inputs (Wave 1 haiku inventory)

- `wave1/todo_open_items.json` — **586** unchecked items in `TODO.md` (any indentation;
  top-level-only count is 569). `wave1/todo_sections.json` — 114 `## ` sections
  (two named "Config", two named "Dedup"; counted separately). 164 checked items.
- `wave1/briefs_inventory.json` — **251** `TASK-*.md` briefs: 208 `todo-completion`
  (18 workstreams with briefs; `handoff/` holds session notes, not briefs) + 43 in
  siblings (abs-sync 10, ux-small-items 8, bug-techdebt 7, torrent-relocation 7,
  dedup-pipeline-hardening 6, ai-responses-migration 5).
- `wave1/merged_prs_since_0902.json` — 138 PRs merged ≥ 2026-09-02.
- `wave1/git_log_since_d2fcef16a.txt` — 399 commits.
- `wave1/todo_chunk_{1..4}.json` — the 586 items split 155/150/147/134 on section
  boundaries (lines 45–3297, 3326–6609, 6642–11021, 11073–17789).
- Known extractor defect: `subsection` in `todo_open_items.json` only tracked `## `,
  so ~50 items at lines 4714–6523 carry a wrong parent heading. The verdict agents
  re-derived headings from `grep -n '^#\{2,3\} ' TODO.md`; verdict files are correct.

## Wave 1 — brief reconciliation (251/251)

**Totals: DONE 120 · REAL 124 · STALE 6 · UNKNOWN 1.** 9 verdicts changed since 09-02.
REAL by risk: hygiene 62, correctness 40, data-loss 11, perf 8, security 3.
REAL by effort: S 45, M 61, L 18. 0 rows lack evidence; 251 unique paths.

### A — audiobooks, ci-tooling, config, database, dedup, docs, itunes (74)
DONE 41 · REAL 32 · STALE 1 · 0 flips. All 40 cited PRs confirmed MERGED (GraphQL
batch); all 36 cited SHAs confirmed ancestors of HEAD; 24/41 DONE code-checked; all 32
REAL re-verified by running each brief's own greps. Path drift (verdict unchanged):
TASK-064 `internal/itunes/fs_regroup_shape.go` → `internal/itunes/service/…`;
TASK-192/193 `calibrate_composite.go` is in `internal/plugins/dedup/`, not
`plugins/maintenance/`. TASK-190 STALE confirmed (`audiobooks_helpers.go:113-127`
already prefers `matchTotal`). TASK-041 DONE only as duplicate-closure of TASK-047 —
its own title (audit of stale wide-type comments) is uncovered. Weakest: TASK-026
(CodeQL triage, no grep-verifiable artifact), TASK-031/033 (merge-only evidence).

### B — maintenance, metadata, misc-go, missing-file-lane, operations, organize, scanner (69)
DONE 28 · REAL 39 · STALE 1 · UNKNOWN 1. **6 flips:**
- TASK-067 open→DONE — `bookPathByID` fallback, commits `2d1db2222`/`4ed8f109f`,
  `internal/plugins/maintenance/missing_file_repoint.go:260-271,372`.
- TASK-078 open→DONE — `internal/maintenance/jobs/backfill_sync_ids.go` sets
  `Concurrency: BackfillConcurrency()`.
- **TASK-083 done(#2781)→REAL (security)** — `gh api`: 18 open `go/path-injection`
  alerts at HEAD; #2781 fixed 2 of the 4 originally named.
- TASK-121 open→REAL (correctness), narrowed: PR #3046 fixed `resolveOrganizedFilePath`
  but `internal/organizer/organizer.go:121,140-142` single-file fast path returns
  success with no existence check.
- TASK-196 open→STALE — `metadata.candidate-fetch` op (`internal/server/metadata_candidate_op.go`)
  already is the bounded/resumable equivalent.
- TASK-198 open→UNKNOWN — needs a live Playwright run.
Process note: this agent forked 8 children; two overran scope and clobbered the
output file; the coordinator re-adjudicated. Final file is the reconciled one.

### C — search, server, server-handlers, web (65) + 6 sibling initiatives (43)
todo-completion: DONE 23 · REAL 40 · STALE 2. Siblings: DONE 28 · REAL 13 · STALE 2.
**3 flips:** TASK-135 open→DONE (`5df6b70cb`, `ResumePolicy: ResumeRestart` on
batch-apply); TASK-213 open→DONE (`6af53a0b6` / #3059 removed the 4th organize path);
TASK-214 stays REAL (sibling `ListCachedCandidates` paging fixed `9ef923ba7`, target
`GetCacheReviewResults` still unpaged).
**Owner-gated REAL (7, not dispatchable):** torrent-relocation TASK-02/03/05/07
(parked — `docs/plans/DECISIONS-PENDING.md` row 2, PR #2715); ai-responses-migration
TASK-01/02/05 (on hold; TASK-02/05 acceptance unsatisfiable under the locked two-arm
`useResponsesAPI` design). Undeterminable: ux-small-items TASK-08 and
dedup-pipeline-hardening TASK-06 (operational runs, no repo artifact); abs-sync TASK-07
regression test never runs in CI (no workflow installs ffmpeg).
Stale doc claim: `TODO.md:17482` says AI-RESP-D closed; GitHub issue #1263 is open.

## Wave 2 — TODO.md reconciliation (586 items)

### Chunk 1 — lines 45–3297, 27 sections (155)
**REAL 131 · DONE 17 · STALE 1 · UNCLEAR 6.** (Inbox + newest sections; high REAL
rate expected.) Agent forked 8 children (pre-dates the no-subagent rule); file
rewritten 3× by out-of-scope forks; final counts unchanged across rewrites.

### Chunk 2 — lines 3326–6609, 25 sections (150)
**REAL 107 · DONE 27 · STALE 3 · UNCLEAR 13.** UNCLEAR are all prod-observation
tasks (need a completed prod scan summary or prod SSH; standing ban respected):
3480, 3482, 4137, 4145, 4175, 4357, 4447, 4471, 4474, 4529, 4544, 4582, 4589.
Top REAL (data-loss unless noted): 5163 AUTHOR-MEMBERSHIP-UNGUARDED
(`GetBooksByAuthorIDWithRoleCore` still calls unguarded memdb path; fired in prod
2026-08-24); 5139 ORPHAN-FILES-HARD-DELETE-FAIL-OPEN; 4967 `dedup.series-dedup` apply
writes no undo-ledger rows; 5018 SERIES-MERGE-UNGUARDED-DENOMINATOR; 4241/4242
`BatchUpsertBookFiles` no within-batch FilePath dedup (+ iTunes-PID twin
`enforceBookFilePIDUniqueness`); 4244 no repair pass for duplicate `book_file` rows;
3966 Unknown-Author repair never built; 3395 no same-filesystem detection for backup
location; 5246 MEMDB-LOSSY-READERS sequencing dependency.
`dup_of`: 6479→TASK-158, 6523→TASK-040, 6560→TASK-143, 6598→TASK-127, 6609→TASK-182.
Fixed a mislabel (TODO.md:3970 had been tagged 3968).

### Chunk 3 — lines 6642–11021, 28 sections (147)
**REAL 80 · DONE 43 · STALE 11 · UNCLEAR 13.** Agent forked 6 children (pre-rule);
three overwrite incidents on the shared file, each rebuilt deterministically from the
per-group source files; final merge validated (147 unique lines, no gaps).
Top REAL: L9126 SEC-CODEQL-BACKLOG 326 open CodeQL alerts on main incl. 2 critical;
L10432 CA12 CodeQL log-injection sanitizer modeling never added; L10906 SEC-2 bootstrap
still writes plaintext credential files; L10908 SEC-4 no CSP header; L6891 SEC-9 OpenAI
API key still sent from the browser; L7211 `registry.RunItems` Label counter read outside
its lock; L8042 ITUNES-SMARTCRIT-PARSE; L10306 raw `*bool` post-filters treat nil
`is_primary_version` as false; L7143 per-chapter split files as own books never
investigated; L8152 MERGE-CACHE-EVICT (= TASK-023).
UNCLEAR (13, need prod query / DB census / operator confirmation): 7048, 7214, 7676,
8798, 8998, 9933, 10075, 10312, 10658, 10830, 10841, 10870, 10916.
### Chunk 4 — lines 11073–17789, 6 sections (134)
**REAL 94 · DONE 33 · STALE 4 · UNCLEAR 3.** No forks; 81k tokens / 234 tool calls.
Top REAL: 17185 ABS-SYNC TASK-12 three identity gaps (data-loss, L); 15799 duplicate
resolution still DELETES instead of re-associating (data-loss, L); 15693 multidisc-apply
has no before/after snapshot canary, `review_apply_enabled` OFF (data-loss, M); 17307 /
17329 iTunes 2-way sync P3 + writeback undelivered (data-loss, L); **16949 TODO-SEC-BIND
service binds `0.0.0.0`** (security, M); **16966 TODO-SEC-SYSTEMD 5 hardening directives
missing** (security, M); **16960 TODO-SEC-JWT `ABS_JWT_SECRET` not rotated after
plaintext exposure** (security, S); **11964 `OperationDef.Permissions` enforced by
nothing** and the enforcement code is slated for deletion (security, M); 17162 ABS
surface not exempted from `BasicAuth()` when `basic_auth_enabled` (security, S).
UNCLEAR: 11615 (live prod dir scan), 12263 (mutation-testing run), 13222 (Playwright,
TASK-198 scope). Reclassified after self-review: 11821 (staticcheck still aborts
`make ci`, 9 findings), 12251, 11611 (artifact exists, prod run/record not done).

## Wave 3 — untracked-work audit

### go-specialist — internal/database + internal/operations — `wave3/audit_database_operations.json`
6 findings: 1 high, 4 medium, 1 low. (gopls unavailable to the agent; grep + reads.)
- **DB-01 high data-loss** — `author_bookref.go` `CountAuthorReferences` pass 2 still
  uses the narrow `book:0`..`book:;` byte-range bound that was fixed v2→v3 (2026-08-23)
  in `pebble_store_versiongroup_backfill.go`; a non-ULID-leading book ID is invisible
  to the delete guard → a referenced author can be deleted.
- DB-03 medium correctness — `migration014UpPebble` (repair for corrupted
  `{series}`/`{author}` placeholder paths) is fully written but never dispatched from
  the `migration014Up` stub (`lint:ignore` since 2026-07-12); never ran on prod Pebble.
- DB-02 medium data-loss — `RunMigrations` writes effect / record / version bump as 3
  unbatched writes; crash between them replays a non-idempotent migration (latent).
- OPS-01 medium correctness — `resumeRestart` publishes `op.created "queued"` + pings
  the dispatcher even when `ResetOperationV2ForResume` failed → ghost op.
- OPS-02 medium perf — `MigratingActivityStore` dual-writes to SQLite secondary but
  `Prune`/`CompactByDay`/`Summarize` run only on the active backend → secondary grows
  unbounded until the read flip.
- DB-04 low — swallowed error in dormant `NutsActivityStore` digest compaction.
Checked CORRECT: RunItems, memdb_sync write-through, UpdateBook batch/index discipline,
dispatcher/worker/watchdog/batch.go, embedding_store candidate-index invariant.

### silent-failure-hunter — metafetch/metadata, scanner, organizer — `wave3/audit_silent_failures_pipeline.json`
4 findings: 2 critical, 1 high, 1 medium; 11 paths verified fail-closed.
- **SF-02 critical correctness** — `internal/scanner/scanner.go:1705,1714`: inline
  AI-parse phase's `AIPhaseSummary` return is discarded (bare statement), so a fully
  aborted LLM phase (revoked key / quota / 3+ batch failures) still lets `library.scan`
  and folder-autoscan report COMPLETED; the fix built for the *queued* AI-parse op never
  reaches the inline path.
- **SF-03 critical correctness** — `internal/metafetch/isbn.go:399,402,423,426`: nightly
  ISBN/ASIN sweep does `results, _ = SearchByTitle*(...)`; breaker-open/throttled provider
  is indistinguishable from "no ISBN", zero logs, zero counters.
- SF-01 high correctness — `internal/organizer/organizer.go:141-143,184-190`: single-file
  organize no-op success paths never `os.Stat`; directory sibling `service.go:1490-1498`
  does. (Same gap as TASK-121, narrowed.)
- SF-04 medium correctness — `internal/metafetch/service_apply.go:933-938`: MATCH-4
  auto-merge primary selection scores a `GetBookFiles` error as "0 files", unlogged →
  may pick the wrong merge primary.
Checked CORRECT: move.go, apply_failure.go, field_locks.go, cache.go preserve-on-empty,
ai_batch_phase.go, write_tags_safe.go, ai/retry.go (+4 more in the JSON).
### schema-auditor — queries/indexes/migrations — `wave3/audit_schema_queries.json`
5 findings: 3 high, 2 medium; 7 items verified correct.
- **SQ-01 high perf** — `buildSearchIndexIfEmpty` reindexes the whole library serially
  and `BookToDoc` does 3 point-gets per book (author/series/tags) instead of the batch
  `GetAuthorsByIDs` used elsewhere (`internal/server/server_search.go:44-100`,
  `internal/search/index_builder.go:82-165`).
- **SQ-02 high perf** — no persisted author→books secondary index (docs describe one);
  pre-memdb-warmup fallback does two full-keyspace scans per single-author lookup, and
  that window recurs ~130s after every restart (`internal/database/pebble_store.go:2254-2339`).
- **SQ-04 high perf** — `external_id_backfill_v4_done` is written but never read; the
  full-library iTunes external-ID backfill (with an acknowledged N+1 `GetBookFiles`)
  reruns on every boot despite "one-time, idempotent" comment
  (`internal/itunes/backfill.go:29-58`, `internal/server/server_lifecycle.go:856-874`).
- SQ-03 medium hygiene — `DeleteBook` never deletes `book_authors:`/`book_narrators:`
  sidecar rows written by `SetBookAuthors`/`SetBookNarrators`; same dangling-row class
  this function already fixed twice (`internal/database/pebble_store.go:3112-3291`).
- SQ-05 medium correctness — ABS filterdata "published decades" built from an offset-0
  scan of the first 5,000 books in creation order; later decades permanently omitted,
  no truncation signal (`internal/server/handlers/abs/browse.go:1959-1995`).
Checked CORRECT: UpdateBook/DeleteBook ISBN/ASIN + version-group + work-ID index
maintenance; `FetchBookFilesForBooks` batch-first; `service_query.go` batched author
enrichment; activity scan-budget handling; dedup search full scan + filterdata bound
are self-documented/tracked tradeoffs.
### expert — dedup + activity — `wave3/audit_dedup_activity.json`
4 findings: 1 critical, 1 high, 1 medium, 1 low; 9 areas verified correct.
- **DA-01 critical data-loss** — `internal/dedup/split_book_merge.go:67`
  `MergeSplitBookCluster` mutates book/file rows without `merge.LockMergeRMW` (the lock
  `internal/merge/serialize.go` says every merge-family RMW must hold); 4th unguarded
  path, reachable from unkeyed handler `internal/server/handlers/split_book.go:142`;
  can race a locked `MergeBooks`/`CombineBooks` on the same book.
- **DA-02 high data-loss** — two unattended auto-merge triggers (`engine.go:1373`
  exact-hash, `engine.go:4047` LLM high-confidence) and three bulk/manual merge
  endpoints (`handler.go:940`, `:1125`, `:1196`) call `MergeBooks` directly, not
  `MergeJournaled` → no undo trail. Broader than TODO.md:6523 MERGE-UNDO (review lane only).
- DA-03 medium perf — `cleanup_orphan_embeddings.go` and `reembed_embeddings.go` are
  sequential full-keyspace loops with per-item point lookups; siblings in the same
  package use `registry.RunItems` + `Concurrency: runtime.NumCPU()`.
- DA-04 low hygiene — `isBatchable` tier gate: warn/error-level lines of a batchable
  Type are never coalesced, only debug-tier ones.
Checked CORRECT: FullScan concurrency fix, auto-resolve journaling, merge service
locking, sync-follow idempotency, activity migration checkpoint/resume (digest
never resumes), batcher/writer shutdown ordering, series-dedup reference-count guard.

### go-specialist — server/handlers + scheduler — `wave3/audit_server_handlers.json`
4 findings: 3 medium, 1 low; 9 areas verified correct (auth middleware, ABS identity,
request-size limits, shutdown ordering, all 22 scheduler tasks, route auth wiring,
pagination bounds, SSE broadcast concurrency). Already fixed/tracked, not re-reported:
timeline `?status=` (fixed 2026-09-09), config masking gaps (TODO.md), scheduler
"never runs" case (now WARN).
- SV-01 medium correctness — `DELETE /operations/history` deletes from the retired v1
  `operation:` keyspace (`internal/server/handlers/operations/handler.go:244`); returns
  `{"deleted": N}` while touching nothing the v2 UI shows. 0 frontend callers.
- SV-02 medium data-loss — `POST /backup/restore` `Verify: true` is a silent no-op
  (`internal/server/handlers/system/handler.go:697`); 200 never says verify was skipped.
- SV-03 low security — `/api/events` SSE sets hardcoded `Access-Control-Allow-Origin: *`
  (`internal/realtime/events.go:221`), undoing the MED-2 allowlist hardening.
- SV-04 low perf — `IPRateLimiter.limiterForIP` O(n) sweep under one mutex per
  rate-limited request (`internal/server/middleware/ratelimit.go:47`).

### typescript-specialist — web — `wave3/audit_web.json`
6 findings: 3 high, 3 medium; 9 items verified correct (apiFetch timeout/auth-redirect,
useLibraryQuery abort/retry/stale-cache, eventSourceManager backoff, SSE cleanup,
optimistic-op rollback, link `rel`, no `dangerouslySetInnerHTML`, no tokens in
localStorage, no auto-retry on mutations). 27 tool calls.
- **WEB-01 high perf** — `GET /authors` has no pagination; `Authors.tsx` loads the whole
  ~17k-author table on every mount, paginates client-side.
- **WEB-03 high ux** — `Dashboard.tsx` count loaders `catch { setCount(0) }`; a failed
  count API and an empty library render identically.
- **WEB-04 high data-loss** — `DedupAuthorTab.tsx` `AuthorBooksPopover` has no error
  state; `getBooksByAuthor` swallows non-2xx as `[]`, so a populated author can look
  like a safe empty duplicate right before a human merges it.
- WEB-02 medium perf — same whole-table fetch for `GET /series` in `Series.tsx`.
- WEB-05 medium ux — `getOperationTimeline` returns `[]` on non-2xx and network
  failure; the operations bell shows "no recent operations" when the endpoint is down.
- WEB-06 medium hygiene — `Authors.tsx` / `Series.tsx` have zero Vitest/Playwright
  coverage despite destructive bulk delete/merge actions.

### Explore — CI/workflows + scripts — `wave3/audit_ci.json`
6 findings: 2 high, 3 medium, 1 low. 40 tool calls, 84k tokens.
- **CI-01 high correctness** — `Makefile.local.example:72,102` deploy/deploy-debug guard
  is `merge-base --is-ancestor origin/main HEAD` ("not behind" only); HEAD AHEAD of
  origin/main passes → unpushed commits can ship. Documented invariant (TODO.md:372) is
  `rev-list --left-right --count HEAD...origin/main` == `0 0`.
- **CI-02 high security** — `.github/workflows/frontend-ci.yml:20-26` grants
  contents/actions/checks/packages/id-token/attestations `write` to an external reusable
  workflow (`falkcorp/github-common reusable-ci.yml`); ci.yml does the same work with
  `contents: read` + `checks: write`.
- CI-03 medium — `security.yml:77` Node `20.x` for dependency submission; every other
  workflow uses 22.
- CI-04 medium — `test-action-integration.yml:104-117` Go-version check truncates to
  major.minor, ignores `.envrc`/Dockerfiles, and only `::warning::`s.
- CI-05 medium — `nightly-burndown.yml` (08:00/20:00 UTC) and `hard-burndown.yml`
  (Sun 10:00 UTC) share `hub_repo` with no `concurrency:` group.
- CI-06 low — `frontend-ci.yml:54` computed `if:` can skip the frontend job silently.

**All 8 Wave 3 audits complete: 35 findings — critical 3, high 11, medium 16, low 5.**
Dropped: separate pr-test-analyzer pass (audits already record missing tests per finding).

### Cost note (12:50 EDT)
Parent-only usage: verifiers A/B/C 199k/180k/165k tokens; TODO chunks 1/2 241k/155k;
audits 233k/171k/206k/279k. Chunk 3 forked 7 children (pre-rule); told to stop and
consolidate. Remaining prompts carry a ≤60-tool-call budget.

## Process notes for the next coordinator
- Hard cap 4 concurrent agents. Agents launched before 12:23 EDT forked children of
  their own (8, 8, 6) and raced on shared output files; every prompt since carries
  "Do NOT spawn subagents". Keep that line.
- The old package was generated by `docs/agent-tasks/todo-completion/tools/gen_package.py`
  from `skeleton.json` (plan-op pipeline). The new package must go through the same
  generator to honor the "same format" deliverable — do not hand-write 100+ briefs.
- Nothing in this branch touches application code. `git diff --stat main -- internal cmd web`
  must stay empty.

## Final analysis — 2026-09-10 (four read-only auditors, no forks, budgets held)

Launched after the package was committed (17db73953) and PR #3179 opened, at the owner's
"do the final analysis". Outputs saved verbatim under `state/final/`; conclusions and the
generator fixes they drove are in `../FINAL-ANALYSIS-2026-09-10.md`.

| Agent | Type / model | Tool calls | Tokens | Wall | Output |
|---|---|---|---|---|---|
| Mechanical package audit | `plan-op:plan-auditor` / sonnet | 28 | 142k | 12m | `final/plan_audit.json` |
| Adversarial re-check, top 11 findings | `audiobook-organizer:go-specialist` / sonnet | 53 | 142k | 6m | `final/adversarial_top11.json` |
| Cold-execute 6 top briefs | `plan-op:brief-verifier` / sonnet | 34 | 85k | 5.5m | `final/brief_verifier_top6.json` |
| Validate 30 TODO-section briefs | `Explore` / sonnet | 31 | 112k | 9.7m | `final/todo_sections_validation.json` |

Final reports, condensed:

- **plan-auditor:** 219 files header-clean, 0 path mismatches, 0 intra-package guid dupes,
  **111 guid collisions with the archive twins**; 65 new anchors: 1 missing file (TASK-339),
  1 `test -f` on a directory (WEB-06), 1 zero-hit grep, **11 briefs grepping a different
  paragraph than their title**; 111 carried re-verify greps run: 106 still open, 0 look done,
  5 stale anchor strings (TASK-071/129/189/192/193); 3 same-wave file collisions; 2 matrix rows
  pointing at the undated old path; 176/176 briefs structurally complete.
- **go-specialist:** 10 CONFIRMED, 1 PARTLY (SF-01: second branch already stat-guarded),
  0 REFUTED. Design notes: DA-02 needs an interface-shape decision; DB-02 option (b) is ~60
  signatures; DB-03 writes `LibraryState` — audit-first; SF-02 must stay non-fatal for the scan.
- **brief-verifier:** 5 NEEDS-EDIT + 1 REWRITE. Systemic: `REPO=/path/to/…` placeholder;
  dry-run/`apply=false` boilerplate unsatisfiable for pure code changes; re-verify windows
  covering one of several call sites. TASK-337's "fix OR unregister" needs a decision.
- **Explore:** all 30 briefs' items still `- [ ]` at HEAD; 20 DISPATCH, 8 HOLD-FOR-OWNER,
  2 RECLASSIFY; the `section` field is systematically unreliable (7 cosmetic, 8 substantive
  mismatches); TASK-363 bundles four operator-only items and touches the deploy and scan bans.

What changed because of them (generator only; `gen_new_package.py` 1.0.0 → 1.1.0,
`build_matrix.py` 1.0.0 → 1.1.0; new inputs `final/todo_line_overrides.json`,
`final/carried_anchor_drift.json`; new output `final/brief_index.json`): real repo path;
decidable rollback rule; `anchor_windows()` over every cited line; `uuid5(path)` guids for
carried copies; drift notes on the 5 carried briefs; TODO briefs regrouped by the REAL
enclosing heading with bold-named items split out and anchored by their text at HEAD
(30 → 41 briefs); dispatch verdicts stamped on briefs, BREAKDOWN and matrix (13 held,
3 reclassified); collision-aware waves; `Brief` column and new-package paths in the matrix.
Package: 187 briefs, 171 dispatchable. Matrix: 303 rows.

Cost/process: the four agents together used 146 tool calls and ~481k tokens — under the
cost of ONE forking wave-1 verifier (241k) per agent, and no shared-file races. The
"working harder than they should" incident from earlier in the day was three wave-1 shell
loops hung on zsh's `> "$OUT"` (NULLCMD `cat` on the harness stdin) for 80+ minutes with
0 bytes written; killed, lesson recorded in memory.

