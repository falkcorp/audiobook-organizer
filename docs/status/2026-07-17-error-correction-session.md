<!-- file: docs/status/2026-07-17-error-correction-session.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3f8a1c62-9b4e-4d17-a2c5-7e0d94f6b813 -->
<!-- last-edited: 2026-07-18 -->

# 2026-07-17 error-correction session — status record

One-day multi-discipline review-and-fix session ("fable-error-correction").
Five parallel discipline reviews (docs, dedup, pipeline, logging, devops) produced
66 findings ([`docs/audits/2026-07-17-multi-discipline-review.md`](../audits/2026-07-17-multi-discipline-review.md)),
followed by two fix waves and a live verification run on the dedup sandbox.
This document is the authoritative "where things stand" record. The follow-on
work is specified as weak-model-proof task briefs in
[`docs/agent-tasks/error-correction-2026-07/TASKS.md`](../agent-tasks/error-correction-2026-07/TASKS.md).

## Merged (15 PRs, 2026-07-17)

| PR | Content | Findings closed |
|---|---|---|
| #1972 | GetAllBooksFullFrom index-key JSON abort (killed acoustid.backfill every startup) | (sandbox-found) |
| #1973 | Dismissed dedup candidates no longer resurrect to pending | F1 |
| #1975 | Docs consolidation: 194 files archived, live-only TODO, 4 consolidated docs, findings record | docs audit |
| #1976 | regroup-apply: soft-deleted-corpse filter, stale-primary demotion, cross-group refusal | F2 |
| #1977 | dedup store: status/entity index maintenance in bulk ops, 1M whole-backlog cap, suppression summary | F3 F4 F5 C7 |
| #1978 | NEW op `maintenance.title-repair` (CONS-17b re-derivation over stored books) | remediation step 1 |
| #1979 | project-context skill + agents + AI-REFERENCE + pebble-schema refresh; IP scrubs | plugin update |
| #1980 | registry: birth-hang watchdog, zombie-op terminal status + dedupe guard, queued-op cancel, strike/timeout hygiene | R-2 C-3 C-2 C-4 C-5 C-1 |
| #1981 | logging criticals: iTunes stub crons removed (all 3), AI retry logging, triage/split-scan/cleanup progress | C1 C6 C4 C5 C3 |
| #1982 | NEW op `dedup.breakdown-backfill` + title-leak triage relax (identical-normalized-title, non-iTunes) | remediation steps 2–3 |
| #1983 | devops: IP scrub (cmd + 3 scripts), worktree-proof pre-commit hook + secret/IP content scan, deploy template fix, embed_frontend smoke step | devops 1–5,8(part),9 |
| #1984 | iTunes importer: ITL fixes marked applied only when written, blocked-hash delete verified, multi-file rollback fails loud, error counters | DL-5 C-6 C-7 M5 M6 |
| #1985 | scanner: refcounted caches (concurrent-run safety), store-error-aware dup detection (skip, don't re-import), walk-error visibility, tail-hash Seek check | R-4 H5 R-5 H6 DL-4 M8 |
| #1986 (OPEN, CI) | organizer: rename rollback + stranded-temp resume, overwrite refusal, O_EXCL reflink | DL-1 DL-2 DL-3 M4 |

Also merged: CodeQL alert #1430 dismissed as false positive (pre-existing
`len(attrs)+1` allocation pattern in reporter_db.go; documented per SEC-AUDIT-11
practice).

**Prod: NOTHING deployed today.** Prod runs the pre-session binary.

## Sandbox verification (dedup sandbox, fresh reset, isolation gate PASSED)

Instance ran main @ `862bb1d1` (v0.217.8-rc.73-14). Baseline matched the
runbook exactly: **9,074 exact-pending / 10,319 total pending**.

Measured results:

| Step | Result |
|---|---|
| `maintenance.title-repair` dry-run | examined=48,369 · would_retitle=555 · skipped_single_file=34,390 · skipped_no_agreement=3,696 · skipped_provenance=4,418 · skipped_title_ok=1,268 · skipped_deleted=4,042 · mixed_dir=2,664 · errors=0 (158 s) |
| `maintenance.title-repair` apply | retitled=555, all other counters identical to dry-run, errors=0 (idempotency confirmed) |
| `dedup.purge-stale` after retitle | only **15** candidates removed → 9,059 exact-pending. **Key learning:** the shattered single-file chapter-books (the 76 % clique residue) each live in their **own directory**, so the same-dir stale rule cannot catch them, and title-repair correctly skips single-file books. The designed lever for that population is triage (with the #1982 relaxed title_leak class) → per-population purge — NOT retitle, NOT purge-stale. |
| `dedup.breakdown-backfill` dry-run | targets=10,062 · would_backfill=9,419 · skipped_has_breakdown=242 · zero_signal=643 · errors=0 |
| `dedup.breakdown-backfill` apply | was RUNNING at session stop (op `01KXSJHBDDP17AMR8WYKSTQH30`); completes server-side harmlessly (sandbox only). Verify + continue per task brief T02. |
| `maintenance.dedup-exact-triage` (classify) | purgeable=**7,891** (title_leak+stub) · keep=278 (genuine) · review=2,150 — the former `unknown=9,950` collapsed into real classes, exactly as predicted. |
| `maintenance.dedup-exact-triage {"apply":true}` (purge-apply, NEW op #2008) | dismissed 7,891 → exact-pending **9,074 → 1,183**; total-pending 10,319 → 2,428; 0 errors. |
| `dedup.purge-stale` + `dedup.full-scan` (final) | exact-pending **1,311** · total-pending 2,554 (full-scan embedding re-emission). **Net −85.5% exact-pending; the whole title-repair → backfill → relaxed-triage → purge chain is proven end-to-end, 0 errors.** Full 2026-07-18 re-run recorded in `docs/dedup/STATUS.md`. |

Sandbox operational notes (for the next operator):
- Instance may or may not still be running on :8485 (plain HTTP, NOT https).
- API key: `/tmp/abk-sandbox/.api-key` on the server. If the instance restarts,
  re-bootstrap: token at `/tmp/abk-sandbox/data/.bootstrap-token`, exchange via
  `POST /api/v1/auth/bootstrap` with body `{"token": "<token>", "name": "..."}`,
  key at `resp["data"]["api_key"]`.
- Launch detached ONLY: `setsid nohup bash sandbox-run.sh </dev/null >/dev/null 2>&1 & disown`
  (the script execs the server in the foreground; a dying ssh kills it otherwise).
- `run-op.py <def_id> '<json>'` enqueues fine but its poll endpoint 404s on this
  build — poll `GET /api/v1/operations/v2/<op_id>` yourself instead.
- Full runbook: falkcorp/infra-docs `docs/runbooks/dedup-sandbox.md` (private).

## Status (updated 2026-07-18)

The follow-on briefs T01–T13 ([`docs/agent-tasks/error-correction-2026-07/TASKS.md`](../agent-tasks/error-correction-2026-07/TASKS.md))
are **complete except the human-gated prod step**:

- **T01–T02, T05–T13 DONE.** The 2026-07-18 coordination wave merged all nine
  code fixes across PRs **#2001–#2010** (SSE terminal publisher R-1; registry
  hygiene R-3/R-7/P-2; orphan-VG pool R-6; logging H/M batch; remux/transcode +
  external-ID-backfill reporter threading C2/H7; legacy MergeBooks rerouted off
  hard-delete F6; concurrency + duration hygiene F7/R-9/R-8; devops follow-ups;
  the triage purge-apply op) plus docs cleanup #2011. Main builds + `go vet`
  clean.
- **T03 DONE** — the full remediation chain is proven end-to-end on the sandbox
  (see the table above and `docs/dedup/STATUS.md`): exact-pending **9,074 → 1,311
  (−85.5%)**, 7,891 junk candidates dismissed, 0 errors.
- **T04 — the only thing left — is HUMAN-GATED.** Nothing has been deployed to or
  run on production. Prod deploy + prod dry-runs + the apply decision await an
  explicit human go-ahead ([`docs/plans/DECISIONS-PENDING.md`](../plans/DECISIONS-PENDING.md);
  pending-prod-actions rows 1–2).
