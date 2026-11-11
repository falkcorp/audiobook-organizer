- [ ] **F4-02-ROLE-GUARD** Guard role definitions in core auth's last-admin rule
      before anything implements `rbac.Holders`. `core/auth`'s `keepAnAdmin`
      guards the other admin holder's user record but only reads the role
      definitions: `rbac.Store` cannot stage an Expect on a role into auth's
      batch, so an rbac edit that takes `core:admin` from that holder's role and
      commits between auth's read and commit is not seen. Add an optional
      role-guard interface on the role store (F4-01's `ImportSealer` pattern) and
      an `rbac.Service` method auth calls inside its batch, with a concurrent
      test (about 100 lines across `rbac`, `rbacstore` and tests). Hard
      prerequisite of F4-08's `rbac.Holders` (see
      `docs/agent-tasks/platform-core/seams.d/F4-02.md`, "F4-08 must know").
