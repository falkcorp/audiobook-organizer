<!-- file: docs/security/path-validation-policy.md -->
<!-- version: 1.0.0 -->
<!-- guid: 68cafa38-50e8-4a5b-905e-e8a7dea5134f -->
<!-- last-edited: 2026-08-22 -->

# Path Validation Policy

## Purpose

This document defines how the audiobook-organizer validates filesystem paths to prevent
path injection vulnerabilities (CWE-22, CodeQL `go/path-injection`).

## Rules

### Rule 1 — User-supplied paths must use safepath.Join

All filesystem paths that include values from user input, the database, or external sources
MUST be constructed via `internal/security/safepath.Join` or validated with
`internal/util.WithinRoot` before use. Direct calls to `filepath.Join` with unvalidated input
are prohibited.

### Rule 2 — Prefer SafePath as the typed return value

`safepath.SafePath` is the preferred type for passing validated paths between functions.
Where a string return is needed for external APIs (taglib, os calls), call `.String()`.
This makes validation boundaries visible in function signatures.

### Rule 3 — Protected paths are an additional guard, not a replacement

Protected paths (iTunes mount, Deluge download dir) are additionally guarded by
`isProtectedPath` in the server layer. `safepath.Join` validation and protected-path checks
are complementary — both must pass before any write operation.

## Sanitizer declarations

The CodeQL MaD pack at `.github/codeql/models/go-sanitizers.model.yml` declares
`safepath.Join` and `util.SafeJoin` (and its alias `util.WithinRoot`) as sanitizers.
This causes CodeQL to treat any path value flowing through these functions as safe,
suppressing `go/path-injection` alerts for their outputs.

## Dismissed alerts — maintenance/jobs/

The 13 alerts previously dismissed in `internal/maintenance/jobs/` (approximately alerts
#544–#560) were dismissed because they all construct paths using `util.SafeJoin`, which is
declared as a sanitizer in the MaD pack. These are true negatives. The dismissal rationale
is "Used in tests" / already validated at entry point via `util.SafeJoin`.

## Review checklist for new filesystem code

1. Does any user-controlled value (request param, body field, DB column sourced from
   external input) flow into `os.Open`, `os.Stat`, `os.ReadFile`, `filepath.Join`, or
   similar functions?
2. If yes: wrap with `safepath.Join(root, userValue)` where `root` is a trusted base path
   (config value or hardcoded constant). Return HTTP 400 on escape attempts (when
   `safepath.Join` returns an error).
3. Pass `SafePath` values between functions; call `.String()` only at the OS boundary.
4. Do NOT skip the `isProtectedPath` check for any write path — it guards the iTunes mount
   and Deluge download directory independently of safepath validation.

## Reference

- Sanitizer package: `internal/security/safepath/`
- Legacy sanitizer: `internal/util/safe_join.go` (alias: `util.WithinRoot`)
- CodeQL model: `.github/codeql/models/go-sanitizers.model.yml`
