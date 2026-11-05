<!-- file: docs/agent-tasks/platform-core/R1-29-react-router-8-import-swap.md -->
<!-- version: 1.1.2 -->
<!-- guid: ce2b7186-99af-4660-bb86-0490806511db -->
<!-- last-edited: 2026-11-05 -->

# R1-29 — react-router-dom 7 to react-router 8 with the declarative BrowserRouter unchanged: an import swap only

| Field | Value |
|---|---|
| ID | `R1-29` |
| Tier | **T1** (Sonnet / medium): an import swap over 122 files, checked by one grep |
| Release / wave | R1 / W12 |
| Depends on | R1-05 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 07 §7.19 step 1; 13 §13.4.1 (exception E4 ends); 09 §9.4 R1 Content: UI |
| Branch | `agent/platform-core-r1-29-react-router-8-import-swap` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `web/package.json`
- `web/package-lock.json`
- `web/src/`
- `web/tests/`
- `web/vite.config.ts`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-29" -b agent/platform-core-r1-29-react-router-8-import-swap origin/main
cd "$REPO/.worktrees/platform-core-r1-29"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Move ABO's web client from `react-router-dom` 7 to `react-router` 8 with the declarative `<BrowserRouter>`
unchanged: an import swap only (07 §7.19 step 1; 13 §13.4.1 exception E4, whose exit condition this task
reaches; 09 §9.4 R1 "Content: UI"). react-router 8 removed the `react-router-dom` package: every name ABO
imports comes from `react-router`; only the DOM-only router components live in `react-router/dom`
(13 §13.4.1 E4). The target range is `react-router@^8.5.1` (13 §13.4 table). No data router, no
`createShell`, no test-harness rewrite: those are 07 §7.19 steps 4–5.

Done when (the gate test this task owns, 09 §9.4 R1 Verification "The UI steps"):

```bash
! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts
```

## Dependency check

R1-05 (one web build entry point) is merged: `test -f scripts/ci/build_web.sh && echo ok` prints `ok`.
If not, STOP.

## Re-verify (counts at `f7ac5b11e`)

```bash
grep -n '"react-router' web/package.json
#   expect 1 line: "react-router-dom": "^7.18.5",
git grep -l 'react-router-dom' -- web/src web/tests | wc -l
#   expect 126 (125 under web/src, 1 under web/tests); 07 counts 122 with a narrower grep (from-imports only)
git grep -c 'react-router-dom' -- web/src web/tests | awk -F: '{s+=$2} END {print s}'
#   expect 135 occurrences (122 `from '...'`, 6 `import('...')`, 5 `vi.mock('...'`, 1 importActual argument, 1 comment)
git grep -n '"react-router-dom"' -- web/src web/tests | wc -l
#   expect 0 (every occurrence uses single quotes or is a comment)
grep -n 'react-router-dom' web/vite.config.ts
#   expect 1 line (83 at f7ac5b11e; 07 cites 81-82 at 748fd787a): the vendor chunk regex
grep -c '"node_modules/react-router-dom"' web/package-lock.json
#   expect 1
```

If a count differs, use the output of the `git grep -l` command at your base as the file list for step 3;
do not hand-pick files. If `web/package.json` already has no `react-router-dom`, the task is applied: run
the acceptance block instead.

## Steps

1. Dependencies (from the worktree root):

   ```bash
   cd web
   npm install --save react-router@^8.5.1
   npm uninstall react-router-dom
   cd ..
   ```

   `web/package.json` now lists `"react-router": "^8.5.1"` (or the caret range npm writes for the newest
   8.x it resolved, which must be ≥ 8.5.1) in `dependencies` and no `react-router-dom`; `web/package-lock.json`
   has no `node_modules/react-router-dom` entry. react-router 8 needs Node ≥ 22.22.0 (13 §13.4 table); if
   `node --version` is lower, STOP.
2. Chunk regex in `web/vite.config.ts`: change
   `(react|react-dom|react-is|scheduler|react-router|react-router-dom)` to
   `(react|react-dom|react-is|scheduler|react-router)`. Nothing else in the file changes.
3. Import swap, every file at once:

   ```bash
   git grep -l 'react-router-dom' -- web/src web/tests | xargs sed -i 's/react-router-dom/react-router/g'
   ```

   This rewrites the `from`, `import(...)`, `vi.mock(...)` and `importActual(...)` specifiers and the one
   comment in `web/tests/unit/BookDetail.test.tsx`.
4. Type-check: `npm run typecheck --prefix web`. For each error of the form "Module '\"react-router\"' has
   no exported member 'X'", change that one import of `X` to come from `'react-router/dom'` instead
   (13 §13.4.1 E4: DOM bits from `react-router/dom`). Any other new type error (a changed signature, a
   removed hook) is not an import swap: STOP and report the error text; do not rewrite router code.
5. Header bump on every file you changed (126 source and test files, `web/vite.config.ts`,
   `web/package.json` has no header). Use this helper once over the changed list:

   ```bash
   git diff --name-only -- web/src web/tests web/vite.config.ts | python3 -c '
   import re,sys,datetime
   d=datetime.date.today().isoformat()
   for f in sys.stdin.read().split():
       s=open(f).read()
       s=re.sub(r"^(// version: )(\d+)\.(\d+)\.(\d+)",lambda m:f"{m.group(1)}{m.group(2)}.{m.group(3)}.{int(m.group(4))+1}",s,count=1,flags=re.M)
       s=re.sub(r"^(// last-edited: )\d{4}-\d{2}-\d{2}",lambda m:m.group(1)+d,s,count=1,flags=re.M)
       open(f,"w").write(s)'
   ```

6. Run the frontend gates and the e2e suite locally (acceptance). Add a headerless fragment
   `changelog.d/<today>_react_router_8.md` (`### Changed`: "The web client uses react-router 8; routing is
   unchanged.").
7. Commit: `refactor(web): react-router-dom 7 to react-router 8, import swap only`.

## Code contract

No exported symbol changes. `<BrowserRouter>` stays the router in `web/src/main.tsx`/`App.tsx`; no
`createBrowserRouter`, `RouterProvider` or data API is introduced.

## Tests

No new test: the existing vitest and Playwright suites are the proof that routing is unchanged, and the grep
above is the gate.

## Acceptance

```bash
! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts && echo GONE   # expect GONE
grep -c '"node_modules/react-router-dom"' web/package-lock.json                                # expect 0
node -e "const v=require('./web/node_modules/react-router/package.json').version;console.log(v);process.exit(v.split('.')[0]==='8'?0:1)"   # expect 8.x.y, exit 0
npm run typecheck --prefix web          # expect exit 0
npm run lint --prefix web               # expect exit 0
npm test --prefix web -- --run          # expect every vitest file passing
make web-build                          # expect "Frontend built (web/dist)"
make test-e2e                           # expect the Playwright suite green
make ci                                 # expect exit 0 and "All CI checks passed"
```

## Out of scope

The data router, `createShell`, `renderWithProviders` rewrite, `useUnsavedChangesBlocker` (07 §7.19
steps 4–5). Any behaviour change. `core/web` (R1-30).

## Rollback

Revert the commit; `npm ci --prefix web` restores 7.18.5 from the reverted lockfile.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
