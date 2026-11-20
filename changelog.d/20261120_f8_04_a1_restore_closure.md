### Security

#### `core/backup` — staging a restore needs the owner proof, plain restores included (dark, F8-04 A1)

05-backup §5B.3.4 names the restore's API phase "task core.backup.stage-restore
(POST /api/core/v1/backups/{id}/restore, permission core:backup.restore
(Sensitive, AuthRequired, 05-rbac), credential-change route with owner proof".
The service's gate only asked for proof when a privileged option (foreign,
replace config, accept unreadable secrets, weak verification) was set, so a
plain restore reached through the `core.backup.stage-restore` task needed only
`core:backup.restore`, with no interactive owner. `StageRestore` and
`RevertLastRestore` now refuse every staging without the owner proof the
restore routes establish (`ErrRestoreNotAuthorized`), and the task keeps
`core:backup.restore` as its start permission but is marked owner-route-only,
so the generic task API refuses to start it. No run of the task can stage a
restore until whatever enqueues it can hand the run an owner proof; the restore
route stages restores itself. Nothing registers core backup yet, so nothing in
production changes. An archtest (`RestoreProofCallers`) now fails the build when any
non-test file outside `core/internal/backuphttp`, `core/backup`'s own task
bodies included, mints the proof with `backup.WithRestoreProof`.

### Fixed

#### `core/backup` — legacy restores are re-hashed at boot and bound to the archive that was verified (dark, F8-01 review m6)

A HEAD-format (`legacy-abo/0`) archive has no MAC and no per-entry digests.
The staged legacy manifest recorded only file sizes, so
`core.backup.restore.rehash_on_boot` silently skipped every legacy file and a
same-size corruption of the staged store passed the boot re-check. Each staged
file's SHA-256 is now recorded as it is written, and a full re-hash refuses an
entry that has no digest instead of skipping it. Staging also refuses a legacy
archive whose bytes differ from the ones the verification accepted (an archive
replaced, sidecar and all, between verification and extraction), and
verification refuses an archive replaced between its sidecar check and its
scan. The offline `storage restore-offline --archive` extractor is unchanged.
