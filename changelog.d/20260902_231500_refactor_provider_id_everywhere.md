### Changed

- **Providers are now identified by their id everywhere, and the display name is
  only a display name.** Metadata sources gained a canonical `ProviderID()` —
  the same string `config.MetadataSource.ID` uses — and every lookup that used
  to key on the human-readable `Name()` now keys on that id: per-provider rate
  limits, per-provider concurrency, and the metadata-fetch cache.
- `providerhttp`'s request-budget table is keyed by those same ids, so the
  Google Books budget moved from `googlebooks` to `google-books`. There is now
  one vocabulary rather than three (`google-books` in config, `googlebooks` in
  the budget table, `Google Books` as the label).

### Fixed

- A rate limit could be stored under one spelling of a provider and read under
  another. The value was written, never consulted, the provider silently kept
  its built-in budget, and the settings page reported the configured number as
  though it applied. Keying everything on the id removes the class of bug rather
  than translating between the spellings.
- Cached metadata rows were keyed by the provider's **display name**. Rewording
  a label — say, disambiguating "Audnexus" to "Audnexus (Audible)" — would have
  orphaned every row written under the old wording, and an orphaned row is not
  an error, it is a cache miss: the only symptom would have been the library
  quietly re-fetching itself from every provider. Rows are now written under the
  id, and reads fall back to the legacy display-name key so existing entries
  stay reachable and converge without a migration.
