<!-- file: changelog.d/20260802_040000_promtool_version_resolution.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7a3f92e0-5c14-4d8b-a06f-1e58b7d3ca94 -->
<!-- last-edited: 2026-08-02 -->

### Fixed

- **`setup-prometheus-auth.py` rejected a valid config because it validated with the
  wrong `promtool`.** The script called `shutil.which("promtool")` and trusted PATH
  order. On the production host an unpackaged **promtool 2.13.1 (built 2019)** sits in
  `/usr/local/bin`, ahead of the packaged **2.53.5** in `/usr/bin` that matches the
  running Prometheus. The 2019 binary predates the `authorization:` scrape-config
  field (added in Prometheus 2.26), so it failed with:

  ```
  field authorization not found in type config.plain
  ```

  The script then correctly restored the original config and reported failure — so the
  error read as a bad config rather than a bad validator, which is the confusing part.

  `find_promtool()` now picks the `promtool` whose version matches
  `prometheus --version`, falling back to the newest available, and logs which one it
  chose and why (explicitly calling out when the winner is *not* first on PATH). A
  validator newer than the server can only be over-permissive; an older one produces
  false rejections like this one.

- **The same script's rollback was incomplete.** On a validation failure it restored
  `prometheus.yml` but left the shared `file_sd` discovery entry moved aside, so the
  target ended up in **neither** the shared job nor the dedicated one — silently
  scraped by nothing, which is worse than either intended end state. Both edits are
  now rolled back together, and the docstring no longer overpromises "the original is
  restored".
