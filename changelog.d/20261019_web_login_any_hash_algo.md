### Fixed

#### One ABS phone sign-in no longer locks the user out of the web UI

The ABS sign-in rehashes a bcrypt user's password to argon2id and records
`PasswordHashAlgo=argon2id`. The web `Login` and `ChangePassword` handlers
called `bcrypt.CompareHashAndPassword` directly and ignored the algorithm column,
so after one sign-in from the phone app the user got "invalid credentials" on
the web and could not change their own password. Both now verify through
`absauth.VerifyPassword`, the same algorithm-aware verifier the ABS sign-in
uses, so bcrypt and argon2id hashes both verify and an unknown or `oauth`
algorithm is denied.

Web password writes now use one hashing policy too: initial setup, password
change, invite acceptance (both routes) and the generated bootstrap admin all
call `absauth.HashPassword` and store argon2id. Existing bcrypt rows still
verify (including rows with an empty algo) and the ABS sign-in still upgrades
them; the web sign-in does not rehash.

argon2id work is now capped process-wide at the CPU count. Each derivation
allocates 64 MiB, so a burst of parallel sign-ins no longer allocates that many
times over.

`TestNoDirectBcrypt` fails if production code in the root package, `internal/`,
`cmd/`, `pkg/` or `tools/` calls `bcrypt.CompareHashAndPassword` or
`bcrypt.GenerateFromPassword` outside `absauth/password.go`, including through an
import alias.
