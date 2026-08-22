<!-- file: docs/agent-tasks/todo-completion/docs/TASK-054-re-verify-docs-reference-abs-target-client-contr.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5d2c5d26-51ac-4b9d-b5c8-826b02e67710 -->
<!-- last-edited: 2026-08-21 -->

# TASK-054 — Re-verify docs/reference/abs-target-client-contract.md §11's 'safe to stub' list — playlists AND collections are now both falsified (TODO.md L497)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · docs subagent · **Why:** Requires re-checking each §11 entry (not just the 3 already known-stale) against real app/client behavior per the TODO's own instruction ('re-check every other entry in that list against real app behaviour rather than against the corpus') — a genuine verification pass, not a mechanical text edit, even though the edit itself is small. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 497 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`docs/reference/abs-target-client-contract.md` §" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-01.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/docs-054-re-verify-docs-reference-abs-target-client-contr" -b agent/docs-054-re-verify-docs-reference-abs-target-client-contr origin/main
cd "$REPO/.worktrees/docs-054-re-verify-docs-reference-abs-target-client-contr"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Update §11 of docs/reference/abs-target-client-contract.md to remove playlists and collections from 'safe to stub' (both are now real, fully-implemented features per items L468 and the playlists fix), re-verify 'series detail' and 'authors' against actual current behavior (not just the fixture corpus, which the TODO notes proves absence-of-evidence rather than evidence-of-absence), and re-check every OTHER entry in the full §11 list the same way.

## Background (verify before editing)

- The TODO's root insight generalizes beyond playlists: 'The §11 list rests on the same fixture corpus that contains zero playlist requests — absence there bounds what the fixtures prove, never what the client does.' The same reasoning applies to collections (also fixed since) and potentially other §11 entries never audited against real app behavior.
- This doc entry being stale is now proven THREE ways independently in this same scope (L468 for collections, L484 for series, and the wire_abs_routes.go playlists comment) — strong convergent evidence this doc genuinely needs the full re-check the TODO asks for, not just a spot patch.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'Safe to stub' -A 1 docs/reference/abs-target-client-contract.md   # 1 hit listing all 4 names — the doc still lists playlists, collections, series detail, authors as safe to stub
  grep -n 'last-edited' docs/reference/abs-target-client-contract.md   # 2026-08-11 — the doc has not been edited since the 2026-08-11 audit, despite the playlists/collections/series fixes landing 2026-08-13/2026-08-16
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Read the full §11 list in docs/reference/abs-target-client-contract.md (lines ~488-545).
2. Remove 'playlists' and 'collections' from the 'safe to stub' line, citing the fixes (playlists: 2026-08-13, wire_abs_routes.go; collections: 2026-08-16, handlers/abs/collections.go).
3. For 'series detail' and 'authors' (the two remaining entries) and every other §11 claim, re-verify against real registered routes/handlers (not the fixture corpus) whether each is still genuinely unimplemented — use the same method as this scope's L468/L484 investigations (grep for a real handler/store, not fixture presence).
4. Update the doc's last-edited date and version header per this repo's mandatory file-header convention.
5. Add a short doc note explaining WHY this drifted (fixtures proving absence-of-evidence, not evidence-of-absence) so future audits don't repeat the same trap — cross-reference the TODO's own framing.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_docs_054.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- 'series detail' specifically may still be genuinely stubbed even though the series LIST was fixed (L484) — list-level pagination and detail-route implementation are different pieces of work; verify detail specifically, don't assume the list fix covers it.

## Tests

- N/A — docs only, but step 3's verification should cite real grep evidence for each remaining §11 entry in the doc's own text or an accompanying commit message, not just be asserted.

Anti-over-suppression: N/A

## How to test

```bash
git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] docs/reference/abs-target-client-contract.md's §11 no longer lists playlists or collections as safe to stub, and every remaining entry has been re-verified against real routes/handlers rather than left as an artifact of the fixture corpus.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `git diff --check && grep -L 'last-edited: ' $(git diff --name-only origin/main -- '*.md' '*.yml' '*.py' '*.sh') ; echo 'docs/tooling task: header check only'` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_docs_054.md`.

## Commit message

```
refactor(docs): Re-verify docs/reference/abs-target-client-contract.md §11's (TODO L497)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Depends on L468 and L484's findings in this same scope for two of the four corrections; the other two (series detail, authors) need fresh verification not yet done by this scout.
