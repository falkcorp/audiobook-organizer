<!-- file: changelog.d/20260803_110000_abs_admin_key_identity.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f2c6b13-40ae-4d75-b83c-1e57a09d4f62 -->
<!-- last-edited: 2026-08-03 -->

### Added

- An `abk_` application API key is now accepted as an identity on the
  Audiobookshelf-compatible surface, so one credential can exercise the whole
  app instead of only `/api/v1`. Previously every ABS route answered `401
  no-credential` for an API key, which made the ABS surface untestable without
  first performing a password login.

  This is not a privilege bypass: the key still goes through the same hash
  lookup, revoked/inactive/expiry and owner-active checks, its scopes are
  intersected with the owner's role permissions so every ABS route keeps its
  normal authz, and the new step runs last so it cannot shadow the Cloudflare
  Access or ABS-token paths.
