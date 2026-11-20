### Changed

- The HTTP Basic gate is core's perimeter stage 8 for the legacy mount, not a gin middleware. `internal/server/middleware/basicauth.go` and its `router.Use` are deleted; the exempt set (the health routes, ABS routes with their own auth, the asset paths) comes from the legacy route manifest and is pinned against HEAD's behaviour by `internal/server/head_basic_gate_test.go`. The password is compared with `crypto/subtle`; an enabled gate with no usable password refuses every non-exempt request.
- `DELETE /api/v1/health` answers 401 under an enabled gate (the gin gate let every method through on that path). A Basic-gate refusal of a path core redirects is labelled with the mount's policy (core `policyOf`).
