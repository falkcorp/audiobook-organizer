### Fixed

- **Flaky `TestSlowCollectionKeepsHeartbeat` (internal/aiscan).** The test
  slowed each batch status check with a 400ms sleep and required a cancel to
  return within 200ms, which failed under `-race` in CI at 201ms. That bound
  also could not catch the regression it guarded: the poll in flight at cancel
  had only about 110ms left. The fake LLM now holds status checks at a gate the
  test controls, and the test asserts that progress keeps arriving and that
  `RunScan` returns on cancel while the poll is still held. Making the
  heartbeat poll synchronous now fails the test.
