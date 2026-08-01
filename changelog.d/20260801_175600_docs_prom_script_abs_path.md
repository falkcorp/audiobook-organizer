<!-- file: changelog.d/20260801_175600_docs_prom_script_abs_path.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8b1c47e2-5f30-4a96-b2d1-0e7a93c46f18 -->
<!-- last-edited: 2026-08-01 -->

### Fixed

- `scripts/setup-prometheus-auth.py` documented a repo-relative invocation, but
  the production host has no git checkout (deployment ships only the binary to
  `/usr/local/bin`). The docstring now shows the `scp` + absolute-path form that
  actually works.
