- [ ] **AUTH-OFF-EXPORT** Under core identity with authentication disabled, diagnostics
      exports are refused (409 at start) because no account exists to narrow them to.
      Whether auth-off installs should get whole exports is an owner decision; until it is
      made, an export under identity needs a signed-in account. See the start refusal in
      `internal/server/diagnostics_export_start.go` (the definition's start precondition) and `errNoExportActor` in
      `internal/server/wire_repairs_routes.go`.
