<!-- file: changelog.d/20260731_234500_oauth_email_as_username.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9f2b6d14-7e83-4a05-b1c6-3d8e5f0a7b21 -->
<!-- last-edited: 2026-07-31 -->

### Changed

- SSO/OAuth-provisioned accounts are now named by their full verified email address
  instead of a username derived from the part before the `@`. Signing in with
  `owner@example.com` creates the user `owner@example.com`, not `owner`. The old
  derived form collided across domains and providers, and named the account something
  the owner had never typed. Existing users are unaffected — this only applies when a
  new account is auto-created, and sign-in still resolves an existing account by
  identity link first and verified email second.
