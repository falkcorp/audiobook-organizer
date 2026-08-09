<!-- file: todo.d/20260809-webkit-scan-import-drawer-backdrop.md -->
<!-- version: 3.0.0 -->
<!-- guid: 4d20e8b7-1c63-49fa-85e0-7b3f9a06c214 -->
<!-- last-edited: 2026-08-09 -->

- [x] **RESOLVED — webkit was marginal on TIMING, and its own 60s budget fixed the
      class.** The nightly now blocks too; `continue-on-error` is a plain `false`.

      **Final measurement on the real runner:**

      | configuration | result |
      |---|---|
      | chromium (PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **544 passed / 0 failed / 16 skipped** |

      The fix was one line — `timeout: 60 * 1000` on the webkit project only, chromium
      keeping 30s — and it was chosen as the *discriminating experiment* for the
      population hypothesis rather than as a workaround. It came back green, so the
      hypothesis is confirmed: webkit had several tests close to the shared 30s limit and
      roughly one lost per run.

      **Why this is headroom and not blindness:** a genuinely broken test does not finish
      in 60s either. What changed is that a slow-but-correct one stopped being reported as
      a failure. Chromium keeps the tighter budget because it had margin to spare once CI
      dropped to one worker.

      **Cheaper than the plan this fragment originally proposed.** It suggested three
      measurement runs to characterise the failing set first; one config change answered
      the same question and fixed it. Worth remembering: when a hypothesis implies a
      one-line change, the change often IS the measurement.

      ---

      **Original entry, for the record.**

      ## The update that changes the shape of this problem

      The drawer fix landed and **worked** — `scan-import-organize.spec.ts:259` passed on
      CI. But the same both-engine run came back with an identical score and a different
      casualty:

      | run | result | which test failed |
      |---|---|---|
      | before the fix | 543 / 1 / 16 | `[webkit] scan-import-organize.spec.ts:259` |
      | after the fix (#2253) | 543 / 1 / 16 | `[webkit] itunes-bidirectional-sync.spec.ts:121` |

      Different spec file, so the fix cannot have caused it. **The conclusion: webkit under
      CI has several tests sitting close to their timeouts, and roughly one loses per run.**
      Fixing them individually is a treadmill — each fix is real, and the score does not
      move.

      **So do not chase individual webkit failures.** Find out why webkit is marginal as a
      class:

      1. **Measure the margin.** Dispatch the webkit suite on CI 3+ times and collect the
         failing set. If it is large and varies run to run, this is systemic timing, not N
         separate bugs.
      2. **Consider a per-project timeout.** The config uses one 30s `timeout` for both
         engines, but webkit is measurably slower here — chromium stopped failing at
         `workers: 1` and webkit did not. A per-project override is a one-line change that
         would settle whether headroom is all that is missing.
      3. Only then decide whether individual tests need waits.

      ## Original entry — the drawer case, now FIXED (#2253)

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
