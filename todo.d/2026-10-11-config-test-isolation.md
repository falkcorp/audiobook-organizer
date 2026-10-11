- [ ] **CONFIG-TEST-ISOLATION** `TestUpdateConfig_WithoutRemovedKeyUnaffected`
      (`internal/config`) returns 403 under `go test -shuffle` (7 of 8 seeds
      tried) on main: `enable_auth` defaults to true and 20+ tests call
      `InitConfig` without restoring the config afterwards, so whether the test
      sees auth on or off depends on test order. Proposed fix: a baseline-config
      test helper (restores `AppConfig` and the viper state, with auth off
      unless a test opts in) that those tests use instead of bare `InitConfig`.
      Found reviewing the `enabled_sort_indexes` boot-only change (2026-10-11);
      not fixed there.
