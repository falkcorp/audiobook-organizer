- [ ] **ABS-REHASH-BREAKS-WEB-LOGIN** One ABS password login locks the user out
      of the web UI. `handlers/abs/login.go` `rehashPassword` re-stores a
      verified bcrypt password as argon2id (spec §3.5), but web `Login`
      (`internal/server/handlers/auth.go` ~L397) and `ChangePassword` (~L608)
      check the password with `bcrypt.CompareHashAndPassword` only, so after a
      phone signs in once the web login answers "invalid credentials" and the
      user cannot change their own password. Found while writing #4081's
      integration test, which has to put the bcrypt hash back to change the
      password. Fix properly: one shared verifier that dispatches on
      `PasswordHashAlgo` (bcrypt and argon2id) used by every password check,
      and decide whether `ChangePassword` should write argon2id too.
