<!-- file: docs/agent-tasks/todo-completion/misc-go/TASK-085-measure-the-real-double-primary-rate-library-wid.md -->
<!-- version: 1.0.0 -->
<!-- guid: cfab9436-db56-491e-ab38-5ffee205ea68 -->
<!-- last-edited: 2026-08-21 -->

# TASK-085 — Measure the real double-primary rate library-wide, then build the demote-extras sibling of ElectMissingPrimaries (VG-DOUBLE-PRIMARY)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · misc-go subagent · **Why:** Report + repair op following an existing, well-documented, in-repo sibling pattern (ElectMissingPrimaries) -- moderate size, low novelty once the exact reuse target (electPrimaryFor) is identified. · **Depends on:** none · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 2373 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**VG-DOUBLE-PRIMARY**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/misc-go-085-measure-the-real-double-primary-rate-library-wid" -b agent/misc-go-085-measure-the-real-double-primary-rate-library-wid origin/main
cd "$REPO/.worktrees/misc-go-085-measure-the-real-double-primary-rate-library-wid"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

First, measure the library-wide rate of version groups with more than one is_primary_version=true member (not just the 15-sample offset-500 window already observed) -- either as a new counting pass inside elect_primaries.go or a small extension to its existing group-bucketing loop. Then build DemoteExtraPrimaries (or similarly named), a sibling of ElectMissingPrimaries in the same file, dry-run-gated by default, that for each over-elected group calls electPrimaryFor on its members to pick the SAME winner the forward-elect logic would choose, and writes IsPrimaryVersion=false to every other member via UpdateBook -- NEVER DeleteBook.

## Background (verify before editing)

- ElectMissingPrimaries's own group-bucketing loop already counts `gs.primaries` per group (verified above) -- the measurement pass for THIS item can reuse that exact same bucketing code, just inverting the filter from `primaries == 0` to `primaries > 1`, rather than writing new enumeration logic from scratch.
- electPrimaryFor's docstring (verified) states its rule explicitly: 'the earliest-created member wins, tie-broken by book ID... deliberately does NOT try to pick the best quality copy.' The new demote function must apply this SAME rule for consistency, even though electPrimaryFor's current signature takes 'members of a primary-less group' -- it needs no change to be reused for 'members of an over-elected group', since it just picks a winner from a slice of database.Book regardless of their current primary status.
- Owner standing rule across this whole scope: never delete rows in a repair; only REPOINT/demote flags. This backfill's ENTIRE effect must be flipping IsPrimaryVersion from true to false on losers -- it must never call DeleteBook.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'func (p \*PebbleStore) GetBooksByVersionGroup' internal/database/pebble_store.go   # 1 hit ~L3194 — the existing GetBooksByVersionGroup query path this backfill needs to enumerate groups by
  grep -n 'gs.primaries > 0' internal/reconcile/elect_primaries.go   # 1 hit ~L158 — ElectMissingPrimaries exists and explicitly only handles the zero-primary case, skipping any group with an existing primary
  grep -n 'func electPrimaryFor' internal/reconcile/elect_primaries.go   # 1 hit ~L71 — the real winner-selection helper to reuse is electPrimaryFor, not 'BookIsBetter' (which does not exist)
  grep -rln 'primaries > 1\|MultiPrimary\|DoublePrimary\|double_primary' --include='*.go' internal | grep -v _test.go   # 0 hits — no production code measures multi-primary groups yet (one test file mentions it) — no double-primary detection/repair exists anywhere in the repo yet
  grep -n 'reconcile.ElectMissingPrimaries' internal/server/reconcile.go   # 1 hit ~L195 — the existing caller site for ElectMissingPrimaries, the wiring template for the new sibling function
  ```

### Reuse — don't invent

- Use `electPrimaryFor -- the actual winner-selection rule (earliest-created wins, tie-broken by ID), to be reused for CHOOSING which of several existing primaries stays primary` in `internal/reconcile/elect_primaries.go` (verify: `grep -n 'func electPrimaryFor' internal/reconcile/elect_primaries.go`) — do NOT write a parallel helper.
- Use `ElectMissingPrimaries' worker-pool/clobber-guard structure (bounded errgroup sized to runtime.NumCPU, per-group re-read before writing) as the concurrency template for the new sibling function` in `internal/reconcile/elect_primaries.go` (verify: `grep -n 'g.SetLimit(max(runtime.NumCPU()' internal/reconcile/elect_primaries.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add a measurement pass (report-only): reuse ElectMissingPrimaries' existing per-group bucketing loop (or extract it into a shared helper both functions call) to count how many groups have >1 member with IsPrimaryVersion=true. Report the total groups affected and total excess-primary rows, library-wide -- not a sample.
2. Once the real scope is known, add `func DemoteExtraPrimaries(store Store, dryRun bool) (*DemotePrimaryResult, error)` to internal/reconcile/elect_primaries.go, following ElectMissingPrimaries' exact structure: snapshot enumeration via loadAllBooksCore, bucket by group, filter for `primaries > 1`, then a bounded errgroup (runtime.NumCPU()) with per-group re-read before writing (the same clobber guard).
3. For each over-elected group, call the existing (unmodified) `electPrimaryFor(members)` to pick exactly one primary; write IsPrimaryVersion=false to every OTHER member via UpdateBook (never DeleteBook).
4. Report exact before/after counts per run, and re-read the DB after a non-dry-run apply to verify the fix landed.
5. Add an invariant check (a small helper both ElectMissingPrimaries and DemoteExtraPrimaries can call, or extend the existing one if it exists) that can be run post-repair to confirm zero groups remain with 0 or >1 primary.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_misc-go_085.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A version group where ALL members are currently non-primary (a zero-primary group) is ElectMissingPrimaries' job, not this one's -- DemoteExtraPrimaries' filter (`primaries > 1`) naturally excludes those groups, but confirm the two functions' filters are mutually exclusive (0 vs >1) so a future single combined pass can safely call both without double-processing a group.
- A group's true winner per electPrimaryFor may differ from whichever one happened to be more-recently-created -- don't assume 'keep the newest' is correct, apply the real (earliest-created) selection rule electPrimaryFor already implements.

## Tests

- internal/reconcile/elect_primaries_test.go: a test seeding several version groups with double/triple primaries and asserting DemoteExtraPrimaries (dry-run and real) correctly identifies and fixes each, leaving exactly one primary per group (the same one electPrimaryFor would pick) and zero deleted rows.
- Idempotency test: running the repair twice in a row produces no further changes on the second run.

Anti-over-suppression test: `The repair must be dry-run by default and require explicit apply=true from an operator, per this scope's standing REPOINT/never-auto-apply convention -- this is the safeguard against silently mutating primary elections at scale on a first run.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] The measurement report shows the real library-wide count (superseding the '10 of 15 at one offset' sampled figure).
- [ ] A dry-run against real data reports the exact number of groups/rows it would fix.
- [ ] A non-dry-run apply, followed by the invariant check from step 5, shows zero groups with >1 primary.
- [ ] `grep -n 'DeleteBook' internal/reconcile/elect_primaries.go` returns 0 hits -- confirms the never-delete constraint.
- [ ] Anti-over-suppression test: `The repair must be dry-run by default and require explicit apply=true from an operator, per this scope's standing REPOINT/never-auto-apply convention -- this is the safeguard against silently mutating primary elections at scale on a first run.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_misc-go_085.md`.

## Commit message

```
feat(misc-go): Measure the real double-primary rate library-wide, then buil (VG-DOUBLE-PRIMARY)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (`The measurement report shows the real library-wide count (superseding the '10 of 15 at one offset' sampled figure).`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

review_critical=true: on the prod-data path (version-group primary election). Corrects the prior scout's reuse guess: the winner-selection helper is electPrimaryFor (internal/reconcile/elect_primaries.go:71), NOT 'BookIsBetter', which does not exist anywhere in the repo. Per this scope's owner decision pattern for prod-run items, the actual EXECUTION of this repair against the live library should be logged in docs/operations/pending-prod-actions.md once built, even though building the op itself is actionable engineering work now.
