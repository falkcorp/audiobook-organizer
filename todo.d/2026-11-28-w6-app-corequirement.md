- [ ] **W6-APP-COREQUIREMENT** Core-owned native routes can require only core
      scopes: `core/httpapi/router.go` checkPerm R6 refuses a foreign scope
      (ErrForeignScope). The fix is app-declared co-requirements through
      `app.Options`, which the router ANDs with the route's own permission, with
      R6 relaxed only for the path the app declared, plus an archtest that core
      never names an app scope. No migrated principal is affected today, but a
      native role created after R2 holding only the core scope would pass a
      route the app meant to protect with its own scope. It must land before the
      R2 release.
