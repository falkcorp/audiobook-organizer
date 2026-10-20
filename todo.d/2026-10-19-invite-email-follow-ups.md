- [ ] **INVITE-FOLLOWUPS** Follow-ups from emailed invitations (`feat/invite-email`):
      (1) reactivating a user does not restore an invitee's SSO grant or
      Cloudflare policy entry: decide whether it should, or keep re-invite as
      the path;
      (2) Cloudflare Access bypass for `/invite/*`, `/api/v1/auth/invite/*`,
      `/api/v1/auth/accept-invite` and the web assets, so a guest can accept in
      a browser before the policy sync adds them;
      (3) OAuth login and the Cloudflare Access middleware still do not refuse
      a deactivated user (pre-existing);
      (4) set `TUNNEL_CONNECTOR_ADDRS` in production to the address the
      Cloudflare tunnel connector reaches the service from: until then the
      public invite limiter skips its per-client layer for tunnel traffic and
      bootstrap callers share one bucket;
      (5) `UserHandler.AcceptInvite` (internal/server/handlers/user.go) is not
      routed (the route uses `Server.handleAcceptInvite`) and still echoes
      store errors as a 400: delete it or route through the generic refusal.
