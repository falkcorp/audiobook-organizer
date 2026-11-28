- [ ] **W6-B3A-COREQ** B3-a: fill ABO's `app.Options.RouteCoRequirements` with
      `AllOf(books:integrations.manage)` for the six integrationhttp manage
      routes (`createIntegrationInstance`, `updateIntegrationInstance`,
      `deleteIntegrationInstance`, `testIntegration`, `testIntegrationInstance`,
      `releaseIntegrationInstanceHold`), per 05-rbac section 6.1 and
      `internal/books/atoms.go`. The core mechanism shipped with an empty ABO
      map; until the entries land, those routes admit on `core:integrations.manage`
      alone. The map must be passed from every `app.Options` ABO builds (a
      non-empty map refuses boot without identity boot and a catalog). The
      `app.Options` hunk overlaps the F5-18 flip-r2, F6-03 flip-r5/r6 and F4-08
      main-green branches.
