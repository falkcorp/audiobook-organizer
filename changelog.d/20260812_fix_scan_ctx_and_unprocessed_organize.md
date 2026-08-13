### Fixed

#### A cancelled scan kept going, and filed books whose details were never read

Two faults on the scan path, found while measuring why a full scan takes over four hours.

**Cancelling a scan did not stop it.** The scan checked its own stop button but never
the request's cancellation signal. When that signal fired, the scan carried on into
every remaining folder — 2,406 of them in a single production run — failing to read
details for each one and then reporting overall success.

**Books were filed even when their details could not be read.** When reading a folder's
audiobook details failed, the failure was written to the log and then the books were
organized anyway. A book whose title and author were never read has no title and no
author, so the naming pattern produced the same placeholder name for all of them and
they were all filed to the same destination — where every one after the first failed as
"already occupied". The same run recorded 7,561 refusals to overwrite an existing
destination and 3,481 duplicate candidates, with 848 books aimed at one placeholder path.

A cancelled scan now stops and says it was cancelled, and a folder whose details could
not be read is no longer organized or counted as successful.
