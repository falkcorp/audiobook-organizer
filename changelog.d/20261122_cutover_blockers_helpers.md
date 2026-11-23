### Added

- Two root-owned rollback helpers, `cutover-blockers-list` and `cutover-blockers-apply`, run the R2 runbook's Step 0 offline command (`storage cutover-blockers list`, `fix`, `delete`) without raw `sudo`. Each takes no arguments and is granted in the sudoers drop-in with `""` (exactly two new lines, no wildcard). Both refuse unless the service unit is already `inactive` and never stop or start it. `list` leaves the printed list and, when something blocks, the rows file in `export_dir`; `apply` reads one fixed-name request file from `export_dir` (one `fix` or one `delete`, strictly parsed), runs the command as the service user, writes the printed result to `export_dir`, and deletes the request only after a success.
- `make cutover-blockers-list` and `make cutover-blockers-apply REQUEST=<file>` run the helpers from the Mac and copy the files with size and sha256 checks.
- Owner scripts in `deploy/owner/` (copied to `~/aorg-owner/` on the server): `cutover-blockers-list.sh`, `cutover-blockers-apply.sh`, `r2-p6-reinstall.sh` and `r2-p6-undo.sh`. Each has a `--dry-run`, exits non-zero on failure and is safe to run again.

### Changed

- The R2 cutover runbook's P6 and Step 0 now name the owner scripts instead of commands to type, and Known gap 2 narrows to the one missing piece: nothing tracked stops only the service. The sudoers block and the helper table in `docs/system/deploy-and-gpu-ops.md` list the two new helpers. The owner must reinstall the helper bundle (`bash ~/aorg-owner/r2-p6-reinstall.sh`) to get the new stubs and sudoers lines.
