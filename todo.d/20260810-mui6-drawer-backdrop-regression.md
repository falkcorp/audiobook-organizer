- [x] 🔴 **The Drawer's close transition can stall forever, leaving an invisible
      full-viewport backdrop that swallows every click until reload.** FIXED
      2026-08-11 in `web/src/theme.ts` — `MuiDrawer.defaultProps.slotProps
      .transition = { exit: false }`. No longer blocks TODO-MUI-1 (5.14 → 6.5).

      🚨 **This entry's original verdict — "real v6 regression" — was WRONG, and
      so was the 2026-08-10 fix it was chasing.** The defect is present on
      `main` (MUI 5.18.0) too. v6 did not introduce it; v6 moved its timing
      window onto the common path, which is why v6 failed 2/2 and v5 passed 2/2
      in the original n=2 comparison. Two runs per side cannot tell a regression
      apart from a shifted race.

      Measured 2026-08-11, chromium, `workers=1`, `--repeat-each=10`, n=10 per
      cell. P1 = press Escape with nothing crossing the CDP boundary first;
      P2 = identical but with one `page.evaluate()` immediately before the
      Escape. (The pre-existing probe called `page.evaluate` before *every*
      Escape, so its "closes 6/6" result had measured the instrumented world.)

      | build | P1 pass | P2 pass |
      |---|---|---|
      | v6.5.0, `exit: 0` only (as filed) | **0/10** | 10/10 |
      | v5.18.0, `exit: 0` only (i.e. `main` today) | 9/10 | **1/10** |
      | v6.5.0 + `exit: false` | 10/10 | 10/10 |

      MECHANISM (in-page instrumentation + a patched react-transition-group):
      Escape is delivered correctly and `onClose(_, 'escapeKeyDown')` fires —
      focus, `isTopModal()` and the Select menu are all fine, so the four
      theories in the original report are disproven. The Slide and the
      Backdrop's Fade both enter `exiting`, RTG schedules completion with
      `setTimeout`, **the timer fires**, and RTG calls `setState({status:
      'exited'})` on an instance whose `updater.isMounted` is `true`. React
      never applies that update: the same instance re-renders 4 ms later still
      `exiting`, `componentDidUpdate` agrees at +9 ms, and a 300 ms probe still
      reads `exiting`. So `onExited` never runs, `useModal`'s `exited` stays
      false, `Modal` never returns null, and its `position: fixed; inset: 0`
      backdrop stays hit-testable — `document.elementFromPoint(20, 300)`
      returns it. `exit: false` makes RTG take the synchronous branch in
      `performExit` instead, so the lost update is never scheduled.

- [ ] 🟡 **Why does React silently drop a `setState` issued from a `setTimeout`
      on a mounted class component inside a portal?** This is the residual
      unknown under the Drawer fix above and under the 2026-08-10 "invisible
      sheet" incident, and it is now the *only* part still unexplained. Ruled
      out: duplicate React copies (single `react@18.3.1`, deduped), StrictMode
      (dev-only in `web/src/main.tsx`), `flushSync`/`startTransition`/
      `unstable_batchedUpdates` (absent from `web/src`), uncaught exceptions
      (none observed), and unmounting (`componentWillUnmount` never runs,
      `isMounted` stays true). Leading untested hypothesis: this is a
      production-build React 18 concurrent-root update issued while the root is
      mid-render, where the dev-only warning that would have named it does not
      exist. `exit: false` sidesteps the question rather than answering it, so
      any other component that keeps a timer-driven exit transition is still
      exposed — `MuiMenu` currently is (its `exit: 0` measured 20/20 on
      2026-08-10, but that is the same kind of evidence that `exit: 0` on the
      Drawer produced before it failed 10/10 on v6).

- [ ] 🟡 **`batch-operations.spec.ts:100` [webkit] is an intermittent flake —
      find its mechanism.** Observed 2026-08-10 failing once on `main`
      (`76269d57`) and once on the MUI v6 branch, and **passing** on a re-run of
      each. `main` is green: 556 passed / 0 failed / 8 skipped, exit 0.

          Error: expect(locator).toBeChecked() failed
          Locator: getByLabel('Select Test Book 1', { exact: true })
          Timeout: 5000ms — element(s) not found

      "Batch Operations › selection persists across page navigation". When it
      does fail, the checkbox is not merely unchecked — its **label is absent**,
      so the row is not rendering as the test expects at all. Webkit only. That
      shape (row not rendered yet, rather than state lost) points at the
      navigation completing before the list re-renders, which is a timing
      mechanism worth finding rather than re-running past.

      🚨 **This entry previously claimed `main` was red.** That claim came from a
      single run and was wrong; it was published in a PR body and a memory file
      before being re-run. A failure seen once on a suite with known webkit
      flake is not a measurement — re-run before recording it. Per
      `feedback_fix_flaky_tests`, this still gets its mechanism found rather
      than being ignored as noise.
