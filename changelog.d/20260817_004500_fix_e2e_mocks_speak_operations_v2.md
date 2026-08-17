### Fixed

- **E2E mocks now speak the v2 operations API.** Retiring the v1 operation
  triggers and reads (`2c8e3b3c`, `1ce1de7d`, `542a6929`) moved every start and
  poll onto `POST /operations/v2` and `GET /operations/v2/:id`, but the test
  doubles still implemented only the retired URLs. Every spec that started a
  scan, organize or transcode and then waited for a spinner, progress bar or
  toast failed at the first step, because the start call went unhandled and no
  operation was ever created — 23 failures across six specs. The shared mock
  and the four specs that stub operations themselves now use the v2 routes and
  envelopes, serving from the same fixture store as before so no fixture
  changed.
