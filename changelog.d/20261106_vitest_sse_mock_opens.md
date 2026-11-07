### Fixed

- A frontend test no longer fails intermittently on a slow CI agent: the test event-stream stub now reports itself connected, so no "Connection lost" message can interrupt a test.
