### Changed

- core: the diagnostics bundle shows no query or fragment of a request target or URL. Everything from the first `?` or `#` to the end of the token is `?<query>` or `#<fragment>` in the redacted view, so a raw `&`, `#`, `=`, `;` or `?` in a directory name can no longer split a query and leave the part after it. The per-parameter judgment, its decoding rounds and its depth cap are gone. The admin view is unchanged.

### Fixed

- core: the bundle's path redaction now covers a percent-encoded path outside a route. A token such as `path=%2FUsers%2F...` in a log line or a JSON value, single or multiply encoded, is replaced whole, and a full URL (`https://host/x?path=...`) gets the same opaque query as a route. Ordinary text with a `%25` is left alone. Pinned by tests that assert the planted marker never survives, a widened fuzz generator, and linear-time tests for the new shapes.
