### Changed

- The settings cutover now logs a note when a stored `oauth_default_role` other than `viewer` has no OAuth provider entry to ride on (sign-in disabled, or enabled with no client id: an Access-only install). Under the flip a new Cloudflare Access user gets the `viewer` role, because there is no Access-only default-role setting; the stored role is kept sealed in the cutover's quarantine. The cutover is not blocked and no access is widened.
