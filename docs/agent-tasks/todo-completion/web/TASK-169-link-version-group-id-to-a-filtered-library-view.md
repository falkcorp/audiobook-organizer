<!-- file: docs/agent-tasks/todo-completion/web/TASK-169-link-version-group-id-to-a-filtered-library-view.md -->
<!-- version: 1.0.0 -->
<!-- guid: eb8dbcd7-7d7b-4595-9caf-570d0d87c771 -->
<!-- last-edited: 2026-08-21 -->

# TASK-169 — Link version_group_id to a filtered library view (now unblocked — the filter works as of commit b0ebccb0) (TODO.md L3168)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Small, well-scoped link addition now that the backend filter is confirmed working; the main remaining work is confirming BookDetailVersionGroup.tsx already surfaces enough of the group's other members that a link is additive rather than a full new UI section. · **Depends on:** none · **Wave:** 6

Source: `TODO.md` line 3168 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**⚠️ Do not link `version_group_id` to a filtered " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-169-link-version-group-id-to-a-filtered-library-view" -b agent/web-169-link-version-group-id-to-a-filtered-library-view origin/main
cd "$REPO/.worktrees/web-169-link-version-group-id-to-a-filtered-library-view"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add an '(other versions of this book)' link on the book detail page's version-group section, targeting `/library?filters=[{\"field\":\"version_group_id\",\"value\":\"<vg-id>\",\"negated\":false}]`, using the same filters=JSON mechanism as the narrator/publisher/genre links in todo_line 3164 (version_group_id is a plain field-filter, not a dedicated int param like author_id/series_id).

## Background (verify before editing)

- This was explicitly blocked in the source TODO pending the filter working — the same silent-ignore bug is tracked at todo_line 3356 in this same document set, and dedicated evidence (commit b0ebccb0, 2026-08-14) shows it was fixed the day after these TODO items were filed (2026-08-13).
- Per the fix commit's own message: 'nil VersionGroupID matches nothing' — a standalone book (no version group) must not render this link at all, matching the conformance test's explicit ungrouped-book assertion.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n '\"version_group_id\"' internal/audiobooks/service_filtering.go   # hits at L529 (matcher case) and L654 (allFilterFieldNames) — version_group_id is now a real filter field
  git merge-base --is-ancestor b0ebccb0 8f6d0d99 && echo ancestor   # 'ancestor' printed — the fix commit is merged to the HEAD this scout ran against
  grep -n 'TestVersionGroupIDFilter_MatchesMembersOnly' internal/audiobooks/filter_field_conformance_test.go   # 1 hit, with a nested assertion 'a nil VersionGroupID must not match any group filter' — a conformance test pins the fixed semantics, including the nil-group edge case
  ```

### Reuse — don't invent

- Use `BookDetailHeader.tsx (existing component that already reads book.version_group_id — BookDetailVersionGroup.tsx, by contrast, only receives a pre-grouped Book[] and never references the field name directly)` in `web/src/components/bookdetail/BookDetailHeader.tsx` (verify: `grep -n 'version_group_id\|VersionGroupID' web/src/components/bookdetail/BookDetailHeader.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Read web/src/components/bookdetail/BookDetailVersionGroup.tsx to see what it already renders for a book that has a version_group_id (it likely already lists sibling versions inline).
2. Add a <Link> (react-router-dom) near that section, visible only when book.version_group_id is non-empty, targeting the URL-encoded filters=JSON shown in 'goal'.
3. Confirm the is_primary_version=true default filter does NOT suppress non-primary siblings on this specific link's target view — since the whole point is to see 'other versions', consider explicitly appending &is_primary_version=false or omitting the default in this one link's URL if the Library page's default would otherwise hide exactly the books this link exists to show. Check how Library.tsx applies its default before deciding.
4. Verify against a real multi-member version group in dev/test data that the link surfaces the sibling books.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_169.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Per the fix's own test, a nil/empty version_group_id must not render a link at all — do not link to filters=[{field:version_group_id,value:\"\"}], which would 400 per the existing empty-filter-value rejection (commit 27f386b2).

## Tests

- web/src/components/bookdetail/BookDetailVersionGroup.test.tsx: assert the link is absent for a book with no version_group_id, and present with the correct filters= value for one that has it.
- Reuse TestVersionGroupIDFilter_MatchesMembersOnly's fixture shape (member/other/ungrouped) as the model for what the E2E test data should look like.

Anti-over-suppression: N/A

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web run lint && npm --prefix web test passes.
- [ ] grep -n 'version_group_id' web/src/components/bookdetail/BookDetailVersionGroup.tsx returns >0 hits after the change.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_169.md`.

## Commit message

```
feat(web): Link version_group_id to a filtered library view (now unbloc (TODO L3168)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`npm --prefix web run lint && npm --prefix web test passes.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This item's own blocking condition is resolved — cross-reference todo_line 3356 in this same scope, which is separately marked stale_done for the exact fix that unblocks this one. Downgrade any earlier assumption that this stays 'parked'.
