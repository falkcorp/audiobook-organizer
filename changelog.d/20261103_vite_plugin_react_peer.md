### Fixed

- Deploys failed at the frontend install step after the November dependency
  bump: the React build plugin's pinned version did not accept the newer Babel
  plugin it was paired with. CI did not notice because it installs exactly what
  the lockfile records, while the deploy re-resolved the dependencies and
  rejected the pair. The React plugin is now on a version that accepts it, so
  both install paths agree again.
