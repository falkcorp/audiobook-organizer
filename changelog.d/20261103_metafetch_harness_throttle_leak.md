### Fixed

- metafetch tests: the search fan-out harness now clears the process-wide provider
  throttle registry before and after each test. A hold left by an earlier test
  failed `TestSearchFanoutProbe_NotFoundASINIsAnswered` and
  `TestSearch_PartialRuntimeEnrichesRuntimelessASINAnswers` with "provider is
  throttled", depending on test order.
