- [ ] **M3-RETIREMENT-GUARD** Before any change removes `books.identity-cutover` (M3) from books' manifest, make the kernel's identity boot (P4d/P5) refuse, before any write, a store not yet at the identity level (due with that change; R3-02 retires M3).
      Today the kernel's DAG check at P2 is the only thing stopping that boot, and only while
      `books.rewrite` still names M3 in its `After`. A manifest that has lost every mention of
      M3 boots over a store killed before M3's first commit, and P4d/P5 writes core's
      `c/auth/user/_system`, `c/auth/user/name/_system`, the seed roles, the rbac generation
      and a bootstrap token, all before Attach's `identityStoreReady` refuses. Between Register
      and Attach the store's ListUsers answers core's lone system user while HEAD's accounts
      are still `u:` rows. The next boot of the correct build then stops in M3 with
      `the cutover is blocked; see storage cutover-blockers: 1 blocking row(s)`: HEAD's `_system`
      collides with the seeded one. Evidence: the `lost-m3-attach-refused` subtests of
      `TestMidM3_HalfStoreIsNeverServed` (internal/books/midm3_test.go), which pin today's
      writes. Fix: P4d/P5 refuses that store with no write, pinned by a killed-before-M3 cell
      that asserts the identity rows and `c/auth/` are byte-identical after the refusal. The
      tripwire is the comment in `TestManifest_FlipDeclaresM2M7M8`.
