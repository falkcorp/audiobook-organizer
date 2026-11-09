### Fixed

- CI: the R0 release download in the books tests retries transient failures (network errors, 5xx, 429) with backoff, and the Dockerfiles' curl calls now fail and retry on HTTP errors instead of saving an error page that then fails its sha256 check.
