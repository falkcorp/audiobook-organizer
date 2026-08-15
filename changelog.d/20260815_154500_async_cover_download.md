### Changed

- **Applying metadata to a book returns immediately instead of waiting on a cover
  download.** Measured on production: a single-book apply took **6.44s**, and ~4s
  of that was a synchronous cover-art HTTP download sitting on the request path —
  its code comment described it as a "fast network fetch". Everything else the
  apply does (tag writes, rename, write-back) was already backgrounded. The
  download now runs in the background file-IO pool, ahead of the cover embed so
  the newly fetched image is the one embedded. The cover appears a moment after
  the rest of the metadata.
