- [ ] **PLATFORM-CORE-F3-BODYLIMIT** Spec a per-route body-limit override for
      legacy routes in 05-http-api §8.2 (for example `LegacyRoute.MaxBody`), so
      F3 can move gin's per-group body limits into core's stage 11. R1-17 left
      the legacy mount at `MaxBody: -1` (no core cap), because stage 11's two
      classes (JSON 1 MiB, upload 10 MiB) cannot express HEAD's limits: the
      20 GB OpenLibrary dump import and the 8 MiB fingerprint-worker routes.
      Until the override exists, gin's `bodyLimitMiddleware` is the only body
      limit on legacy routes.
