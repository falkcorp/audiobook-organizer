<!-- file: changelog.d/20260802_050000_metrics_double_gzip.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1c58aef0-6b93-4d27-8054-9f2e37b0da61 -->
<!-- last-edited: 2026-08-02 -->

### Fixed

- **`/metrics` was double-gzipped, so every Prometheus scrape failed.**
  `promhttp.Handler()` compresses its own response when the client sends
  `Accept-Encoding: gzip`, and the global `gzip.Gzip` middleware then compressed that
  already-compressed body a second time. Prometheus decompresses exactly once, found
  gzip magic bytes underneath, and failed each scrape with:

  ```
  expected a valid start token, got "\x1f" ("INVALID") while parsing: "\x1f"
  ```

  which reads like a corrupt exposition format rather than a transport problem.

  `/metrics` now joins `/api/events` in the middleware's excluded-paths list. Surfaced
  in production on 2026-08-02 the moment the newly auth-gated `/metrics` became
  reachable again — the target authenticated fine and then failed to parse, so this
  had been latent behind the auth failure.

  Covered by a regression test that reproduces a real Prometheus scrape
  (`Accept-Encoding: gzip`, exactly one decode) and asserts the result is not still
  gzip — verified to FAIL without the exclusion and pass with it — plus a test that
  ordinary endpoints are still compressed, so the exclusion cannot silently widen.
