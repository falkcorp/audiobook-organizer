### Added

- **`require_gpu` for Whisper endpoints.** A Whisper worker on the wrong silicon
  does not fail — it serves healthy responses from a CPU backend at roughly a
  tenth of the speed, which looks like a slow library rather than a
  misconfiguration. Setting `"require_gpu": true` on an entry in
  `WHISPER_ENDPOINTS` now refuses that endpoint instead of using it. The check is
  fail-closed: an endpoint whose `/health` cannot be read, or which is too old to
  report a device, is refused rather than assumed healthy, and accepted devices
  are an explicit allow-list (`cuda`, `metal`, `mps`, `rocm`, `hip`) so an
  unrecognised backend is refused rather than waved through.

### Changed

- **`/health` now reports the device Whisper actually loaded**, not the one it was
  configured with. `WHISPER_DEVICE` was echoed back verbatim, so a host still
  carrying an old launch script advertised its old accelerator regardless of the
  hardware present. The requested values are still reported separately, under
  `requested_device` / `requested_compute_type`.

### Fixed

- The Whisper pool made two separate `/health` requests per endpoint per run — one
  to pick the batch path, one for the gate — which could describe two different
  server states if the worker restarted between them. It now makes one.
