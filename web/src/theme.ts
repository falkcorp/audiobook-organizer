// file: web/src/theme.ts
// version: 1.2.0
// guid: 2b3c4d5e-6f7a-8b9c-0d1e-2f3a4b5c6d7e
// last-edited: 2026-08-10

import { createTheme } from '@mui/material/styles';

type PaletteMode = 'light' | 'dark';

export function createAppTheme(mode: PaletteMode = 'dark') {
  return createTheme({
    palette: {
      mode,
      primary: {
        main: '#1976d2',
      },
      secondary: {
        main: '#dc004e',
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
