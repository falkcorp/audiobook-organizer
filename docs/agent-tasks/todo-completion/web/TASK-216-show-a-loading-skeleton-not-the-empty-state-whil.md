<!-- file: docs/agent-tasks/todo-completion/web/TASK-216-show-a-loading-skeleton-not-the-empty-state-whil.md -->
<!-- version: 1.0.0 -->
<!-- guid: f6178aa1-2cbe-41ac-a5e5-5f86fd185710 -->
<!-- last-edited: 2026-08-21 -->

# TASK-216 — Show a loading skeleton, not the empty state, while the metadata review list is still fetching (REV-EMPTY-1)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · web subagent · **Why:** The guard condition has a real correctness trap (guarding on `loading` alone would flicker the spine to a loading skeleton on every post-apply refresh, since results are stale-but-present during a refresh's loading window) that a Haiku-tier pass is likely to get wrong; needs the reasoning spelled out in steps, which this brief does. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 90020 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90020p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-20.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-216-show-a-loading-skeleton-not-the-empty-state-whil" -b agent/web-216-show-a-loading-skeleton-not-the-empty-state-whil origin/main
cd "$REPO/.worktrees/web-216-show-a-loading-skeleton-not-the-empty-state-whil"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In MetadataPanel.tsx, render a loading indicator instead of CompareSpine's empty state ONLY while metadata.loading is true AND there is nothing to show yet (metadata.rows.length === 0 && metadata.groups.length === 0). Once results arrive (even if the resolved set is genuinely empty), or once anything is already on screen, hand off to CompareSpine exactly as today.

## Background (verify before editing)

- useMetadataLane's fetch effect runs on mount and on every refresh() call (e.g. after an apply operation completes) -- see the effect's deps `[active, refreshKey]` -- and sets `loading` true at its start every time, not just on first mount.
- During a REFRESH (not the initial mount), `metadata.rows`/`metadata.results` still hold the previous page's data while the new fetch is in flight, because `setResults` is not called until the fetch resolves. So a naive guard of `metadata.loading ? <Skeleton/> : <CompareSpine/>` would replace an already-rendered, non-empty spine with a loading skeleton on every refresh -- a visible regression (flicker) the bug report does not ask for and that would fail review.
- The correct guard is therefore `metadata.loading && metadata.rows.length === 0 && metadata.groups.length === 0`: true only on the genuine first-paint/no-data-yet case this bug is about, false on every refresh where something is already on screen, and false once the fetch resolves (whether to a populated or a genuinely empty set) so CompareSpine's real empty state still works for an actually-empty library.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "if (rows.length === 0 && groups.length === 0)" web/src/components/review/spine/CompareSpine.tsx   # 1 hit, L1047, inside export function CompareSpine, followed by a Box data-testid="spine-empty" rendering {emptyMessage} — CompareSpine shows the emptyMessage box whenever rows and groups are both empty, regardless of a loading flag (it has none)
  grep -n "emptyMessage?: string;" web/src/components/review/spine/CompareSpine.tsx   # 1 hit, ~L1040, the last prop in CompareSpine's prop type -- no `loading` prop is declared — CompareSpine's props do not include a loading flag at all
  grep -n "rows={metadata.rows}" web/src/components/review/MetadataPanel.tsx   # 1 hit, L121, inside the CompareSpine JSX block (L120-126) — MetadataPanel passes metadata.rows/groups to CompareSpine without gating on metadata.loading
  grep -n "loading: boolean;" web/src/components/review/lanes/useMetadataLane.ts   # 1 hit, L274, first field of the MetadataLane interface — MetadataLane exposes a `loading` boolean the panel can read
  grep -n "setLoading(true)" web/src/components/review/lanes/useMetadataLane.ts   # 1 hit, ~L407, first line of the fetch useEffect body, whose deps are [active, refreshKey] — the metadata lane's fetch effect sets loading true at the start of every fetch (initial mount AND every subsequent refresh(), not just once)
  grep -n "emptyMessage:" web/src/components/review/lanes/metadata.ts   # 1 hit, ~L39: 'No metadata matches to review. Search providers from the Metadata menu to find some.' — the lane's own emptyMessage string is what the reviewer saw during the wait
  ```

### Reuse — don't invent

- Use `CompareSpine's existing data-testid="spine-empty" box (do not change CompareSpine itself -- guard its caller instead)` in `web/src/components/review/spine/CompareSpine.tsx` (verify: `grep -n "data-testid=\"spine-empty\"" web/src/components/review/spine/CompareSpine.tsx`) — do NOT write a parallel helper.
- Use `metadata.loading (already computed, no new state needed)` in `web/src/components/review/lanes/useMetadataLane.ts` (verify: `grep -n "const \[loading, setLoading\] = useState(true);" web/src/components/review/lanes/useMetadataLane.ts`) — do NOT write a parallel helper.
- Use `DupesPanel's existing loading-bar pattern, for visual consistency (LinearProgress)` in `web/src/components/review/DupesPanel.tsx` (verify: `grep -n "dupes.loading && <LinearProgress" web/src/components/review/DupesPanel.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Open web/src/components/review/MetadataPanel.tsx. Add `LinearProgress` and `Typography` to the existing `import { Box } from '@mui/material';` (L21) -> `import { Box, LinearProgress, Typography } from '@mui/material';`.
2. Locate the CompareSpine block (L119-127): `<Box sx={{ minWidth: 0, overflowY: 'auto' }}><CompareSpine rows={metadata.rows} groups={metadata.groups} viewMode={viewMode} ctx={metadata.spineCtx} emptyMessage={LANES.metadata.emptyMessage} /></Box>`.
3. Replace it with a conditional: when `metadata.loading && metadata.rows.length === 0 && metadata.groups.length === 0` is true, render a loading placeholder instead of CompareSpine; otherwise render CompareSpine exactly as before. Example:
```tsx
<Box sx={{ minWidth: 0, overflowY: 'auto' }}>
  {metadata.loading && metadata.rows.length === 0 && metadata.groups.length === 0 ? (
    <Box sx={{ p: 3 }} data-testid="metadata-loading">
      <LinearProgress sx={{ mb: 2 }} />
      <Typography variant="body2" sx={{ color: 'text.secondary' }}>
        Loading the review queue…
      </Typography>
    </Box>
  ) : (
    <CompareSpine
      rows={metadata.rows}
      groups={metadata.groups}
      viewMode={viewMode}
      ctx={metadata.spineCtx}
      emptyMessage={LANES.metadata.emptyMessage}
    />
  )}
</Box>
```
4. Bump MetadataPanel.tsx's version header (currently `// version: 1.0.0`) to `1.0.1`, update `// last-edited:` to today.
5. Create web/src/components/review/ReviewWorkspace.metadataLoading.test.tsx (new). Copy the render harness from ReviewWorkspace.refetchStale.test.tsx (makeResult/renderWorkspace/seed/beforeEach), but do NOT use `seed()`'s default immediate mock resolution for this file's tests -- you need a controllable, not-yet-resolved promise for `api.getCachedReviewResults`. Give the file a version header (1.0.0, fresh v4 guid, today).
6. Write test 1: 'shows a loading indicator, not the empty state, while the review fetch is pending'. In the test, do: `let resolveFetch: (v: unknown) => void; const pending = new Promise((r) => { resolveFetch = r; }); vi.mocked(api.getCachedReviewResults).mockReturnValue(pending as ReturnType<typeof api.getCachedReviewResults>);` plus the other beforeEach mocks (getDedupCandidates, getDedupStats, getReviewItems, getReviewCount, getConfig) exactly as seed() sets them. Render the workspace. Assert `await screen.findByTestId('metadata-loading')` appears and `screen.queryByTestId('spine-empty')` is null. Then `resolveFetch({ results: [], total_count: 0, matched: 0, no_match: 0, errors: 0 });` inside `act()` (or await it via `await waitFor`), and assert `await screen.findByTestId('spine-empty')` now appears with text matching `LANES.metadata.emptyMessage` and `screen.queryByTestId('metadata-loading')` is null.
7. Write test 2: 'a refresh does not flicker an already-populated spine back to loading'. Seed with one normal result via the standard `seed([makeResult('a')], 0)` helper (copy it from ReviewWorkspace.refetchStale.test.tsx too), open the workspace, wait for `compare-spine`. Then trigger a second fetch of `getCachedReviewResults` that resolves slowly (mock a second call to return a pending promise), and cause a refresh (the simplest lever already wired to `metadata.refresh()` is clicking a row's Apply button and letting `runApplyOp`'s poll->refresh fire -- if that is too indirect for a Haiku-tier implementer, instead directly assert the invariant at the guard-condition level: render with `metadata.loading` forced true via a lingering pending promise from mount, but seed one row into the FIRST resolution before triggering the second pending fetch, then assert the row (`screen.getByText(/Book a/)`) is still visible and `metadata-loading` is NOT rendered during the second, still-pending fetch).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_216.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Guarding on `metadata.loading` alone (without the rows/groups-empty check) is the wrong fix -- see 'background' above; it would flicker an already-populated spine to a loading skeleton on every post-apply refresh. The second test exists specifically to catch that mistake.
- A genuinely empty library (zero cached candidates ever) must still show CompareSpine's real empty state once `loading` resolves to false -- do not suppress that case.

## Tests

- web/src/components/review/ReviewWorkspace.metadataLoading.test.tsx: 'shows a loading indicator, not the empty state, while the review fetch is pending' -- asserts metadata-loading is shown before the fetch resolves and spine-empty is shown only after it resolves to an empty set.
- web/src/components/review/ReviewWorkspace.metadataLoading.test.tsx: 'a refresh does not flicker an already-populated spine back to loading' -- the anti-over-suppression case: proves the loading guard does not hide already-rendered rows during a later in-flight refresh.

Anti-over-suppression test: `a refresh does not flicker an already-populated spine back to loading` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npm --prefix web test -- src/components/review/ReviewWorkspace` passes, including both new tests in the new file.
- [ ] `grep -n "data-testid=\"metadata-loading\"" web/src/components/review/MetadataPanel.tsx` returns 1 hit.
- [ ] Manual/visual check not required for scout sign-off, but note for the reviewer: confirm in a real slow-network dev session (throttle the /cache/review request) that the spine shows the LinearProgress bar, not 'Search providers from the Metadata menu to find some', during the wait.
- [ ] Anti-over-suppression test: `a refresh does not flicker an already-populated spine back to loading` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_216.md`.

## Commit message

```
feat(web): Show a loading skeleton, not the empty state, while the meta (REV-EMPTY-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``grep -n "data-testid=\"metadata-loading\"" web/src/components/review/MetadataPanel.tsx` returns 1 hit.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Independent of todo_line 90020 part 1 (different files: MetadataPanel.tsx here vs ReviewWorkspace.tsx there) -- safe to run in parallel with it. Shares no file with todo_line 90021 (server-side) or 90022 (evidence panel), also safe to parallelize with those.
