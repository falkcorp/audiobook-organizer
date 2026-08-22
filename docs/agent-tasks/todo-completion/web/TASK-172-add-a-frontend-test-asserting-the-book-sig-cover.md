<!-- file: docs/agent-tasks/todo-completion/web/TASK-172-add-a-frontend-test-asserting-the-book-sig-cover.md -->
<!-- version: 1.0.0 -->
<!-- guid: ca20ab27-078e-4058-8e2e-5fcc8c6af54e -->
<!-- last-edited: 2026-08-21 -->

# TASK-172 — Add a frontend test asserting the book-sig coverage % badge renders (TODO.md L10586)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · web subagent · **Why:** mechanical: one component render test with two data variants · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 10586 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Fingerprint UI verifications ×2** (H1:1383-1384)" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-14.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-172-add-a-frontend-test-asserting-the-book-sig-cover" -b agent/web-172-add-a-frontend-test-asserting-the-book-sig-cover origin/main
cd "$REPO/.worktrees/web-172-add-a-frontend-test-asserting-the-book-sig-cover"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add/extend web/src/components/dedup/DedupEmbeddingTab.test.tsx with a test that renders the component with a book whose book_sig_coverage_pct is e.g. 62, and asserts a 'partial fp 62%' chip/label is present in the DOM; and a companion test with book_sig_coverage_pct == 100 (or null) asserting the badge is absent.

## Background (verify before editing)

- The render logic is a conditional: `book.book_sig_coverage_pct != null && book.book_sig_coverage_pct < 100` renders a Tooltip+Chip labeled `partial fp ${pct}%` (DedupEmbeddingTab.tsx:783-788).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "book_sig_coverage_pct" web/src/components/dedup/DedupEmbeddingTab.tsx   # ≥1 hit ~L783 — The coverage badge render code exists
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Locate or create web/src/components/dedup/DedupEmbeddingTab.test.tsx following this repo's existing Vitest + Testing Library conventions (check a sibling test file in the same directory for the render-with-props pattern).
2. Add TestBookSigCoverage_RendersPartialBadge: render DedupEmbeddingTab with a candidate book having book_sig_coverage_pct=62, assert getByText(/partial fp 62%/) is present.
3. Add TestBookSigCoverage_HidesBadgeAtFullCoverage: render with book_sig_coverage_pct=100 (and separately with null/undefined), assert the partial-fp text is NOT present.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_172.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- book_sig_coverage_pct exactly 100 must NOT show the badge (boundary condition per the `< 100` check).
- book_sig_coverage_pct undefined/null must not throw or show the badge.

## Tests

- web/src/components/dedup/DedupEmbeddingTab.test.tsx: the two tests described above.

Anti-over-suppression test: `TestBookSigCoverage_HidesBadgeAtFullCoverage is the anti-suppression companion to the partial-badge test — proves the badge doesn't always render.` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npm --prefix web run test -- DedupEmbeddingTab` passes with both new tests green.
- [ ] Anti-over-suppression test: `TestBookSigCoverage_HidesBadgeAtFullCoverage is the anti-suppression companion to the partial-badge test — proves the badge doesn't always render.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_172.md`.

## Commit message

```
feat(web): Add a frontend test asserting the book-sig coverage % badge  (TODO L10586)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (``npm --prefix web run test -- DedupEmbeddingTab` passes with both new tests green.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Part 1 of 2 for TODO item 23; Part 2 is the live-prod verification, filed separately as prod_run.
