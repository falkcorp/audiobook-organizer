- [ ] **GUEST-SERVICE-TOKEN-FOLLOWUPS** Per-guest Cloudflare service token follow-ups:
      (1) an admin "issue key" action (re-mint) for a guest whose mint failed at
      accept (for example the missing "Access: Service Tokens > Edit" permission)
      or who was reactivated after a deactivation revoked the key; the secret
      must then be delivered once to the admin or by email, so decide the channel;
      (2) hook `revokeGuestServiceToken` into user deletion when a user-delete
      path exists (none on main today);
      (3) first live run against the real Cloudflare API with the extra
      permission, to confirm the `last_seen_at` field and the `forever` duration;
      (4) `cfservicetoken.Client.Revoke` should check the exact name
      `aorg-guest-<user id>` of the record it is revoking, not just the
      `aorg-guest-` prefix;
      (5) the accept request waits on Cloudflare for up to 45 s while minting:
      consider a shorter bound, or minting after the response with the key
      delivered another way.
