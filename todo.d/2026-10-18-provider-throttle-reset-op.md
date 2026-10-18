- [ ] **THROTTLE-RESET-OP** Add an op to release metadata provider holds by
      hand. 01-P81f retired `DELETE /api/v1/metadata/providers/throttles` and
      `DELETE /api/v1/metadata/providers/throttles/:id` (no caller), and with
      them `ThrottleRegistry.Clear`/`ClearAll`. A quota or auth hold now ends
      only on expiry or a successful call, and it survives restarts. Add a
      registered op (with a preview mode, per the owner's rule), not a route,
      and give the planned Throttle panel a "reset now" button on top of it.
