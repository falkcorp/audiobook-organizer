- [ ] **REALTIME-GLOBAL-HUB** `realtime.GlobalHub`, `globalHubMu` and `SetGlobalHub` are now a
      write-only global: 01-T6b deleted their only readers (`GetGlobalHub`, `InitializeEventHub`,
      both unreachable). `SetGlobalHub` is still called from `internal/server/server.go` and
      `internal/testutil/integration.go`, so `deadcode` keeps reporting it live. Delete the three
      symbols and both call sites, or wire a real reader if one is intended.
