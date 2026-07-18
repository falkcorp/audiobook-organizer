<!-- file: docs/agent-tasks/error-correction-2026-07/TASKS.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b2e94d1-4c7a-48f3-b1e9-2a85c0df7e46 -->
<!-- last-edited: 2026-07-17 -->

# Error-correction follow-on task briefs (2026-07-17)

Thirteen self-contained briefs, T01–T13. Each is written so a weak/small-model
agent can execute it without any other context. Read the RULES block, then your
assigned task section ONLY, then execute exactly.

## RULES (apply to every task — read first)

1. **Worktree discipline.** Never edit the main working tree. For any code/doc
   change: `cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer && git worktree add .worktrees/<short-name> -b <branch-name>` and work only inside it.
   After your PR merges: `git worktree remove .worktrees/<short-name> && git worktree prune`.
2. **File headers.** Every file you edit: bump the `version:` in its header
   (minor bump for behavior changes, patch for comment/log-only) and set
   `last-edited: <today>`. New files get the 4-line header block with a fresh
   `uuidgen` GUID.
3. **Tests.** Write/extend tests for every behavior change, run the FULL package
   (`go test ./internal/<pkg>/... -race -count=1`), never a single-test subset
   as final proof. `go build ./...` must be clean.
4. **Commits/PRs.** Conventional commits (`fix(scope): ...`). Push branch, open
   PR with `gh pr create`, wait for CI (`gh pr checks <n>` exits 0 when green),
   merge with `gh pr merge <n> --rebase` (this repo is rebase/FF only). Then
   `git -C <repo-root> pull --ff-only`.
5. **Docs.** In the SAME PR: add a CHANGELOG.md entry (prepend under today's
   date), and tick the corresponding line in TODO.md §"2026-07-17 review
   findings — remaining" (delete the line and add PR # to the "Fixed" list).
6. **Never** touch production (the `--prod` flag on sandbox scripts, port 8484,
   `make deploy`) unless the task explicitly says so. Sandbox (:8485) is fair game.
7. **Never** commit an internal IP address (`172.16.*`) or hostname. Refer to
   hosts as "the prod server" / "the GPU box". The pre-commit hook blocks
   violations — do not bypass with `--no-verify`.
8. **Stuck?** If a step fails twice for the same reason, STOP, write what you
   tried into the PR/issue body, and report — do not improvise around a wall.
9. **Test helpers** must have task-unique names (e.g. `t04StrPtr`, not `strPtr`)
   — same-named helpers in one package have broken main before.
10. Mock regeneration: NEVER run global mockery. Hand-write any needed mock
    methods in the test file, matching existing style.

Sandbox access recipe (used by T02/T03/T04): see
[`docs/status/2026-07-17-error-correction-session.md`](../../status/2026-07-17-error-correction-session.md)
§"Sandbox operational notes" — key file, re-bootstrap procedure, detached
launch, and the poll-`/api/v1/operations/v2/<op_id>`-yourself note.

---

## T01 — Land PR #1986 (organizer data-loss fixes) — TRIVIAL

DEPENDS ON: nothing. BLOCKS: T05 acceptance (M4 line).

1. `gh pr checks 1986` — if green: `gh pr merge 1986 --rebase`.
2. If a check FAILED: `gh run view <run-id> --log-failed | grep -E "\-\-\- FAIL|DATA RACE|panic:"`.
   Reproduce locally in `.worktrees/fix-organizer-dataloss` (branch
   `fix/organizer-rename-dataloss`): `go test ./internal/organizer/... -race -count=3`.
   If local passes 3/3 and the CI failure is in an UNRELATED package (known
   intermittent race-job stalls), re-run the failed job once. If the failure is
   in `internal/organizer`, fix it in that worktree, push, re-check.
3. After merge: `git -C /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer pull --ff-only`,
   remove the worktree (`git worktree remove .worktrees/fix-organizer-dataloss`),
   delete the branch, and tick DL-1/DL-2/DL-3/M4 in TODO.md per RULES 5 (that
   tick + CHANGELOG entry is a tiny docs commit direct on a new branch + PR).

ACCEPTANCE: #1986 merged; worktree gone; TODO/CHANGELOG updated.

## T02 — Sandbox: finish breakdown-backfill, run triage, report populations

DEPENDS ON: nothing (sandbox already prepared). BLOCKS: T03.

CONTEXT: The sandbox (:8485, plain HTTP) holds a full prod replica where
`maintenance.title-repair` was already APPLIED (555 retitles) and
`dedup.breakdown-backfill` apply (op `01KXSJHBDDP17AMR8WYKSTQH30`) was launched.
Expected backfill: ~9,419 candidates gain ScoreBreakdowns. Goal: prove the
triage tool now classifies the exact-pending backlog into actionable
populations (formerly `unknown=9,950`, `title_leak=0`).

1. SSH to the prod server as jdfalk. Check instance:
   `curl -s -m 30 http://localhost:8485/api/v1/health` → expect `"status":"ok"`.
   If down, relaunch per the Sandbox access recipe. If the DB was never
   backfilled (step 2 shows backfilled=0), re-run apply:
   `cd /tmp/abk-sandbox && export AUDIOBOOK_API_KEY=$(cat .api-key) && python3 run-op.py dedup.breakdown-backfill '{"apply":true}'`
   (enqueue succeeds; ignore its 404 polling — poll yourself per recipe).
2. Confirm backfill finished: poll
   `GET /api/v1/operations/v2/01KXSJHBDDP17AMR8WYKSTQH30` (Bearer key from
   `/tmp/abk-sandbox/.api-key`) until logs contain `backfill complete`; record
   the final counter line (backfilled=N, zero_signal=N, update_errs=N).
   `update_errs` MUST be 0; if not, STOP and report.
3. Run triage: enqueue `maintenance.dedup-exact-triage` params `{}` the same
   way. Poll to completion. Record the population line
   (`genuine N | stub N | fragment N | title_leak N | unknown N` and the
   purgeable/keep/review summary) from the op logs.
4. Baseline comparison (2026-07-17 pre-work): `unknown=9950, title_leak=0,
   purgeable=1`. Expected after backfill+relax: unknown collapses to near the
   `zero_signal` count (~643), `title_leak` in the thousands.
5. Write the measured numbers into
   `docs/status/2026-07-17-error-correction-session.md` (replace the "NOT yet
   run" triage row) and into `docs/dedup/STATUS.md` (triage-population section)
   via a normal docs PR (RULES 1–5).

ACCEPTANCE: backfill counters + triage populations recorded in both docs;
update_errs=0; PR merged.

## T03 — Sandbox: purge wave + rescan + final backlog measurement

DEPENDS ON: T02 (needs its triage numbers). BLOCKS: T04.

CONTEXT: `title_leak` and `stub` triage classes are purge-safe (purge dismisses
candidates — it mutates NO files, no books). The purge apply path lives in the
triage op family — inspect
`internal/server/server_maintenance_deps.go` `DedupTriageExactPending` and
`internal/plugins/maintenance/dedup_triage.go` for the apply/purge parameter
shape (grep for `purge` / `apply` in those files) before running anything.

1. Record pre-state: `GET /api/v1/dedup/stats` → note exact-pending and
   total-pending (post-T02 state).
2. Run the triage purge for purgeable classes ONLY (dry-run first if the op
   supports it; read the code to confirm parameter names — do NOT guess).
3. Re-run `dedup.purge-stale` `{}` and then `dedup.full-scan` `{}` (full-scan is
   embedding-only; it refreshes embedding candidates after the retitles).
   Full-scan can take tens of minutes — poll with patience (op timeout 120 min).
4. Final measurement: `GET /api/v1/dedup/stats` → exact-pending, total-pending;
   plus triage populations again. The 2026-07-17 baseline was 9,074 / 10,319.
5. Record all before/after numbers in `docs/dedup/STATUS.md` +
   `docs/status/2026-07-17-error-correction-session.md` (docs PR). State the %
   collapse plainly. If exact-pending did NOT drop materially (<20 %), STOP —
   write the numbers + your op logs into the status doc and flag for human
   review; do not try creative remediation.

ACCEPTANCE: purge/scan run on sandbox only; before/after numbers in both docs;
PR merged; explicit statement of collapse % vs the 9,074 baseline.

## T04 — Prod deploy + prod DRY-RUNS + human gate  ⚠️ TOUCHES PROD

DEPENDS ON: T01, T02, T03 (sandbox proof). HUMAN-GATED at step 6.

1. Preconditions: `git -C <repo-root> pull --ff-only` on main; `git log
   --oneline -1` must include or postdate #1986's merge. All of #1972–#1986
   must be in main (`git log --oneline | head -30` sanity).
2. Deploy: run `make deploy` from the MAIN checkout (never a worktree — the
   LOCAL_ROOT footgun ships stale binaries). This is the standard target; run
   it verbatim.
3. Post-deploy verify: `curl -sk https://localhost:8484/api/v1/health` via ssh →
   version field must match `git describe --tags` of local main. If mismatch,
   STOP and report (stale-binary footgun).
4. Prod DRY-RUN (read-only, allowed): enqueue `maintenance.title-repair` `{}`
   (dry-run is the default; `apply` must be ABSENT). Poll to completion. Compare
   counters to the sandbox dry-run (expected ballpark: would_retitle≈555,
   errors=0 — prod drifted slightly since the snapshot; ±10 % is normal).
5. Prod DRY-RUN 2: `dedup.breakdown-backfill` `{}` — same comparison
   (would_backfill≈9,419 ballpark).
6. **STOP FOR HUMAN.** Present both dry-run counter comparisons via
   AskUserQuestion and get an explicit decision before ANY apply on prod
   (title-repair apply, breakdown-backfill apply, triage purge). A text reply
   is not sufficient per project rules; use the question tool.
7. After human approval: apply in the same order as the sandbox (title-repair →
   breakdown-backfill → triage → purge wave → purge-stale → full-scan),
   measuring at each step, then update `docs/dedup/STATUS.md` +
   `docs/operations/pending-prod-actions.md` (rows for CONS-10/PH-2 become
   DONE with numbers) + CHANGELOG + executive summary (this qualifies: it
   drains a long-standing data-quality backlog).

ACCEPTANCE: prod on today's build with version verified; both dry-runs recorded;
explicit human decision captured before apply; docs updated with prod numbers.

## T05 — Logging H/M batch (mechanical, parallelizable)

DEPENDS ON: nothing. Files are disjoint from T06–T11 except where noted.

Fix the remaining swallowed-error/no-progress findings. For EACH: add a Warn
(or Error) log with context + a counter surfaced in the op/scan summary; keep
behavior otherwise identical (log-and-continue stays log-and-continue).
Anchors verified at main @ a3cef740 — re-locate with grep if lines drifted
(function names given). One worktree/PR for the whole batch is fine.

- H1 `internal/dedup/engine.go:574` and `:805-811` (in the unified-scoring
  book-loading loops): `GetBookByID`/`GetBookByFileHash` err → bare `continue`.
  Add per-scan error counter + one summary Warn.
- H2 `internal/dedup/author.go:745-752,759` (all-authors series-map build):
  store errs silently degrade author-dedup. Counter + summary Warn.
- H3 `internal/reconcile/itunes_heal.go:366-371,379-381,476`: fpcalc, AcoustID
  `ac.Lookup`, and Whisper failures all `continue` — indistinguishable from
  "no match", silently inflating `ambiguous`. Add per-resolver failure counters
  into the existing RunItems label/summary + rate-limited Warn (first + every
  100th).
- H4 `internal/itunes/service/writeback_batcher.go:409`, `position_sync.go:162`,
  `importer.go:398`: `GetBookByID` err treated as nil-book skip. Log errors
  distinctly from nil-book skips + counter. (importer.go was touched by #1984 —
  rebase carefully, re-locate by function.)
- H8 `internal/plugins/maintenance/author.go:167-172`: `GetBookAuthors` err →
  continue in author-merge. Counter + Warn (data-affecting miscounts).
- H9 `internal/plugins/dedup/llm_review.go:35-49` + `internal/dedup/engine.go`
  (~3114-3124, `RunLLMReview` input-building loop): wire the op progress
  reporter (`StepN` every 100 pairs) so the 120-min op is not silent while
  building up to 10K pair inputs.
- M1 `internal/plugins/maintenance/transcribe_stats_accum.go:112`:
  `_ = a.sink.PutTranscribeStats(...)` → Warn on error (live-monitor staleness).
- M2 `internal/dedup/collectors_metadata.go:401` (GetBookByID err, no counter)
  and `:240-253` (four `_ = EnsureSingletonBookTag(...)`): Warn + counters.
- M3 `internal/dedup/engine.go:1247-1267`: four more swallowed
  `EnsureSingletonBookTag` calls — same treatment as M2.
- M7 `internal/plugins/maintenance/metadata.go:66-80` +
  `internal/server/server_maintenance_deps.go` (`MetadataUpgradeRun`, ~:124):
  add progress reporting between start and result (books processed / total,
  every 25 books — network-bound 30+ min shape).

ACCEPTANCE: every bullet has a log+counter, full packages
(`internal/dedup`, `internal/reconcile`, `internal/itunes/service`,
`internal/plugins/...`, `internal/server`) build + race-test green; PR merged;
TODO lines ticked.

## T06 — R-1: publish `op.terminal` SSE events

DEPENDS ON: nothing (registry PRs merged).

CONTEXT: The event contract (`internal/server/events/bus.go:15` — re-locate by
grepping `op.terminal`) and the frontend
(`web/src/services/api.ts` ~:501, `web/src/stores/useOperationsStore.ts` ~:352)
already define/handle `op.terminal`, but NO backend code publishes it —
completed ops linger as phantom "running" in the UI bell until refresh.

1. Find where terminal statuses are written: `internal/operations/registry/worker.go`
   (~:230 and ~:336 at the review baseline — grep for the terminal-status write
   calls). Note the registry does NOT import the SSE hub — find how other
   subsystems publish events (grep `Publish(` under `internal/server/events/`
   and its callers) and mirror the existing decoupling pattern (likely a
   callback/interface injected in `registry_wire.go`).
2. Publish `op.terminal` with op_id, def_id, final status on EVERY terminal
   transition: completed, failed, canceled, timeout, abandonment
   (interrupted_*), force-drop. The #1980 fixes added `notifyDepTerminal` —
   the same call sites are the correct hook points.
3. Test: extend the registry tests to assert the publisher callback fires for
   each terminal path (inject a recording stub). Frontend needs NO change.
4. Manual verify per the `verify` skill: run an op locally (`make run-api`),
   watch `/api/events` SSE with curl, confirm the event arrives.

ACCEPTANCE: event published on all six terminal paths, race-green registry
package, SSE verified live, PR merged, TODO R-1 ticked.

## T07 — R-6: AssignOrphanVGs worker pool + VG clobber guard

DEPENDS ON: nothing.

CONTEXT: `internal/reconcile/reconcile.go:1270-1327` (`AssignOrphanVGs`, called
from `internal/server/reconcile.go:178-185`) is a serial 2-DB-calls-per-book
whole-library loop (violates the repo concurrency mandate) and unconditionally
overwrites `VersionGroupID` on a re-hydrated book — clobbering a VG assigned
concurrently (e.g. by regroup apply or a merge).

1. Parallelize with the `registry.RunItems` pattern (see
   `internal/plugins/acoustid/backfill.go` for the canonical shape), workers =
   `runtime.NumCPU()`, partitioned by book ID (disjoint rows — no shared row).
2. Clobber guard: after re-fetch, if `VersionGroupID` is now non-empty and
   differs from the planned assignment, SKIP with a counter (someone else
   assigned it) — do not overwrite.
3. Fetch-full → mutate only VersionGroupID (+IsPrimaryVersion if the code
   already does) → UpdateBook. Never write a partial Book.
4. Progress log every 500 books + summary (assigned, skipped_already_grouped,
   skipped_concurrent_assignment, errors).
5. Tests: concurrent-assignment skip case + pool correctness (use the package's
   existing store fixtures).

ACCEPTANCE: pooled, guarded, logged; `internal/reconcile` + `internal/server`
race-green; PR merged; TODO R-6 ticked.

## T08 — Registry/scanner hygiene bundle: R-3, R-7, P-2

DEPENDS ON: nothing.

- R-3 `internal/operations/registry/reporter_db.go` (~:217-245) + `worker.go`
  (~:162): after abandonment the reporter's flushLoop exits (runCtx canceled)
  while the wedged Run keeps calling `Log` — `logBuf` grows unbounded and every
  line is lost. FIX: cap the buffer (e.g. 1,000 entries, drop-oldest with a
  dropped-counter) AND make post-abandonment `Log` calls cheap no-ops after a
  terminal flag is set (the goroutine is already monitored; its logs are
  unrecoverable by design — just stop the growth + count drops).
- R-7 `internal/scanner/service.go:73-83`: scan "checkpoint support" saves
  `ScanParams` that nothing loads (`LoadParams[ScanParams]` has zero callers)
  and `ClearState` runs unconditionally. DECIDE by reading the resume design in
  `internal/operations/registry/resume.go`: either wire actual resume (only if
  trivially supported by the existing checkpoint API) or DELETE the dead
  save/clear calls with a comment explaining why (scan restart-from-scratch is
  the intended ResumePolicy). Deleting is acceptable and likely correct.
- P-2 `internal/operations/registry/run_items.go:114`: parallel RunItems
  reports the item INDEX not a completion count — progress can jump backwards.
  FIX: atomic completed-counter; report its value. Cosmetic but user-visible.

ACCEPTANCE: all three addressed (R-7 may be a deletion), registry + scanner
packages race-green, PR merged, TODO lines ticked.

## T09 — C2: thread the op reporter through remux/transcode (+H7 same shape)

DEPENDS ON: nothing.

- C2: `internal/plugins/maintenance/backfill.go` (~:90-95 remux, ~:117-122
  transcode ops) log one line then call
  `p.deps.RemuxMalformedM4BFiles(ctx)` / transcode twin — the reporter is never
  passed down and the deps return void, so a 6-hour ffmpeg walk shows ONE
  op-log line and failures cannot fail the op. The real work is in
  `internal/remux/remux.go` (~:48-118) and `internal/remux/transcode.go`
  (~:48-141); the dep wrappers are `internal/server/malformed_m4b_wrappers.go:16-26`.
  FIX: change the dep signature to accept a progress callback
  (`func(processed, total int, msg string)`) and return `error`; wire the
  wrappers to the op reporter (`reporter.UpdateProgress` every 25 files:
  "Probing M4B: X/Y (remuxed=N failed=N)") and return the impl's error so the
  op can fail. Update the consumer-side interface + hand-written mocks.
- H7 (same pattern, do here): `internal/plugins/maintenance/backfill.go:40-45`
  → `internal/server/external_id_backfill.go:47-65` — error demoted to Warn,
  op completes unconditionally, no progress on the whole-library pagination in
  `internal/itunes/backfill.go:37`. Same fix shape: error return + progress
  callback.

ACCEPTANCE: both ops report progress and can fail; full packages race-green;
PR merged; TODO C2+H7 ticked.

## T10 — F6: legacy dedup.MergeBooks verification (verify FIRST, then fix)

DEPENDS ON: nothing. CAUTION: merge semantics — read thoroughly before editing.

CONTEXT: `internal/dedup/book_dedup.go:353-462` (legacy `MergeBooks`, reached
from `POST /audiobooks/merge` → `handlers/duplicates/handler.go:292` →
`duplicates_ops.go:151`) copies six iTunes fields first-win then HARD-deletes
loser books via `store.DeleteBook`. The modern path
(`internal/merge/service.go:184-224`) instead: collects PIDs →
`ReassignExternalIDs` → `EnqueueRemove` (ITL) → soft-delete.

1. VERIFY: does `DeleteBook` (PebbleStore) tombstone external-ID mappings or
   enqueue ITL removals? Trace with LSP. Record the answer in the PR body.
2. If the legacy path really loses external-ID mappings / ITL entries: the fix
   is to REROUTE the endpoint to `merge.Service.MergeBooks` (preferred — one
   merge path), keeping the legacy function only if tests depend on it.
   Check what behavior differences exist (field-copy semantics) and preserve
   the endpoint's response shape.
3. If verification shows DeleteBook already handles it: document that in a code
   comment at the legacy call site + close the TODO line as verified-safe.
4. Tests either way (reroute: endpoint-level test asserting soft-delete +
   reassignment happen).

ACCEPTANCE: verification answer recorded; reroute or documented-safe; race-green;
PR merged; TODO F6 ticked.

## T11 — Concurrency + duration hygiene: F7, R-9, R-8

DEPENDS ON: nothing.

- F7 `internal/plugins/dedup/quarantine_chapter_artifacts.go:121-162` (pass-2
  per-book `GetBookFiles`, serial) and apply loop `:180-199`: convert both to
  the `registry.RunItems` pool shape (workers = NumCPU, disjoint books). The
  apply path's soft-delete already uses the correct fetch-full-mutate pattern —
  keep it.
- R-9 `internal/itunes/service/path_repair.go:218-317`: main track loop is
  sequential per-track DB read/write. Parallelize with a bounded pool
  (8 workers), partition by track — BUT first check for shared mutable state in
  the loop (result counters → make atomic; any map → mutex or shard).
- R-8 `internal/scanner/chapter_consolidation.go:103-106`: mediainfo failure
  leaves duration 0; a group of ALL-unreadable files averages 0 < threshold and
  is consolidated as "short" when duration is actually UNKNOWN. FIX: track
  readable-count; if zero durations were readable, classify as unknown and SKIP
  consolidation for that group (counter + Warn), never treat unknown as short.

ACCEPTANCE: pools bounded + logged, R-8 unknown-guard tested, all packages
race-green, PR merged, TODO lines ticked.

## T12 — DevOps follow-ups

DEPENDS ON: nothing. Multiple independent commits, one PR.

1. IP scrub, remaining 8 scripts (found by #1983's worker):
   `scripts/test-tag-roundtrip.py`, `scripts/series_dedup.py`,
   `scripts/dedup_bench_apply.py`, `scripts/dedup_bench_crossval.py`,
   `scripts/dedup_bench_pass2.py`, `scripts/manage-whisper-server.py`,
   `scripts/setup-openssh-windows.ps1`, `scripts/setup-ssh-from-mac.sh`.
   Same pattern as #1983: env var (`ABK_API_URL` / `WHISPER_URL` /
   `ABK_GPU_HOST` as appropriate) with REQUIRED semantics, no internal-IP
   defaults. Self-check: `git diff` shows internal IPs ONLY on removed lines.
2. Op-stall alert: add a Prometheus example rule to
   `deploy/prometheus/alert-rules.yml` for "op active but items_processed rate
   == 0 for 30m" — FIRST check whether an op-progress metric exists (grep
   `prometheus` / `metrics` under `internal/`); if none exists, add the alert
   as a commented-out rule with a note naming the missing metric, and add a
   TODO.md line for the metric export (do NOT build the exporter in this task).
3. Coverage floor on PR gate: in `.github/workflows/ci.yml` (or the reusable
   minimal workflow it calls — read both), add the coverage-check-short step
   mirroring `make ci`'s gate (floor read from `.ci/coverage-floor.txt`).
   Keep actions SHA-pinned.
4. systemd unit dedupe: `deploy/audiobook-organizer.service` vs
   `deploy/systemd/audiobook-organizer.service` — diff them; keep the one
   `Makefile.local` ships (top-level), replace the other with a symlink or
   delete it + update any references (grep `deploy/systemd`).
5. Credential entropy: `scripts/manage-credentials.sh` — replace the 3-word+4-
   digit generator with `openssl rand -base64 15`; print the file PATH not the
   secret on create.

ACCEPTANCE: all five done; `bash -n`/`py_compile`/`actionlint` clean; no new
internal IPs; PR merged; TODO devops lines ticked.

## T13 — Docs truth-up after measurements

DEPENDS ON: T02+T03 (sandbox numbers), ideally T04 (prod numbers).

1. `docs/dedup/STATUS.md`: replace projections with measured numbers (triage
   populations, purge counts, final exact-pending), mark remediation steps 1–3
   as executed-on-sandbox (and on-prod if T04 completed).
2. `docs/operations/pending-prod-actions.md`: update the CONS-10/PH-2 rows with
   real state.
3. `docs/executive-summaries/2026-07-<current>-*`: if T04's prod apply ran, add
   the backlog-drain summary (plain language: "thousands of false duplicate
   suggestions removed; real duplicates now visible").
4. Verify no stale claims remain: grep docs/ for `9,074`/`9074` and `10,319` —
   every hit must be labeled as the 2026-07-17 baseline, not current state.

ACCEPTANCE: all three docs consistent with measured reality; PR merged.
