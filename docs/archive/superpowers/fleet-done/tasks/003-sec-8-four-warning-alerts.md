# Task 003: SEC-AUDIT-8 — Fix four warning-level security alerts

**Depends on:** none
**Estimated effort:** M (2–3 hours)
**Wave:** 1 (run immediately, no dependencies)

## Goal

Fix four warning/medium severity CodeQL/Dependabot alerts:
- Alert #379: Disabled TLS cert verification in `internal/mtls/provisioning.go`
- Alert #468: Potential allocation overflow in `internal/itunes/itl.go`
- Alert #160: TLS cert verification disabled in `scripts/record_demo.js`
- Alert #50: Incomplete URL sanitization in `web/src/pages/Settings.tsx`

## Context

- `internal/mtls/provisioning.go:138` — `InsecureSkipVerify: true` in bootstrap TLS config
- `internal/itunes/itl.go` — uint32 arithmetic that could overflow on large ITL files
- `scripts/record_demo.js:27` — `rejectUnauthorized: false` in HTTPS agent (dev script)
- `web/src/pages/Settings.tsx:1873` — `sanitizeImportPayload` with incomplete sanitization

## Files to modify

- `internal/mtls/provisioning.go`
- `internal/itunes/itl.go`
- `scripts/record_demo.js`
- `web/src/pages/Settings.tsx`

## Instructions

### Alert #379 — `internal/mtls/provisioning.go`

Find `InsecureSkipVerify: true` at line 138. Add a `// #nosec G402` comment with rationale:
```go
// #nosec G402 -- bootstrap-only: InsecureSkipVerify is required during initial mTLS
// cert provisioning before a valid client cert exists. This code path only runs once
// per installation and is never used in normal operation.
&tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13},
```

If this same config is also used in normal (non-provisioning) operation, create a separate
config for provisioning vs. production that does NOT skip verification in production.

### Alert #468 — `internal/itunes/itl.go`

Find uint32-to-slice conversions like `make([]byte, someUint32Field)` where the field comes
from parsing untrusted binary data. Add a max-size cap before any such allocation:
```go
const maxITLFieldSize = 256 * 1024 * 1024 // 256 MiB sanity cap
if someField > maxITLFieldSize {
    return nil, fmt.Errorf("itl: field size %d exceeds max %d", someField, maxITLFieldSize)
}
```
Add the check immediately before any `make([]byte, n)` or `make([]SomeType, n)` where n
derives from the parsed file.

### Alert #160 — `scripts/record_demo.js`

Find `rejectUnauthorized: false` at line 27. Remove it. If the script needs to work with
self-signed certs in dev, gate it explicitly:
```js
// Remove the rejectUnauthorized: false — use proper certs or NODE_EXTRA_CA_CERTS env var
```
If the script is only for local development and removing this breaks it, document how to
use `NODE_EXTRA_CA_CERTS` instead, and remove the unconditional disable.

### Alert #50 — `web/src/pages/Settings.tsx`

Find `sanitizeImportPayload` around line 1873. The alert means CodeQL sees user-controlled
input flowing into a dangerous sink. Review the function:
- If any field value is rendered as HTML (via `innerHTML` or similar), ensure it is encoded
- Ensure all string fields pass through a string type check: `typeof val === 'string'`
- Ensure no field can be set to an object or function that bypasses validation
- Add explicit allowlisting of expected fields rather than blocklisting unexpected ones

## Test

```bash
make ci
make build
```

Bump version headers on every modified file.

## Commit (one per file)

```
fix(mtls): document InsecureSkipVerify bootstrap rationale (SEC-AUDIT-8 #379)
fix(itl): add size cap before uint32 buffer allocations (SEC-AUDIT-8 #468)
fix(scripts): remove unconditional TLS bypass from demo script (SEC-AUDIT-8 #160)
fix(settings): tighten sanitizeImportPayload type checks (SEC-AUDIT-8 #50)
```

## PR title

`fix(security): four warning-level alerts — SEC-AUDIT-8`

## After merging

Mark `- [ ] **SEC-AUDIT-8**` as `- [x]` in `TODO.md`.
