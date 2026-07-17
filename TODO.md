<!-- file: TODO.md -->
<!-- version: 10.1.0 -->
<!-- guid: 8e7d5d79-394f-4c91-9c7c-fc4a3a4e84d2 -->
<!-- last-edited: 2026-07-17 -->

# Project TODO — live items only

The 2026-H1 TODO history (3,220 lines) is frozen verbatim at
[`docs/archive/todo-2026-H1.md`](docs/archive/todo-2026-H1.md).
Source anchors below (`H1:NNN`) cite line numbers of the **original** TODO.md;
in the frozen archive copy add 6 (banner block) to each number.

This file lists the 49 items confirmed ACTIVE by the 2026-07-17 docs audit, plus
the 2026-07-17 multi-discipline review-findings backlog (crash-recovery record,
last section).
Everything shipped or obsolete was dropped, including every stale 380K/384K/387K
dedup-candidate figure — the real backlog is **15,269 pending / 9,074
exact-pending** (see [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)).
Corrections applied per the audit: review-queue **PR-B2 is MERGED (#1953)**;
INIT completion is **~46/50 briefs** (not "35 remaining"); the managed
tool-lifecycle **IS built** (`internal/tools/*`, `/api/v1/tools`, Settings → Tools).

Companion docs:
- Run-on-prod queue: [`docs/operations/pending-prod-actions.md`](docs/operations/pending-prod-actions.md)
- Human-decision queue: [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md)
- Dedup state: [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)
- 2026-07-17 multi-discipline findings: [`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)

## Dedup (10)

1. **CONS-10 / INIT-2 T6 — prod drain/triage of the exact-candidate backlog** (H1:983;
   [plan](docs/plans/2026-07-10-dedup-pipeline-hardening.md)) — code merged, run NOT
   executed; operator-gated; validate on the dedup sandbox first (private runbook in
   falkcorp/infra-docs). Real backlog ~15,269 pending.
2. **PH-2 — run `maintenance.dedup-exact-triage` on prod + review populations; PH-2b
   per-population purge wave** (H1:916) — never blanket-purge; four residual
   populations (see `docs/dedup/STATUS.md`).
3. **REVIEW-band candidate producer for the review queue** (H1:35) — B2 fast-follow;
   no commit yet.
4. **Flip `review_apply_enabled` ON in prod** — apply path merged (#1953) but default
   OFF (6f2f7ce0); gated human decision (see DECISIONS-PENDING).
5. **C8 — auto-file issues per `not_dup` cluster** (H1:1332; INIT-10 T5) — deferred.
6. **INIT-1 T05 follow-up — per-kind confidence field in `DedupSignalConfig`** (H1:250)
   — makes the confidence round applyable.
7. **Async breakdown-refresh for bulk/cluster dismiss** (H1:1877) — per-pair synchronous
   refresh may need an async variant at scale (latency note).
8. **Omnibus detection + dedup** — spec-only
   ([`docs/superpowers/specs/`](docs/superpowers/specs/) 2026-05-31); not started.
9. **Regression tests for the 2 untested deluge hydrate sites** (H1:568) — optional.
10. **Hide system-sourced tags from the Browse-by-Tag cloud** (H1:433) — UX preference,
    not a bug.

## Identification / metadata (5)

11. **AI-enrichment tier for the ambiguous regroup pile** (H1:35) — B2 fast-follow;
    blocked on local Ollama capacity.
12. **Cover recovery fast-follow** (H1:35) — B2 fast-follow.
13. **Community audiobook fingerprint index (INIT-8)** — spec-only
    ([spec](docs/specs/2026-07-10-community-fingerprint-index-design.md));
    STOP-FOR-HUMAN brainstorm/review session required.
14. **Description fetch campaign — ~29,083 books without descriptions** (H1:790).
15. **LLM/embeddings backend-mode toggle** (extracted from the archived 2026-07-02
    status doc) — config enum + FE selector (disable-all / OpenAI-only / local-only /
    OpenAI+local-fallback) + model-download prompt; local target qwen2.5:7b-instruct
    on the GPU box. Status unverified — check before building.

## Pipeline (8)

16. **Library heavy-filter + non-title-sort returns 0 books** (H1:301-330) — CONFIRMED
    bug (BookSummary projection gap); fix hints recorded inline; was explicitly out of
    INIT-4 T06 scope.
17. **iTunes path-heal residuals** (H1:899-906) — 3,720 ambiguous / 5,349 not-found /
    4,734 doubled-path records still unresolved.
18. **AP-1b — physically co-locate survivor's files after Combine** (H1:936) — inside
    RootDir only.
19. **AP-3 duration-reextract ~721-book tail** (H1:949) — re-enqueue apply
    (see pending-prod-actions).
20. **AP-3b — consolidate the 3 duration extractors into one** (H1:954).
21. **CONS-18 Part 2 — file-tag duration write-back** (H1:1019; spec 2026-06-19 DRAFT)
    — config-gated; deferred until dedup re-scope settles.
22. **Torrent relocation INIT-5 T2–T7** ([plan](docs/plans/2026-07-10-torrent-relocation.md))
    — T1 shipped (18570a39); T2 = human-gated Deluge spike blocks T3–T7.
23. **Fingerprint UI verifications ×2** (H1:1383-1384) — [hold] verify the 14K
    false-positive purge is visible in dedup UI; book-sig coverage % renders.

## Workflow / ops (4)

24. **Workflow system WF-0/2/3/4/5 (INIT-6)** (H1:1128-1133;
    [plan](docs/plans/2026-07-10-workflow-system.md)) — STOP-FOR-HUMAN spec review;
    PR #1935 open. WF-6 closed NOT-DOING.
25. **PD-1 — subprocess isolation via parent-RPC bridge + MDA3 `Isolate:false` revert**
    (H1:1554-1561, 1435-1438; [spec](docs/specs/subprocess-isolation-rpc.md)) — [hold].
26. **INTERNAL-SERVER-PKG-STALL structural decision** (H1:849-877) — leak fixed;
    residual needs an owner decision: raise timeout / split package / migrate ~60 call
    sites to `newTestServer` (see DECISIONS-PENDING).
27. **Responses-API migration AI-RESP-A/B/E/F (INIT-7)** (H1:2596-2603) — [hold,
    do-not-start-without-greenlight]. AI-RESP-C/D closed.

## Logging / verification / security-ops (5)

28. **SLOG-PROD-VERIFY** (H1:2038; [runbook](docs/operations/slog-prod-verify.md)) —
    live prod smoke test of the op-activity chain.
29. **SLOG-W13 residual ~1,346 raw slog calls** (H1:2037) — remaining calls enumerated
    out-of-scope (no-ctx funcs, lifecycle, background); candidate to CLOSE with a
    scope note.
30. **SEC-AUDIT-11 — CodeQL bulk-dismissal rationales** (H1:2267) — GitHub-console
    action.
31. **PD-3 — post-deploy prod verification checklist** (H1:1568-1574;
    [checklist](docs/pd3-prod-verification.md)) — checklist exists, never filled in.
32. **I1 + I6 — prod pprof verification** (H1:1515, 1538) — measure chromem-lazy
    effect + heap re-audit; measurement only.

## Infra (3)

33. **REPO-SIZE-1 decision** ([plan](docs/plans/2026-07-10-repo-size-history-rewrite-plan.md),
    [package](docs/plans/2026-07-12-repo-size-targeted-purge-package.md)) —
    STOP-FOR-HUMAN; plan recommends Option (d) forward-only + GitHub Support gc.
34. **Execution-manifest human gates**
    ([manifest](docs/plans/2026-07-10-execution-manifest.md)) — the residual gated
    tasks: INIT-5 T2 spike sign-off, INIT-6 spec review, INIT-7 greenlight, INIT-8
    review, REPO-SIZE-1.
35. **Consultancy wave 4+ residuals** ([roadmap](docs/consultancy/00-ROADMAP.md)) —
    unverified; needs a close-out sweep against shipped work.

## UX (4)

36. **1.16 — resizable/sortable columns** (H1:2750) — remaining: dedup results,
    activity log, iTunes write-back preview, metadata review.
37. **1.17 — product rename/branding sweep** (H1:2751) — blocked on name decision.
38. **3.8 Plex-style media server API; 3.9 LLM series detection; 3.10 AI cover art**
    (H1:2772-2774) — all [hold].
39. **Fleet-tasks reconciliation** — real residuals = 030/031/036 (≡ 4.10/4.8/1.16);
    032–035 are stale-shipped and need closing in the fleet tracker.

## Other / close-out (10)

40. **4.8 — Store ISP sweep** (H1:2787) — ~38-file sweep + 18-file noop cleanup remain.
41. **4.10 — MergeService mock-store unit tests** (H1:2789) — partial.
42. **2026-05-01 re-audit block close-out pass** (H1:3137-3177) — TEST-2, DEP-1a-e,
    DEAD-1, CTX-4, LOG-5, R-9, R-10 mostly stale: DEP-1 0 non-test hits, DEP-1e moot
    (post-SQLite removal), PERF-1 OBSOLETE as scoped (Jul-16 truncation fix made
    whole-library ops deliberately unbounded). Needs a checkbox-level close-out.
43. **WaitForWarmup hazard note** (H1:3118) — latent create-then-read-memdb test
    hazard; document or fix.
44. **GFO-4 — graceful-file-ops sub-op phase tracking** — last open graceful-file-ops
    item.
45. **Performance items #1/#2/#6** (2026-04-14 set) — still open.
46. **Duration/filesize aggregation** — Book fields show snapshots instead of sums;
    likely stale (F5-T026 shipped) — verify then close.
47. **Library centralization backlog** — needs a brainstorming session; future work.
48. **Transcription quality filter** — ~40% of transcripts low-quality/unparsed; list
    endpoint omits transcription fields.
49. **iTunes heal Layer-6 re-trigger** (H1:897) — re-run after path-heal residuals
    shrink (see pending-prod-actions).

## 2026-07-17 multi-discipline review — open findings

Crash-recovery record for the four 2026-07-17 discipline reviews (full detail:
[`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md);
anchors verified at main @ a3cef740). Dedup **F1 is already FIXED** by PR #1973
(dismissed candidates no longer resurrect to pending).

### Fixes in flight (worktrees, unpushed — verify/land before re-fixing)

- `fix/regroup-apply-integrity` — dedup **F2**: ApplyVersionGroup/ApplyMultidisc double-primary, stranded-group, and soft-deleted-corpse bugs (`internal/plugins/maintenance/regroup_apply.go:102-170`). Must land before `review_apply_enabled` flips ON.
- `fix/dedup-index-maintenance` — dedup **F3** (MarkCandidatesAsMergedForEntity status-index bypass, `embedding_store.go:1194-1256`), **F4** (bulk-delete index-row leaks, `:1293-1303`/`:1367-1377`), **F5** (Rescore 100K truncation, `engine.go:2816-2819`) + logging **C7** (Debug-only destructive suppression deletes, `engine.go:591-600`).
- `fix/registry-reliability` — pipeline **R-2** (watchdog blind to never-progressed ops), **C-3** (abandoned op = zombie running row + ConcurrencyKey dedupe swallow), **C-2** (force-drop leaks run handle), **C-4** (uncheckpointed-strike constant + strike-row spam), **C-5** (timeout recorded as canceled; dep scheduler not notified), **C-1** (Cancel no-op for queued ops) — all `internal/operations/registry/`.
- `fix/logging-observability-criticals` — logging **C1** (5 silent iTunes stub ops, 2 on cron), **C6** (silent AI retries, `internal/ai/retry.go:66-90`), **C4** (DedupTriageExactPending zero logging + swallowed errors, `server_maintenance_deps.go:260-351`), **C5** (split-book-scan silent detection pass), **C3** (movement-atom-cleanup no heartbeat + swallowed done-flag write).
- `feat/title-repair-op` — title-leak remediation step 1 (repair leaked/junk titles so exact-title cliques dissolve; prerequisite for the triage → rescan → drain sequence in `docs/dedup/STATUS.md`).

### Remaining backlog — pipeline / operations

- [DATA-LOSS] `internal/organizer/pipeline.go:194-203` — DL-1: RenameFiles phase-2 failure strands files at `.tmp-rename`, no rollback; `result.Succeeded` discarded so moved files never get DB path updates.
- [DATA-LOSS] `internal/itunes/service/importer.go:502-521` — DL-5: deferred ITL location fixes marked applied for ALL pending rows when only a filtered subset was written; RenameITLFile error discarded; dropped fixes never retried.
- [CORRECTNESS] `internal/itunes/service/importer.go:417-425` — C-6: blocked-hash soft-delete UpdateBook return ignored; counters/logs claim a delete that may not have happened.
- [RELIABILITY] `internal/operations/registry` (`bus.go:15`; terminal writes `worker.go:230,336`) — R-1: `op.terminal` SSE is in the FE/BE contract but has zero backend publishers; completed ops linger as phantom "running" in the UI bell.
- [RELIABILITY] `internal/scanner/scanner.go:161-231` vs `service.go:161-168` — R-4: package-singleton scan/works caches; concurrent library.scan + library.import → first finisher nils caches under the other (incremental skip off, O(N²) works lookup).
- [RELIABILITY] `internal/scanner/scanner.go:390-396,370-373,433-436` — R-5: WalkDir/ReadDir/stat failures silently drop whole subtrees, zero logging.
- [RELIABILITY] `internal/reconcile/reconcile.go:1270-1327` — R-6: AssignOrphanVGs serial unpooled whole-library loop + unconditional VersionGroupID overwrite clobbers concurrent VG assignments.
- [RELIABILITY] `internal/scanner/service.go:73-83` — R-7: scan "checkpoint support" saves ScanParams no code ever loads; ClearState unconditional; crash mid-scan resumes nothing.
- [DATA-LOSS, plausible] `internal/organizer/service.go:454`, `rename.go:392,450,480` — DL-2: wired move paths have no target-exists check; os.Rename silently replaces on path collision (safe `MoveBookFile` at `move.go:42-45` has zero production callers).
- [DATA-LOSS, plausible] `internal/organizer/reflink_unix.go:26` — DL-3: os.Create truncates an existing destination; stat→create TOCTOU under the 8-worker organize pool.
- [DATA-LOSS, plausible] `internal/scanner/scanner.go:2102` — DL-4: `file.Seek(-chunkSize, io.SeekEnd)` return discarded → wrong-window hash poisons dedup (sibling `process_file.go:123-125` checks).
- [CORRECTNESS, plausible] `internal/itunes/service/importer.go:1199-1229` — C-7: multi-file rollback reverts only Book.FilePath; committed per-file UpdateBookFile writes not reverted → DB/disk inconsistent.
- [RELIABILITY, plausible] `internal/scanner/chapter_consolidation.go:103-106` — R-8: mediainfo failure leaves duration 0; all-unreadable group consolidated as "short" when duration is unknown.
- [PERF] `internal/itunes/service/path_repair.go:218-317` — R-9: main track loop fully sequential per-track DB read/write (concurrency-mandate shape; does report every 500).
- [PERF] `internal/operations/registry/run_items.go:114` — P-2: parallel RunItems reports item index not completion count; progress can jump backwards (cosmetic).
- [RELIABILITY] `reporter_db.go:217-245` + `worker.go:162` — R-3: after abandonment the reporter flushLoop exits while the wedged Run keeps logging; logBuf grows unbounded, lines lost (land with `fix/registry-reliability` or after).

### Remaining backlog — dedup

- [MED, verify] `internal/dedup/book_dedup.go:353-462` — F6: legacy dedup.MergeBooks (POST /audiobooks/merge) copies six iTunes fields first-win then HARD-deletes losers — no external-ID reassignment, no ITL removal, no recovery window; verify whether DeleteBook tombstones mappings, then fix or retire the path in favor of merge.Service.MergeBooks.
- [LOW] `internal/plugins/dedup/quarantine_chapter_artifacts.go:121-162,180-199` — F7: serial whole-subset loops in a mandated `registry.RunItems` shape (no wipe risk; hygiene).
- [OP] ScoreBreakdown-backfill op — populate breakdowns on labeled/pending pairs (remediation step 2 in `docs/dedup/STATUS.md`); required before the next calibration round.
- [OP] Relax the title-leak triage precondition once `feat/title-repair-op` lands, so triage classifies against repaired titles.
- [OP] Post-title-repair rescan/drain sequence — full-scan/rescore, then PH-2b per-population purge (pending-prod-actions rows 1–2; human-gated).

### Remaining backlog — logging (H/M batch)

- [HIGH] `internal/dedup/engine.go:574,805-811` — H1: GetBookByID/GetBookByFileHash err → bare continue; failing store scores nothing silently.
- [HIGH] `internal/dedup/author.go:745-752,759` — H2: store errs silently degrade the all-authors series map (author-dedup).
- [HIGH] `internal/reconcile/itunes_heal.go:366-371,379-381,476` — H3: fpcalc/AcoustID/Whisper failures indistinguishable from "no match"; inflates `ambiguous`.
- [HIGH] `internal/itunes/service/writeback_batcher.go:409` (+ `position_sync.go:162`, `importer.go:398`) — H4: GetBookByID err treated as legit skip; flush-time store errors silently drop iTunes writes.
- [HIGH] `internal/scanner/scanner.go:1844-1847,614,867,733` — H5: dup-detection hash-lookup errs → possible silent re-import; swallowed UpdateScanCache/IncrScanFailCount.
- [HIGH] `internal/scanner/service.go:242` — H6: count-phase WalkDir errors ignored; undercounts the scan denominator.
- [HIGH] `internal/plugins/maintenance/backfill.go:40-45` → `internal/server/external_id_backfill.go:47-65` — H7: error demoted to Warn, op logs "complete" unconditionally; no progress on whole-library pagination (`internal/itunes/backfill.go:37`).
- [HIGH] `internal/plugins/maintenance/author.go:167-172` — H8: GetBookAuthors err → continue in the author-merge path (data-affecting miscounts).
- [HIGH] `internal/plugins/dedup/llm_review.go:35-49` + `internal/dedup/engine.go:3114-3124` — H9: no op-log progress while building up to 10K pair inputs on a 120-min op; wire NewProgress/StepN.
- [MED] `internal/plugins/maintenance/transcribe_stats_accum.go:112` — M1: swallowed PutTranscribeStats; live-monitor key can go stale silently.
- [MED] `internal/dedup/collectors_metadata.go:401,240-253` — M2: GetBookByID err no counter; 4 swallowed EnsureSingletonBookTag (evidence tags dropped).
- [MED] `internal/dedup/engine.go:1247-1267` — M3: four more swallowed EnsureSingletonBookTag calls.
- [MED] `internal/organizer/pipeline.go:187` — M4: swallowed os.Rename in the ROLLBACK path; failed rollback strands a file at temp path with no log.
- [MED] `internal/itunes/service/importer.go:316,358-360,1648` — M5: swallowed CreateExternalIDMapping/SetBookAuthors; DecodeLocation err uncounted.
- [MED] `internal/itunes/service/path_repair.go:244` — M6: undecodable locations skipped without a count in the repair summary.
- [MED] `internal/plugins/maintenance/metadata.go:66-80` + `server_maintenance_deps.go:124` — M7: MetadataUpgradeRun network-bound 30+ min with no progress between start and result.
- [MED] `internal/scanner/scanner.go:402` — M8: swallowed registerDirectory (watcher coverage failures invisible).
- [CRIT, partial] `internal/plugins/maintenance/backfill.go:90-95,117-122` → `internal/remux/{remux,transcode}.go` — C2: remux/transcode ops (up to 6 h ffmpeg) never pass the reporter down; deps return void so failures can't fail the op (NOT covered by `fix/logging-observability-criticals`).

### Remaining backlog — devops (1–12)

- [CRIT] Internal-IP scrub — internal fleet addresses in 61 tracked files (code: `cmd/dedup_bench.go:74`, `tools/cmd/reconcile-paths/main.go`, `scripts/transcribe_monitor.py`, `scripts/dedup_bench_submit.py`, `scripts/setup-winrm-windows.ps1`; plus `docs/system/runbooks.md`, `agents/pii-scanner.md`, `skills/project-context/SKILL.md`, many docs). Env-var the script/code URLs now; docs history rides on the REPO-SIZE-1 decision; add IP grep to hook + CI.
- [HIGH] `Makefile.local` deploy — no origin-freshness pre-flight, no -dirty refusal, no post-restart version verify (~10 lines; stale-binary footgun).
- [HIGH] `Makefile.local.example:30-52` — template deploy cross-compiles `-tags embed_frontend` without web-build; omits fts5/native_taglib/static-link; ships binary only.
- [HIGH] `scripts/setup-git-hooks.sh:8` — pre-commit hook silently no-ops when installed from a linked worktree (`--git-dir` → `--git-common-dir`, 1 line).
- [MED] CI — `embed_frontend` never built pre-merge; add one step to binary-smoke.
- [MED] Observability — no "op went silent" alert (op-progress metric + rate==0-while-active-30m rule); AI-backend gauge startup-only; Prometheus deployment on prod unverified.
- [MED] CI — 30% coverage floor (`.ci/coverage-floor.txt`) not enforced on the PR path.
- [MED] Sandbox tooling — zero in repo; build public-safe `scripts/op_verify.py` (`--base-url --token-file --op DEF_ID --dry-run`) and de-hardcode the 2 prod-URL scripts (`ABK_API_URL`).
- [LOW] Pre-commit hook — add staged-content scan for API-key + internal-IP patterns; CI backstop (bypassable with --no-verify today).
- [LOW] `scripts/manage-credentials.sh` — ~27-bit passwords, secrets echoed to stdout; use `openssl rand -base64 15`, print path not secret.
- [LOW] Duplicate systemd unit files `deploy/` vs `deploy/systemd/` — drift risk; pick one source of truth.
- [LOW] Flaky "Go Tests (short, race)" stalls — no automation; reproduce-in-isolation guidance only.
