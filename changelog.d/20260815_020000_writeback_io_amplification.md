### Changed

- **Tag writes no longer copy and hash the audio file four times over.**
  Every write-back wrapped a tag write in `fileops.WriteTagsSafe` and then called
  a writer that wrapped it *again*. Each wrapper streams the whole file through
  SHA-256 twice and copies it once, so one tag write cost four full-file SHA-256
  passes and two full-file copies of the audio — and the inner pair was discarded
  unread. On NAS-backed audiobooks that redundant I/O, not the tag encoding, was
  the cost of write-back. Writers called from inside the wrapper now write in
  place, and the hashes are computed only when there is a `book_file` row to
  persist them against.

- **Per-chapter titles are preserved.** Both multi-file write paths overwrote the
  `title` tag of every file with a synthetic `"NN - Book Title"` on every run,
  destroying real chapter titles. They are now kept unless they carry no
  per-chapter information.

- **The two write-back implementations are now one.** The fetch/apply path had its
  own ~160-line near-duplicate that had drifted: it never embedded covers from an
  already-downloaded local cover, never propagated to version-group siblings,
  never redirected protected paths to the library copy, and never stamped
  `LastWrittenAt` for multi-file books. It now shares the single implementation
  and gains all of those.
