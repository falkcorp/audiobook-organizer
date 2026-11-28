- [ ] **F7-09-THROTTLES** Retire `GET /api/v1/metadata/providers/throttles`
      (`internal/server/legacy_manifest.go:747`, `library.view`) and
      `POST /api/v1/ai/test-connection` (`:443`, `library.edit_metadata`) only
      once a core route can express `books:view` and `books:edit`. Core's
      metadata health is 404 until then (its payload carries the hold) and its
      ai test asks the settings atom's seven scopes, so `library.view` and
      `edit_metadata` holders would lose these reads and tests if the legacy
      rows went. Enable core metadata health in the same PR. The SPA panels
      (`web/src/components/system/ThrottlePanel.tsx`,
      `MaintenanceTab.tsx:23`) keep calling the throttles route. The guard cell
      in `internal/books` (a `library.view` key still gets it) must be
      changed in the same PR that retires either route. See
      `docs/plans/platform-core/f7-09-integrations-per-kind-reach.md`, table 5.
