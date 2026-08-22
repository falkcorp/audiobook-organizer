<!-- file: docs/agent-tasks/todo-completion/web/TASK-161-strip-dedup-and-metadata-source-namespaces-from-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5cddf90c-ff71-4e41-a3db-8d0f011d6d5e -->
<!-- last-edited: 2026-08-21 -->

# TASK-161 — Strip dedup:* and metadata:source:* namespaces from Browse by Tag widget (TODO.md L1350)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Small, self-contained frontend filter/format change with clear before/after examples given in the TODO item. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 1350 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🏷️ **\"Browse by Tag\" surfaces internal bookkeeping" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-03.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-161-strip-dedup-and-metadata-source-namespaces-from-" -b agent/web-161-strip-dedup-and-metadata-source-namespaces-from- origin/main
cd "$REPO/.worktrees/web-161-strip-dedup-and-metadata-source-namespaces-from-"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In the Browse by Tag widget, hide tags in the `dedup:*` and `metadata:source:*` namespaces entirely from display (they remain in the data and remain searchable/filterable elsewhere — this is display-only, per the owner's explicit 'hide is fine' allowance), while genuine subject tags like `science fiction & fantasy` continue to render normally.

## Background (verify before editing)

- Owner explicitly said hiding (not deleting) is an acceptable implementation for metadata:source:* and its siblings.
- dedup:duration-match is pure internal bookkeeping the owner said nobody browses by.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'renderChip\|label=' web/src/components/library/TagCloud.tsx   # renderChip builds label from `${t.tag} (${t.count})` verbatim, no filter step before sortedTags/previewTags — TagCloud renders every tag with no namespace filtering
  grep -n 'availableTags' web/src/pages/Library.tsx   # destructured from useLibraryFilters(...) at ~L210 and passed directly as a prop at L2215/L2237 — availableTags is passed straight through from a hook, not filtered in Library.tsx
  grep -n 'dedup:duration-match\|dedup:duration-abridged' internal/dedup/engine.go internal/dedup/collectors_metadata.go   # multiple hits in both files — dedup:duration-match / dedup:duration-abridged are the tag writers matching the reported chip text
  grep -n 'metadata:source:' internal/metafetch/service_apply.go   # hits at L677 and L813-822 — metadata:source:* is the tag writer matching the reported chip text
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Find or add a small pure function, e.g. `isHiddenTagNamespace(tag: string): boolean`, that returns true for any tag starting with `dedup:` or `metadata:source:`. Put it in web/src/components/library/TagCloud.tsx near the top (or in a shared lib file if one exists for tag utilities — check `grep -rn 'namespace\|tag.*prefix' web/src/lib/*.ts` first and reuse if found).
2. In TagCloud.tsx, filter `availableTags` through this predicate before computing `sortedTags`/`maxCount`/`previewTags` — i.e. `const visibleTags = availableTags.filter(t => !isHiddenTagNamespace(t.tag));` and use `visibleTags` everywhere `availableTags` is currently used inside the component body (sortedTags, maxCount, previewTags, the `if (availableTags.length === 0) return null` guard, and the header count `sortedTags.length`).
3. Confirm hidden tags remain usable elsewhere: they must still appear in FilterSidebar or wherever else tags are selectable outside this widget (check `grep -rln availableTags web/src/components` for other consumers) — do NOT filter at the data-fetch/hook level (useLibraryFilters), only inside TagCloud's own rendering, so other consumers of the same `availableTags` array are unaffected.
4. If a currently-SELECTED tag is in a hidden namespace (e.g. a saved filter using `dedup:duration-match`), it must still show as a way to clear it — reuse the existing `selectedOutside` logic (TagCloud.tsx ~L86-93) which already handles 'selected but not in the visible preview'; verify it still works when the tag is hidden by namespace rather than just off the top-N list.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_161.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A tag that is BOTH a hidden namespace AND currently selected must not silently vanish with no way to deselect it (see step 4).

## Tests

- web/src/components/library/TagCloud.test.tsx — add a test: given availableTags containing a `dedup:duration-match` entry and a `science fiction & fantasy` entry, only the latter renders as a chip.
- Anti-suppression twin: given a SELECTED `dedup:duration-match` tag (selectedTags includes it) even though it's in a hidden namespace, assert its chip (or an equivalent clear-filter affordance) still renders — proves the hide rule doesn't strand an active filter with no visible control to clear it.

Anti-over-suppression test: `TagCloud hides a namespace only from THIS widget's chips, never from the underlying tag data or other filter UIs — verified by the 'other consumers unaffected' check in step 3, and by the selected-but-hidden test above.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npm --prefix web test -- TagCloud` passes.
- [ ] Manual: on a library with dedup:* tags, Browse by Tag shows no dedup:* chips at any expansion state.
- [ ] Anti-over-suppression test: `TagCloud hides a namespace only from THIS widget's chips, never from the underlying tag data or other filter UIs — verified by the 'other consumers unaffected' check in step 3, and by the selected-but-hidden test above.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_161.md`.

## Commit message

```
refactor(web): Strip dedup:* and metadata:source:* namespaces from Browse b (TODO L1350)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Combine with part 2 (metadata: prefix reformat) in the same PR since both touch the same renderChip function and same file.
