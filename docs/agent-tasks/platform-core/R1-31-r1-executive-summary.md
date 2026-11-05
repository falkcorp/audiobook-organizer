<!-- file: docs/agent-tasks/platform-core/R1-31-r1-executive-summary.md -->
<!-- version: 1.2.1 -->
<!-- guid: f6be479f-8c49-424f-8d3e-ef33dbe233e8 -->
<!-- last-edited: 2026-11-05 -->

# R1-31 — R1 close: R1's executive summary and TODO check-offs (docs only, beside the R1 release cut)

| Field | Value |
|---|---|
| ID | `R1-31` |
| Tier | **T0** (Haiku / low): one docs PR, a plain-language summary and TODO check-offs |
| Release / wave | R1 / W23 |
| Depends on | R1-01, R1-02, R1-03, R1-04, R1-05, R1-06, R1-07, R1-08, R1-09, R1-10, R1-11, R1-12, R1-13, R1-14, R1-15, R1-16, R1-17, R1-18, R1-19, R1-20, R1-21, R1-22, R1-23, R1-24, R1-25, R1-26, R1-27, R1-28, R1-29, R1-30, R1-32, R1-33, R1-34, R1-35, R1-36, R1-37 (each merged on `main` before you start) |
| Spec | plan §(n) item 4; `docs/process/executive-summaries.md`; 09 §9.4 R1 (Content) |
| Branch | `agent/platform-core-r1-31-r1-executive-summary` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this
list without recording it under the PR's "Scope notes"):

- `docs/executive-summaries/` (create `docs/executive-summaries/<today>-r1-core-runtime-executive-summary.md`)
- `TODO.md` (check-offs only)

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-31" -b agent/platform-core-r1-31-r1-executive-summary origin/main
cd "$REPO/.worktrees/platform-core-r1-31"
git rebase origin/main
```

## Goal

Write R1's executive summary and check off R1's TODO entries, once every other R1 row has merged (plan §(n) item 4:
R1's closing row carries them, so no R1 task PR edits them and siblings in a wave never collide). This row no longer
touches the release freeze: since D15 (02 §2.6; advisor S3/S5) the freeze is the owner's repository variable, and
the owner clears it in OWNER-03 after cutting R1. This PR is docs only and does not gate the R1 cut; it may merge
before or after OWNER-03.

## Steps

1. Read `docs/process/executive-summaries.md` and one existing file under `docs/executive-summaries/` for the format.
2. Create `docs/executive-summaries/<today>-r1-core-runtime-executive-summary.md` with the four-line header: what R1
   changed for the user, in plain language and without file paths or function names (the engine now runs the app,
   the stored data is untouched, rollback is a binary swap, the new UI build, the deploy and rollback tooling), and
   what the user has to do (nothing beyond the R1 release note). Name the rows only in a closing "Tracked work" line.
3. In `TODO.md`, grep for each R1 row's fragment title or text (`todo.d/` fragments may already be folded in,
   CLAUDE.md "Post-Task Hygiene") and check off the R1 entries whose work merged. Add no new tasks here.
4. Add a headerless `changelog.d/<today>_r1_executive_summary.md` with one line under `### Documentation`.
5. Commit: `docs(platform): R1 executive summary and TODO check-offs`; push, open the PR, merge once every required
   check is green (through the merge queue, D15).

## Acceptance

```bash
ls docs/executive-summaries/*-r1-core-runtime-executive-summary.md | wc -l      # expect 1
head -1 docs/executive-summaries/*-r1-core-runtime-executive-summary.md          # expect the file header line
git diff --stat origin/main -- .github/                                          # expect nothing (no workflow change)
make ci                                                                          # expect exit 0
```

## Out of scope

The release freeze (the owner's repository variable, D15). Cutting the R1 release (OWNER-03). Any code or workflow.

## Rollback

Revert the commit.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green, `gh pr merge <n> --rebase` (enqueues it; the merge queue re-tests it on top of `main`, D15). Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
