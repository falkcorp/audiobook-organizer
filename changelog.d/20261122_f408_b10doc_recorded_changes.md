### Changed

- An API key can now disable an API key through core (`PATCH /api/core/v1/me/api-keys/{id}`, `PATCH /api/core/v1/api-keys/{id}`); re-enabling still needs a signed-in session.
- Core's user list and user read need `core:users.view`; imported seed roles are unaffected.
- First run: before the first user exists the app shows the sign-in page in setup mode.
- User-management writes under core apply core's dominance, last-admin and role-assignment checks.
- The Users page's pending-invite list no longer shows a copy-link column; the invite email and Resend still deliver the link.
- Pre-existing orphaned rows are kept and reported, never deleted.
- The invite-accept session keeps 30 days; remember-me sign-ins keep 7 days and plain sign-ins 24 hours.
- Docs: the bootstrap route is core's `POST /api/core/v1/auth/bootstrap`; the books route answers 401 after the identity flip.
