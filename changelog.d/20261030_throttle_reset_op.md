### Added

- New operation `maintenance.throttle-reset` releases metadata provider holds by
  hand. 01-P81f retired the `DELETE /metadata/providers/throttles` routes, so a
  quota or auth hold could only end on expiry (up to 24 hours) or on a
  successful call. The op previews by default: it lists each hold's provider,
  kind, expiry and reason and releases nothing. With `dry_run=false` it releases
  them and reports per provider what was dropped. The optional `provider` param
  scopes it to one provider; naming a provider with no active hold is an error
  that lists the held ones. The throttle registry gains `ReleaseHold` and
  `ReleaseAll` for it.
