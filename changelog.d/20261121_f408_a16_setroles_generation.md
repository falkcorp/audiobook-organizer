### Changed

- core/auth: a committed change of a user's roles now bumps rbac's generation and reports it through
  `rbac.Deps.OnChange` (new `rbac.Service.AssignmentsChanged`), so once the kernel publishes it as
  `core.rbac.changed.v1` an open realtime stream drops a removed role's topics at once instead of at its next
  heartbeat (F4-08 A16, dark until the flip binds `OnChange`). An unchanged role set, a refused change and a
  write that never commits report nothing. Rights are not a credential, so no invalidation is published.
