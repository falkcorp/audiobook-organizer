### Fixed

- **Applying metadata no longer blanks the cover art.** `ApplyMetadataToBook`
  writes the candidate's remote cover URL into `cover_url`, and the UI serves
  covers through `/api/v1/covers/proxy`, which rejects hosts outside its
  allow-list (production returned `400 URL not from an allowed cover source` for
  `m.media-amazon.com`). That was invisible while the download ran inline and
  replaced the value microseconds later; once the download moved to the
  background it became observable and the cover rendered blank until a refresh.
  The previous cover is now kept until the new image is actually on disk.
