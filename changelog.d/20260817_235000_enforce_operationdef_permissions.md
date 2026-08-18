Fixed: `POST /operations/v2` now enforces the permissions each operation
declares. Previously every operation sat behind a single blanket `scan.trigger`
check, so an account with the `editor` role could start maintenance operations
that the older maintenance route reserved for administrators.
