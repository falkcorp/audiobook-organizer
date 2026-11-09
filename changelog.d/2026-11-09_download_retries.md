### Fixed

- CI: the R0 release download in the books tests retries transient failures (network errors, 5xx, 429) with backoff, and the Dockerfiles' curl calls now fail and retry on HTTP errors instead of saving an error page that then fails its sha256 check.

- CI: the fixture-test shards and boot-crash-matrix-abo now prefetch the R0 release archive with `gh release download` (cached, retried, checksum-pinned) through `scripts/ci/fetch_r0.sh` and hand the extracted binary to TestR0OpensR1Store via `ABO_R0_BINARY`, so the test no longer depends on the github.com release redirect at run time.
