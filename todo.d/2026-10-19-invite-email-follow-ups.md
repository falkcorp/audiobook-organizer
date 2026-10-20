- [ ] **INVITE-FOLLOWUPS** Follow-ups from emailed invitations (`feat/invite-email`):
      (1) reactivating a user does not restore an invitee's SSO grant or
      Cloudflare policy entry: decide whether it should, or keep re-invite as
      the path;
      (2) Cloudflare Access bypass for `/invite/*`, `/api/v1/auth/invite/*`,
      `/api/v1/auth/accept-invite` and the web assets, so a guest can accept in
      a browser before the policy sync adds them;
      (3) OAuth login and the Cloudflare Access middleware still do not refuse
      a deactivated user (pre-existing);
      (4) confirm that `CF-Connecting-IP` reaches the origin in production:
      without it the public invite limiter's per-client layer falls back to
      the proxy's address and only the per-token and global layers separate
      clients;
      (5) with `write_startup_readonly_key` on (the default),
      `InitStartupReadOnlyKey` calls `findOrCreateAdminUser` at every start,
      so a fresh install gets an admin whose generated password is
      thrown away, after which first-run setup (`CountUsers() == 0`) is
      refused and bootstrap returns no password (pre-existing).
