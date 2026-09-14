### Fixed

- The remaining tag writers no longer copy a Deluge-seeding file into the library root. Single-tag fixes, tag reverts and the book PATCH write-back (the metadata package's guard), and the one-time movement-atom cleanup, used to hand a protected path to the Deluge importer. The importer copied the file to `RootDir/<basename>`, outside any book folder, and repointed its row there. These writes now refuse a protected path with `ErrProtectedPathWrite`, the same way the write-back does since #3402. The movement-atom cleanup counts the refusal as skipped.
