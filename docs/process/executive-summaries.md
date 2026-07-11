<!-- file: docs/process/executive-summaries.md -->
<!-- version: 1.3.0 -->
<!-- guid: b608794a-eac5-44bc-95b9-643872bd0ca8 -->
<!-- last-edited: 2026-07-11 -->

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

**Check for an existing rolling summary for the current month first**
(`docs/executive-summaries/YYYY-MM-*-executive-summary.md`). If one exists,
ADD a new Executive Summary bullet + a new numbered section to it — do not
create a second file for the same month. One doc per month is the goal;
several small dated files fragmenting the same month is exactly the sprawl
this convention exists to prevent (it happened once, in three separate
files, and had to be manually consolidated back into one roundup).

Only create a new file when either no summary exists yet for the current
month (first qualifying change of the month), or the work is big/
self-contained enough to warrant its own deep-dive report that the monthly
roundup then links to instead of repeating (see the iTunes-hardening report
linked at the top of the current roundup for the pattern) — that's the
exception, not the default.

New-file naming: `docs/executive-summaries/YYYY-MM-DD-<short-topic>-executive-summary.md`,
using the date the work shipped (merge date), not the date work started.

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
