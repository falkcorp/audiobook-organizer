<!-- file: docs/agent-tasks/todo-completion/dedup/TASK-044-measure-whether-dedup-duration-abridged-3-573-is.md -->
<!-- version: 1.0.0 -->
<!-- guid: b9f4ccab-9264-46a3-901d-086d8f9dea61 -->
<!-- last-edited: 2026-08-21 -->

# TASK-044 — Measure whether dedup:duration-abridged (3,573) is over-firing before touching its display (TODO.md L1350)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · dedup subagent · **Why:** Requires reading the abridged-detection condition, sampling real tagged pairs, and manually judging whether the duration difference genuinely indicates an abridged edition -- needs judgment, not just a mechanical count. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 1350 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🏷️ **\"Browse by Tag\" surfaces internal bookkeeping" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-044-measure-whether-dedup-duration-abridged-3-573-is" -b agent/dedup-044-measure-whether-dedup-duration-abridged-3-573-is origin/main
cd "$REPO/.worktrees/dedup-044-measure-whether-dedup-duration-abridged-3-573-is"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build a new report-only maintenance op (internal/plugins/maintenance/dedup_abridged_measure.go, registered in plugin.go alongside dedupExactTriageDef) that: (1) counts how many books currently carry dedup:duration-abridged, (2) samples N of them and cross-checks whether their paired book's title carries an (Un)?Abridged marker (reusing the regex from calibrate_scoring.go:315 as an independent confirmation signal), and (3) reports a confirmation/false-positive rate. Do NOT hide or delete the tag as part of this task -- that decision comes after the measurement, per the owner's explicit instruction.

## Background (verify before editing)

- internal/dedup/collectors_metadata.go:134-135 doc comment: dedup:duration-match applied when duration within ±2%; dedup:duration-abridged applied when duration differs 10-20% AND Levenshtein title distance ≤ cfg.LevenshteinMax (verified exact condition at L263: `pct >= 0.10 && titleDist <= cfg.LevenshteinMax`, gated by an earlier short-circuit at `pct >= cfg.AbridgedThreshold` which presumably caps the window at 20%).
- internal/plugins/metafetch/calibrate_scoring.go:315 already has an '(Un)?abridged' title regex that provides an independent cross-check signal (does the title actually say Abridged/Unabridged) against the duration-based tag.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'dedup:duration-abridged\|pct >= 0.10' internal/dedup/collectors_metadata.go   # hits at ~L134 (doc comment), L261/264 (EnsureSingletonBookTag calls), and L263 (pct >= 0.10 condition) — the abridged tag is applied by a duration-ratio check in collectors_metadata.go, with the exact threshold visible
  grep -n 'func (p \*Plugin) dedupExactTriageDef' internal/plugins/maintenance/dedup_triage.go   # 1 hit ~L299 — a directly analogous report-only maintenance op already exists to model the new one on
  grep -n 'dedupExactTriageDef()' internal/plugins/maintenance/plugin.go   # 1 hit ~L81 — maintenance ops are registered in plugin.go by listing their Def() constructors
  grep -n '(un)?abridged' internal/plugins/metafetch/calibrate_scoring.go   # 1 hit ~L315 — an existing title-based Abridged/Unabridged regex exists to cross-check against
  ```

### Reuse — don't invent

- Use `dedup_triage.go's report-op structure (OperationDef + Run function pattern) as the template for the new op` in `internal/plugins/maintenance/dedup_triage.go` (verify: `grep -n 'func (p \*Plugin) runDedupExactTriage' internal/plugins/maintenance/dedup_triage.go`) — do NOT write a parallel helper.
- Use `the (un)?abridged title regex as an independent cross-check signal` in `internal/plugins/metafetch/calibrate_scoring.go` (verify: `grep -n 'regexp.MustCompile.*abridged' internal/plugins/metafetch/calibrate_scoring.go`) — do NOT write a parallel helper.

## Step-by-step

1. Read internal/dedup/collectors_metadata.go around L240-266 to confirm the exact upper bound (cfg.AbridgedThreshold's configured value) alongside the already-confirmed 10% lower bound.
2. Write internal/plugins/maintenance/dedup_abridged_measure.go following dedup_triage.go's OperationDef + Run-function structure: query every book pair currently tagged dedup:duration-abridged, compute their actual duration ratio, and cross-check both books' titles against the `(?i)\s*\((un)?abridged\)\s*$` regex reused from calibrate_scoring.go:315.
3. Report three numbers: total tagged count (compare against the ~3,573 figure observed 2026-08-10), how many of a sampled subset (e.g. 200) have a title-based marker confirming the tag, and how many do not (candidate false positives).
4. Register the new op's Def() constructor in plugin.go alongside dedupExactTriageDef() (~L81).
5. Do not change the tagger or the display based on this report alone -- hand the numbers back for the owner's over/under-firing decision.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_dedup_044.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book pair may have been re-tagged or merged since 2026-08-10 -- the count may already differ from the originally observed 3,573; report the CURRENT count, don't assume it matches.

## Tests

- internal/plugins/maintenance/dedup_abridged_measure_test.go: a unit test with synthetic book pairs at known duration ratios and title markers, asserting the report correctly buckets them as confirmed/unconfirmed.

Anti-over-suppression test: `N/A -- this is a measurement task, not a filter/guard.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] The report, run against the current library, outputs a total count and a sampled confirmation rate with specific book IDs/titles for manual spot-check.
- [ ] Anti-over-suppression test: `N/A -- this is a measurement task, not a filter/guard.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_dedup_044.md`.

## Commit message

```
feat(dedup): Measure whether dedup:duration-abridged (3,573) is over-firi (TODO L1350)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`The report, run against the current library, outputs a total count and a sampled confirmation rate with specific book IDs/titles for manual spot-check.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Explicitly separate from any display-cleanup work per the owner's own instruction: 'Do not fix it by hiding the tag -- measure whether the abridged detection is correct first.'
