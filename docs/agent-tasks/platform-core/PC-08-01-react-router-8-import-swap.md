<!-- file: docs/agent-tasks/platform-core/PC-08-01-react-router-8-import-swap.md -->
<!-- version: 1.0.0 -->
<!-- guid: 738fc63c-349c-4696-b1d5-3274a459cc4f -->
<!-- last-edited: 2026-11-04 -->

# PC-08-01 — react-router-dom 7 to react-router 8 in ABO, import swap only

| Field | Value |
|---|---|
| ID | PC-08-01 |
| Tier | **T0 (Haiku, low).** One `npm install`, one `sed` over a fixed file list, one regex edit, one marker line. No judgement calls. |
| Wave | W08 (UI-0, release `UI0`). **First PR of UI-0: sets `release: UI0`.** |
| Depends on | OWNER-06 (the current `S2r3` soak row) |
| Trailer | `Stage-Release: UI-0 1` on every commit |
| Kind | Move PR (import-path only): announced move window (09 §9.2.10) |
| Required checks | `compat-rehearsal` (this PR changes `release:`), `extraction-gates`, `docker-image`, `vet-darwin`, `vet-integration`, the frontend jobs |
| Normative digest | `docs/plans/platform-core/digests/UI-0.md` |

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-08-01 -b refactor/pc-08-01-react-router-8 origin/main
cd "$REPO/../audiobook-organizer-pc-08-01"
git rebase origin/main
git worktree list
make web-deps
```

## Step 0 — gates (STOP if any line differs)

```bash
grep -E '^(shipped_stage|release):' ci/stage.txt        # exactly: shipped_stage: S2r3 and release: S2r3
grep -c '^S2r3 ' ci/soak.txt                             # 1
grep -rn -i 'move window' docs/plans/platform-core/stage-ledger/   # a line naming web/ and the current dates
node --version                                           # v22.22.0 or later (react-router 8's engines floor)
```

If `release:` is not `S2r3` (for example `S2br1`), STOP and report: the plan lands UI0 before the S2b flip.

## Re-verify (counts at the plan's HEAD; re-run at your base and paste both into the PR)

```bash
grep -n 'react-router' web/package.json
#   HEAD: 34:    "react-router-dom": "^7.18.5",
grep -rl 'react-router-dom' web/src web/tests | wc -l
#   HEAD: 126   (52 source files + 74 test files incl. web/src/test/renderWithProviders.tsx and web/tests/unit/BookDetail.test.tsx;
#   the spec's "122 = 52 + 70" was counted at 748fd787a with a narrower grep; 126 is the number to use)
grep -rhoE "from ['\"]react-router-dom['\"]" web/src web/tests | wc -l
#   HEAD: 122
grep -rhE "vi\.mock\(['\"]react-router-dom|import\(['\"]react-router-dom|importActual<.*react-router-dom" web/src web/tests | wc -l
#   HEAD: 11 lines (5 vi.mock calls, 2 vi.importActual, 2 importOriginal<typeof import(...)>, 2 typeof import(...).useNavigate)
grep -n 'react-router' web/vite.config.ts
#   HEAD: 83:              test: /[\\/]node_modules[\\/](react|react-dom|react-is|scheduler|react-router|react-router-dom)[\\/]/,
grep -rn 'RouterProvider\|HydratedRouter\|createBrowserRouter' web/src | wc -l
#   HEAD: 0   (so nothing needs react-router/dom)
```

The spec cites the chunk regex at `web/vite.config.ts:81-82` (748fd787a); at the plan's HEAD it is line 83.
If the source-file count is not 52 or the regex line is not the one shown, re-read the hits and continue only if
every hit is an import, a `vi.mock`, an `importActual`/`importOriginal`, or a `typeof import(...)`.

## Exact files

- `web/package.json`, `web/package-lock.json` — remove `react-router-dom`, add `"react-router": "^8.5.1"`
  (13 §13.4's range; 8.5.1 is `latest` as checked on 2026-11-04).
- Every file in `grep -rl 'react-router-dom' web/src web/tests` (126 at HEAD).
- `web/vite.config.ts` — the `vendor` chunk regex.
- `ci/stage.txt` — `release: S2r3` → `release: UI0`. `shipped_stage:` unchanged.

## Code contract

None: only module specifiers change. `<BrowserRouter>`, `MemoryRouter`, `Routes`, `Route`, `Link`, `Navigate`,
`useNavigate`, `useLocation`, `useParams`, `useSearchParams`, `useNavigationType` and the type `NavigateFunction`
(every symbol ABO imports at HEAD) are exported by `react-router` 8.5.1's main entry; nothing needs
`react-router/dom`.

## Tests

No new test. The existing vitest suite and the Playwright e2e suite are the oracle, and the grep in Acceptance
is the done-check (09 UI-0).

## Steps

1. Dependency swap (from the worktree root):

   ```bash
   cd web
   npm uninstall react-router-dom
   npm install react-router@^8.5.1
   cd ..
   grep -n '"react-router' web/package.json        # exactly one line: "react-router": "^8.5.1",
   ```
   If `npm install` reports `ERESOLVE`, STOP and paste the output.
2. Import swap (every occurrence is a module specifier):

   ```bash
   grep -rl 'react-router-dom' web/src web/tests > "$TMPDIR/rr-files.txt"
   wc -l < "$TMPDIR/rr-files.txt"                   # same number as the re-verify count
   xargs sed -i 's/react-router-dom/react-router/g' < "$TMPDIR/rr-files.txt"
   ```
   On macOS use `sed -i ''`.
3. Chunk regex in `web/vite.config.ts`: change `(react|react-dom|react-is|scheduler|react-router|react-router-dom)`
   to `(react|react-dom|react-is|scheduler|react-router)`. Nothing else in the file changes.
4. Headers: every changed `.ts`/`.tsx` file has a `// version: X.Y.Z` and `// last-edited:` line in its first
   five lines. Bump the patch number and set today's date with this script, saved outside the repo:

   ```python
   #!/usr/bin/env python3
   import datetime, re, sys
   today = datetime.date.today().isoformat()
   for path in sys.argv[1:]:
       lines = open(path).read().split("\n")
       for i in range(min(6, len(lines))):
           m = re.match(r"// version: (\d+)\.(\d+)\.(\d+)$", lines[i])
           if m:
               lines[i] = f"// version: {m[1]}.{m[2]}.{int(m[3]) + 1}"
           if lines[i].startswith("// last-edited: "):
               lines[i] = f"// last-edited: {today}"
       open(path, "w").write("\n".join(lines))
   ```
   Run: `python3 -I "$TMPDIR/bump.py" $(cat "$TMPDIR/rr-files.txt") web/vite.config.ts`.
   `package.json` and `package-lock.json` have no header.
5. Marker: edit `ci/stage.txt` so the `release:` line reads `release: UI0`; bump its header if it has one.
6. Commit once: `refactor(web): move from react-router-dom 7 to react-router 8 (import swap)` with the trailer
   lines below.
7. Run the acceptance block.

## Acceptance (09 UI-0 "Done when"; 07 §7.19 step 1)

```bash
! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts && echo OK   # prints OK
grep -c 'react-router-dom' web/package-lock.json                     # 0
grep -E '^(shipped_stage|release):' ci/stage.txt                     # shipped_stage: S2r3 / release: UI0
make web-typecheck                                                   # exit 0
make web-test                                                        # exit 0 (vitest)
make web-build                                                       # exit 0
make test-e2e                                                        # exit 0 (Playwright, chromium + webkit)
make ci                                                              # exit 0
git status --porcelain                                               # empty
```

## Out of scope

The data router, `RouterProvider`, `useBlocker` (the custom `useUnsavedChangesBlocker` stays) and the
`renderWithProviders` rewrite: S9 (07 §7.19 steps 4 and 5). `<BrowserRouter>` stays. Nothing else in `web/`
changes behaviour.

## Rollback

Binary swap (the UI is embedded per build). A revert of this commit is admitted by `check_soak.py`.

## Repo rules

- Commit body ends with:

  ```
  Stage-Release: UI-0 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- PR description lists `react-router-dom ==> react-router` and the file count; `gh pr merge <n> --rebase`;
  never squash; never `--admin`. It merges first inside its move window.
