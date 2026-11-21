### Changed

- The Production Release workflow takes a `skip-changelog-collect` input. Dispatched from a tag ref (the R2 "Keep" from the deployed release candidate), its changelog job cannot run in the main-only `ci-bot` environment; setting the input skips collection, and `changelog.d/` is collected by a normal pull request on `main` afterwards. Left unset, a release behaves as before.
