<!-- file: docs/process/status-reports.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3cbd8de8-4d8e-442d-958e-fc04662bc325 -->
<!-- last-edited: 2026-07-11 -->

# Status Report Convention

A status report is a terse, internal/operational record of a work session,
sprint, or execution wave: what shipped, what's still in flight, and what's
blocked or deferred. It is written for the maintainer/engineer, not for a
non-engineer stakeholder — unlike an
[executive summary](executive-summaries.md), a status report is allowed to
use file:line references, PR numbers, and internal jargon freely.

## When to produce one

Produce a status report after a significant work session, sprint, or
execution wave, when the maintainer needs a quick record of what shipped,
what's in flight, and what's blocked or deferred — for example:

- A multi-PR execution wave (planning → task briefs → several PRs merged in
  one session).
- A focused push on a specific track (e.g. a backend cutover, a calibration
  pass) that produced several merged PRs plus some open follow-up items.
- Handing off mid-task, so the next session (or a teammate) can pick up
  without re-deriving state from `git log`.

Do **not** produce one for a single small PR or a routine fix — that's what
the commit message and CHANGELOG entry are for.

## Where it goes

`docs/status/YYYY-MM-DD-<short-topic>.md`, using the date the report was
written (typically the date the session wrapped up).

Note the naming difference from executive summaries: status reports do
**not** use the `-executive-summary` suffix — that suffix is reserved for
[`docs/executive-summaries/`](../executive-summaries/).

## Structure

Based on `docs/status/2026-07-02-local-cutover-and-matching.md` as the
reference example:

1. **Header block**: the standard 4-line file header.
2. **`## TL;DR`**: a short paragraph covering what happened, why, and what's
   left — enough for someone to understand the session's shape without
   reading further.
3. **`## Shipped this session`**: a table of what merged, with columns
   `PR | Area | What`. Keep each "What" cell to one terse line.
4. Any of the following sections that apply:
   - **`## In flight`** — work started but not yet merged/complete.
   - **`## Blocked / deferred`** — work explicitly paused, waiting on a
     decision, sign-off, or external dependency.
   - **`## Next steps`** — what should happen in the next session.

Additional freeform sections (setup notes, config dumps, findings) are fine
when they help a reader resume the work, following the shape of the
reference example above.

Write for the maintainer/engineer audience: technical detail, file:line
references, and PR numbers are all appropriate here — the opposite of the
"no jargon, no file paths" rule for executive summaries.

## Status reports vs. executive summaries

A status report and an executive summary **may both exist for the same body
of work** — they are not mutually exclusive. A large execution wave often
warrants both: a terse internal one in `docs/status/` for the maintainer,
and a polished narrative one in `docs/executive-summaries/` for
non-engineer stakeholders. When both exist, cross-link them.

## Workflow

1. Do the work, ship it (PRs merged).
2. Draft the status report using the structure above.
3. Commit it and open a normal PR like any other change.
4. Mention the file path back to the user so they can find it.
