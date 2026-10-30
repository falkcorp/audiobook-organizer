### Fixed

- The `internal/server/handlers/abs` test package now lowers the argon2id
  password-hash cost in its `TestMain` (the same test-only seam
  `internal/server` uses), cutting the package from 176.6 s to 53.8 s in the
  fixture shards. No test asserts on the cost; hashes still verify with the
  parameters stored in them.
