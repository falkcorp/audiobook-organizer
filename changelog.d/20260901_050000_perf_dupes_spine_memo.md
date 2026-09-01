### Changed

#### The dupes review lane no longer re-renders every row to repaint one checkbox

`perf(review): memoize spine rows so one checkbox re-renders one row`
(d01f15a87) memoized `CompareSpine` and `RegroupSpine` and skipped
`DupesSpine`, which had no memoization at all. `benchmark-review-lanes.spec.ts`
then measured the consequence: on this machine, ticking one dupes checkbox at
the 100-row page cap took a median 61 ms against a 13 ms N=5 noise floor, while
the already-memoized metadata lane's equivalent took 26 ms.

Two changes, both required — either alone is inert:

- **`DupesSpine` now follows `CompareSpine`'s shape.** It lifts the four
  callbacks out of the context into a `useMemo`'d `handlers` object, resolves
  each row's `selected` / `focused` / `expanded` itself and passes them as plain
  booleans, and wraps `CandidateRow` and `BookSide` in `memo()`. Constant `sx`
  literals inside both are hoisted to module scope. `DupesSpineContext`'s public
  shape is unchanged, so `DupesPanel` needed only a comment and
  `DupesSpine.test.tsx` needed no edits.
- **`ReviewWorkspace` passes the dupes `onToggleExpand` as a `useCallback`.** It
  was an inline arrow, so it got a new identity on every render — and a dupes
  checkbox tick re-renders `ReviewWorkspace`, because `useDupesLane`'s state
  lives there. That alone would have rebuilt `handlers` on every tick and left
  every memo above present, correct and completely inert.

Measured with the same harness, back to back on one machine (median of 5 reps):
at N=100 the checkbox toggle went 61 ms → 32 ms against an N=5 noise floor that
barely moved (13 ms → 12 ms); at N=50, 35 ms → 21 ms. Under the 6x CPU-throttle
control, 390 ms → 220 ms.

An A/B run with the memo in place but ReviewWorkspace's inline arrow restored
measured 68 ms and 65 ms in two separate runs -- i.e. no improvement on the
61 ms baseline at all. That is what "either alone is inert" means concretely,
and it is why the `useCallback` is not a tidiness edit.

The lane's **filter** cost is unchanged and was not expected to move: the
harness's filter goes 100 rows → 1 → 100, so its 714 ms of blocking time at
N=100 is dominated by ~99 unmounts followed by ~99 mounts, and `React.memo` does
not skip a mount. That number is still on the table and still real; it needs
row virtualization or a debounce, not memoization.

`DupesSpine.memo.test.tsx` pins both halves of this: a render counter
(`recommendedKeepSide` fires once per row render) asserts one row re-renders per
tick, and five staleness assertions — a checkbox cleared by the store, the focus
ring moving in both directions, an expand revealing exactly one evidence panel,
a candidate whose status changed — assert the memo is not serving stale rows.
The two halves fail on opposite mutations, so neither can substitute for the
other. A third pair pins the `handlers` `useMemo` above the empty-state early
return, which React would otherwise punish on the empty → populated transition.
