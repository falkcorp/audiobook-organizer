### Fixed

- **`HEAD` now works on the ABS byte-serving routes** — `/api/items/:id/file/:ino`,
  `.../download`, and `/api/items/:id/cover`. gin registers one method per call,
  so a GET-only route answered `HEAD` with **404, not 405**, which is
  indistinguishable from "this file does not exist".

  That silently destroyed a real measurement. A probe built to find `book_file`
  rows with no bytes behind them reported **100% of 1,786 files missing** and
  passed its own sanity check, because a fabricated file id returned the same 404
  as every real one. A uniformly-dead instrument agrees with any hypothesis.

  No handler changed. These routes already end in `http.ServeContent`, which
  omits the body for `HEAD` while still sending `Content-Length`, `Content-Type`,
  `ETag` and `Accept-Ranges`, and the ABS auth middleware already accepted
  `?token=` on `MethodHead`. Only the routing table was missing.
