- [x] **GOFMT-SWEEP** `gofmt -l` reported **43 unformatted Go files across 24
      packages** on `main` (measured 2026-08-20, excluding `web/`). Root cause was
      the same one behind `sdkguard` and the bench build: **`gofmt` was verified
      nowhere** — `grep -rn 'gofmt' .github/workflows/ Makefile` returned zero
      hits, so there was no format check in CI and no `make fmt`/`fmt-check`
      target, and drift accumulated silently.

      **Done.** Both steps landed together, in the required order: the 43 files
      were swept, and only then did a `make fmt-check` target join `make ci` and
      the CI job (renamed `Repo Guards`, since it now covers three checks). The
      gate could not have preceded its own sweep without being red on 43
      pre-existing files.

      Verified semantically inert: `gofmt` is idempotent on the result, and all
      24 affected packages pass `go test -short` (22 with tests, 2 without).
      Note the sweep was **not** whitespace-only, as first assumed — alongside
      indentation, `gofmt` split `stmt; os.Exit(1)` onto separate lines, expanded
      inline struct definitions, and normalised doc comments to the Go 1.19+
      heading form. `git diff -w` was therefore not empty; the tests are what
      establish inertness, not the whitespace-ignoring diff.
