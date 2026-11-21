### Added

- `core/auth`: `Service.UpdateProfile` (the PATCH /me writer: display name and e-mail; a changed e-mail is unverified, is refused when another account holds it verified, and is checked and written under the account e-mail lock), `KeySpec.ExpiresAt`, `PlainEmail` and `ErrEmailNotPlain` (shared with `auth/oidc`). Dark: nothing mounts `core/internal/authhttp` yet.
- `core/internal/authhttp`: `expires_at` on API key creation (RFC 3339, future, at most 1096 days; sending it with `expires_in` is a 400) and the profile writer wired through `FromService`.
