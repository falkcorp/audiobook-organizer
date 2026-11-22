### Added

- `make export-keyring` and `make export-install-secrets` bring the files only the service user can read (the keyring from `secrets export-keyring`, the install's `.encryption_key` and `config.yaml`) to the machine you run `make` on, through two new root-owned rollback helpers (`export-keyring`, `export-install-secrets`), with no raw `sudo`. Each helper takes no arguments and is granted in sudoers with `""`. The reads run as the service user in a transient unit and reach the helper over a pipe, not the group-readable restore log. The helper writes each file into a new `export_dir` setting in `rollback.conf` (a directory the deploy user owns, mode 0700, opened with no symlink in any component) with `O_EXCL|O_NOFOLLOW`, mode 0600, owned by the user who ran `sudo`, under a fixed timestamped name. The make target copies the files with `scp` into `OFFHOST_DIR` (default `~/aorg-offhost/<UTC stamp>`, mode 0700), checks size and sha256, and only then deletes the remote copies. Neither helper stops the service or needs it stopped.
- `install_helpers.py` takes `--export-dir`, and the owner must reinstall the helper bundle (`make install-rollback-helpers APPLY=1`) to get the new stubs and sudoers lines.

### Changed

- `offline_env` in `rollback.conf` now accepts `ABO_*` names beside `HOME` and `ABK_*`. The flipped binary refuses `ABK_STATE_DIR` at boot and in its offline commands, so a helper that runs one as the service user needs `ABO_CORE_PATHS_STATE_DIR`.
- The R2 cutover runbook's Step 1 and Step 7 name the two make targets, add precondition P6 (the reinstalled bundle with `export_dir`), and narrow Known gap 2 to `storage cutover-blockers`.
