<!-- file: changelog.d/20260801_144000_prometheus_auth_script.md -->
<!-- version: 1.0.0 -->
<!-- guid: 29013af0-bed6-4821-b6eb-09440c4b5b59 -->
<!-- last-edited: 2026-08-01 -->

### Added

- A setup script for operators running Prometheus against this server. Now that the
  metrics endpoint requires a credential, `scripts/setup-prometheus-auth.py` asks for an
  API key and does the rest: checks the key actually works before changing anything,
  installs it so only Prometheus can read it, adds the scrape configuration, and
  confirms metrics start flowing again. Re-running it is safe.
