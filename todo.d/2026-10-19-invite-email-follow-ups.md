- [ ] **INVITE-FOLLOWUPS** Follow-ups from emailed invitations (`feat/invite-email`):
      (1) an admin action to clear or set another user's email, which also runs
      the guest-access revoker; today a self-set address blocks inviting that
      address (409) until its holder changes it, and only a verified SSO login
      can take it back;
      (2) reactivating a user does not restore an invitee's SSO grant or
      Cloudflare policy entry: decide whether it should, or keep re-invite as
      the path;
      (3) Cloudflare Access bypass for `/invite/*`, `/api/v1/auth/invite/*`,
      `/api/v1/auth/accept-invite` and the web assets, so a guest can accept in
      a browser before the policy sync adds them;
      (4) the public invite limiter is effectively global behind the proxy
      (`SetTrustedProxies(nil)`), so key it on a trusted client-IP header;
      (5) OAuth login and the Cloudflare Access middleware still do not refuse
      a deactivated user (pre-existing).
