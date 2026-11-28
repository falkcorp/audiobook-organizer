- [ ] **META-TTL** Stop expiring found metadata answers after 180 days
      (`DefaultMetadataFetchCacheTTLDays` in `internal/config/config.go`, and
      the matching rule in `internal/plugins/metafetch/pcache_sweep.go`). A
      book's metadata does not change, so a found answer should be kept
      (default 0, never expire, or a multi-year value). A new search is a new
      key, so an edited title followed by a search, and "Search again", still
      fetch fresh results; check that this holds. Decide separately whether
      not-found answers (`metadata_fetch.negative_ttl_days`) keep their short
      life, since a book missing today may be listed later. Low priority:
      after the platform-core plan (owner, 2026-11-27).
