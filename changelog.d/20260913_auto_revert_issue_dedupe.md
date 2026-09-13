### Fixed

- `auto-revert.yml` no longer files a second "CI red on main" issue for a commit that already has an open one. It comments on the open issue instead, using the same open-issue search as `auto-revert-backstop.yml`.
