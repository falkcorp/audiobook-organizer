- [ ] **WEB-SESSIONS-SURVIVE-PASSWORD-CHANGE** Changing a password, an admin
      password reset (direct or via reset link) and deactivating a user do not
      end the user's web-UI sessions. #4081 made all four paths revoke ABS
      (mobile) sessions through `handlers.RevokeAllABSSessions`; the web
      session store has the same gap and was left alone on purpose. Fix
      properly: revoke every web session for the user on each of those paths
      (keeping the caller's own session on a self-service change is a product
      call), fail closed the same way the ABS revoke does, and cover each path
      with a test that a pre-existing web session stops authenticating.
