### Security

#### core/backup: a staged restore is bound to the archive its verification accepted

`StageRestore` verified the selected archive at step 2 and opened its path again
at step 3 without checking that the file was still the same archive. A file
renamed over the path in between staged its own content under the selected ID.
Step 3 now refuses a manifest whose `archive_id` is not the selected ID. The
extraction hashes the bytes it reads and refuses when they do not hash to the
digest the verification vouched for, so a foreign archive forged to name the
selected ID is refused too. The entries and open verification levels now vouch
for the digest of the bytes their entries pass read, so a no-sidecar foreign
restore has a digest to bind to. The refusals map to 409
`archive_verification_failed`. The legacy refusal does as well, because it now
wraps `ErrChecksumMismatch`. Dark: `core/backup` is not wired into the deployed
binary.

#### core/backup: a refused restore leaves no bare `core.restore.stage` record

The `core.restore.stage` audit record was written before extraction, so a
restore refused afterwards left a record that read like a staged restore. The
record is now written after every refusing check, just before the intent
create. A refusal counts once in `mediacore_backup_restore_total{stage="rejected"}`.
A create that refuses after the record writes a second record with
`outcome=rejected` and a fixed reason code (`swap_pending`, `rename_refused`,
`stage_failed`). `RevertLastRestore` follows the same rule.
