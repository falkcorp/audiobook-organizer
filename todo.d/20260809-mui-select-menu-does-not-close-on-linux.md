<!-- file: todo.d/20260809-mui-select-menu-does-not-close-on-linux.md -->
<!-- version: 2.0.0 -->
<!-- guid: c47a0e63-91d8-4f52-bb06-3d5e28a71c9f -->
<!-- last-edited: 2026-08-09 -->

- [x] **RESOLVED — it was worker contention, not a defect.** This fragment previously
      claimed "a MUI Select's menu does not close on the ubuntu runner — suspected REAL
      defect". **That was wrong.** Kept rather than deleted, because the sequence of wrong
      answers is the useful part.

      ## What it actually was

      Two chromium tests failed on ubuntu and passed on macOS, both 30s `locator.click`
      timeouts with a MUI modal backdrop still intercepting pointer events. Measured in the
      official Playwright linux image pinned to 2 CPUs:

      | configuration | result |
      |---|---|
      | `--workers=2` | `library-browser` + `scan-import` **FAIL** (3 separate runs) |
      | `--workers=1` | **27 passed, 0 failed** |

      Two browser workers plus the Go server on two cores starve the close **transition**,
      so the backdrop outlives any timeout worth setting. The menu does close; the machine
      is simply too busy to animate it. **Neither the app nor the tests are wrong** — a real
      user is not running two headless browsers on two pinned cores.

      **Fix:** `workers: process.env.CI ? 1 : 2` in `playwright.config.ts`, with the
      measurement recorded inline. Costs wall-clock (chromium ~4.5min → ~9min), which is the
      right trade for a gate meant to block merges.

      ## Four wrong answers, and what killed each

      Worth keeping, because every one of them looked well-evidenced at the time:

      | # | hypothesis | killed by |
      |---|---|---|
      | 1 | MUI close-transition race → add `waitForMenuClosed` at all 18 option sites | CI: failure count unchanged at 3, failures merely moved to the new wait |
      | 2 | The Selects are `multiple`, so the menu stays open by design | Reading `FilterSidebar.tsx` — they are single (`:143`, `:181`); the only `multiple` is `:222`, another control |
      | 3 | **"The menu never closes on linux — suspected real defect"** (this fragment's original claim) | A probe in the linux image: menu gone in <500ms and the value lands ("Stormlight Archive") |
      | 4 | The Drawer backdrop is the sole culprit → wait on `.MuiDrawer-modal` | Strict-mode violation — the sidebar renders twice, so the selector matched 2 nodes; and library-browser's blocker was the Select menu, a different overlay |

      **The lesson is about method, not MUI.** Every hypothesis came from reading a call log
      and reasoning about what *should* follow. What finally settled it was changing one
      variable and measuring: workers 2 → 1. The cheap discriminating experiment was
      available from the beginning and was reached fourth.

      **Second lesson: build the repro before iterating.** Three of the four rounds cost a
      ~6-minute CI cycle each because there was no way to run linux locally. Building that
      (Go binary compiled in a `golang` container because CGO/`libtag` blocks
      cross-compilation, then the official Playwright image) took one round and turned the
      loop into seconds. It should have come first.

      Runner script: `<scratchpad>/linux-probe.sh` — copies the tree in, `npm ci` inside
      (the host `node_modules` is a symlink to another worktree full of darwin binaries),
      starts the prebuilt binary, runs Playwright against it with `CI` unset so
      `reuseExistingServer` attaches instead of trying to `go build`.
