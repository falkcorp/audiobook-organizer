<!-- file: docs/agent-tasks/todo-completion/web/TASK-218-operationactivitypanel-stop-re-appending-the-las.md -->
<!-- version: 1.0.0 -->
<!-- guid: a6027183-2807-4de8-8984-d0812b9039a7 -->
<!-- last-edited: 2026-08-21 -->

# TASK-218 — OperationActivityPanel: stop re-appending the last SSE log line on every progress tick (REV-EMPTY-4)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · web subagent · **Why:** Single-file, single-effect fix with a stable, already-existing dedup key (`sequence`) to use -- no design decision, no new type, no cross-file wiring; owner explicitly tagged this (S, haiku) in the scope brief and the investigation confirms that estimate. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 90023 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90023p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-20.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/web-218-operationactivitypanel-stop-re-appending-the-las" -b agent/web-218-operationactivitypanel-stop-re-appending-the-las origin/main
cd "$REPO/.worktrees/web-218-operationactivitypanel-stop-re-appending-the-las"
git rebase origin/main
npm ci --prefix web   # frontend task: Playwright/vitest must come from the worktree
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In OperationActivityPanel.tsx, the SSE-append effect (currently L211-228) must append each distinct `latestLogEvent` exactly once, regardless of how many times the unrelated `op` object (activeOperations lookup) changes identity in between. Track the last-appended event's `sequence` number in a ref and skip when unchanged; stop depending on `op` directly by mirroring it into a ref updated in its own effect, so a progress tick that does not carry a new log event cannot re-trigger the append effect at all.

## Background (verify before editing)

- useOperationsStore's SSE handler for `op.progress` events (fired repeatedly while a scan or other long-running op reports progress) always builds a brand-new `ActiveOperation` object via `{ ...existing, progress: ..., total: ..., message: ... }` and a brand-new `operations`/`activeOperations` array, even when only `progress` changed. `op` in OperationActivityPanel.tsx is selected from `activeOperations` via `.find()`, so it gets a new reference on every one of those ticks.
- The append effect's dependency array `[latestLogEvent, operationId, op]` therefore re-runs on every progress tick, not just when a new log line actually arrives over SSE -- and because the effect body unconditionally appends `latestLogEvent` to `entries`, it re-appends the SAME event once per tick. This is what the owner saw as 'the last message repeating forever' during a library scan on 2026-08-21.
- Each `OperationLogEvent` the store emits already carries `sequence: ++logEventSequence` (useOperationsStore.ts, module-level monotonic counter incremented once per real SSE log event) -- this is a strictly reliable dedup key, better than the todo brief's own fallback suggestion of created_at+message (two distinct log lines can legitimately share a timestamp, or even a message, but never a sequence number).
- `op` is only read inside the effect to stamp `operation_type: op?.def_id ?? op?.type ?? ''` onto the newly-appended entry -- it does not need to be a live reactive dependency of THIS effect; reading its current value via a ref at append time is sufficient and correct.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "}, \[latestLogEvent, operationId, op\]);" web/src/components/OperationActivityPanel.tsx   # 1 hit, L228 — the append effect's deps include `op`, the store-derived object that changes identity every progress tick
  grep -n "const op = useOperationsStore" web/src/components/OperationActivityPanel.tsx   # 1 hit, L171-173: `const op = useOperationsStore((state) => state.activeOperations.find((o) => o.id === operationId));` — `op` is selected fresh from the store on every render via a `.find()` that returns whatever object identity is currently in `activeOperations`
  grep -n "const updated: ActiveOperation = {" web/src/stores/useOperationsStore.ts   # \u22652 hits; the op.progress handler's is the one directly above `const operations = { ...state.operations, [opId]: updated };` inside the `else if (name === 'op.progress' ...)` branch — the store's progress handler (op.progress SSE events, which fire repeatedly during a scan) replaces the operation's object with a fresh spread on every tick, which is what makes `op`'s identity change without `latestLogEvent` changing
  grep -n "sequence: ++logEventSequence" web/src/stores/useOperationsStore.ts   # 1 hit, ~L374, inside the op.log SSE handler's `set({ latestLogEvent: { ... } })` call — each SSE log event the store records already carries a monotonically increasing `sequence` number, which is a strictly better dedup key than created_at+message (two log lines can share a timestamp; a sequence number cannot repeat)
  grep -n "export interface OperationLogEvent" -A6 web/src/stores/useOperationsStore.ts   # 1 hit at L43, interface body includes `sequence: number;` as a non-optional field — OperationLogEvent's shape confirms `sequence: number` is a real, always-present field on every event, not optional
  grep -n "}, \[latestLogEvent, expandedOpId\]);" web/src/pages/ActivityLog.tsx   # 1 hit, ~L409 — a sibling consumer of the exact same store field (ActivityLog.tsx) does NOT have this bug, because its effect's deps are only [latestLogEvent, expandedOpId] -- confirms `op` is the specific defect, not something inherent to using latestLogEvent
  ```

### Reuse — don't invent

- Use `OperationLogEvent.sequence -- the monotonic id to dedupe on, already emitted by the store, no new field needed` in `web/src/stores/useOperationsStore.ts` (verify: `grep -n "sequence: number;" web/src/stores/useOperationsStore.ts`) — do NOT write a parallel helper.
- Use `ActivityLog.tsx's simpler effect (no `op` dependency) as the reference for what a correctly-scoped deps array looks like here` in `web/src/pages/ActivityLog.tsx` (verify: `grep -n "if (!latestLogEvent || latestLogEvent.op_id !== expandedOpId) return;" web/src/pages/ActivityLog.tsx`) — do NOT write a parallel helper.

## Step-by-step

1. Open web/src/components/OperationActivityPanel.tsx. Directly above the SSE-append effect (currently starting at L211), add two new refs: `const opRef = useRef(op);` and `const lastAppendedSequenceRef = useRef<number | null>(null);`.
2. Add a small effect right after declaring `opRef` to keep it current without re-running the append effect: `useEffect(() => {\n  opRef.current = op;\n}, [op]);`.
3. Change the SSE-append effect body (L211-228) from:
```tsx
useEffect(() => {
  if (!latestLogEvent || latestLogEvent.op_id !== operationId) return;
  const cap = limit ?? 1000;
  setEntries((prev) => {
    const next = [
      ...prev,
      {
        timestamp: latestLogEvent.created_at,
        level: latestLogEvent.level,
        operation_id: latestLogEvent.op_id,
        operation_type: op?.def_id ?? op?.type ?? '',
        message: latestLogEvent.message,
      },
    ];
    return next.length > cap ? next.slice(next.length - cap) : next;
  });
  setTotal((prev) => prev + 1);
}, [latestLogEvent, operationId, op]);
```
to:
```tsx
useEffect(() => {
  if (!latestLogEvent || latestLogEvent.op_id !== operationId) return;
  // Guards against re-appending the same SSE event when this effect re-runs
  // for an unrelated reason. `sequence` is a monotonic counter the store
  // stamps on every real log event, so it is a reliable identity even across
  // renders that hand back an equal-looking but structurally new event object.
  if (lastAppendedSequenceRef.current === latestLogEvent.sequence) return;
  lastAppendedSequenceRef.current = latestLogEvent.sequence;
  const cap = limit ?? 1000;
  const currentOp = opRef.current;
  setEntries((prev) => {
    const next = [
      ...prev,
      {
        timestamp: latestLogEvent.created_at,
        level: latestLogEvent.level,
        operation_id: latestLogEvent.op_id,
        operation_type: currentOp?.def_id ?? currentOp?.type ?? '',
        message: latestLogEvent.message,
      },
    ];
    return next.length > cap ? next.slice(next.length - cap) : next;
  });
  setTotal((prev) => prev + 1);
}, [latestLogEvent, operationId]);
```
`op` is intentionally removed from this effect's own deps array -- it is now read through `opRef.current`, which the separate ref-mirroring effect from step 2 keeps current without ever re-triggering the append logic.
4. Add a `// last-edited: YYYY-MM-DD` line (today's date) to the file's header block, immediately after the `// guid:` line -- the header is currently missing this line (only `file`/`version`/`guid` are present), which is out of step with the project's mandatory file-header format; bring it into compliance while this file is touched. Bump `// version: 1.3.3` to `1.3.4`.
5. In web/src/components/OperationActivityPanel.test.tsx, add `import { act } from '@testing-library/react';` (or use the existing `render`/`waitFor` import's `act` re-export if already available) and `import { useOperationsStore } from '../stores/useOperationsStore';`. Add a `beforeEach`/`afterEach` pair (alongside the existing `afterEach`) that resets the real store between tests: `useOperationsStore.setState({ activeOperations: [], operations: {}, latestLogEvent: null }, false);` -- run this both before and after the new describe block's tests so this file's store mutations cannot leak into other test files that import the same singleton store.
6. Add a new test 'appends a live SSE log line exactly once, even when the op object changes shape on progress ticks with no new event': mock `fetchOperationActivity` to resolve `{ operation_id: 'op-9', entries: [], total: 0 }`, render `<OperationActivityPanel operationId="op-9" />`, wait for the empty-state text to disappear/load to finish (`await waitFor(() => expect(screen.queryByText(/No activity recorded/)).not.toBeInTheDocument())` is not quite right for an empty list -- instead just `await waitFor(() => expect(activityApi.fetchOperationActivity).toHaveBeenCalled());`). Then, inside `act(() => { ... })`, push one event: `useOperationsStore.setState({ latestLogEvent: { op_id: 'op-9', level: 'info', message: 'Scanning shelf 3', created_at: '2026-08-21T23:47:00Z', sequence: 1 } });`. Assert `await screen.findByText('Scanning shelf 3')` and `expect(screen.getByText('1 entry')).toBeInTheDocument()`.
7. Continuing the same test: inside `act(() => { ... })`, update the op object TWICE with different progress values but the SAME latestLogEvent still in state (simulating progress ticks with no new log line): `useOperationsStore.setState({ activeOperations: [{ id: 'op-9', type: 'scan', status: 'running', progress: 10, total: 100, message: '' }] }); useOperationsStore.setState({ activeOperations: [{ id: 'op-9', type: 'scan', status: 'running', progress: 20, total: 100, message: '' }] });` (each call creates a fresh array/object, exactly like the real store's progress handler does). Assert the count is STILL exactly 1: `expect(screen.getByText('1 entry')).toBeInTheDocument()` and `expect(screen.getAllByText('Scanning shelf 3')).toHaveLength(1)`.
8. Continuing the same test: push a second, distinct event: `act(() => { useOperationsStore.setState({ latestLogEvent: { op_id: 'op-9', level: 'info', message: 'Shelf 3 complete', created_at: '2026-08-21T23:48:00Z', sequence: 2 } }); });`. Assert `await screen.findByText('Shelf 3 complete')` and `expect(screen.getByText('2 entries')).toBeInTheDocument()` -- proving the dedup is keyed on the event, not a blanket suppression of all future appends.
9. Bump the test file's version header (currently 1.0.1) to 1.1.0, and add the missing `// last-edited:` line as in step 4.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_web_218.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- The second distinct-event assertion (step 8) is the anti-over-suppression case: the fix must not accidentally suppress ALL future appends after the first one -- only true duplicates of the same `sequence` are skipped.
- `latestLogEvent.op_id !== operationId` (events for a DIFFERENT operation, e.g. two panels open for two different ops sharing the same SSE stream) must continue to be ignored exactly as today -- this guard is unchanged, just re-verify it is not accidentally removed.
- If `latestLogEvent` is reset to `null` (e.g. on unmount/remount of the SSE connection) and then a brand-new event arrives with `sequence: 1` again from a fresh session, `lastAppendedSequenceRef` is per-component-instance (a fresh ref on mount), so this is not a concern for a newly-mounted panel; a long-lived panel across an SSE reconnect is out of scope for this fix (sequence is a module-level counter that only increases for the lifetime of the page, per `useOperationsStore.ts`).

## Tests

- web/src/components/OperationActivityPanel.test.tsx: 'appends a live SSE log line exactly once, even when the op object changes shape on progress ticks with no new event' -- pushes one latestLogEvent, updates the op object twice with new progress values, asserts still exactly 1 entry; then pushes a second distinct event and asserts exactly 2 entries.

Anti-over-suppression test: `pushes a second distinct event and asserts exactly 2 entries (step 8)` — a known-good input still passes with the new guard active.

## How to test

```bash
npm --prefix web run lint && npm --prefix web test
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `npm --prefix web test -- src/components/OperationActivityPanel` passes, including the new test and all three pre-existing ones.
- [ ] `grep -n "}, \[latestLogEvent, operationId\]);" web/src/components/OperationActivityPanel.tsx` returns 1 hit (op is gone from the deps array) and `grep -n "lastAppendedSequenceRef" web/src/components/OperationActivityPanel.tsx` returns \u22652 hits (ref declaration + the skip check).
- [ ] `npm --prefix web run lint` is clean on both changed files (react-hooks/exhaustive-deps must not flag the trimmed deps array, since `op` is now read only through a ref).
- [ ] Anti-over-suppression test: `pushes a second distinct event and asserts exactly 2 entries (step 8)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `npm --prefix web run lint && npm --prefix web test` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_web_218.md`.

## Commit message

```
refactor(web): OperationActivityPanel: stop re-appending the last SSE log l (REV-EMPTY-4)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

No file overlap with any other object in this scope (90020/90021/90022) -- fully independent, safe to run in parallel with all of them. The store reset in step 5 matters: useOperationsStore is a real module-singleton Zustand store, not mocked in this test file, so leaving activeOperations/latestLogEvent set after this test file runs could leak into another test file executed in the same vitest worker.
