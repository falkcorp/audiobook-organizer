### Fixed

- `storage restore-offline` in place now removes the staging an earlier restore left without linking an intent. That is a restore killed after it staged and before `Stage` linked its intent, or a killed escape. Each rerun takes a fresh id, so these copies of the state files, the encryption key among them, were never removed. The sweep runs under the exclusive swap lock once the slot check passes. It looks in the `.mediacore-restore/` of the state directory, the DB's parent, and each declared state file's or `--keyring`/`--gap-fill` target's directory. It removes `<id>/` only when all of these hold:
  - `<id>/` is a real directory;
  - it holds `staging/` alone;
  - no `restore/<id>` exists;
  - nothing under it is a symbolic link or other special file.

  Anything else that holds `staging/`, and anything it cannot read, is left in place and named in a warning. That includes a `restore/<id>` with no journal that the slot does not name, which a `Stage` stopped before it linked the intent leaves behind. A linked intent (its journal is there, or the slot names it) is the swap's own and is passed over.
- The restore's staging chain is fsynced level by level from the directory holding `.mediacore-restore` (`restorejournal.MkdirAllSynced`) and is 0700 at every level. Before, only the leaf directory was synced, so a power loss after step c could drop the staged copy and step d would finish with the state file missing. A checkpoint state file's staged copy is 0600 from creation, not only after it is verified.
