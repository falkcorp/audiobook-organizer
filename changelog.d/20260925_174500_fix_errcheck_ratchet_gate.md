### Fixed

#### Silent-failure sweep Wave 0's errcheck gate was configured but never ran

`.golangci.yml` has enabled `errcheck` with the bucket-(f) exclude list since
Wave 0 of the silent-failure sweep landed, but nothing ever ran it as a gate:
`make ci` did not call `make lint-errcheck`, and the `Continuous Integration`
workflow's golangci-lint job explicitly excluded errcheck
(`--enable-only nolintlint`) because the pre-existing 834-finding backlog would
make a plain pass/fail check permanently red. A newly introduced discarded
error return was invisible to CI.

Added an errcheck ratchet, matching the existing interface-width ratchet:
`.errcheck-baseline` records the 834-finding backlog,
`scripts/check-errcheck-ratchet.sh` fails if the count goes up (or goes down
without the baseline being lowered in the same PR), and both `make ci` and a
new `Errcheck Ratchet` CI job run it. A new discarded error now fails CI
immediately instead of silently joining the backlog; paying the backlog down
in later waves is expected to lower the baseline file in the same PR.
