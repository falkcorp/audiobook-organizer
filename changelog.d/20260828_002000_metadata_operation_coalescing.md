<!-- file: changelog.d/20260828_002000_metadata_operation_coalescing.md -->
<!-- version: 1.0.0 -->
<!-- guid: c2a62683-e1dc-48da-8d07-c71e3a70fc6d -->
<!-- last-edited: 2026-08-28 -->

### Improved

- Compatible queued cached-metadata apply requests now merge their book lists
  into one operation without changing a running operation or mixing write-back
  modes.
