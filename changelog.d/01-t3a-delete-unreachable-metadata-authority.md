### Removed

- Deleted 19 unreachable functions from `internal/metadata`, `internal/metadata/providerhttp`, `internal/openlibrary` and `internal/authority` (found by `deadcode`), together with the tests that only exercised them. `DownloadCoverArt` and its skip-existing path are gone; `ReplaceCoverArt` is the single cover entry point. Behaviour of the running system is unchanged.
