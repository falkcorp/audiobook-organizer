### Fixed

- Tag and cover writes no longer copy a Deluge-seeding file into the library root. Until now these writers handed a protected path to the Deluge importer:
  - single-tag fixes, tag reverts and the book PATCH write-back, all through the metadata package's guard;
  - cover-art embeds through the metadata fetch service;
  - the one-time movement-atom cleanup.

  The importer copied the file to `RootDir/<basename>`, outside any book folder, and repointed its row there. These writes now refuse a protected path with `ErrProtectedPathWrite`, the same way the write-back does since #3402. The cover embed and the movement-atom cleanup count the refusal as skipped. The server no longer builds a write-guard importer at all.
