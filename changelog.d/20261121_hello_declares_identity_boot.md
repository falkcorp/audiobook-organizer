### Changed

- The hello example app now declares the identity boot, so it serves core's own sign-in, user, role, session, API-key and audit routes. `core/openapi/core.json` and the generated TypeScript client under `core/web/packages/api-client/src/generated/` are regenerated for those 44 routes, including `POST /api/core/v1/users/{id}/review-admin`. A first run mints a bootstrap token in the state directory, and a plain-HTTP peer is served with a session cookie that has no `__Host-` prefix.
