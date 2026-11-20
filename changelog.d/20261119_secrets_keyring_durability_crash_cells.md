### Fixed

- The secret store no longer trusts a keyring file whose directory entry may not be durable. A boot that created the keyring linked it into place and then synced the directory; when that sync failed the boot returned the error, but the entry was only in the page cache, so the next boot found the file, trusted it and sealed secrets under it, and a power loss could then drop the entry and leave stored secrets with no keyring (every secret re-entered). `secrets.Open` now syncs the keyring's directory (crash-point label `secrets.keyring.load-sync`) before it loads a present keyring and fails closed, before anything is sealed. `export-keyring` does the same (label `secrets.export-sync`) before it copies the keyring, so an operator never holds a backup of a key the next boot does not have. The shipped binary does not reach either path until the config boot is switched on (F3-07 slice e8).

### Added

- Crash-matrix cells for every boot-time write F3-07 introduces: the keyring and `c/secrets/meta` writes, the config rename migration (M2) that drains the four secret rows, both halves of the level step (M7) and the store rewrite (M8), over both the audiobook organizer and the hello example. After each kill the secrets are either in place or sealed in the core store, never lost or half drained.
- The crash-matrix kit understands a store rewrite's swap (a rewrite keeps no `previous/` once applied; a killed rewrite is swapped back and retried under a new journal id), and its error mode skips the operations Pebble itself panics on (a failed write-ahead-log rotation kills the process in production, which kill mode already covers).

### Fixed

- The crash-matrix recorder no longer deadlocks when a crash clone is taken while Pebble opens a store (`MemFS.Lock` and `ReuseForWrite` re-enter the clone mutex).
