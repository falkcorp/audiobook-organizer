<!-- file: todo.d/20260809-webkit-scan-import-drawer-backdrop.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4d20e8b7-1c63-49fa-85e0-7b3f9a06c214 -->
<!-- last-edited: 2026-08-09 -->

- [ ] **One webkit test still fails on CI: the Drawer backdrop swallows the click after
      Escape.** This is the only thing keeping the *nightly* (chromium + webkit) advisory
      rather than blocking. The PR path (chromium) is green and blocks as of 2026-08-09.

      **Measured on the real runner:**

      | configuration | result |
      |---|---|
      | chromium (the PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **543 passed / 1 failed / 16 skipped** |

      The one failure: `[webkit] scan-import-organize.spec.ts:259` — *complete workflow: add
      import path → scan → organize*. After `page.keyboard.press('Escape')` closes the
      filter drawer, the `Select All` click times out at 30s because MUI's full-page modal
      backdrop is still intercepting pointer events:

      ```
      <div class="MuiBackdrop-root MuiModal-backdrop"> from
      <div aria-hidden="true" class="MuiDrawer-root MuiDrawer-modal MuiModal-root">
      subtree intercepts pointer events
      ```

      `workers: 1` (#2249) fixed the identical failure on chromium. **Webkit is slower and
      it persists there.**

      ## 🚨 Read this before you start: the local container is NOT a valid oracle

      The linux repro container (`<scratchpad>/linux-probe.sh`, `--cpus=2`) is **harsher
      than the GitHub runner** and invents failures CI does not have. Across four runs of
      the same spec it produced four different signatures:

      | attempt | what failed |
      |---|---|
      | baseline | `:259` drawer backdrop (matches CI) |
      | after `toHaveCount(0)` fix | `:259` **plus** `:386` "Cancel Scan" — a test CI passes |
      | after re-run | `:259` plus `:390` |
      | after visible-filter fix | `:259` failing **earlier**, on the Filters button itself |

      Tuning against it is a treadmill — it was exited deliberately, not because the problem
      was solved. **Iterate against a dispatched CI run, or a container with more CPU.**

      ## Three assertion shapes already ruled out, by measurement

      Do not re-try these:

      | shape | why it fails |
      |---|---|
      | `expect(locator).toBeHidden()` | **Strict-mode violation.** Sidebar renders its content twice (temporary Drawer + permanent one), so the selector matches 2 nodes |
      | `expect(locator).toHaveCount(0)` | **Never converges.** Count sits at 2 forever — MUI keeps the backdrop MOUNTED and merely hides it |
      | `.filter({ visible: true })` + `toHaveCount(0)` | Failure moved earlier (to the Filters button) in the container; **unvalidated against CI**, so this one is "unproven", not "disproven" |

      ## Suggested next steps

      1. Re-test the `.filter({ visible: true })` variant **on CI**, not in the container.
         It is the only shape that is semantically right for a hidden-not-unmounted
         backdrop, and it was abandoned because of an unreliable oracle rather than
         evidence against it.
      2. If that is not enough, consider whether the test should dismiss the drawer by
         clicking its close control rather than pressing Escape — a more deterministic
         path than relying on a transition finishing.
      3. **Do not add a blind retry.** The app does close the drawer; the wait is legitimate
         and should assert the closed state, not paper over it.

      **When this is green, `continue-on-error` in `.github/workflows/e2e.yml` becomes a
      plain `false`** and the nightly blocks too. The conditional expression there exists
      only because of this one test.
