### Fixed

#### Silent-failure sweep Wave 0's errcheck gate was configured but never ran

`.golangci.yml` has enabled `errcheck` with the bucket-(f) exclude list since
Wave 0 of the silent-failure sweep landed, but nothing ever ran it as a gate:
`make ci` did not call `make lint-errcheck`, and the `Continuous Integration`
workflow's golangci-lint job explicitly excluded errcheck
(`--enable-only nolintlint`) because the pre-existing 838-finding backlog would
make a plain pass/fail check permanently red. A newly introduced discarded
error return was invisible to CI.

Added an errcheck ratchet, matching the existing interface-width ratchet:
`.errcheck-baseline` records the 838-finding backlog measured on the
ubuntu-latest runner CI actually uses (four platform-gated findings only
appear under `GOOS=linux`, so a local darwin count reads 834 instead),
`scripts/check-errcheck-ratchet.sh` fails on a net increase in the count (or on
a net decrease that does not lower the baseline in the same PR), and both
`make ci` and a new `Errcheck Ratchet` CI job run it. Like the interface-width
job, it reports rather than blocks pre-merge -- main has no required status
checks, so `auto-revert.yml` is what makes a red run here consequential. A PR
that adds one new discard while fixing another still passes; the gate catches
a net increase, not every individual new discard. Paying the backlog down in
later waves is expected to lower the baseline file in the same PR.
