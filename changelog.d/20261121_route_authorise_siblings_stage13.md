### Added

- core/httpapi: `Guards.Session`, a stricter interactive guard that admits only the user's own sign-in
  (`auth.Method.Interactive`), so a delegated session (temp-login, invite) is refused 403 `interactive_required`
  where `Guards.Interactive` admits it. OpenAPI lists it as `session` in `x-mediacore-guards`.

### Fixed

- core diagnostics: the bundle download declares `Guards.Session`, so the router itself refuses a delegated session
  (05-health SEC2-13). Until now only the handler refused it, and the route's declared guards said less than was
  enforced.
- core health, updater and factory-reset routes: the handlers' own repeat of the route's permission and guards now
  asks stage 13's `httpapi.Authorize`, so a caller failing more than one leg is refused for the leg the router
  refuses first (the guards, then the permission). They checked the permission first while their comments said
  stage 13's order. The factory reset's `core:admin` check also moves from `Has` to `Satisfies`, which admits a
  subset of what `Has` admits.
