&lt;!-- file: docs/agent-tasks/dedup-dataset/TASK-02-sibling-parts-relation.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: dbfeffe1-e732-489c-a093-5360752cf3e2 --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# TASK-02 — sibling_parts folder relation in folderRelation (C5-folder)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku · go-backend subagent · **Depends on:** none (but run in Wave 2, after TASK-01 merges — see orchestration.md for why)

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dd-sibling-parts-relation" -b agent/dd-sibling-parts-relation origin/main
cd "$REPO/.worktrees/dd-sibling-parts-relation"
git rebase origin/main
```
Do all work inside that worktree. Never edit `main` or the primary checkout.
Before starting, make sure `origin/main` already includes TASK-01's merged
change to `signatureRelation` in the same file (`git log --oneline -5 -- internal/dedup/dataset/builder.go`) — this task edits a different function
(`folderRelation`) but both live in `builder.go`, so rebasing onto the latest
`main` first avoids an avoidable conflict.

## Goal

`folderRelation` in `internal/dedup/dataset/builder.go` classifies how two book
files' parent directories relate, but it currently has no case for "same parent
directory as each other's *sibling*, but that parent directory is not an
ancestor relationship between the two files themselves" — actually more
precisely: it returns `same_dir` when both files share the exact same parent
directory, but has no case for **two files that are each other's siblings at a
higher level** (e.g. `.../Book Title/Part 1/file.m4b` and
`.../Book Title/Part 2/file.m4b` — different immediate parent dirs, but those
parent dirs share a common parent, and neither is an ancestor of the other).
Add the `sibling_parts` case for exactly this pattern.

## Background (verify before editing — line numbers drift)

- `internal/dedup/dataset/builder.go`, function `folderRelation` (around lines
  148-166 as of this writing). The doc comment says:
  `"Note: currently produces only these four values; sibling_parts is planned
  but not yet returned."`
- Existing values: `unrelated`, `same_dir`, `a_ancestor_of_b`, `b_ancestor_of_a`.
- There is already an `isAncestor(anc, desc string) bool` helper right below
  `folderRelation` — reuse it, do not duplicate its logic.

Run these to confirm the current state before editing:
```bash
grep -n "func folderRelation\|func isAncestor" internal/dedup/dataset/builder.go
sed -n '145,175p' internal/dedup/dataset/builder.go
```

## Step-by-step

1. In `folderRelation`, after the existing `same_dir` check and the two
   `isAncestor` checks (which cover `a_ancestor_of_b` / `b_ancestor_of_a`), add
   a new check: compute `filepath.Dir(da)` and `filepath.Dir(db)` (the parents
   of the two files' own parent directories). If those two grandparent
   directories are equal AND `da != db` (already guaranteed at this point since
   `same_dir` didn't match) AND neither `isAncestor(da, db)` nor
   `isAncestor(db, da)` holds (already guaranteed since those checks fell
   through), return `sibling_parts`.
2. Mirror the existing branch style exactly — same variable names (`da`, `db`),
   same early-return pattern, no added imports beyond what's already used
   (`filepath` and `strings` are already imported).
3. Update the function's doc comment to list all five values
   (`unrelated`, `same_dir`, `a_ancestor_of_b`, `b_ancestor_of_a`,
   `sibling_parts`) and remove the "planned but not yet returned" sentence.
4. Bump the file header `version` (increment patch) and `last-edited`.

## How to test

Add test cases to `internal/dedup/dataset/builder_test.go` (create it if it
doesn't exist yet — check with `ls internal/dedup/dataset/*_test.go`; if
TASK-01 already created this file and merged first, just add cases to it).
Cover: `.../Book/Part 1/a.m4b` vs `.../Book/Part 2/b.m4b` → `sibling_parts`;
keep existing `same_dir` / `a_ancestor_of_b` / `b_ancestor_of_a` / `unrelated`
cases passing unchanged.

```bash
go build ./...
go test ./internal/dedup/... ./internal/database/... -count=1
go vet ./internal/dedup/...
```

## Acceptance criteria

- [ ] `folderRelation` returns `sibling_parts` for two files whose parent
      directories are distinct siblings under a common grandparent directory.
- [ ] All four pre-existing return values (`unrelated`, `same_dir`,
      `a_ancestor_of_b`, `b_ancestor_of_a`) are unchanged for their existing
      test cases.
- [ ] Doc comment updated to list all five values; "planned but not yet
      returned" sentence removed.
- [ ] New test case added and passing.
- [ ] `go test ./internal/dedup/... ./internal/database/... -count=1` passes;
      `go vet ./internal/dedup/...` clean.
- [ ] File headers bumped.

## Commit message
```
feat(dedup): add sibling_parts case to folderRelation (C5-folder)

folderRelation had no case for two files whose parent directories are
siblings under a common grandparent (e.g. .../Book/Part 1/ vs .../Book/Part 2/).
Add sibling_parts, mirroring the existing branch style, to close a gap that
was explicitly called out as planned-but-unreturned.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/dd-sibling-parts-relation
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency: if `folderRelation` already returns `sibling_parts` and the doc
comment no longer says "planned but not yet returned", this task is already
done — verify a test case exists and stop.

Rollback: revert the single commit; the added branch is purely additive and
sits after all pre-existing checks, so removing it restores prior behavior
exactly.