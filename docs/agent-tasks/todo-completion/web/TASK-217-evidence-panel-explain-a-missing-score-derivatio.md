<!-- file: docs/agent-tasks/todo-completion/web/TASK-217-evidence-panel-explain-a-missing-score-derivatio.md -->
<!-- version: 1.0.0 -->
<!-- guid: 04dcdd75-7065-4b4c-b835-d2c7d477557c -->
<!-- last-edited: 2026-08-21 -->

# TASK-217 — Evidence panel: explain a missing score derivation in plain language and offer re-search inline (REV-EMPTY-3)

**Priority:** P2 · **Effort:** L · **Recommended subagent:** Sonnet-class · web subagent · **Why:** Touches a shared, documented interface (SpineContext) and its sole real constructor plus two render call sites and a test fixture builder; getting the reachable-vs-unreachable emptyReason branch distinction right (see edge_cases) needs the reasoning spelled out, which is more than a mechanical Haiku pass should be trusted with. · **Depends on:** none · **Wave:** 2

Source: `TODO.md` line 90022 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90022p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-20.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-217-evidence-panel-explain-a-missing-score-derivatio" -b agent/web-217-evidence-panel-explain-a-missing-score-derivatio origin/main
cd "$REPO/.worktrees/web-217-evidence-panel-explain-a-missing-score-derivatio"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Two changes, shipped together because the second one's test depends on the first one's exact wording. (1) In adapters.ts, reword metadataEvidence's emptyReason to name the cause in plain language and point at the remedy, matching the scope brief's suggested phrasing. (2) In CompareSpine.tsx, when a candidate's waterfall evidence has zero steps (i.e. no recorded derivation), render a 'Re-search this book' button next to the empty-state text that calls the existing per-book refetch path (metadata.refetchBooks via a new SpineContext.onRefetch field) -- reusing the exact mechanism MetadataPanel's QueueRail already uses for the same single-book refetch, not a new API call.

## Background (verify before editing)

- metadataEvidence(candidate) (adapters.ts:79-108) is called from exactly two places in the whole app, both inside CompareSpine.tsx's EvidenceSection helper (L90-99), which itself is only ever invoked with a non-null `r.candidate` (both call sites are already inside a `r.candidate &&`-guarded region). This means the OTHER emptyReason branch in metadataEvidence -- `!candidate` -> 'No candidate selected.' (adapters.ts:82-84) -- is unreachable from the real UI today; only the 'breakdown missing or empty' branch (L86-93) is ever shown. Do not touch the `!candidate` branch's text; only L91-92 is in scope.
- Candidates cached before 2026-08-20 (commit cf3aeb9b added internal/metafetch/score_breakdown.go) have no score_breakdown at all; a fresh search attaches one (internal/metafetch/service_search.go:619 and :702 both set `ScoreBreakdown: rec.breakdown()`/`asinRec.breakdown()`). So 'this candidate predates the derivation feature; re-search to get one' is a literally true, actionable explanation, not just friendlier phrasing.
- SpineContext (CompareSpine.tsx:120-129) already mixes a dispatched-action field (`onAction`) with several direct single-purpose callback fields (`onPreviewCover`, `onToggleSelect`, `onToggleExpand`) -- adding `onRefetch: (bookId: string) => void` follows that existing precedent rather than extending the separately-documented, exhaustively-switched MetadataAction union in reviewActions.ts (which is used by three lanes and has its own reversibility/confirmation/affectedCount semantics that a single-purpose refetch button does not need to participate in).
- The real (non-test) construction of a SpineContext happens in exactly one place: useMetadataLane.ts's `spineCtx` useMemo (L860-871), which already has `refetchBooks` in its own closure (defined later in the same hook, L882-913) -- wiring `onRefetch: (bookId) => { void refetchBooks([bookId]); }` there, plus adding `refetchBooks` to the useMemo's dependency array, is a same-scope closure reference and does not need any reordering of the file (JS closures resolve `refetchBooks` at call time, which is always after the whole hook body -- including `refetchBooks`'s own `const` -- has run once).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "emptyReason:" web/src/components/review/evidence/adapters.ts   # 5 hits total in the file; the relevant one is L91-92: `emptyReason: 'This candidate was produced without a recorded derivation, so its score cannot be explained here.'` — the current emptyReason text names no cause and offers no next step
  grep -n "function EvidenceSection" web/src/components/review/spine/CompareSpine.tsx   # 1 hit, L90: `function EvidenceSection({ candidate }: { candidate: MetadataCandidate }) {` — EvidenceSection (the only renderer of metadataEvidence in the app) takes just `candidate`, nothing else
  grep -n "<EvidenceSection" web/src/components/review/spine/CompareSpine.tsx   # 2 hits: L723 (inside CompactRow, which declares `const bookId = r.book.id;` at L383) and L1005 (inside TwoColumnCard, which declares the same at L739) — EvidenceSection is called from exactly two sites, both already holding `ctx: SpineContext` and a `bookId`/`r.book.id` in scope
  grep -n "onPreviewCover: (url: string) => void;" web/src/components/review/spine/CompareSpine.tsx   # 1 hit, ~L124, inside the SpineContext interface (L120-129) — SpineContext already carries direct (non-dispatched) single-purpose callbacks alongside onAction, e.g. onPreviewCover -- the precedent this task's onRefetch field follows
  grep -n "refetchBooks: (ids: string\[\]) => Promise<string | null>;" web/src/components/review/lanes/useMetadataLane.ts   # 1 hit, in the MetadataLane interface — the metadata lane already has a single-book refetch function to reuse -- no new API call needed
  grep -n "void metadata.refetchBooks(\[bookId\]);" web/src/components/review/MetadataPanel.tsx   # 1 hit, ~L114, inside QueueRail's onRefetchRow prop — MetadataPanel already wires that exact function to a per-row refetch button for the identical single-book use case (the pattern to mirror for SpineContext.onRefetch's real implementation)
  grep -n "const spineCtx: SpineContext = useMemo" web/src/components/review/lanes/useMetadataLane.ts   # 1 hit, L860 — the real spineCtx object is constructed in exactly one place in production code
  grep -n "without a recorded derivation" web/src/components/review/ReviewWorkspace.test.tsx   # 1 hit, L296: `expect(panel).toHaveTextContent(/without a recorded derivation/i);`, inside the test at L286-297 — an existing passing test asserts the OLD wording verbatim and will fail once the text changes unless updated
  grep -n "function makeCtx" web/src/components/review/spine/CompareSpine.test.tsx   # 1 hit, ~L71 — CompareSpine.test.tsx builds its own SpineContext fixture by hand and will need the new field added once SpineContext gains a required onRefetch
  ls web/src/components/review/evidence/adapters.test.ts   # 'No such file or directory' -- confirms the file does not exist yet — no adapters.test.ts exists yet -- this is a new file, not an edit
  grep -n "produced without a recorded derivation" web/src/components/review/evidence/EvidencePanel.test.tsx   # 1 hit, L126, inside a hand-built `emptyReason: '...'` object literal, unrelated to adapters.ts's actual constant — the OLD wording is also used as a literal test FIXTURE (not an assertion against the real string) in EvidencePanel.test.tsx, which is therefore NOT affected by this change and must not be edited
  ```

### Reuse — don't invent

- Use `metadata.refetchBooks([bookId]) -- the exact single-book fetch this task must dispatch, already implemented, no new API call` in `web/src/components/review/lanes/useMetadataLane.ts` (verify: `grep -n "const refetchBooks = useCallback" web/src/components/review/lanes/useMetadataLane.ts`) — do NOT write a parallel helper.
- Use `SpineContext's onPreviewCover pattern (direct callback field, not a dispatched MetadataAction) -- the precedent onRefetch follows` in `web/src/components/review/spine/CompareSpine.tsx` (verify: `grep -n "onPreviewCover: (url: string) => void;" web/src/components/review/spine/CompareSpine.tsx`) — do NOT write a parallel helper.
- Use `MUI Button, already imported in CompareSpine.tsx -- no new import needed for the re-search button itself` in `web/src/components/review/spine/CompareSpine.tsx` (verify: `grep -n "^import {" web/src/components/review/spine/CompareSpine.tsx`) — do NOT write a parallel helper.
- Use `makeCtx() test fixture builder for SpineContext` in `web/src/components/review/spine/CompareSpine.test.tsx` (verify: `grep -n "function makeCtx" web/src/components/review/spine/CompareSpine.test.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. In web/src/components/review/evidence/adapters.ts, change the emptyReason string at L91-92 from `'This candidate was produced without a recorded derivation, so its score cannot be explained here.'` to `'This candidate was cached before score derivations were recorded. Re-search this book to see one.'` (keep the surrounding object shape and the `steps: []` field untouched -- only the string literal changes).
2. Bump adapters.ts's version header (currently 1.0.0) to 1.0.1, today's date.
3. In web/src/components/review/spine/CompareSpine.tsx, widen the `SpineContext` interface (L120-129) by adding one field, following the style of the existing `onPreviewCover` line: `/** Dispatches a single-book metadata refetch -- the remedy the 'no recorded\n * derivation' evidence empty-state offers. Not a MetadataAction: this is a\n * direct single-purpose callback like onPreviewCover, not something the three\n * lanes' reversibility/confirmation machinery needs to know about. */\nonRefetch: (bookId: string) => void;`.
4. Change `EvidenceSection`'s signature (L90) from `function EvidenceSection({ candidate }: { candidate: MetadataCandidate }) {` to accept two more props: `function EvidenceSection({ candidate, bookId, onRefetch }: { candidate: MetadataCandidate; bookId: string; onRefetch: (bookId: string) => void }) {`.
5. Inside EvidenceSection's body, compute the evidence once (`const evidence = metadataEvidence(candidate);`) instead of inlining the call in the JSX, and derive `const noDerivation = evidence.kind === 'waterfall' && evidence.steps.length === 0;`. Change `<EvidencePanel evidence={metadataEvidence(candidate)} />` to `<EvidencePanel evidence={evidence} />`, and immediately after it, conditionally render: `{noDerivation && (\n  <Button\n    size="small"\n    variant="text"\n    data-testid="evidence-refetch"\n    onClick={() => onRefetch(bookId)}\n    sx={{ mt: 0.5 }}\n  >\n    Re-search this book\n  </Button>\n)}` (Button is already imported at the top of this file).
6. Update both call sites: L723 (inside CompactRow) `<EvidenceSection candidate={r.candidate} />` -> `<EvidenceSection candidate={r.candidate} bookId={bookId} onRefetch={ctx.onRefetch} />`; L1005 (inside TwoColumnCard) `{r.candidate && <EvidenceSection candidate={r.candidate} />}` -> `{r.candidate && <EvidenceSection candidate={r.candidate} bookId={bookId} onRefetch={ctx.onRefetch} />}`.
7. Bump CompareSpine.tsx's version header (currently 1.3.0) to 1.4.0, today's date (minor bump: new public field on an exported interface).
8. In web/src/components/review/lanes/useMetadataLane.ts, inside the `spineCtx` useMemo (L860-871), add `onRefetch: (bookId: string) => { void refetchBooks([bookId]); },` as a new field in the returned object, and add `refetchBooks` to the useMemo's dependency array (currently `[rowStates, selectedIds, toggleSelect, dispatch, expandedId, toggleExpand]`).
9. Bump useMetadataLane.ts's version header (currently 1.4.0) to 1.4.1 unless todo_line 90021's task already bumped it to 1.4.1 in the same worktree first, in which case bump to 1.4.2 -- check the file's current header before writing a version, do not blindly overwrite a bump made by the other task.
10. In web/src/components/review/spine/CompareSpine.test.tsx, update `makeCtx()` (L71-85) to add `onRefetch: vi.fn(),` to the default `ctx` object (alongside the existing `onPreviewCover: vi.fn()` etc.), so every existing test in this file keeps compiling and passing.
11. In web/src/components/review/ReviewWorkspace.test.tsx, update the existing test 'says so when a candidate has no recorded derivation' (L286-297): change the assertion at L296 from `expect(panel).toHaveTextContent(/without a recorded derivation/i);` to `expect(panel).toHaveTextContent(/cached before score derivations were recorded/i);`, and extend the same test (or add a new one directly below it) to also assert the button renders and works: `const refetchBtn = within(panel).getByTestId('evidence-refetch'); expect(refetchBtn).toHaveTextContent(/re-search/i);` then, after mocking `vi.mocked(api.batchFetchCandidates).mockResolvedValue({ operation_id: 'op-1' })` in this test (it is NOT set in this file's shared beforeEach), `await user.click(refetchBtn); await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledWith({ book_ids: ['a'] }));` (book 'a' is the row this test already expands, per `makeResult('a')` in this file's beforeEach).
12. Bump ReviewWorkspace.test.tsx's version header (currently 1.6.0) to 1.7.0, today's date.
13. Create web/src/components/review/evidence/adapters.test.ts (new). Import `metadataEvidence` from './adapters' and `MetadataCandidate` from '../../../services/api'. Write three tests: (a) 'a candidate with no score_breakdown reports the cached-before-recording reason' -- pass a candidate object with no `score_breakdown` field, assert `result.kind === 'waterfall'`, `result.steps` is empty, and `result.emptyReason` matches `/cached before score derivations were recorded/i`. (b) 'a candidate with an empty steps array reports the same reason' -- pass `score_breakdown: { score: 0.5, steps: [] }`, assert the same emptyReason. (c) 'a candidate with recorded steps returns them verbatim, with no emptyReason' -- pass a `score_breakdown` with 1-2 real `steps` (id/label/op/operand/running), assert `result.steps.length` matches and `result.emptyReason` is `undefined`. Give the file a version header (1.0.0, fresh v4 guid, today's date).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_217.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The `!candidate` branch of metadataEvidence ('No candidate selected.') is unreachable from EvidenceSection today (both call sites are already `r.candidate &&`-guarded) -- do not add a re-search button for that branch, and do not reword its text; this task's scope is only the 'breakdown missing or empty' branch.
- A candidate whose steps array is non-empty but whose waterfall is INCONSISTENT (recomposeWaterfall does not reproduce the score -- see EvidencePanel.tsx's `inconsistent` chip) is a DIFFERENT problem from having no derivation at all, and must NOT show the re-search button -- `noDerivation` must be gated on `evidence.steps.length === 0`, not on `waterfallIsConsistent`.
- GroupedCard (CompareSpine.tsx, the third card renderer) does not call EvidenceSection at all -- do not add the button there; grouped rows never show the waterfall panel today and this task does not change that.

## Tests

- web/src/components/review/evidence/adapters.test.ts (new): three cases above -- missing breakdown, empty-steps breakdown, and populated breakdown -- pin metadataEvidence's emptyReason text and the steps pass-through.
- web/src/components/review/ReviewWorkspace.test.tsx: updated 'says so when a candidate has no recorded derivation' -- now also asserts the 'Re-search this book' button renders inside the evidence panel and, on click, calls api.batchFetchCandidates with `{ book_ids: ['a'] }` (via metadata.refetchBooks, no new API surface).
- web/src/components/review/spine/CompareSpine.test.tsx: existing tests continue to pass once makeCtx() supplies `onRefetch: vi.fn()` -- no new test strictly required here, but add one asserting the button is ABSENT when a candidate's steps are non-empty (the anti-over-suppression case: proves the button does not appear on a normal, fully-derived candidate).

Anti-over-suppression test: `asserting the button is ABSENT when a candidate's steps are non-empty (CompareSpine.test.tsx)` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npm --prefix web test -- src/components/review/evidence` passes, including the new adapters.test.ts.
- [ ] `npm --prefix web test -- src/components/review/ReviewWorkspace` and `npm --prefix web test -- src/components/review/spine/CompareSpine` both pass.
- [ ] `grep -n "onRefetch" web/src/components/review/spine/CompareSpine.tsx` shows the interface field plus both call sites wired (3+ hits).
- [ ] `npm --prefix web run lint` is clean on all six changed/new files.
- [ ] Anti-over-suppression test: `asserting the button is ABSENT when a candidate's steps are non-empty (CompareSpine.test.tsx)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_217.md`.

## Commit message

```
feat(web): Evidence panel: explain a missing score derivation in plain  (REV-EMPTY-3)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — ``grep -n "onRefetch" web/src/components/review/spine/CompareSpine.tsx` shows the interface field plus both call sites wired (3+ hits).` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Shares web/src/components/review/lanes/useMetadataLane.ts with todo_line 90021 (different region of the file -- spineCtx useMemo here vs the fetch call site there); step 9 explicitly tells the implementer to check the file's live version header rather than assume a starting value, in case 90021 lands first in the same worktree. No other file overlap with 90020 part 1/2 or with 90021.
