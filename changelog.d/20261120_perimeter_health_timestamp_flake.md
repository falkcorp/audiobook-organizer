### Fixed

- Test only: the books perimeter test's /health body comparison now removes `data.timestamp` and `data.warmup_ms` (the health answer is wrapped in a `data` envelope, so the earlier top-level removal removed nothing) and no longer fails when its two requests straddle a second boundary. All other fields are still compared exactly.
