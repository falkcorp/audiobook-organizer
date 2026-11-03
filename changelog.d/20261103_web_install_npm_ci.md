### Changed

- `make web-install` (and so `make build` and deploys) now installs the
  frontend's dependencies exactly as the lockfile records them, the same way CI
  does, instead of re-resolving them. A build can no longer pick up different
  dependency versions from the ones CI tested. To add or bump a dependency, run
  `npm install <package>` in `web/` by hand and commit the lockfile, as before.
