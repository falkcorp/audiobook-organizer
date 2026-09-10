<!-- file: docs/operations/pending-prod-actions.md -->
<!-- version: 1.2.0 -->
<!-- guid: 84079e70-633b-4bf5-849a-7b6671f6de61 -->
<!-- last-edited: 2026-09-10 -->

# Pending Prod Actions — the run-on-prod queue

Every outstanding action whose remaining work is a **run against the production
server** (code already merged), consolidated from the 2026-07-17 docs audit.
Two gating classes:

- **operator-run** — an operator can execute when convenient; dry-run first where
  the op supports it.
- **human-decision** — blocked on an explicit human approval recorded via a real
  AskUserQuestion decision (see
  [`docs/plans/DECISIONS-PENDING.md`](../plans/DECISIONS-PENDING.md)); a text
  reply is not a gate release.

Generic op trigger: `POST /api/v1/operations/v2 {"def_id": "<DEF_ID>", "params": {...}}`
with `Authorization: Bearer <api key>`. High-risk dedup actions should be
validated on the dedup sandbox first (private runbook in falkcorp/infra-docs).

| # | Action | What / why | Op / command shape | Gating | Source |
|---|--------|------------|--------------------|--------|--------|
| ~~1~~ ✅ **PH-2 exact-triage (DONE ON PROD 2026-07-18)** | Run the triage op that classifies the exact-pending dedup backlog into its four populations, then review the population report before any purge | def-id `maintenance.dedup-exact-triage` (read-only classify) → review report → PH-2b per-population purge wave (**never blanket-purge**) | ✅ done (operator-run triage + human-signed-off apply) | **DONE ON PROD 2026-07-18**, prod journal: `scanned=10319 purgeable=7891 keep=278 review=2150 lookup_errors=0 apply=true dismissed=7891 dismiss_errors=0`, `outcome=completed` — exact-pending **9,074 (2026-07-17 baseline) → 1,311 (2026-07-18)**, −85.5%, 0 errors. Journal transcribed at [`docs/audits/2026-08-11-docs-inventory.md`](../audits/2026-08-11-docs-inventory.md) §1.3.1; detail in [`docs/dedup/STATUS.md`](../dedup/STATUS.md); TODO #2 |
| ~~2~~ ✅ **CONS-10 / INIT-2 T6 backlog drain (DONE ON PROD 2026-07-18)** | Drain/triage the 15,269-pending candidate backlog (2026-07-17 baseline) now that title-repair + rescore code is merged | **EXECUTED ON PROD 2026-07-18** (deploy `v0.217.8-rc.80-2-g0b474707` → dry-run matched the sandbox on steps 1–2 → apply under human sign-off): exact-pending **9,074 → 1,311**, both 2026-07-17/18 figures. Ops #1978/#1982/#2008 | ✅ done | TODO #1 |
| 2b | **Exact-pending backlog has re-accumulated** | The 2026-07-18 drain held for weeks, not months: exact-pending measured **5,947 on 2026-08-12** (from 1,311), ~4.5× regrowth in 3.5 weeks. A repeat drain would only re-drain — the candidate **source** needs the fix. **Nothing has been measured since 2026-08-12** | measure first (`GET /api/v1/dedup/stats`), then fix the emitter; do **not** queue another blanket triage-apply | operator-run (measure) → human-decision (any further purge) | [`docs/audits/2026-08-11-docs-inventory.md`](../audits/2026-08-11-docs-inventory.md) §1.3.3 |
| 3 | **Duration-reextract tail** | Re-enqueue the duration-reextract apply for the ~721-book tail that the v3 run left behind | duration-reextract op re-enqueue (see archived design [`2026-06-21-duration-reextract-v3-design.md`](../archive/2026-07-consolidation/specs/2026-06-21-duration-reextract-v3-design.md)); dry-run supported | operator-run | TODO #19 |
| 4 | **iTunes heal Layer-6 re-trigger** | Re-run the iTunes path-heal op so Layer-6 (the last-resort matching layer) reprocesses the residuals (3,720 ambiguous / 5,349 not-found / 4,734 doubled-path) | re-enqueue the iTunes path-heal op after the residual pools shrink | operator-run | TODO #17, #49 |
| 5 | **SLOG-PROD-VERIFY** | Live smoke test that the op-activity logging chain (start/progress/complete + activity tags) actually lands in prod journald/op-log | runbook: [`docs/operations/slog-prod-verify.md`](slog-prod-verify.md) | operator-run | TODO #28 |
| 6 | **PD-3 post-deploy checklist** | The post-deploy verification checklist exists but has never been filled in against a live deploy | checklist: [`docs/pd3-prod-verification.md`](../pd3-prod-verification.md) — run after next `make deploy` | operator-run | TODO #31 |
| 7 | **I1 + I6 prod pprof** | Measurement-only: verify chromem-lazy memory effect and re-audit heap breakdown on prod | `go tool pprof` against the prod pprof endpoint; compare vs [`docs/perf-audit-2026-05-29-heap-breakdown.md`](../perf-audit-2026-05-29-heap-breakdown.md) | operator-run | TODO #32 |
| 8 | **Flip `review_apply_enabled`** | Review-queue apply path merged (#1953) but globally OFF by default (6f2f7ce0); enabling turns dry-run holds into real prod mutations | settings flip (config/Settings UI) — only after a recorded approval | human-decision | TODO #4; [`docs/plans/2026-07-13-review-queue-and-regroup.md`](../plans/2026-07-13-review-queue-and-regroup.md) |
| 9 | **SEC-AUDIT-11 CodeQL dismissals** | Record rationales for the bulk-dismissed CodeQL alerts; console action, not code | GitHub Security console → dismiss-with-rationale per alert group | operator-run (GitHub console) | TODO #30 |
| 10 | **`maintenance.dedupe-book-file-rows` apply** | Collapse books holding more than one `book_file` row for the same `file_path`. **OPEN OWNER DECISION:** deleting duplicate rows conflicts with the standing *never delete rows in any repair; REPOINT* rule. The op deletes; it does not repoint. Nothing below releases that gate | `POST /api/v1/operations/v2 {"def_id": "maintenance.dedupe-book-file-rows", "params": {"apply": true}}` — run `{"apply": false}` first and review the TSV/summary | human-decision | DUPROW-3; see §"Duplicate book_file rows" below |

## Duplicate `book_file` rows — row 10 detail

**Mitigation now in place (DUPROW-3).** Before this change the op deleted rows
with no record of what it removed. It now:

- writes one undo-ledger row per deleted `book_file` **before** the delete
  commits — `change_type` `book_file_delete`, `field_name` = the deleted row id,
  `old_value` = the **entire row** as JSON, so the deleted row can be
  reconstructed field-for-field;
- **fails closed**: if the ledger write (or the JSON encode) fails, that book's
  rows are left intact and the book is counted as failed. An unreplayable
  deletion never happens; a surviving duplicate costs one more run of an
  idempotent op;
- **refuses to apply while `library.scan` is queued or running**, and refuses if
  the operation queue cannot be read at all. A dry run is read-only and stays
  allowed during a scan.

Replay reads: `GetOperationChanges(<op id>)` for the whole run, or
`GetBookChanges(<book id>)` per book.

**⚠️ Recoverable is not the same as undoable.** `internal/undo`'s `revertChange`
switch has no `book_file_delete` case, so pressing undo on a dedupe run reports
`unknown change_type` once per journaled row and restores nothing. The ledger
holds everything needed to rebuild each row; the reader does not exist yet. The
replay tool is filed as **DUPROW-4**. Until it lands, a rollback means reading
`old_value` out of the ledger and re-inserting by hand.

**⚠️ OPEN VERIFICATION ITEM — do not assume either way.** The op's own source
comments describe a production run that already collapsed rows. Verbatim, from
`internal/plugins/maintenance/dedupe_book_file_rows.go`:

> "The first full production run was killed at book 19/194 by exactly that"

> "This was originally written up as an observed loss on 'The Trapped Mind
> Project', which read 0.00h after its 130 rows were collapsed."

> "A later full-library dry run confirmed it — 'would salvage fields on 0
> keepers' across all 194 books."

> "⚠️ Operational note, learned the hard way on the first canary: the corrected
> totals are NOT visible until memdb catches up."

`TODO.md` records the same run as finished, with figures (`grep -n 'duplicate
.book_file. rows are gone library-wide' TODO.md`):

> "**DONE 2026-08-04 — duplicate `book_file` rows are gone library-wide.** […]
> Total across all runs: **204 books, 3,239 redundant rows deleted, 0 failures**"

What is verifiable from the repository, and what is not:

- **Verifiable:** the code path had **no** `CreateOperationChange` call before
  this change (`grep -rn "CreateOperationChange" internal/plugins/` returned only
  the interface declaration). Combined with the `TODO.md` entry above, the
  repository's own record says roughly **3,239 rows across 204 books were deleted
  on production around 2026-08-04 with no undo-ledger row**, and therefore with
  no replay path. Journaling starts from this change forward; it is **not**
  retroactive.
- **Not verifiable from here (no prod access):** whether those figures match what
  the live operation history actually shows, and whether any further apply has
  run since. That needs the production history for
  `def_id=maintenance.dedupe-book-file-rows`. Treat the numbers above as the
  repository's claim, not as a confirmed prod census.
- **When checking that history, read `scan_capped`.** `/operations/timeline` is a
  bounded scan, not a census — a raw count from it will under-report. Confirm
  `truncated` and `scan_capped` are both false *and* `matched < limit` before
  treating the window as complete.
- **The rows are not necessarily lost.** The op deletes *duplicate* rows for a
  path the surviving keeper still holds, and the salvage-before-delete step
  merges any field the keeper lacked, so the deletion is by design
  content-preserving. The gap is that this cannot be *proved* per row after the
  fact, and could not be reversed if the keeper choice were ever wrong.

**Pre-run checklist (row 10):**

1. No `library.scan` queued or running (the op now refuses, but check first so
   the refusal is not the surprise).
2. Run `{"apply": false}` on the **current** dataset and review the summary — the
   stale-dry-run rule below applies.
3. Record the owner decision on the delete-vs-repoint conflict before any apply.
4. After an apply, expect corrected totals to lag until memdb refreshes; a
   re-run dry run should report `would delete 0`.

## Standing rules

- Dry-run → apply transitions on prod data always require a fresh dry-run on the
  **current** dataset — stale dry-run reports (pre-merge counts) do not carry over.
- Record each completed run in CHANGELOG.md and tick the matching TODO item; if
  the run fixes/changes user-visible data at scale, check the executive-summary
  criteria (`docs/process/executive-summaries.md`).
- Dedup mutations (rows 1, 2, 8): sandbox-first, then diff the prod dry-run
  against the sandbox dry-run before the human gate.
- ⚠️ **Rows 1/2 did not fully meet that rule, and the gap is still open.** The
  sandbox covered steps 1–2 and the triage *classify* pass only (purgeable **7,878**
  of **10,304** scanned); the purge-**apply** was never mirrored there, so the prod
  apply had no replica rehearsal to diff against. That parity run is **T03**
  (`grep -n '\*\*T03\*\*' TODO.md` → still `- [ ]`), a **sandbox** action rather than
  a prod one, which is why it is not a numbered row above. Note that the sandbox's
  7,878-of-10,304 and prod's 7,891-of-10,319 are **two populations, not a drift** —
  the replica held 15 fewer candidates. Detail:
  [`docs/dedup/STATUS.md`](../dedup/STATUS.md) sandbox section.
