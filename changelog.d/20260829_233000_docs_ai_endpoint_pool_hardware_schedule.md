### Added

- The AI endpoint pool design now covers the hardware it actually runs on:
  per-endpoint device selection, a `host` grouping so endpoints sharing a machine
  cannot over-commit it, and availability windows so a desktop GPU can be used
  only overnight while a dedicated box runs around the clock.
