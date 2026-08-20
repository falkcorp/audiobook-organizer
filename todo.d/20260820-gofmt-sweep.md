- [ ] **GOFMT-SWEEP** `gofmt -l` reports **43 unformatted Go files across 24
      packages** on `main` (measured 2026-08-20, excluding `web/`). Deferred out
      of the bench-build/sdkguard PR deliberately: formatting 43 files is a
      different change from fixing two broken gates, and would bury that diff.

      Root cause is the same one that PR fixed twice over: **`gofmt` is verified
      nowhere.** `grep -rn 'gofmt' .github/workflows/ Makefile` returns zero
      hits — there is no format check in CI and no `make fmt`/`make fmt-check`
      target, so drift accumulates silently and nothing ever reports it.

      Two pieces of work, in this order:
      1. Sweep — `gofmt -w` the 43 files. Purely mechanical, no behaviour
         change, but it touches 24 packages, so land it alone and rebase
         in-flight branches after rather than before.
      2. Add a `fmt-check` target (`gofmt -l` with a non-empty result failing),
         put it in `make ci` next to `sdkguard` and `bench-check`, and add it to
         the `SDK Deps & Bench Build` job in `ci.yml`. Without step 2 the sweep
         is a one-off and the debt just re-accrues.

      Note the ordering constraint: step 2 must not land before step 1, or CI
      goes red on 43 pre-existing files — the exact failure mode the
      `--enable-only nolintlint` comment in `ci.yml` describes for errcheck, and
      the reason `interfacebloat` is kept out of that selector.

      Reproduce: `gofmt -l . | grep -v '^web/'`
