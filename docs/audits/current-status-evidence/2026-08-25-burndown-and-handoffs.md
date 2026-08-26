<!-- file: docs/audits/current-status-evidence/2026-08-25-burndown-and-handoffs.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1f0a4416-4db5-4bea-b382-96739bfdf3b5 -->
<!-- last-edited: 2026-08-25 -->

# Burndown and Handoff Triage — 2026-08-25

## Handoffs

The nine files in `docs/handoffs/` are dated 2026-08-13 through 2026-08-16.
They are all older than the requested 1.5-day freshness limit.  They remain
historical context only and must not drive a production action without a fresh
code/configuration check.

## Burndown package

`docs/agent-tasks/todo-completion/` contains 297 task briefs.  Its master
breakdown is dated 2026-08-21; the state/handoff files are dated 2026-08-23.
They are an older, broad code-quality program rather than a current scan
readiness board.  The package itself warns that branch-based counts are
unsound for not-done work and that its derived counts must be re-enumerated.

Consequently, do not dispatch its aggregate "remaining" count or its waves as
current work.  Reuse only an individual brief after its anchors and acceptance
criteria are revalidated against main.

## Relevant examples

| Brief | Triage | Reason |
|---|---|---|
| `config/TASK-019-...` | Superseded | It proposes fixing factory-reset chapter consolidation; Wave 2 records it merged, and production now reports the live threshold as 10.  The still-valid operational concern is a canary confirming the live value is retained. |
| `missing-file-lane/TASK-198-...` | Needs re-find/revalidation | The filename cited by the index is no longer present, so the generated package cannot presently link it reliably. |
| `missing-file-lane/TASK-201-...` | Valid but unrelated to immediate ingestion | It concerns intro transcription for shattered-book classification, not whether a new scan creates Book/BookFile records. |

## Current readiness implication

The burndown does not contain a validated task that implements the user's
newly stated desired behavior: keep metadata candidates durably pending while
the local LLM is unavailable.  That gap is recorded explicitly in
`docs/CURRENT-STATUS.md` rather than inferred from an old brief.
