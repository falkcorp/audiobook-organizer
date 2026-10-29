### Changed

- The scanner's ownership skip no longer carries its file list inside an error.
  `saveBook` and `saveBookToDatabase` now return an `*ownershipSkip` record
  (owners, reason, and the files to append to the owning book) beside a bare
  `errFileOwnedByOtherBook`, and `handleOwnershipSkip` takes that record instead
  of unpacking an error with `errors.As`. Behaviour is unchanged.
- CodeQL alerts #2022 and #1651 (go/path-injection), dismissed as false
  positives on 2026-10-28, can be re-opened once this merges.
