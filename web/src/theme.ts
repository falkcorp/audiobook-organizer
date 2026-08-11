// file: web/src/theme.ts
// version: 1.4.0
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
        // exit: 0 for the same reason as MuiMenu below — see that comment for
        // the mechanism and the measurements. This is the SECOND component hit
        // by the same stuck-modal defect, and it was named alongside the Select
        // menu in playwright.config.ts long before either was understood.
        //
        // Measured 2026-08-10 on current main (which already carries the
        // MuiMenu fix), 20 copies of the scan-import-organize "complete
        // workflow" e2e test across 12 workers: 17/20 FAILED with
        // `.MuiDrawer-modal .MuiBackdrop-root` still visible after 15s.
        //
        // Note Drawer already used NUMERIC durations (enteringScreen /
        // leavingScreen), so this is direct evidence against the "a numeric
        // duration restores react-transition-group's fallback timer and fixes
        // it" theory — the Drawer had that fallback all along and stalls
        // anyway. Only removing the exit window helps.
        defaultProps: {
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
