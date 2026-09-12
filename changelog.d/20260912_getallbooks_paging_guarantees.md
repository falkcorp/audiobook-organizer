### Fixed

- `ListBookIDs` on the Pebble path (`UseMemDB=false`) stopped its key scan at `book:;`, which dropped every book whose ID starts with a letter. It now scans to `book:~`, the same range `GetAllBooksFullFrom` covers. Follow-up to #3325.
- The ISBN enrichment sweep no longer warns that its cursor book may have been deleted when its first page is empty. Since #3325 an absent cursor resumes at the next book ID, so an empty first page only means the cursor is at or past the last book, and it is now logged at info level as an ordinary wrap.
- `transcribe-book-intros` resumes at the next book ID when its checkpointed book was merged or deleted since the last run, where it used to restart from the first book in the library.
