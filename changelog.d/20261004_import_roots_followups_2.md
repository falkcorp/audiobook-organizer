### Fixed

#### Bulk apply: the Doctor Who / Big Finish rule now checks the candidate's publisher and author and the transcribed authors; a failing import-path store is read once per backoff

- **Owner-manual-only gap.** A blank-titled rip outside any franchise folder, with no transcription, could be matched by Audible to "The Chimes of Midnight" from publisher "Big Finish Productions" with no series. The franchise then appeared only in the publisher, which `applygate.ManualOnlyDetail` never checked, so a review-page bulk pin over an overridable leg applied it. The same rule now runs over the candidate's publisher and author. `BulkManualOnlyGuard` also checks the book's and each file's transcribed author, catching an intro such as "Big Finish Productions presents...". A book from an ordinary publisher is not held.
- **Failed import-path reads back off.** After #3725 a failed read did not back off, so a failing store was read once per root check, and a waiter could stall each time. A failed read now starts a 5 s backoff. During it, callers get the previous list, or "unavailable", without reading. A success clears it. A read that panics still records nothing.
- **Waiter timeouts are logged on their own.** A caller that gives up waiting on another caller's read now logs a rate-limited Warn of its own, separate from "import paths unreadable".
- **`aibackendshandler.New` ignores a nil `Option`** instead of calling it.
