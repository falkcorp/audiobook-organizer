<!-- file: changelog.d/20260801_130000_oidc_discovery_probe.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5884da18-0014-4af6-8bfb-36c3480f2c40 -->
<!-- last-edited: 2026-08-01 -->

### Added

- A temporary, opt-in diagnostic (`OIDC_DISCOVERY=1`) that records exactly what an
  audiobook player app asks for when it tries to sign in with single sign-on. It is off
  by default, creates no account and issues no login of any kind — it only writes to the
  server log so the sign-in support can be built against what the app actually does
  rather than a guess.
