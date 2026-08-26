<!-- file: PLAN.md -->
<!-- version: 1.0.2 -->
<!-- guid: 2ebf9dde-55a7-41fc-a9e3-e712944e4179 -->
<!-- last-edited: 2026-08-25 -->

# Audiobook Organizer Current-Status Source of Truth

## Goal

Determine whether the current application is ready to scan audiobooks, add them to the library, and apply metadata safely. Reconcile the repository's overlapping planning artifacts and open pull requests into one evidence-backed current-status document that future agents can use as the authoritative entry point.

## Affected files

- `PLAN.md` — record this investigation's approved scope, verification strategy, and rollback path.
- `docs/CURRENT-STATUS.md` — create the consolidated source of truth, including readiness verdict, blockers, outstanding work, corrections, artifact freshness, PR disposition, and links to valid task documents.
- `docs/audits/current-status-evidence/*.md` — preserve each specialist's evidence and exact census in a separate, non-overlapping checkpoint file.

No other repository files will be modified during this investigation. Specialists write only their assigned evidence file; the coordinating agent verifies and commits all checkpoints centrally.

## Steps

1. Dispatch read-only specialists for the independent evidence domains:
   - reconcile `TODO.md` with `CHANGELOG.md`;
   - compare `todo.d/` fragments with `TODO.md` and identify stale, missing, or contradictory entries;
   - inspect every open GitHub pull request for completion, correctness, CI/review state, overlap, and merge readiness;
   - inventory handoff documents, classify useful versus stale using the requested 1.5-day threshold, and extract still-relevant evidence;
   - classify the task-burndown folder and inspect the concrete tasks generated from the major burndown.
2. Independently verify agent findings against repository state, Git history/timestamps, GitHub PR metadata/checks/diffs, and the live project-context corpus.
   Save and commit the evidence directory after each specialist wave; update and commit the current-status document after each major synthesis section.
3. Verify the recent deployment wherever repository and accessible production evidence allow: identify the deployed version/commit, distinguish merged from deployed changes, and check runtime gates, scheduled-task settings, migrations/backfills, and scan/metadata prerequisites relevant to real use.
4. Resolve contradictions using explicit precedence: current production evidence, current code and tests, current GitHub state, live status documents, then older planning/handoff artifacts. Preserve unresolved owner decisions instead of guessing.
5. Write `docs/CURRENT-STATUS.md` with exact counts and a direct answer to scan/import/metadata readiness. Link valid task artifacts; label links that require correction or supplementation before use.
6. Verify the document's links, file header, counts, timestamps, and consistency with the inspected sources. Review the final diff and report exact completed, remaining, and blocked counts.

## Test strategy

- Commands:
  - `git diff --check`
  - a repository-local link/path validation pass over every relative Markdown link added to `docs/CURRENT-STATUS.md`
  - `git diff -- PLAN.md docs/CURRENT-STATUS.md`
  - targeted `gh pr view` / `gh pr checks` verification for every open PR included in the status document
  - targeted deployed-version and runtime-readiness checks using existing repository-supported diagnostics where access is available
- Success criteria:
  - every requested evidence domain has a specialist report and coordinator verification;
  - every open PR has an explicit disposition;
  - stale or contradictory artifacts are clearly marked;
  - outstanding tasks have exact counts and traceable links;
  - the document states whether scan, library ingestion, and metadata application are usable now, conditionally usable, or blocked, with evidence and prerequisites.

## Rollback

- Delete the unmerged `docs/current-status-source-of-truth` branch and remove its worktree; the primary checkout remains untouched.
- If only the generated status document needs revision, revert the documentation commit on the feature branch before opening or merging a PR.
