### Fixed

#### A rescan no longer rewrites an existing book's title, author or series

The nightly `library.scan` of 2026-10-06 rewrote the title or author of 1,176 existing books from their tags and file names. Each title or author change is a search-identity change, so the store dropped the book's cached metadata candidates in the same write, and the scanner recorded no history, so nothing showed it. The earlier folder-parse hold (`holdFolderFieldsForExisting`, `folderDerivedLocks`) covered only values the folder parse supplied; a tag, the file-name fallback or the AI parse still got through.

A rescan, move or rename of an existing row now keeps its title, author, series and series position, and with them its work and its author credits, whatever the scanned value came from (`internal/scanner/scan_identity_hold.go`). It is enforced twice: before any author, series or work row is resolved (`holdIdentityForExisting`) and as write-time locks on the merge (`identityMergeLocks`), which the raced-row and late hash-duplicate paths also reach. A field the row does not have yet (an empty title, no author or the "Unknown Author" placeholder, no series, no position) is still filled. A row the same scan created (a new import's inline AI re-save) takes every value. New imports are unchanged.

Where the file reads differently, the scan records a proposal under the raw key `scan_identity_proposal:<book id>` (`database.ScanIdentityProposal`: stored -> scanned per field). The key is rewritten only when the proposal changes, cleared when the file agrees with the row again, and never written for a user-locked field. The scan summary logs how many books it held.

#### Every scanner book write records metadata history

Every scanner `ModifyBook` (the rescan merge, organizer-ID relink, path normalization, version links, raced-row join, the queued AI parse's fills) now goes through `modifyBookRecorded`. It records one history row per changed field with change type `scan` (`database.ChangeTypeScan`; source `scan` or `scan.ai-parse`). The queued single-book apply does not read `scan` rows as a later edit (`metafetch` `fileSideChangeTypes`), so a scan behind a queued apply still does not refuse it.

#### "183 of 301" is no longer re-titled "of 301"

The leading track-number strip in the scanner's `extractInfoFromPath` and in `metadata.extractFromFilename` dropped the number from a counted part ("183 of 301", from "Shadow's Edge - 183 of 301") and left "of 301". It hit 299 Shadow's Edge fragments in the 2026-10-06 scan and affected new imports too. A name that starts "N of M" is now kept whole (`metadata.LeadingNumberIsCount`), and the chapter-only checks then title it from its folder. A track number before a title ("10 Zero History") is still stripped.
