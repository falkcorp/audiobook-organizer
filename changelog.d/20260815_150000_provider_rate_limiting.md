### Added

- **Centralized outbound rate limiting for every metadata provider**
  (`internal/metadata/providerhttp`). Five of the six providers (Audible,
  Audnexus, Google Books, OpenLibrary, Wikipedia) previously had no throttling at
  all — just a 30s timeout — and there was no jitter, backoff, or 429/`Retry-After`
  handling anywhere. Hardcover's limiter was a process-wide mutex + sleep that
  serialized every caller instead of pacing them, which blocked any fan-out.

  The limiter lives in the `http.RoundTripper`, so a request cannot bypass it by
  forgetting to call `Wait()`. Includes 429/`Retry-After` handling (both
  delta-seconds and HTTP-date forms), exponential backoff with jitter, and
  context-aware waiting. Cover-art downloads are throttled too, wrapping rather
  than replacing their SSRF-guard transport.
