### Added

- A manual workflow to delete orphan RC tags — `vX.Y.Z-rc.N` git tags with no GitHub
  release attached. The RC cleanup workflow enumerates *releases* and deletes each one's
  tag with it, so a tag whose release is already gone is invisible to it. 684 such tags
  had accumulated, from RC series abandoned as far back as `v0.206.1`. They were inert
  (the notes diff base resolves the newest stable tag, and the RC-count guard counts
  releases) but bloated every clone and every `git ls-remote`. Ships dry-run by default,
  refuses to run if any tag with a release leaks into the orphan list, and tolerates
  per-tag failures so a rate limit cannot abort the sweep partway.
