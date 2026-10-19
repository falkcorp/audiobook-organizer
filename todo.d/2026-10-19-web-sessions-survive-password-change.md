- [ ] **WEB-SESSIONS-SURVIVE-PASSWORD-CHANGE** Changing a password, an admin
      password reset (direct or via reset link) and deactivating a user do not
      end the user's web-UI sessions. #4081 ended ABS (mobile) sessions on all
      four paths by bumping `User.CredentialEpoch` in the same write as the
      change and checking each session's stamp on every use; the web session
      store has the same gap and was left alone on purpose. Fix properly: stamp
      web sessions with the epoch too and refuse a mismatch in the session
      middleware (keeping the caller's own session on a self-service change is
      a product call: re-stamp it in the same request), then revoke as cleanup,
      and cover each path with a test that a pre-existing web session stops
      authenticating, including one created mid-change.
