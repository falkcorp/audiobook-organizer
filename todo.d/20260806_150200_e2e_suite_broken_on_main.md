<!-- file: todo.d/20260806_150200_e2e_suite_broken_on_main.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c7f480a-6b13-49de-95a1-8e4d3b6f0721 -->
<!-- last-edited: 2026-08-06 -->

- [ ] **The Playwright e2e suite is broken on `main` and gates nothing.** Every
  test dies at fixture collection with `unknown parameter "_page"` — 49 errors.
  Confirmed pre-existing on 2026-08-06: the identical failure reproduces on the
  pre-react-router-v7 tree with unchanged specs, and the v7 PR touched zero files
  under `web/tests/`.

  Why this matters beyond the red: the react-router v6 → v7 upgrade merged with
  **no runtime routing signal at all**. `tsc` was clean and 402 frontend unit
  tests passed, but nothing exercised actual navigation. A routing major landing
  without e2e coverage is precisely the case the suite exists for.

  Fix the fixture signature, then re-run against the v7 tree to retroactively
  confirm the upgrade — and treat `make test-e2e` as a required gate for any
  future routing or auth-flow change.
