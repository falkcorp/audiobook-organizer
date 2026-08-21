<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-106-typescript-6-0-3-7-0-2-migration-the-one-remaini.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0abe44df-468c-4a38-b7f1-0bc102b35f74 -->
<!-- last-edited: 2026-08-21 -->

# TASK-106 — TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece of the frontend-framework-versions survey) (TODO.md L8273)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** the item itself says this is 'not a version bump... budget as a migration' — a different compiler implementation with its own compatibility surface · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 8273 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Frontend framework versions — how far behind we " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-12.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-106-typescript-6-0-3-7-0-2-migration-the-one-remaini" -b agent/missing-file-lane-106-typescript-6-0-3-7-0-2-migration-the-one-remaini origin/main
cd "$REPO/.worktrees/missing-file-lane-106-typescript-6-0-3-7-0-2-migration-the-one-remaini"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Upgrade web/'s TypeScript from 6.0.3 to 7.0.2 (the native Go-compiler rewrite), fixing whatever new type-check or eslint-integration errors the rewrite surfaces, without touching MUI or React (both already done).

## Background (verify before editing)

- web/package.json already sits at react ^19.2.8, @mui/material ^9.3.1, vite ^8.2.1, eslint ^10.8.1, zustand ^5.0.15, jsdom ^30.0.1 — items 1, 2, and 4 of the item's own suggested cheapest-value-first order are ALL ALREADY DONE. Only item 3, TypeScript 7, remains.
- 🔴 The item explicitly says: 'Do not attempt any of this until the e2e suite is fixed' ([[e2e-suite-broken-on-main]]). This scout could not confirm that item's status — it is outside this scope's 26 items. TREAT AS A HARD BLOCKER until independently verified fixed.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n '"typescript":' web/package.json   # 1 hit: ^6.0.3 — typescript is ^6.0.3, not yet 7.x
  grep -nE '"(react|@mui/material|vite|eslint|zustand|jsdom)": "\^(19|9|8|10|5|30)' web/package.json   # 6 hits, one per package — react/mui/vite/eslint/zustand/jsdom are already at the versions this item names as 'latest'
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. FIRST verify the e2e suite is actually fixed: run `npm --prefix web run test:e2e:check-discovery` and `npm --prefix web run test:e2e` and confirm they collect and run real specs, not just exit 0 on zero collected tests. Do not proceed past this step until confirmed.
2. `cd web && npm install -D typescript@7.0.2`.
3. Run `npx tsc --noEmit` and `npm run build` to surface every new type error; fix the underlying type issue rather than blanket-suppressing with `// @ts-expect-error`.
4. Run `npm run lint` — typescript-eslint (^8.67.0) may need its own compatibility bump for TS7; check its peerDependencies if it errors.
5. Run the full web test suite and e2e suite again post-upgrade.
6. Bump file headers on every file actually edited to fix a type error.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_106.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- typescript-eslint may not yet support TS7's compiler API — check compatibility before upgrading and pin an explicitly-compatible version if required.

## Tests

- Rely on the existing web test suite + `npx tsc --noEmit` as the acceptance gate — this is a toolchain upgrade, not new business logic, so no new test cases are prescribed beyond what tsc/vitest/playwright already cover.

Anti-over-suppression test: `N/A — the anti-suppression risk here is specifically 'do not @ts-expect-error away real type errors', called out explicitly in step 3.` — a known-good input still passes with the new guard active.

## How to test

```bash
make ci && npm --prefix web run lint && npm --prefix web test
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] `npm --prefix web run build` exits 0 with typescript@7.0.2.
- [ ] `npm --prefix web run lint` exits 0.
- [ ] `npm --prefix web test` and `npm --prefix web run test:e2e` both exit 0.
- [ ] Anti-over-suppression test: `N/A — the anti-suppression risk here is specifically 'do not @ts-expect-error away real type errors', called out explicitly in step 3.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci && npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_106.md`.

## Commit message

```
refactor(missing-file-lane): TypeScript 6.0.3 → 7.0.2 migration (the one remaining piece  (TODO L8273)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

The item's own text lists MUI 5→9 as 'do last, purely maintenance' — but MUI is ALREADY at 9.3.1 in this repo, so that step is moot; TS7 is now the only remaining piece of the whole L8273 survey. HARD BLOCKED on the e2e suite being fixed first, per the item's own explicit instruction.
