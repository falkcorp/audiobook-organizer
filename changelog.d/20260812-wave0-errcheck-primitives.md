### Added

- Wave 0 of the silent-failure sweep: a single-linter `.golangci.yml` (errcheck with
  `check-blank: true`, which is the whole point — errcheck's default ignores `_ = f()`,
  the exact form of every discard the sweep exists to fix), plus `internal/errhandling`
  providing the two primitives waves 4–13 need: `MustLog` for a deliberate one-off
  discard that leaves an auditable WARN, and `SkipCounter` for loops that `continue`
  on error and silently report a wrong denominator. Added `make lint-errcheck` and
  `make lint-errcheck-full`. Not wired into `make ci` — 922 findings is a backlog to
  burn down, not a gate to fail on today.
