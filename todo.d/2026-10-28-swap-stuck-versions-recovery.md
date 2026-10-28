- [ ] **TODO-SWAP-STUCK-RECOVERY** Give a book whose version swap did not finish
      (`swapping_out` / `swapping_in`, from `versions.ErrSwapIncomplete`) a way to be found
      and repaired. Nothing lists these books today (`ListTrashedBookVersions` is trash-only,
      so the nightly cleanup cannot report them) and `ResumeVersionSwaps` is a stub, so the
      only sign is the 500 `VERSION_SWAP_INCOMPLETE` the trash request returned and the
      error log. Until it is repaired by hand: the book has no active version; ingest would
      create a second active version for it; the Deluge follow skips the book; the version
      rows can name slot paths that do not exist (the files may sit in the other version's
      slot or back at the original path); a retried trash or swap of either version is
      refused (409 `VERSION_SWAPPING` / `NO_SWAP_TARGET`). Needed: (1) list versions in
      `swapping_*` (a status-taking store query, then show them in the Version Management
      page or the nightly report, with the book's path and each version's file rows);
      (2) a resume that reads the rows and the disk and either finishes the swap or undoes
      it, no-replace, then restores both statuses; (3) the rarer case where every file move
      and row write succeeded but the final status write failed, which leaves the entering
      version `swapping_in` and the leaving one `alt` (a retried trash then trashes the
      `alt` one, which is the version the owner asked to remove).
