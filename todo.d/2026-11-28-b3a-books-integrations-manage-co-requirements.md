- [ ] **W6-B3A-COREQ** B3-a: fill ABO's `app.Options.RouteCoRequirements` with
      `AllOf(books:integrations.manage)` (a list of `httpapi.CoRequirement`
      entries, `Route` plus `Need`) for the six integrationhttp manage routes
      (`createIntegrationInstance`, `updateIntegrationInstance`,
      `deleteIntegrationInstance`, `testIntegration`, `testIntegrationInstance`,
      `releaseIntegrationInstanceHold`), and a per-value entry
      `{Route: "getIntegrationInstanceHealth", Param: "kind", Value:
      "download", Need: AllOf(books:integrations.manage)}` for the download kind
      of the health route (its own requirement is `core:integrations.read`; the
      other kinds stay as they are), plus the download test route if #5084's
      table says so, per 05-rbac section 6.1 and `internal/books/atoms.go`. The core mechanism shipped with an empty ABO
      list; until the entries land, those routes admit on `core:integrations.manage`
      alone. The list must be passed from every `app.Options` ABO builds (a
      non-empty list refuses boot without identity boot and a catalog). The
      `app.Options` hunk overlaps the F5-18 flip-r2, F6-03 flip-r5/r6 and F4-08
      main-green branches.
