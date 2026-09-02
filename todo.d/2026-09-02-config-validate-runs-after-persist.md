- [ ] **CFG-VALIDATE-AFTER-PERSIST** `PUT /api/v1/config` still calls
      `Config.Validate()` AFTER `UpdateService.UpdateConfig` has persisted the
      blob (`internal/server/handlers/system/handler.go`), so for every field
      other than `dedup.signals` a value that `Validate()` rejects is already in
      the DB by the time the handler answers 400 — and `cmd/root.go` runs the
      same `Validate()` as a hard startup error, so the next restart fails
      closed on a setting the API appeared to reject. PR #3052 closed this for
      the dedup score ladder only (validated inside `UpdateConfig` before the
      save, with a memory/blob rollback). Move the whole `Validate()` call
      inside `UpdateConfig` (before `SaveConfigToDatabase`) for every field, or
      delete the post-persist call in the handler and make the pre-persist one
      authoritative. Blocked on: several config tests drive `UpdateConfig`
      against a zero `AppConfig` that `Validate()` rejects for unrelated
      reasons (empty `database_type`, etc.), so those fixtures need a valid
      baseline first.
