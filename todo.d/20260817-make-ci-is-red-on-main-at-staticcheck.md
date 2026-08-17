- [ ] **`make ci` cannot pass on `main` — staticcheck has 10 findings and aborts the target**

  Measured 2026-08-17 by running `make staticcheck` at `origin/main` (detached) and on a
  feature branch and diffing the two lists: **10 findings, byte-identical on both**. The
  feature branch introduced none and removed none. Because `staticcheck` runs before
  `test-all-short` and `coverage-check-short` in the `ci:` target, `make ci` exits 1 on a
  clean checkout of `main`, and the two stages after it never run at all.

  Why this went unnoticed: GitHub CI merges PRs green, so whatever the required checks run,
  it is not this target. The documented local gate and the enforced remote gate disagree —
  which means "I ran `make ci`" currently proves less than it reads.

  The 10 findings (8 are dead code, 1 is a real nil-deref candidate):

  - `internal/metafetch/service_apply.go:637` — **SA5011 possible nil pointer dereference**,
    with the contradicting nil-check at `:662`. This is the one with a bug behind it.
  - `internal/plugins/maintenance/regroup_shattered_ai_test.go:180` — SA4006/SA4010, an
    `append` result that is never used. A test that discards what it builds.
  - U1000 unused: `dlIntPtr` + `dlInt64Ptr`
    (`internal/database/dataloss_preserve_invariant_test.go:26-27`), `(*Plugin).pathRepairDef`
    (`internal/plugins/itunes/path_repair.go:16`), `updatedBooks` field
    (`internal/plugins/maintenance/author_conjunction_repair_test.go:22`), `udRowByItem`
    (`internal/server/handlers/abs/userdata_test.go:332`), `errString`
    (`internal/server/handlers/metadata_cache.go:403`), `operationV2ToLegacy`
    (`internal/server/handlers/operations/handler.go:114`).

  Fix order that matters: triage the SA5011 first (it is the only one that can misbehave at
  runtime), then clear the U1000s, then decide whether staticcheck belongs in the required
  remote checks — a gate that only fails locally trains people to skip it.
