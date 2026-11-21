### Fixed

- Repo Guards on main is green again. The tests for `scripts/ci/build_web.sh` and `scripts/ci/ensure_core_web.sh` now expect the three core web packages the scripts have built since F9-02 slice 0 (api-client, ui-kit, shell, in that order); before, they expected api-client only. The `ensure_core_web.sh` tests also now prove that rebuilding a package rebuilds every later one and no earlier one, and that `npm ci` runs at most once.
