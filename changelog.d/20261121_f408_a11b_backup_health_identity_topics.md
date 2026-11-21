### Added

- core/kernel: `core.backup`'s health probe (`backupProbe`), which combines the revocation journal's five tokens and
  rbac's two restore tokens into the component's one result: the highest severity wins, and a fixed, documented order
  (`backupReasonRank`) picks the Reason among equal severities; the Detail lists every token (F4-08 A11b, dark).
- core/kernel: the identity control topics `core.rbac.changed.v1` and `core.principal.invalidated.v1` join core's
  catalog, with their publishers (`publishRBACChanged`, `publishPrincipalInvalidated`), so realtime can end a revoked
  credential's stream and re-check rights at once instead of on the next heartbeat (F4-08 A11b, dark). Nothing
  declares core's catalog or calls the publishers until the identity flip wires the boot.
