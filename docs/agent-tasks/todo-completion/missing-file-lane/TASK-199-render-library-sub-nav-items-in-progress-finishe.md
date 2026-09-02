<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-199-render-library-sub-nav-items-in-progress-finishe.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6b44cbb3-6aa4-4e0a-8ad3-049a594a1939 -->
<!-- last-edited: 2026-09-02 -->

# TASK-199 — Render Library sub-nav items (In Progress/Finished) in collapsed-sidebar mode (TODO.md L7819)

> **Status 2026-09-02:** ✅ DONE — PR #2768 merged 2026-08-23 (d9b61a45c).

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · missing-file-lane subagent · **Why:** requires a real UI/UX decision embedded in the fix (how do 3 sub-items appear under one collapsed icon -- a flyout menu, an auto-expand-on-hover, or simply keeping the sidebar from collapsing while on a Library sub-route) plus MUI Tooltip/Menu composition, not a pure mechanical change · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 7819 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Fix the Library \"In Progress\" nav item — the sel" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-199-render-library-sub-nav-items-in-progress-finishe" -b agent/missing-file-lane-199-render-library-sub-nav-items-in-progress-finishe origin/main
cd "$REPO/.worktrees/missing-file-lane-199-render-library-sub-nav-items-in-progress-finishe"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In web/src/components/layout/Sidebar.tsx's collapsed-sidebar branch (L173-191), give the user a way to reach the In Progress and Finished sub-routes without expanding the whole sidebar -- the simplest correct option is converting the Library icon's Tooltip into a small MUI Menu (or a submenu-on-click/hover pattern already used elsewhere in this MUI version) listing 'Library (All Books)', 'In Progress', and 'Finished', each navigating via the same handleNavigation(path) the expanded mode uses, with selection state driven by the same isSubItemSelected()-style decoded-search-param matcher #2193 introduced for the expanded mode (do not reintroduce the location.pathname-only comparison bug the original fix removed).

## Background (verify before editing)

- The two original bugs from this TODO entry (highlight never moves; click is a no-op) are ALREADY FIXED in #2193 (2026-08-08) via isSubItemSelected() (decoded search-param based selection) and lastWrittenSearch (replacing the stuck isInternalUpdate boolean) -- this item does not need to touch that logic, only reuse its selection-matching approach for the new collapsed-mode UI.
- The 'Still open' list in the TODO names 3 threads: (1) collapsed-sidebar sub-items missing entirely -- THIS item's scope; (2) the result count reflecting the fetched page instead of the whole library, explicitly marked in the TODO as 'the companion backend-filtering task, not a sidebar concern -- close it there rather than duplicating the work here' -- OUT OF SCOPE for this item, tracked elsewhere; (3) 'not verified interactively' -- folded into this item's acceptance criteria as a manual click-through, since it is cheap to do alongside this UI change and there is no separate code deliverable for it.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '173,191p' web/src/components/layout/Sidebar.tsx   # a ListItem/ListItemButton for 'Library' navigating to /library, with NO map over librarySubItems and no In Progress/Finished entries in this branch — the collapsed-sidebar branch renders only a single Library button with no sub-item list
  sed -n '192,220p' web/src/components/layout/Sidebar.tsx   # shows the Collapse-wrapped librarySubItems.map(...) starting around L213-215, only reached in the else branch of the isCollapsed ternary — the expanded branch is where the sub-items actually render, gated behind !isCollapsed
  git show 46628240:TODO.md | grep -n 'Shipped in #2193'   # 1 hit confirming the fix landed, with 'isSubItemSelected()' and 'lastWrittenSearch' named as the mechanisms — the two original root-cause bugs this entry originally reported are already fixed and covered by tests, so this item is scoped ONLY to the collapsed-mode gap, not a re-fix of the highlight/click bugs
  ```

### Reuse — don't invent

- Use `librarySubItems (the same array already used by the expanded-mode Collapse block)` in `web/src/components/layout/Sidebar.tsx` (verify: `grep -n 'librarySubItems' web/src/components/layout/Sidebar.tsx`) — do NOT write a parallel helper.
- Use `isSubItemSelected() -- the already-fixed, decoded-search-param-based selection matcher from #2193, must be reused for any collapsed-mode sub-item selection state too` in `web/src/components/layout/Sidebar.tsx` (verify: `grep -n 'isSubItemSelected' web/src/components/layout/Sidebar.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Read librarySubItems' declaration at web/src/components/layout/Sidebar.tsx:51 to get its exact shape (text/icon/path/matchPath/matchSearch). NOTE: it already contains an 'All Books' entry pointing at /library?reset=1 - do NOT add a second one.
2. In the isCollapsed branch (Sidebar.tsx:173-191), replace the single static Tooltip+ListItemButton with a stateful menu: track an anchorEl (const [libraryMenuAnchor, setLibraryMenuAnchor] = useState<null | HTMLElement>(null)), open it on click of the Library icon instead of navigating immediately, and render an MUI Menu with exactly one MenuItem per librarySubItems entry, each calling handleNavigation(item.path) and then closing the menu.
3. Drive each MenuItem's selected state with the imported isSubItemSelected(item, location.pathname, location.search) - it is imported from './sidebarSelection' at Sidebar.tsx:39 and already used by the expanded branch at Sidebar.tsx:219. Do not reimplement it and never compare raw pathname/search strings.
4. LOCKED UX (do not re-decide): in collapsed mode a single click on the Library icon OPENS THE MENU; it does not navigate. The existing Tooltip('Library') stays for hover. The icon's broader selected= state (Sidebar.tsx:177-182, matching /library, /fingerprints, /series, /authors) is preserved unchanged.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_199.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A user on a Library sub-route (e.g. already viewing In Progress) who then collapses the sidebar -- the collapsed Library icon's selected= state (L177-182, matching on /library, /fingerprints, /series, /authors path prefixes) must still show as selected even though it can no longer show WHICH sub-item is active without opening the menu; do not regress this broader 'on some Library-family page' indicator while adding the submenu.
- Do not reintroduce the original location.pathname-only comparison bug (the root cause #2193 fixed) anywhere in the new collapsed-mode selection logic -- always compare against the decoded search param via the reused matcher, never a raw pathname/search string.

## Tests

- w
- e
- b
- /
- s
- r
- c
- /
- c
- o
- m
- p
- o
- n
- e
- n
- t
- s
- /
- l
- a
- y
- o
- u
- t
- /
- S
- i
- d
- e
- b
- a
- r
- .
- t
- e
- s
- t
- .
- t
- s
- x
-  
- (
- N
- E
- W
-  
- F
- I
- L
- E
-  
- -
-  
- n
- o
-  
- c
- o
- m
- p
- o
- n
- e
- n
- t
-  
- r
- e
- n
- d
- e
- r
-  
- t
- e
- s
- t
-  
- f
- o
- r
-  
- S
- i
- d
- e
- b
- a
- r
-  
- e
- x
- i
- s
- t
- s
-  
- t
- o
- d
- a
- y
- ;
-  
- t
- h
- e
-  
- 1
- 1
- -
- c
- a
- s
- e
-  
- #
- 2
- 1
- 9
- 3
-  
- s
- u
- i
- t
- e
-  
- i
- s
-  
- w
- e
- b
- /
- s
- r
- c
- /
- c
- o
- m
- p
- o
- n
- e
- n
- t
- s
- /
- l
- a
- y
- o
- u
- t
- /
- s
- i
- d
- e
- b
- a
- r
- S
- e
- l
- e
- c
- t
- i
- o
- n
- .
- t
- e
- s
- t
- .
- t
- s
- ,
-  
- a
-  
- p
- u
- r
- e
- -
- f
- u
- n
- c
- t
- i
- o
- n
-  
- t
- e
- s
- t
-  
- o
- v
- e
- r
-  
- i
- s
- S
- u
- b
- I
- t
- e
- m
- S
- e
- l
- e
- c
- t
- e
- d
- ,
-  
- a
- n
- d
-  
- c
- a
- n
- n
- o
- t
-  
- s
- i
- m
- p
- l
- y
-  
- b
- e
-  
- e
- x
- t
- e
- n
- d
- e
- d
- )
- .
-  
- R
- e
- n
- d
- e
- r
-  
- <
- S
- i
- d
- e
- b
- a
- r
-  
- o
- p
- e
- n
-  
- o
- n
- C
- l
- o
- s
- e
- =
- {
- .
- .
- .
- }
-  
- d
- r
- a
- w
- e
- r
- W
- i
- d
- t
- h
- =
- {
- 2
- 4
- 0
- }
-  
- c
- o
- l
- l
- a
- p
- s
- e
- d
-  
- /
- >
-  
- (
- S
- i
- d
- e
- b
- a
- r
- P
- r
- o
- p
- s
-  
- i
- s
-  
- d
- e
- c
- l
- a
- r
- e
- d
-  
- a
- t
-  
- w
- e
- b
- /
- s
- r
- c
- /
- c
- o
- m
- p
- o
- n
- e
- n
- t
- s
- /
- l
- a
- y
- o
- u
- t
- /
- S
- i
- d
- e
- b
- a
- r
- .
- t
- s
- x
- :
- 4
- 2
- -
- 4
- 8
-  
- a
- n
- d
-  
- `
- c
- o
- l
- l
- a
- p
- s
- e
- d
- `
-  
- d
- e
- f
- a
- u
- l
- t
- s
-  
- t
- o
-  
- f
- a
- l
- s
- e
-  
- a
- t
-  
- L
- 1
- 0
- 4
- )
- ,
-  
- o
- p
- e
- n
-  
- t
- h
- e
-  
- L
- i
- b
- r
- a
- r
- y
-  
- m
- e
- n
- u
- ,
-  
- a
- s
- s
- e
- r
- t
-  
- '
- I
- n
-  
- P
- r
- o
- g
- r
- e
- s
- s
- '
-  
- a
- n
- d
-  
- '
- F
- i
- n
- i
- s
- h
- e
- d
- '
-  
- a
- r
- e
-  
- p
- r
- e
- s
- e
- n
- t
-  
- a
- n
- d
-  
- e
- a
- c
- h
-  
- c
- l
- i
- c
- k
-  
- n
- a
- v
- i
- g
- a
- t
- e
- s
-  
- t
- o
-  
- t
- h
- e
-  
- e
- x
- p
- e
- c
- t
- e
- d
-  
- p
- a
- t
- h
- .
-  
- A
- d
- d
-  
- a
-  
- s
- e
- c
- o
- n
- d
-  
- c
- a
- s
- e
-  
- a
- s
- s
- e
- r
- t
- i
- n
- g
-  
- c
- o
- l
- l
- a
- p
- s
- e
- d
- -
- m
- o
- d
- e
-  
- s
- e
- l
- e
- c
- t
- i
- o
- n
-  
- h
- i
- g
- h
- l
- i
- g
- h
- t
- i
- n
- g
-  
- a
- g
- r
- e
- e
- s
-  
- w
- i
- t
- h
-  
- e
- x
- p
- a
- n
- d
- e
- d
- -
- m
- o
- d
- e
-  
- h
- i
- g
- h
- l
- i
- g
- h
- t
- i
- n
- g
-  
- f
- o
- r
-  
- t
- h
- e
-  
- s
- a
- m
- e
-  
- U
- R
- L
- ,
-  
- d
- r
- i
- v
- e
- n
-  
- b
- y
-  
- t
- h
- e
-  
- s
- a
- m
- e
-  
- i
- s
- S
- u
- b
- I
- t
- e
- m
- S
- e
- l
- e
- c
- t
- e
- d
-  
- i
- m
- p
- o
- r
- t
- .

Anti-over-suppression test: `N/A -- this is a missing-UI-affordance fix, not a filter/guard/skip` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] npm --prefix web test -- Sidebar passes including the new collapsed-mode test(s)
- [ ] manual interactive check (the still-open item 3 from the TODO): with the sidebar collapsed, In Progress and Finished are each reachable and correctly highlight when active, both when arriving fresh (hard refresh) and when navigating from Dashboard
- [ ] npx tsc --noEmit (or the project's equivalent) and eslint report no new errors in Sidebar.tsx
- [ ] Anti-over-suppression test: `N/A -- this is a missing-UI-affordance fix, not a filter/guard/skip` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_199.md`.

## Commit message

```
feat(missing-file-lane): Render Library sub-nav items (In Progress/Finished) in colla (TODO L7819)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run sed -n '173,191p' web/src/components/layout/Sidebar.tsx` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Scoped ONLY to the collapsed-sidebar gap (still-open thread 1 of 3). Thread 2 (result count reflects page not whole library) is explicitly redirected by the TODO's own text to 'the companion backend-filtering task' and must NOT be duplicated here -- if that task is not separately captured elsewhere in this scope run, flag it to the coordinator rather than folding it into this item's scope. Thread 3 (not verified interactively) is folded into this item's acceptance criteria as a manual check rather than spawned as separate work, since it has no code deliverable of its own.
