// file: web/src/theme.ts
// version: 1.5.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-08-11

import { createTheme } from '@mui/material/styles';

type PaletteMode = 'light' | 'dark';

export function createAppTheme(mode: PaletteMode = 'dark') {
  return createTheme({
    palette: {
      mode,
      // Mode-aware, because a single brand colour cannot serve both.
      //
      // THE BUG (reported 2026-08-11: library buttons in dark mode "too closely
      // match the existing"): primary.main was '#1976d2' in BOTH modes. That is
      // MUI's LIGHT-mode blue. Against the dark background it measures 3.89:1 —
      // under the 4.5:1 WCAG AA floor for text — and the library toolbar is
      // built from `variant="outlined"` buttons, which draw their label in
      // primary.main and their border at 50% alpha, i.e. roughly 2:1. The
      // buttons were genuinely low-contrast, not a matter of taste.
      //
      // The dark values are MUI's own dark-palette defaults, which exist for
      // exactly this reason. Measured against background.default '#0a1929':
      //
      //   primary   #1976d2 -> 3.89:1   #90caf9 -> 9.94:1
      //   secondary #dc004e -> 3.47:1   #f48fb1 -> 8.77:1
      //
      // Light mode keeps the original brand colours unchanged. Pinned by
      // theme.contrast.test.ts so a future palette edit cannot quietly drop
      // back under the floor.
      primary: {
        main: mode === 'dark' ? '#90caf9' : '#1976d2',
      },
      secondary: {
        main: mode === 'dark' ? '#f48fb1' : '#dc004e',
      },
      background:
        mode === 'dark'
          ? {
              default: '#0a1929',
              paper: '#1e2a38',
            }
          : {
              default: '#f5f5f5',
              paper: '#ffffff',
            },
    },
    typography: {
      fontFamily: [
        '-apple-system',
        'BlinkMacSystemFont',
        '"Segoe UI"',
        'Roboto',
        '"Helvetica Neue"',
        'Arial',
        'sans-serif',
      ].join(','),
    },
    components: {
      MuiAppBar: {
        styleOverrides: {
          root: {
            backgroundImage: 'none',
          },
        },
      },
      MuiDrawer: {
        // ⚠️ 2026-08-11: the `exit: 0` below is NOT what stops the stuck-modal
        // defect. That claim was in this comment from 2026-08-10 and it is
        // WRONG — see "WHAT exit: 0 ACTUALLY DOES" further down. The line that
        // does the work is `slotProps.transition.exit: false`.
        //
        // THE DEFECT (same one as MuiMenu below): close the right-hand filter
        // Drawer with Escape and the Drawer's exit transition can stall
        // forever. The paper animates away and the backdrop reaches opacity 0,
        // so the UI LOOKS closed — but MUI's Modal only unmounts once the
        // child transition reports `exited`, so a `position: fixed; inset: 0`
        // backdrop stays in the DOM and swallows every click on the page until
        // a reload. Confirmed by hit-test: `document.elementFromPoint(20, 300)`
        // returns `.MuiBackdrop-root` whose parent is the Drawer's modal root.
        //
        // MECHANISM, measured 2026-08-11 with in-page instrumentation
        // (`SlideProps` lifecycle hooks + a patched copy of
        // react-transition-group logging every state transition):
        //   1. Escape is delivered correctly. `onClose(_, 'escapeKeyDown')`
        //      fires, `open` goes false. Focus, `isTopModal()` and the Select
        //      menu are all fine — the menu's Modal is already out of the
        //      ModalManager by then. (The four theories in the original bug
        //      report about focus and Escape routing are all disproven.)
        //   2. The Drawer's Slide and the Backdrop's Fade both enter `exiting`
        //      and RTG schedules completion with `setTimeout(cb, exitTimeout)`.
        //   3. The timer FIRES. RTG calls `setState({status: 'exited'})` with
        //      `updater.isMounted(this) === true`.
        //   4. React NEVER APPLIES THAT UPDATE. The same instance re-renders
        //      4ms later still reporting `exiting`, `componentDidUpdate` agrees
        //      at +9ms, and a 300ms-later probe still reads `exiting` on a
        //      mounted instance. Permanent.
        //   5. `onExited` therefore never runs, `useModal`'s `exited` stays
        //      false, and `Modal` never returns null. → step 1 of this comment.
        //
        // WHY `exit: false` FIXES IT: with `exit: false` react-transition-group
        // takes the synchronous branch in `performExit` —
        // `safeSetState({status: EXITED}, () => onExited())` called directly
        // from `componentDidUpdate`, inside React's commit phase — instead of
        // deferring to a timer. The update that gets lost is never scheduled.
        // Visually this is identical to the `exit: 0` we already shipped: the
        // drawer disappears immediately either way.
        //
        // WHAT `exit: 0` ACTUALLY DOES: it narrows the race, it does not close
        // it. Measured on origin/main (MUI 5.18.0, i.e. WITH `exit: 0`
        // shipped), n=10 per cell, chromium, workers=1: an Escape preceded by a
        // CDP round-trip stalled 9/10. On the MUI 6.5.0 branch the same
        // `exit: 0` build stalled 10/10 without the round-trip. v6 did not
        // introduce this defect; it moved the timing window onto the common
        // path. Treat any future "we fixed it by shortening the duration" claim
        // with suspicion.
        //
        // STILL UNEXPLAINED: why React silently drops a setState issued from a
        // setTimeout on a mounted class component inside a portal. Ruled out:
        // duplicate React copies (single react@18.3.1, deduped), StrictMode
        // (dev-only here), flushSync/startTransition (absent from web/src),
        // uncaught exceptions (none), and unmount (componentWillUnmount never
        // runs, isMounted stays true). `exit: false` sidesteps the question
        // rather than answering it.
        //
        // Repro (P1 in the throwaway escape-focus probe, or the real gate):
        //   CI=true npx playwright test --config=tests/e2e/playwright.config.ts \
        //     --project=chromium -g "complete workflow" --repeat-each=10 --workers=1
        defaultProps: {
          slotProps: { transition: { exit: false } },
          // Kept for the enter animation. No Drawer call site passes its own
          // `slotProps`/`SlideProps`, so the defaults above are not clobbered —
          // MUI merges defaultProps shallowly, so adding one would silently
          // drop `exit: false`.
          transitionDuration: { enter: 225, exit: 0 },
        },
        styleOverrides: {
          paper: {
            backgroundImage: 'none',
          },
        },
      },
      // Close Menus with NO exit animation. This is a correctness fix, not a
      // styling preference.
      //
      // THE BUG: a Select menu could get stuck part-way through closing and
      // leave the page unusable. MUI's Modal root is `position: fixed` with
      // `inset: 0`, and it only becomes `visibility: hidden` once the modal
      // has fully exited. If the exit stalls, that root keeps covering the
      // entire viewport with `pointer-events: auto`, so every subsequent
      // click anywhere on the page is swallowed until a reload. The menu
      // itself has already faded to `opacity: 0`, so nothing looks wrong —
      // which is exactly why this was mis-filed twice before.
      //
      // WHAT WAS MEASURED (2026-08-10, 48-core host, 20 copies of the
      // library-browser `clears all filters` e2e test across 12 workers):
      //   - The exit STARTS correctly: paper reaches opacity 0 at ~270ms.
      //   - It never finishes: the paper never receives the inline
      //     `visibility: hidden` that Grow applies in the `exited` state, so
      //     react-transition-group is stuck in `exiting` indefinitely —
      //     observed unchanged 4.8s later, and through a full 30s timeout.
      //   - Stall rate scales with exit duration, which is what makes this a
      //     race rather than a dead timer:
      //         'auto' (~280ms) -> 0/20 passed
      //         250ms           -> 8/20 passed
      //         0ms             -> 20/20 passed
      //
      // NOT EXPLAINED, and deliberately not guessed at here: why RTG's
      // completion callback never runs. Supplying a numeric duration gives
      // RTG its own fallback `setTimeout` and the stall still happened
      // 12/20, so "MUI's auto-timeout is lost" is DISPROVEN as the cause.
      // Zeroing the exit removes the window the race needs, rather than
      // claiming to have fixed the race itself.
      //
      // `enter: 225` keeps the opening animation, which was never implicated
      // — only the exit path stalls. Applied to MuiMenu alone rather than
      // MuiPopover, to avoid changing every Autocomplete and picker popper on
      // the strength of a defect only observed on Select menus.
      //
      // Repro (fails 20/20 without this):
      //   CI=true npx playwright test --config=tests/e2e/playwright.config.ts \
      //     --project=chromium -g "clears all filters" --repeat-each=20 --workers=12
      MuiMenu: {
        defaultProps: {
          transitionDuration: { enter: 225, exit: 0 },
        },
      },
    },
  });
}
