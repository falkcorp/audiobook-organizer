<!-- file: docs/CURRENT-STATUS.md -->
<!-- version: 1.2.0 -->
<!-- guid: 4a37ae70-9dc8-48e1-9b39-5ab3aa5cc05e -->
<!-- last-edited: 2026-08-25 -->

# Current Status: Safe Scan, Import, and Metadata Readiness

This is the current operational source of truth as of 2026-08-25.  It replaces
using `TODO.md`, `todo.d/`, old handoffs, or the burndown package as a single
readiness signal.  Evidence reports are linked rather than copied when their
full detail matters.

## Answer to the main question

**Not until the currently running scan is terminal and production chapter
consolidation is restored to 10; then use a canary first.  Also not yet for the
desired “queue metadata until the LLM recovers” behavior.**

The previously observed deployment ran `5e95fad6`, and a prior full scan
completed `215381/215381` items with no recorded operation errors.  The newest
forensics handoff supersedes the earlier configuration snapshot: a scan is now
active and production currently has `chapter_consolidation_threshold_min=0`.
That value disables the fallback that groups album-less multi-file audiobooks,
causing one Book per track and preventing the intended multi-file BookFile
creation path.  Do not start another full scan in this state.

After the active scan is terminal, restore the threshold using the safe,
authenticated merge-style configuration update established in the forensics
handoff.  Then a deliberately new audiobook can prove the actual production
path before another full scan.  It is not enough to claim metadata will be
applied automatically while the LLM host is down.

## User decisions recorded in this audit

1. Do not make starting a local LLM on the older, constrained GPU a prerequisite.
2. When the LLM is unavailable, ingestion must persist and metadata work must
   wait durably rather than make scanning fail or require another scan.
3. Keep and use the organize flag.  It must be explicit in the canary and in
   the full-run plan because enabling it moves files.

## What is now verified

| Capability | Evidence | Status |
|---|---|---|
| Service deployment | Production health/status was checked during this audit; it reports deployed current main and no active operations. | Ready |
| Full filesystem scan | A `library.scan` operation completed all 215,381 items and recorded no operation error. | Previously succeeds; repeatable with canary gate |
| New scanned single-file book gets `BookFile` | `34e679e48`; scan path calls `createSingleFileBookFile`. | Code fixed and deployed |
| Direct import gets `BookFile` | `02cb13ed1`; importer persists the row. | Code fixed and deployed |
| Chapter grouping is enabled | User-confirmed newest forensics reports live `chapter_consolidation_threshold_min=0`; zero disables consolidation. | Blocked until active scan is terminal and value is restored to 10 |
| Organize flag exists and the HTTP import path honors it | Current importer/server code has an explicit `organize` setting.  Production `auto_organize` is presently false. | Available; activation is a deliberate operational choice |

The historical diagnosis in [the WebArchive review](audits/current-status-evidence/2026-08-25-scan-readiness-webarchive.md)
is still useful, but its old overall “not yet” answer has a narrower current
cause: the two BookFile fixes exist, but the live consolidation setting is
currently disabling the grouping they rely on for album-less multi-file books.

The earlier root-cause investigation is preserved in the
[imported scan-forensics handoff](audits/current-status-evidence/2026-08-25-imported-scan-forensics-handoff.md).
It explains why the old value of zero caused 12,525 no-file books in its
time-bound census, what repair remains, and why those figures must be
re-measured before acting on them.

## The remaining blocker: metadata durability

The current server does queue AI filename parsing as durable `library.ai-parse`
operation records, separate from `library.scan`.  That is a substantial
improvement: scan candidates carry a durable book ID, a cancelled scan can
still enqueue them, and the scan does not wait for LLM calls.

However, this is **not yet a durable wait-until-healthy queue**.  When the
configured LLM cannot answer, the queued operation stops early and returns an
error.  Its `ResumeDrop` policy relies on a later scan re-nominating unstamped
books; it does not retain/retry the failed metadata batch on its own.  This
does not threaten creation of Book/BookFile records, but it does not meet the
user's chosen metadata behavior.

Required remediation, before treating automatic metadata as ready:

- Change `library.ai-parse` failure handling so an unreachable LLM leaves work
  durably pending with bounded retry/backoff and clear operator visibility;
  verify restart recovery and ensure a permanently invalid configuration does
  not hot-loop.  No valid existing task brief was found; create a scoped task
  from this requirement before implementation.

## Safe canary, then full scan

Do not start the full scan until this canary has been observed.  The canary is
a normal database/filesystem mutation, so it requires an operator to choose the
test audiobook and invoke the scan.

1. Wait for the current scan to become terminal.  Restore chapter consolidation
   to 10 through the authenticated merge-style configuration update; verify the
   stored live value before proceeding.  Do not change it mid-scan.
2. Put one new, uniquely identifiable, well-tagged audiobook in a normal
   watched/import path.  Record its original path, file count, and tags.
3. Run the narrowest applicable scan/import path with `organize` explicitly
   chosen.  For the desired filing behavior, choose it on; first confirm the
   resulting destination is acceptable before scaling up.
4. Confirm exactly the expected Book record and `BookFile` record(s) exist,
   their paths resolve to real audio, and no duplicate/dead rows were created.
5. Confirm the scan operation finishes successfully.  With the LLM unavailable,
   confirm the book remains imported/playable and the metadata candidate is
   visibly pending rather than silently stamped/forgotten.  This step is
   expected to expose the durability gap above until it is fixed.
6. Confirm metadata from embedded tags/providers is retained and, when an LLM
   is available later, that queued filename metadata is applied without a new
   scan.
7. Only after steps 1–5 pass, run the full scan; monitor operation completion,
   new Book/BookFile counts, and failed/interrupted operation records.

## Valid outstanding work

| Priority | Item | Action |
|---|---|---|
| P0 | Terminal scan, then restore consolidation to 10 | The current scan must finish before the safe configuration update; verify the live value before the canary. |
| P0 | Fresh canary proof | Perform the seven checks above before a production-wide scan. |
| P0 | Durable LLM-unavailable metadata queue | Scope and implement the requirement in the preceding section. |
| P1 | Organize rollout | Use the explicit flag in the canary, then decide whether to change the production default from false. |
| P1 | Existing library repairs | The valid per-batch BookFile dedup/path/history and chapter backfill tasks remain in [TODO/CHANGELOG audit evidence](audits/current-status-evidence/2026-08-25-todo-changelog.md); revalidate before dispatch. |
| P2 | TODO fragment reconciliation | The assembler currently reports 16 pending fragments.  Curate rather than blindly collect them; see [fragment evidence](audits/current-status-evidence/2026-08-25-todo-fragments.md). |
| P2 | Dependabot PR #2925 | Code correction is sound and CI is mostly green, but workflow lint is failing on unrelated-looking existing style findings; see [PR evidence](audits/current-status-evidence/2026-08-25-open-prs.md). |

## Evidence index and freshness rules

- [TODO/CHANGELOG audit](audits/current-status-evidence/2026-08-25-todo-changelog.md)
- [TODO fragment audit](audits/current-status-evidence/2026-08-25-todo-fragments.md)
- [Open PR audit](audits/current-status-evidence/2026-08-25-open-prs.md)
- [WebArchive review](audits/current-status-evidence/2026-08-25-scan-readiness-webarchive.md)
- [Burndown and handoff triage](audits/current-status-evidence/2026-08-25-burndown-and-handoffs.md)
- [Imported scan-forensics handoff](audits/current-status-evidence/2026-08-25-imported-scan-forensics-handoff.md)

All `docs/handoffs/` material is older than the requested 1.5-day freshness
limit.  The generated task burndown is dated 2026-08-21 to 2026-08-23 and is
not a live queue: validate any individual brief against current main and live
configuration before using it.
