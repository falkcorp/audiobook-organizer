### Added

#### `prerelease.yml` now fails when a version accumulates 10 RCs

Added a `check-rc-ordinal` job to `.github/workflows/prerelease.yml`, running
after the reusable prerelease job, that lists releases via `gh release list`
(same tagName-parsing pattern already used in `cleanup-rc-releases.yml`),
counts how many `-rc.N` releases exist for the base version the run just
minted, and fails the job (`exit 1`) once that count reaches 10. Per the
owner's 2026-08-08 directive ("we are never to get above 10 RCs"), the next
step past 10 is a stable release, not `rc.11`. The counting logic lives in
`.github/scripts/check-rc-ordinal.sh` so it can be exercised locally against
JSON fixtures (`testdata/gh-release-list-10rc.json`,
`testdata/gh-release-list-1rc.json`) — a workflow-only change would otherwise
be untestable outside CI. Only counts RCs sharing the exact base version of
the tag just created; a first RC on a brand-new base, or RCs belonging to a
different base version in the same release list, do not trip the guard.
Deliberately does not touch `cleanup-rc-releases.yml`'s existing keep-latest-3
pruning on stable promotion, and does not add any auto-promotion behavior —
auto-cutting a stable release without a human gate is a separate, more
consequential decision left for the owner.
