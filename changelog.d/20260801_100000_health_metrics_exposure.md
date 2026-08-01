<!-- file: changelog.d/20260801_100000_health_metrics_exposure.md -->
<!-- version: 1.0.0 -->
<!-- guid: a6142a14-99f0-4564-b348-c4aa7a1cc43e -->
<!-- last-edited: 2026-08-01 -->

### Changed

- **The metrics endpoint now requires a credential.** `/metrics` was readable by anything
  that could reach the server. That was recorded as an accepted risk on the grounds that
  monitoring tools cannot log in and that the endpoint would be walled off at the network
  level instead — neither of which held. Prometheus authenticates perfectly well, and the
  network restriction was never built, so in practice the endpoint was simply open.

  Scrape it with an API key from Settings → API keys. `deploy/prometheus/scrape-config.yml`
  has the exact configuration, including how to store the key so that rotating it needs no
  config change.

- **The health endpoint no longer volunteers what it knows.** It used to reply with the
  exact build version, the storage engine, and how many books, authors and series the
  library holds — to anyone, with no credential. It now answers only whether the server is
  alive. Nothing needed the rest: the web app and every script in this repo check only that
  the server responded. The full detail is still available at
  `/api/v1/system/status` to a signed-in administrator.

  Health checks are also much cheaper now. Each one used to run four separate database
  counts, and the web app polls it every five seconds while trying to reconnect.
