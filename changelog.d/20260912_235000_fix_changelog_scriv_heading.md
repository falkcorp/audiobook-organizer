### Fixed

#### Release changelog collection no longer refuses a non-version heading

The v0.222.0 release failed at `scriv collect` with
`Entry 'Corrections, made before release' is not a valid version!`. PR #3031
amended an unreleased fragment with a `## Corrections, made before release`
section. `##` is the version-entry level, the v0.221.1 collect copied the
section into `CHANGELOG.md` verbatim, and from then on every collect read it
as a release that was not a version and stopped. The section is now a `####`
sub-section at the same place, inside the v0.221.1 `### Changed` entry it
corrects. No text moved or changed. Pending fragments are untouched and fold
in on the next successful collect.

A new `changelog-check.yml` job, backed by
`scripts/check_changelog_scriv.py`, runs scriv's own parser over
`CHANGELOG.md` on every PR. It also simulates the collect of every pending
fragment, so a fragment that would add such a heading now fails the PR that
adds it instead of the release after next. `changelog.d/README.md` now says
which heading levels a fragment may use and where corrections go.
