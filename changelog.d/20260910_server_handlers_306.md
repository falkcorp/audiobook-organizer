### Fixed

#### `POST /backup/restore` now actually performs the caller-requested checksum verification

`verify:true` used to log a server-side warning that checksum verification was
"not yet implemented" and then restore anyway, returning the same 200 success
body a real verified restore would return -- a caller relying on the response
(not the server log) could not tell verification never ran. `CreateBackup` now
writes a `<archive>.sha256` sidecar (sha256sum(1) format, atomic temp+rename)
next to every archive it writes; a failed sidecar write fails the backup
rather than warning and proceeding. `backup.RestoreBackup` with `verify=true`
re-hashes the archive and compares it against the sidecar before touching the
restore target: a match restores as normal and reports `"verified": true`; a
mismatch returns `ErrChecksumMismatch` (archive corrupted or tampered with
since creation) without restoring, which the handler surfaces as 409 with the
reason in the response body; a backup with no sidecar at all (created before
this change) still fails closed with `ErrVerificationUnsupported`, reworded to
tell the caller what to do about it, surfaced as 400. `verify=false` is
unchanged and now reports `"verified": false, "verify_requested": false`
explicitly. `DeleteBackup` and retention pruning remove a backup's sidecar
alongside its archive; listing/retention already ignored `.sha256` files
(they don't match the archive-extension filter) so no change was needed
there.
