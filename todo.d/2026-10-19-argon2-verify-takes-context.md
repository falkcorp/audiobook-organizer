- [ ] **Make `absauth.HashPassword` / `VerifyPassword` context-aware** — the argon2id
  semaphore (`internal/server/absauth/password.go`, `argonSem`) is a plain blocking
  acquire, so a request whose client has gone still waits its turn and then runs a full
  64 MiB derivation. Take a `context.Context` and `select` on `ctx.Done()` while
  waiting for a slot. Touches all 7 call sites (web Login/ChangePassword/setup, invite
  accept ×2, bootstrap admin, ABS login + rehash); do it after #4081 lands, since that
  PR rewrites the ChangePassword and rehash call sites. From the #4082 re-review (NIT 2).
