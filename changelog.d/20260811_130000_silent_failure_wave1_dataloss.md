### Fixed

#### Four places where a failed recovery reported success

Wave 1 of the silent-failure sweep: the sites where a discarded error could
lose or corrupt a file without saying so.

**A failed rollback now says it failed.** When copying a file into place fails
partway, the code restores the original from its backup — but the restore's own
error was thrown away. Since the target has already been partially overwritten
by that point, a failed restore left a **truncated audiobook** on disk while the
returned error read `failed to copy file`, which sounds like nothing happened.
The caller moved on, and the only intact copy sat in the backup directory with
nothing pointing at it. The error now names both the damaged file and where the
good copy is.

**The same fix after a checksum mismatch**, where it is worse: that branch is
reached having *just proven* the target is corrupt. It attempted a restore,
ignored whether the restore worked, and returned `operation failed integrity
check` — which reads as "we refused to do it", not "there is a known-bad file on
disk right now".

**The iTunes library restore no longer claims something it didn't check.** After
a failed atomic rename, the code restores the original `.itl` from its backup.
The restore result was discarded while the returned error ended in `(restored
original)` — a false statement in the error string itself. If that restore
fails, the live iTunes library is not at its expected path at all; it is sitting
under a `.bak-<timestamp>` name nothing looks for. The error now reports the
restore honestly and tells you where the library actually is.

**The iTunes conflict guard now fails closed.** It exists to refuse a write when
the library may have changed underneath us. If it could not stat the file it
returned "no conflict" — turning *cannot verify* into *verified safe*, the one
answer it is not entitled to give. It now refuses.

**Import checkpoints report failures.** Five resume-state writes discarded their
errors. They are deliberately non-fatal — a checkpoint failure should not abort
an otherwise-healthy import — but non-fatal is not the same as invisible. If
they all fail, the import still reports success while its resume state points at
an earlier phase, and a later crash silently redoes work.

Covered by three tests that force the **rollback itself** to fail, which is the
only way to exercise these branches; a test that merely makes the copy fail
passes with or without the fix. With the discards restored, two of the three go
red carrying the original misleading messages.
