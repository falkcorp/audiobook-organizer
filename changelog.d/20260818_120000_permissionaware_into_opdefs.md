### Fixed

#### Maintenance ops now register the permission their job actually asks for

`registerMaintenanceJobOp` hardcoded `settings.manage` for all 37 maintenance
operations. That was harmless while `OperationDef.Permissions` was persisted and
read by nothing — but #2536 made `POST /operations/v2` enforce it, which turned
the hardcoded value into the operative one.

`bulkFetchMetadataJob` is the single job implementing `PermissionAware`, and it
asks for `library.edit_metadata`. Its def declared `settings.manage`, so a caller
holding exactly the permission the job requires was refused, while a caller with
`settings.manage` and no metadata rights was let through. The v1 dispatcher route
applied the correct rule, so the two trigger paths disagreed — and phase 1 of the
kill-v1 plan deletes the one that was right.

The def now uses the job's own `PermissionAware` value where it has one and
defaults to `settings.manage` otherwise, deliberately identical to the v1
dispatcher's rule so retiring that route cannot change who can run what.
