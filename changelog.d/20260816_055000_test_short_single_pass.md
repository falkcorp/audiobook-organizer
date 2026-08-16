- `make test-short` now runs the suite once with `-race` and `-coverprofile` together
  instead of twice. Measured on an idle machine: 966s -> 500s (-48%), with byte-identical
  coverage (47.0%). The discarded second run also hid its own failures behind
  `>/dev/null 2>&1`; that failure mode is gone.
