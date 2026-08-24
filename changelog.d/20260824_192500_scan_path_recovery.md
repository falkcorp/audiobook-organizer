### Fixed

- **Multi-file audiobooks imported before today stayed broken, and nothing could bring them back.** An earlier fix stopped the scanner from losing track of a multi-file book when it files it under its containing folder — but only for books imported from that point on. Any book already filed that way was invisible to every later scan: the scanner looked for it under the name of its first file, found nothing, and quietly skipped saving its chapter list and recording that it had been scanned. That repeated on every scan, with no way out. The scanner now recognises a book that is already filed under its folder and picks it back up, so these books recover on the next scan instead of staying stuck. It only claims a book when that folder's book demonstrably contains the file in question, so one book's chapters can never be written onto another.
- **The scanner now says something when it cannot find a book it just saved.** That lookup could fail silently, taking the book's chapters and scan record down with it and leaving no trace in the log.

### Changed

- **Correction to the previous release note.** The earlier fix was described as stopping multi-file books from being re-read and re-fingerprinted on every scan. That was measured and is **not** what it does — those books are still re-read every scan, for two separate reasons in how the scan record is stored. What the fix does deliver is that chapter lists are saved and the book is no longer counted as missing. Making scans actually skip unchanged multi-file books is still outstanding and is tracked as follow-up work.
