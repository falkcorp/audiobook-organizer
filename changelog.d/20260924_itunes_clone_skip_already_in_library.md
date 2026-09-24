### Fixed

- `maintenance.itunes-clone-into-library` no longer clones a book that is already organized in the library under another version group. It looks for a live organized book outside the group, with a file under the library root, that has the same ASIN or the same author plus a matching title (whole or base title, folded as dedup does). A hit skips the group as `already_in_library` and lists those books in `library_copies`. The 2026-09-24 canary had cloned about 11 of 20 books that were already in the library.
