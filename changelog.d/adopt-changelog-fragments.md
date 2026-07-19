### Changed

#### Adopt changelog fragments (`changelog.d/`) for assembling CHANGELOG.md

`CHANGELOG.md` is now assembled from per-change Markdown fragments under
`changelog.d/` by the shared release workflow (`scriv`), instead of being edited
by hand. Contributors add a fragment with `scriv create`; a CI check requires
one on each PR. This removes changelog merge conflicts across parallel PRs.
