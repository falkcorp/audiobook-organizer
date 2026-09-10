### Fixed

#### `POST /backup/restore` no longer silently skips caller-requested checksum verification

`verify:true` used to log a server-side warning that checksum verification was
"not yet implemented" and then restore anyway, returning the same 200 success
body a real verified restore would return -- a caller relying on the response
(not the server log) could not tell verification never ran. Backups carry no
persisted checksum to verify against (the archive's SHA-256 is computed and
returned at create/list time but never written to disk alongside the file), so
there is nothing durable to check a restore against. `backup.RestoreBackup` now
fails closed on `verify=true` with `ErrVerificationUnsupported` before touching
the filesystem, and the handler returns 400 with the reason in the response
body instead of proceeding; a successful restore now also reports
`"verified": false, "verify_requested": false` explicitly so the response
always reflects the real verification status.
