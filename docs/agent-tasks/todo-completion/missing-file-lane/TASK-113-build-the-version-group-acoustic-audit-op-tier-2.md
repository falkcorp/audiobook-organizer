<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-113-build-the-version-group-acoustic-audit-op-tier-2.md -->
<!-- version: 1.0.0 -->
<!-- guid: c703025f-e03b-4d64-bbd9-b8f2f9a4bbe9 -->
<!-- last-edited: 2026-08-21 -->

# TASK-113 — Build the version-group acoustic audit op (tier 2 of First Aid) (TODO.md L8551)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** cross-signal (acoustic + independent transcript) auto-fix op mutating VersionGroupID/IsPrimaryVersion on a prod-data path — needs careful confidence-threshold and insufficient-evidence handling · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8551 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Version-group acoustic audit op**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-113-build-the-version-group-acoustic-audit-op-tier-2" -b agent/missing-file-lane-113-build-the-version-group-acoustic-audit-op-tier-2 origin/main
cd "$REPO/.worktrees/missing-file-lane-113-build-the-version-group-acoustic-audit-op-tier-2"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Build maintenance.version-group-acoustic-audit (tier 2 of First Aid, per .claude/notes/2026-08-05-first-aid-architecture.md): for every existing version group, compare members' AcoustIDFingerprint/AcoustIDSeg0-6 similarity AND, independently, Whisper intro-transcription content similarity; emit verified/refuted/insufficient-evidence per group (never treat a missing fingerprint as refuted); auto-fix ONLY the ungroup remedy (clear VersionGroupID, restore IsPrimaryVersion) when both signals agree the group is wrong, gated behind apply=false-by-default + a confidence threshold, falling back to a review hold when the two signals disagree.

## Background (verify before editing)

- ~65% of books were unfingerprinted as of 2026-07-02 per the item — absent-evidence-means-insufficient (not refuted) is a hard structural requirement, mirrored by probe_directory_books.go's ProbedDuration.OK-flag pattern elsewhere in this codebase.
- ~96.5% of books are transcribed but ~40% low-quality/unparsed — filter low-quality transcripts before trusting the transcript signal, per the item.
- The remedy (ungroup) is additive-safe in the same sense missing_file_repoint.go's repoint is: it destroys no rows/files and is itself reversible.
- This is tier 2 of First Aid — expensive, runs only over the already-small version-grouped population, not the whole library.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'AcoustIDFingerprint\|AcoustIDSeg0' internal/database/store.go   # ≥2 hits — BookFile has AcoustIDFingerprint and AcoustIDSeg0 fields to compare
  grep -n 'VersionGroupID\|IsPrimaryVersion' internal/database/store.go   # ≥2 hits — VersionGroupID/IsPrimaryVersion exist as the fields this op's auto-fix would clear/restore
  grep -rln 'version.group.*audit\|VersionGroupAudit' internal/plugins/maintenance   # 0 hits — no existing op audits version groups against acoustic/transcript agreement
  ```

### Reuse — don't invent

- Use `apply=false-by-default + report-before-write pattern` in `internal/plugins/maintenance/missing_file_repoint.go` (verify: `grep -n 'Apply bool' internal/plugins/maintenance/missing_file_repoint.go`) — do NOT write a parallel helper.
- Use `registry.RunItems bounded pool` in `internal/operations/registry` (verify: `grep -rn 'func RunItems' internal/operations/registry`) — do NOT write a parallel helper.

## Step-by-step

1. Add internal/plugins/maintenance/version_group_acoustic_audit.go modeled structurally on missing_file_repoint.go: sdk.OperationDef named maintenance.version-group-acoustic-audit, params with `Apply bool` (default false) and a confidence threshold param.
2. Enumerate existing version groups (find the accessor — grep -n 'VersionGroupID' internal/database/*.go, or build the grouping from a bulk-book accessor filtered to non-empty VersionGroupID).
3. Per group, per member pair: compute acoustic similarity from AcoustIDFingerprint/AcoustIDSeg0-6 (find existing similarity code in internal/plugins/acoustid before writing a new distance function) and, separately, transcript similarity from IntroTranscription (after filtering unparsed/low-quality per whatever quality signal the intro-transcribe parser already emits).
4. Classify each group verified / refuted / insufficient-evidence: insufficient-evidence whenever EITHER signal has no usable data for a member.
5. For 'refuted' groups above the confidence threshold AND only when apply=true, clear VersionGroupID and restore IsPrimaryVersion via the store's existing update path (find the exact accessor used by ApplyVersionGroup or manual ungroup).
6. Write a per-group TSV report before any apply-mode writes, mirroring writeRepointReport's ordering.
7. Register the op in the maintenance plugin's op list.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_113.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A version group with only ONE remaining member must be skipped, not compared against itself.
- A group where ALL members lack both signals must be insufficient-evidence, not silently omitted from the report.

## Tests

- TestVersionGroupAudit_MissingFingerprintIsInsufficientNotRefuted — a group where one member has NO AcoustIDFingerprint must classify insufficient-evidence, never refuted.
- TestVersionGroupAudit_AgreeingSignalsRefute_ClearsGroupWhenApplied — both signals disagree with the grouping, apply=true → VersionGroupID cleared, IsPrimaryVersion restored, no rows/files deleted.
- TestVersionGroupAudit_DryRunMakesNoWrites — apply=false (default) → the store's write path is never called; anti-over-suppression companion to the apply test.
- TestVersionGroupAudit_DisagreeingSignalsGoToReview — acoustic says match, transcript says no-match (or vice versa) → group is held for review, never auto-fixed.

Anti-over-suppression test: `TestVersionGroupAudit_DryRunMakesNoWrites` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run VersionGroupAudit passes all four cases.
- [ ] A dry run against a fixture DB with a deliberately-wrong version group produces a report row classifying it refuted, with zero write calls (apply=false).
- [ ] Anti-over-suppression test: `TestVersionGroupAudit_DryRunMakesNoWrites` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_113.md`.

## Commit message

```
feat(missing-file-lane): Build the version-group acoustic audit op (tier 2 of First A (TODO L8551)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go test ./internal/plugins/maintenance/... -run VersionGroupAudit passes all four cases.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Owner-requested 2026-08-05, 'not scheduled' — no owner decision among the given 14 either builds or defers this, so it is treated as in-scope actionable work. Home: tier 2 of First Aid per .claude/notes/2026-08-05-first-aid-architecture.md, feeding a tier-3 ungroup fixer; consider splitting audit from fixer if L8890's orchestrator lands first.
