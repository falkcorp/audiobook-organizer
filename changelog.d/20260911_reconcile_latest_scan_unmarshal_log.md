### Fixed

- `GET /reconcile/latest-scan` now logs a WARN naming the operation and the decode error when the newest scan's stored result cannot be decoded. It previously dropped the error silently and answered `preview: nil`, which looks the same as "no scan has run". The response itself is unchanged; whether the endpoint should fall through to an older, readable scan is still an open API decision.
