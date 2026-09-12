<!-- file: docs/process/executive-summaries.md -->
<!-- version: 1.4.0 -->
<!-- guid: b608794a-eac5-44bc-95b9-643872bd0ca8 -->
<!-- last-edited: 2026-09-12 -->

# Executive Summary Convention

After any major section of work (a hardening pass, a multi-PR feature, a
significant bug-fix wave, a format/architecture deep dive), write an
executive summary and save it to this repo so it can be shared with others
without re-explaining the whole session.

## When to produce one

Produce a summary when the work meets any of these:

- It spans multiple files/PRs, or one PR with a wide blast radius.
- It closes out a spec or a tracked set of issues (e.g. "fix all of K13–K17").
- It fixes something that could have silently caused data loss or corruption.
- The user says something like "keep going," "fix all that," or otherwise
  signs off on a multi-step plan that then gets executed to completion.
- The user is stepping away mid-task and asked for thorough written records.

Do **not** produce one for small one-off fixes, typo corrections, or
single-file changes — that's what commit messages and CHANGELOG entries are
for.

## Where it goes

Executive summaries follow a two-stage lifecycle: many small files during
the month, then one combined file per month.

**During the month — per-day / per-topic files are allowed.** Each
qualifying change gets its own file, written in the same PR as the change:
`docs/executive-summaries/YYYY-MM-DD-<short-topic>-executive-summary.md`,
using the date the work shipped (merge date), not the date work started.
Several files in one month is expected; there is no need to hunt for and
append to an existing file.

**At month end — combine them into one monthly summary and delete the
per-day files.** Once the month is over, merge that month's per-day files
into a single `docs/executive-summaries/YYYY-MM-executive-summary.md`
(for example `2026-07-executive-summary.md`), then `git rm` every per-day
file it replaces. The combined file:

- is grouped **by theme, not by date**, and follows the Structure below at
  the month level (header, executive-summary bullets, one section per
  theme);
- keeps **every distinct outcome and number** from the per-day files —
  data-loss fixes, counts, what users would notice — and drops only
  repetition;
- states its real coverage period at the top, especially when a per-day
  file dated in this month covered work from an earlier one;
- stays plain-language: no file paths or function names in the body.

In the same PR, repoint every link to a removed per-day file (docs,
`TODO.md`, other Markdown) at the monthly file. Do not hand-edit
`CHANGELOG.md` — it is assembled; list any CHANGELOG references to the
removed files in the PR description instead.

A month that is already combined gets no new per-day files; a late
qualifying change for a closed month is added to its monthly file as a new
bullet and section.

If the work also produced a formal spec (see `docs/specs/`), link to it and
to the merged PR at the top of the summary.

An executive summary is the polished, stakeholder-facing narrative for a
body of work — distinct from a status report (see
[`docs/process/status-reports.md`](status-reports.md)), which is a terse,
internal/operational update (a TL;DR, a table of what shipped, and what's
still in flight or blocked) aimed at the maintainer/engineer rather than a
non-engineer stakeholder; the two are not mutually exclusive and a large
execution wave often warrants both.

## Structure

1. **Header block**: PR link + merge commit, links to any related specs.
2. **Executive Summary**: 5–8 bullets, one per major change, each phrased so
   a non-engineer stakeholder understands *what* changed and *why it
   mattered* — no jargon, no internal function names. Written for someone who
   will skim this once and move on. Close with a verification/outcome line
   if one exists (e.g., "verified clean against production data").
3. **One section per change**, each with exactly three parts:
   - **What it was** — describe the bug/gap in plain terms, not code terms.
   - **Why it mattered** — the concrete failure it could have caused, in
     terms of user-visible impact (data loss, corruption, wasted work),
     not abstract correctness.
   - **The fix** — what was actually done, in one or two sentences.

Write at a 12th-grade / college-freshman reading level: clear sentences,
but more technical detail than a pure lay summary — it's fine to name the
actual mechanism (a specific check, flag, or concept) as long as it's
explained in context. Define any acronym on first use (PID, ECB, mhoh,
etc.) rather than assuming the reader already knows it. Avoid code
snippets and file paths in the body (those belong in the spec, not the
summary) — describe behavior and mechanism in prose instead.

## Workflow

1. Do the work, ship it (PR merged) — update CHANGELOG.md and TODO.md as
   normal (see CLAUDE.md's Post-Task Hygiene section).
2. **Same PR, not a follow-up:** check the "When to produce one" criteria
   above. If it qualifies, draft the summary using the structure above,
   reusing language from any spec/CHANGELOG entries already written during
   the work — don't re-derive from scratch. Bundling it with the
   CHANGELOG/TODO edit is what keeps this from being skipped.
3. Commit it and open a normal PR like any other change (same review/merge
   flow as the rest of the repo — this doc does not grant an exception to
   branch protection or bypass review).
4. Mention the file path back to the user so they can find and share it.

## Expectation going forward

Once a major piece of work is merged, proactively offer or produce this
summary without being asked again — this file is the standing instruction
to do so.
