### Fixed

#### One ABS phone sign-in no longer locks the user out of the web UI

The ABS sign-in rehashes a bcrypt user's password to argon2id and records
`PasswordHashAlgo=argon2id`. The web `Login` and `ChangePassword` handlers
called `bcrypt.CompareHashAndPassword` directly and ignored the algorithm column,
so after one sign-in from the phone app the user got "invalid credentials" on
the web and could not change their own password. Both now verify through
`absauth.VerifyPassword`, the same algorithm-aware verifier the ABS sign-in
uses, so bcrypt and argon2id hashes both work and an unknown or `oauth`
algorithm is denied. Web password set and change still write bcrypt (with
`PasswordHashAlgo=bcrypt`); web `Login` does not rehash. A new test,
`TestNoDirectBcryptVerification`, fails if production code under `internal/` or
`cmd/` verifies a password with bcrypt directly again.
