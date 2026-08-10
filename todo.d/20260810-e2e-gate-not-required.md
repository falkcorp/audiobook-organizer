- [ ] **E2EGATE-NOTREQUIRED** The E2E suite runs on every qualifying PR and its
      result is enforced by nothing. A red run merges exactly as easily as a
      green one. **Owner decision required — do not enable unattended.**

      This is the April-2026 spec-rot incident one layer up. That incident was
      "the suite existed and nothing ran it." The state now is "the suite runs
      and nothing acts on the answer," which is quieter and looks healthier.

      **Measured 2026-08-10 ~09:5x EDT**, on `main` at `dc724b80`:

          gh api repos/falkcorp/audiobook-organizer/rules/branches/main

      That endpoint is the authoritative one — it returns every rule applying to
      the branch **including org-inherited rulesets**, which a repo-scoped
      `/rulesets` query misses. It returned exactly four rules, all from the
      `falkcorp` **Organization** ruleset:

          required_linear_history
          deletion
          repository_delete
          repository_transfer

      There is **no `required_status_checks` rule and no `pull_request` rule**,
      and classic protection (`/branches/main/protection`) returns 404 "Branch
      not protected". Consequences, all of which follow directly:

      - A PR whose E2E run fails can be merged with the normal green button.
      - `gh pr merge --admin` bypasses nothing on this repo, because nothing is
        required. Tonight's four merges (#2277–#2280) were gated only by the
        agent session's own bash poll loop, not by GitHub.
      - Nothing requires a pull request at all; a direct push to `main` is
        blocked only by `required_linear_history`.

      **The fix is not simply "turn on required status checks."** `e2e.yml`
      carries `paths: ['web/**', '**.go', 'go.mod', 'go.sum',
      '.github/workflows/e2e.yml']`. A required check that is filtered out of a
      given PR can leave that PR pinned at *"Expected — Waiting for status"*
      forever, which would strand every docs-only PR — the exact shape of
      #2279 and #2280. GitHub's behaviour here depends on how the check is
      registered, and **this has not been measured on this repo.** Resolve that
      before enabling, e.g. with an always-runs `E2E Summary` job that reports
      success when the real job is skipped by path.

      **What was checked and found sound**, so it is not the problem: the
      `paths` filter does cover the whole suite. Every spec, plus
      `playwright.config.ts`, `global-setup.ts` and `utils/test-helpers.ts`,
      lives under `web/tests/e2e/`, which `web/**` matches. A fixture or config
      change like the one that caused the original four-month rot *would*
      trigger the job today.

      **NOT claimed:** that enabling enforcement is safe as-is; that the
      `paths`/required-check interaction behaves any particular way here; or
      that any PR has actually merged red. No merged-red PR was searched for.

      Fixed in the same PR as this fragment: `e2e.yml`'s header comment
      asserted the job "BLOCKS on every trigger", which was never true. A
      comment claiming a gate exists is worse than no comment, because it stops
      anyone from checking.
