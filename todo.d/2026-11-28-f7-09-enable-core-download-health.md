- [ ] **F7-09-DL-HEALTH** Enable core's download-kind health and test once the
      W6 per-kind co-requirement lands (#5086). Today
      `core/internal/integrationhttp/kindreach.go` (`probeReach`) answers 404 for
      kind=download on `GET /integrations/download/instances/{id}/health` and on
      both test routes, for every principal, because HEAD's `/deluge/status` and
      `/deluge/test-connection` ask the `integrations.manage` atom
      (`core:integrations.manage` plus `books:integrations.manage`) and the
      settings atom must not gain that reach. Turn the case on by asking the
      co-requirement `books:integrations.manage` for kind=download only, flip
      `TestReach_DownloadHealthIs404ForEveryPrincipal` to the per-kind
      verdicts, and keep the legacy `/deluge/*` routes until then (the
      internal/books reach cell names them). See
      `docs/plans/platform-core/f7-09-integrations-per-kind-reach.md`.
