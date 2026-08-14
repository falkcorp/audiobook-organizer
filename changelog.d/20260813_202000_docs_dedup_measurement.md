### Changed

- Corrected the 2026-08-11 duplication audit. The `Unknown Author/` tree is not
  14 TB of reclaimable space: ZFS block cloning is active and the duplicate
  files already share blocks with their sources. A 50-file pilot — verified
  byte-identical, reflink-replaced, snapshot destroyed so freed blocks could be
  released — reclaimed nothing against a 36 MB/min noise floor. `du` and
  `logicalused` both fail to show block sharing; `bclonesaved` (21.8 TB) is the
  instrument that does. The version-group findings are unaffected.
