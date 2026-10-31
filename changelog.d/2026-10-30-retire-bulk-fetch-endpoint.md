### Fixed

- `POST /api/v1/metadata/bulk-fetch` no longer writes anything. It applied each
  book's top search candidate with no apply gate (no score floor, sequence
  guard or identity check). The route now answers `410 Gone` for one release,
  naming the gated replacements: the `library.bulk-metadata-fetch` operation to
  fetch candidates and `metadata.batch-apply-cached` to apply them through
  `applygate`. The handler, its tests, the web client function
  `bulkFetchMetadata` and the now-unused `Server.updateFetchedMetadataState`,
  `Service.ApplyMetadataSystemTags` interface entry and
  `metafetch.KnownBookAuthors` are removed.
