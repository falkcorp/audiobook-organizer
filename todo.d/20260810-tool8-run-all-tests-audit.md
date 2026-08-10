- [ ] **TOOL-8** Decide whether `scripts/run-all-tests.sh` is retired or wrapped
      as a Makefile manual-smoke target. **Audited 2026-08-10 — the evidence is
      below, the decision is not made.**

      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` filed this as
      "retire or wrap as explicit Makefile manual-smoke targets" and it has never
      been resolved. #2278 *fixed* the script rather than retiring it (its `if
      cmd | tee log` pipelines meant every step reported PASSED unconditionally),
      which settled correctness but not the question of whether it should exist.

      **Nothing automated calls it.** Zero GitHub workflows, zero Makefile
      targets. Every reference in the repo is prose: `docs/archive/`, the audit
      row, and the #2278 changelog fragment.

      **What it does vs. what `make` already does:**

      | Behaviour | `run-all-tests.sh` | Makefile equivalent |
      | --- | --- | --- |
      | Go tests + coverage profile | yes | `make test`, `make coverage` |
      | Go **HTML** coverage report | yes (line 49) | `make coverage` (line 313) — duplicated |
      | Frontend unit tests | yes | `make web-test`, `make test-all` |
      | Playwright e2e | yes | `make test-e2e` |
      | **All three surfaces in one run, continuing past a failure, ending in a pass/fail matrix** | **yes** | **none** |
      | Serves the Playwright HTML report on :9323 | yes | none |

      **So exactly one capability is unique**: the non-fail-fast three-surface
      sweep. `make test-all` is `test web-test` — it does not include e2e, no
      target depends on `test-e2e`, and make is fail-fast, so `make test-all
      test-e2e` stops at the first failing surface instead of reporting all
      three. That is a genuine local pre-PR workflow, and it is also about four
      lines as a `.PHONY` target.

      **Two latent defects if it is kept** (both would need fixing; neither is
      worth fixing if it is deleted):

      - It never clears a stale server on `:8484` before the e2e step. With
        `reuseExistingServer: !process.env.CI`, Playwright silently attaches to
        whatever is already listening, so the e2e verdict can describe a
        different build than the one just compiled. This is the documented
        footgun and the script has no `lsof`/kill at all.
      - Line 80 backgrounds `npx playwright show-report --port 9323 &` and never
        kills it, leaving a server running after the script exits.

      **Recommendation (owner decides):** wrap the one unique behaviour as
      `make test-everything` — run all three surfaces with `-k`-style
      continue-on-failure and print the matrix — then delete the 123-line
      script. Retiring it without that target would lose the only thing it does
      that `make` cannot.
