- [ ] **01-P81K-API-KEY-ROTATE-GAP** Capability gap from 01-P81k: replacing an API
      key is now two calls instead of one. `POST /api/v1/auth/api-keys/:id/rotate`
      answers 410 and names `POST /api/v1/auth/api-keys` (create) followed by
      `DELETE /api/v1/auth/api-keys/:id` (revoke) as the replacement. Create,
      list, get, status change and revoke are unchanged. What the pair does not
      give: (1) the one-hour grace window the old key kept after a rotate, so
      the owner must create the new key and move the client over BEFORE revoking
      the old one; (2) the old key's scopes and lifetime carried over, so the
      owner passes `scopes` and `expires_in_days` again (default 30 days). If
      either matters, add `replaces_key_id` to the create request instead of
      reviving rotate (the grace expiry via `SetAPIKeyExpiry`, and the clamp to
      the calling key's expiry, are both already in the create path).
