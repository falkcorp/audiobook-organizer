<!-- file: docs/ci/merge-queue.md -->
<!-- version: 0.1.0 -->
<!-- guid: 19649fa2-e3bb-4301-be2a-7947ba2e4654 -->
<!-- last-edited: 2026-11-20 -->

# Merge queue: required checks and where the race detector runs

`main` merges only through GitHub's merge queue (repo ruleset `26660812`,
REBASE, ALLGREEN, no bypass actors; on since 2026-11-05). Enqueue with the
GraphQL `enqueuePullRequest` mutation. The queue tests a
`gh-readonly-queue/main/*` commit in a `merge_group` event and merges once
every **required** check on that commit has passed. Non-required checks run
too but do not hold the queue.

## Flow

1. **Pull request.** Every workflow runs on `pull_request`. ci.yml's Go test
   jobs run **with** `-race`: the short suite in five shards (`Go Tests
   (short, race) shard 1/4`..`4/4`, `core/4`) and the fixture tests in three
   (`Fixture Tests (no -short, race) 1/3`..`3/3`).
2. **Merge queue (`merge_group`).** The same workflows run on the queued
   commit, except that ci.yml runs **without** the race detector (owner
   decision 2026-11-20): the short shards drop `-race`, and the fixture shards
   are skipped. Their "-short form" is the short shards, because every test
   they run skips under `-short`. The `race` step of ci.yml's `changes` job
   makes that decision; nothing else reads the event name.
3. **Push to main.** After the merge, ci.yml runs again on main **with**
   `-race`, short and fixture shards both. `auto-revert.yml` watches that
   run's conclusion and reverts a red commit.
4. **Nightly.** At 03:00 America/New_York the Woodpecker cron `race-nightly`
   runs the whole `-race` suite (short and fixture) on main's tip, on a Mac
   agent, never U1 (see [woodpecker.md](woodpecker.md#nightly-race-run)).
   `race-nightly-watch.yml` files an issue at 13:00 UTC when it failed or did
   not run.

Why: in merge-queue run 44770780353 (60 minutes) the race shards took 15-24
minutes each and the fixture shards 14-25, and every job also waited 30-37
minutes for a runner. Dropping `-race` shortens the shards and removes three
fixture jobs per queue run, which also cuts the runner wait.

To rehearse the queue's flavour on a branch before enqueueing:
`gh workflow run ci.yml --ref <branch> -f race=false`.

## Required checks and how each is met in the queue

| required check | workflow / job | in `merge_group` |
|---|---|---|
| `Changes` | ci.yml `changes` | Runs. No diff range for this event, so it fails open (`go=true`, `web=true`) and sets `race=false`. |
| `TODO Fragment Headers` | ci.yml `todo-header-lint` | Runs, unchanged (no `needs`). |
| `Interface Width Ratchet` | ci.yml `interface-width` | Runs, unchanged (`go=true`). |
| `Errcheck Ratchet` | ci.yml `errcheck-ratchet` | Runs, unchanged. |
| `Deadcode Ratchet` | ci.yml `deadcode-ratchet` | Runs, unchanged. |
| `Repo Guards` | ci.yml `repo-guards` | Runs, unchanged. |
| `Mock Freshness` | ci.yml `mocks-check` | Runs, unchanged. |
| `vet-darwin` | ci.yml `vet-darwin` | Runs, unchanged. |
| `vet-integration` | ci.yml `vet-integration` | Runs, unchanged. |
| `Coverage Floor (PR gate)` | ci.yml `coverage` | Runs after the five short shards, which run **without** `-race` but with the same `-covermode=atomic` profile, so the four root profiles merge and meet `.ci/coverage-floor.txt` as before. It runs under `always()` and fails unless every shard succeeded: it is the only required check the test shards reach, and a skipped required check counts as passing. |
| `extraction-gates` | extraction-gates.yml terminal job | Runs, unchanged (no `-race` in that workflow). |

No required check is a race job, so no required name disappears in the
queue. The race jobs keep their names ("(short, race)", "(no -short, race)")
on every event; the step name of each short shard says `race=false` when it
ran without the detector.

Not required, and so not holding the queue: `Fixture Tests` (skipped there),
`Full-Size Tests`, `Minimal CI / *`, the frontend jobs, `Core CI`, `E2E`,
`Docker Image`, `Lint Trial` and the Woodpecker statuses.

## Trade-off

A data race that only the race detector sees no longer stops a PR in the
queue. It is still caught on the PR itself (same code, minus whatever the
queue combined it with), on the post-merge push run (which auto-reverts), and
in the nightly run (which files an issue but reverts nothing).
