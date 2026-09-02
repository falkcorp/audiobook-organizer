### Fixed

#### Organizing two books into the same destination can no longer corrupt either file

Two independent defects in the organizer's copy path could silently write the wrong
bytes under a book's recorded path when two books resolved to the same destination
file (same author + title, a common shape for duplicates) and were organized at the
same time — which the parallel organize op does routinely:

- **Shared temp file.** Every writer used `<dest>.tmp`, opened with truncate, so two
  concurrent copies interleaved into one temp and the last rename won. A 30-iteration
  probe corrupted the destination 30 times out of 30. Each copy now writes to a
  per-call temp (`<dest>.<random-nonce>.tmp`) opened `O_EXCL`, so a second writer on
  the same name fails instead of sharing the file; both defences are tested on their
  own (the nonce pinned, O_EXCL alone must hold).
- **Blind adoption on `EEXIST`.** When the copy lost a race it recorded the *other*
  book's file as its own without looking at it. It now adopts an existing destination
  only when it is proven to be the same file (same inode) or the same content; anything
  else is logged and the file's row is left unchanged rather than pointed at a
  stranger's bytes.

The temp is published with `link(2)`, which is atomic and refuses an occupied
destination (`rename(2)` silently replaces), then verified by size; filesystems that
refuse hard links fall back to the previous rename path. Every guard was mutation-tested
(8 killed, 2 equivalent survivors documented in the test file). Cleanup still sweeps
the new temp names.
