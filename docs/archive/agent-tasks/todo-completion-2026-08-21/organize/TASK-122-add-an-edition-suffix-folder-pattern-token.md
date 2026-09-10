<!-- file: docs/agent-tasks/todo-completion/organize/TASK-122-add-an-edition-suffix-folder-pattern-token.md -->
<!-- version: 1.1.0 -->
<!-- guid: 672f0375-f97c-44e7-a7cb-74cbd6d75b17 -->
<!-- last-edited: 2026-09-02 -->

# TASK-122 — Add an {edition_suffix} folder-pattern token (TODO.md L5021)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — grep edition_suffix pathbuild.go -> 0 hits; model intact ('{edition}' :279, series_prefix-after-trim comment :298, Edition field :198). No commits on pathbuild.go since 2026-08-21. Recommendation: keep — small and self-contained.

**Priority:** P1 · **Effort:** S · **Recommended subagent:** Sonnet-class · organize subagent · **Why:** Small, well-scoped addition with an exact model to copy, but touches the organize target-path computation (prod-data-adjacent) so needs a careful test, not a rubber-stamp. · **Depends on:** none · **Wave:** 1 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 5021 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Add an `{edition_suffix}` folder-pattern token.*" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-10.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-122-add-an-edition-suffix-folder-pattern-token" -b agent/organize-122-add-an-edition-suffix-folder-pattern-token origin/main
cd "$REPO/.worktrees/organize-122-add-an-edition-suffix-folder-pattern-token"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new {edition_suffix} token to internal/organizer/pathbuild.go's path-variable vocabulary, built the same way {series_prefix} is (after the trim/scrub pass, as pattern structure rather than raw metadata), so a pattern like '{author}/{series}/{title}{edition_suffix} ({print_year})' renders '' for books with no edition and ' (Unabridged)'-style separator+parens for books that have one, instead of {edition}'s current dangling-space/empty-parens problem when used raw.

## Background (verify before editing)

- internal/organizer/pathbuild.go:279 already exposes {edition} as a raw scrubbed value (v.Edition), with no separator handling — a pattern author who writes '{title} ({edition})' gets ' ()' for books with no edition.
- internal/organizer/pathbuild.go:298-311 builds {series_prefix} AFTER the main scrub/trim loop specifically so its trailing ' - ' survives TrimSpace and collapses to '' when series is empty — documented in the comment there as the fix for a real bug ('MySeries -Book' from an earlier version that ran the prefix through TrimSpace).
- Two editions of the same title sharing {print_year} currently compute the same target path under the default pattern and collide (OrganizeBook stats the target, fires OnCollision, returns ErrTargetOccupied per rename.go/move.go) rather than clobbering — this is documented as safe-but-invisible, an ergonomics gap not a correctness bug, per the item's own 2026-08-17 deferral note.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'edition_suffix' internal/organizer/pathbuild.go   # 0 hits — no edition_suffix token exists yet
  grep -n '"{edition}"' internal/organizer/pathbuild.go   # 1 hit, L279 — {edition} exists as a raw value in the vocabulary
  grep -n 'series_prefix.*built AFTER the trim pass' internal/organizer/pathbuild.go   # 1 hit, ~L298 — {series_prefix} is built after the trim pass as the model to copy
  grep -n 'Edition   string' internal/organizer/pathbuild.go   # 1 hit, L198 — PathVars.Edition field exists to source the value from
  ```

### Reuse — don't invent

- Use `series_prefix construction block (copy this shape)` in `internal/organizer/pathbuild.go` (verify: `grep -n 'out\["{series_prefix}"\]' internal/organizer/pathbuild.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/organizer/pathbuild.go, after the `out["{series_prefix}"] = ...` block (around L303-311), add a parallel block for {edition_suffix}: `if edition := out["{edition}"]; edition != "" { out["{edition_suffix}"] = " (" + edition + ")" } else { out["{edition_suffix}"] = "" }` — using a leading space + parens as the default separator, matching the {print_year} default pattern shown in the item text ({title} ({print_year})).
2. Confirm the new key is added to `out` (the map returned by the function), not `raw` (which goes through the TrimSpace/scrub loop and would eat the separator, exactly the bug the series_prefix comment warns about).
3. No changes needed to BuildPath / expandFormatSpecs / the placeholder-normalize regex — {edition_suffix} is a plain (non-format-spec) placeholder like {series_prefix}, substituted the same way.
4. Add a doc comment above the new block modeled on the {series_prefix} one, explaining why it must be built after the trim pass.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_organize_122.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Edition value containing its own parens or slashes (rare but possible if sourced from a messy metadata field) — rely on the same scrubVar/sanitization already applied to {edition} itself (out["{edition}"] already went through scrubVar before this new block reads it), so no additional escaping needed.
- A pattern author who uses BOTH {edition} and {edition_suffix} in the same pattern — both remain valid independently; no conflict, since they are just two different map entries.

## Tests

- internal/organizer/pathbuild_test.go — TestBuildPath_EditionSuffix_Present: PathVars with Edition set to e.g. 'Unabridged', pattern '{title}{edition_suffix}' -> expect 'Some Title (Unabridged)'.
- internal/organizer/pathbuild_test.go — TestBuildPath_EditionSuffix_Empty: PathVars with Edition == "", same pattern -> expect exactly 'Some Title' with no dangling space or empty parens (this is the anti-over-suppression / regression test — it is what proves the token collapses cleanly instead of reproducing the {edition} raw-value bug the item complains about).

Anti-over-suppression test: `TestBuildPath_EditionSuffix_Empty` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/organizer/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/organizer/... -run TestBuildPath_EditionSuffix passes
- [ ] grep -n 'edition_suffix' internal/organizer/pathbuild.go returns hits for both the out[] assignment and the doc comment
- [ ] make ci passes
- [ ] Anti-over-suppression test: `TestBuildPath_EditionSuffix_Empty` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/organizer/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_organize_122.md`.

## Commit message

```
fix(organize): Add an {edition_suffix} folder-pattern token (TODO L5021)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — `grep -n 'edition_suffix' internal/organizer/pathbuild.go returns hits for both the out[] assignment and the doc comment` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

The item's own text calls this 'deliberately deferred... an ergonomics fix, not a correctness one' (discussed 2026-08-17) — flagging for the coordinator that this is real, well-specified, low-risk work, but was explicitly deprioritized relative to correctness fixes elsewhere in this scope (e.g. L4919, L5290). Not covered by any of the 14 owner decisions in SCOUT-INSTRUCTIONS.md, so verdict is 'actionable' rather than 'parked' — but sequence it behind higher-priority items unless told otherwise.
