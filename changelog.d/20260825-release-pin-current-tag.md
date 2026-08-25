### Fixed

- **Stable releases could not be cut.** No stable has published since v0.219.0
  (Aug 20): a dispatch for `v0.219.1` built `v0.219.1-rc.96` instead and failed
  uploading assets that the rc.96 release already had, leaving an empty draft
  behind. goreleaser takes its version from the tag `git describe` finds at
  HEAD, and this repo cuts an RC per merge, so that tag is always an RC. Picks
  up the github-common fix that exports `GORELEASER_CURRENT_TAG` explicitly.
