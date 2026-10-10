### Added

- Added a ratchet test (`internal/telemetry/ratchet_test.go`) that counts the `client_golang` metric families declared in the tree (constructors, literal Pebble descriptors and stray `prometheus.NewDesc` calls) and fails when the count rises above the committed baseline of 69. The count only ever falls as families move to OpenTelemetry.
