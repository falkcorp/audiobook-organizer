### Changed

- Woodpecker CI pipelines now run only when queued manually (one at a time via `scripts/ci_woodpecker.py`); automatic push and pull_request runs started several pipelines at once, loading the CI hosts and the production server together.
