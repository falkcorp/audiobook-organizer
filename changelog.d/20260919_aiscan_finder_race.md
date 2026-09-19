### Fixed

- Fixed a data race in the AI scan pipeline's own tests (`TestReattachLookupErrorKeepsSubmitting`) that failed CI runs at random. The test now changes its fake lookup error through an atomic value.
