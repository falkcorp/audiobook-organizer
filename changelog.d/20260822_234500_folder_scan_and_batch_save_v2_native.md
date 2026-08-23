### Fixed

- **Adding a library folder now reports its scan correctly.** The "add folder" response
  handed back an id from the retired operations system, so the progress the UI polled for
  never arrived and the folder's book count did not refresh until the page was reloaded.
  The id now refers to the scan that actually runs.
- **Saving metadata to files reports against the run that actually happens**, for the same
  reason — the batch write-back returned an id that could not be looked up.

### Changed

- Folder auto-scans and batch metadata write-backs are tracked entirely in the current
  operations system. Neither writes a row into the older one any more.
