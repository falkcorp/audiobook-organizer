<!-- file: docs/agent-tasks/ci-flaky-fixes/TASK-02-flaky-backup-endpoints.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1e32cdd4-17b2-4a57-890d-e61a49baea79 -->
<!-- last-edited: 2026-07-01 -->

# TASK-02 — Root-cause + fix TestBackupEndpointsErrors (flaky-backup)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none (but should rebase onto `origin/main` AFTER TASK-01 merges — see workstream README collision note; do not run concurrently with TASK-01).

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cf-flaky-backup-endpoints" -b agent/cf-flaky-backup-endpoints origin/main
cd "$REPO/.worktrees/cf-flaky-backup-endpoints"
git rebase origin/main
```

## Goal

`TestBackupEndpointsErrors` is flaky/environment-sensitive — it has failed on
`main`, not just on feature branches. Find the actual root cause (do NOT just
add a retry or increase a timeout) and fix it so the test is deterministic.

## Background (verify before editing)

- Locate the test and re-confirm the line numbers below (they drift):
  ```bash
  grep -n "func TestBackupEndpointsErrors" -A 40 internal/server/server_extra_test.go
  ```
  As of this writing it is at `internal/server/server_extra_test.go:485-518`.
- Read it carefully. It does this, in order:
  1. `setupTestServer(t)` (starts a full in-memory server, including the
     background operation-registry worker pool and file-I/O pool — see the
     `registry: worker started` / `file I/O pool` log lines when you run it
     verbose).
  2. Saves `config.AppConfig` and the process's current working directory.
  3. Calls `os.Chdir(tempDir)` — **this mutates the OS process's global
     working directory**, shared by every goroutine in the test binary,
     including any leftover background goroutines from `setupTestServer`
     (registry workers, file I/O pool) that may still be doing relative-path
     file I/O when the chdir happens.
  4. Sets `config.AppConfig.DatabasePath = filepath.Join(tempDir, "missing.db")`
     — note this is already an ABSOLUTE path.
  5. Exercises `/api/v1/backup/create`, `/api/v1/backup/restore`,
     `DELETE /api/v1/backup/{file}` and asserts specific HTTP status codes.
  6. `defer`s restoring both the original cwd and `config.AppConfig`.
- Now check what the handlers under test actually do with the backup
  directory — this is the key finding: it does NOT depend on the process cwd
  at all.
  ```bash
  grep -n "DefaultBackupConfig\|BackupDir" internal/server/handlers/system/handler.go
  ```
  In `CreateBackup`, `RestoreBackup`, and `DeleteBackup` (currently around
  lines 523-660 of `internal/server/handlers/system/handler.go`), the backup
  directory is derived like this:
  ```go
  backupConfig := backup.DefaultBackupConfig()
  if dbPath := config.AppConfig.DatabasePath; dbPath != "" && !filepath.IsAbs(backupConfig.BackupDir) {
      backupConfig.BackupDir = filepath.Join(filepath.Dir(dbPath), backupConfig.BackupDir)
  }
  ```
  Since the test already sets `config.AppConfig.DatabasePath` to an ABSOLUTE
  path under `tempDir`, `backupConfig.BackupDir` resolves to an absolute path
  under `tempDir` regardless of the process's current working directory. **The
  `os.Chdir` call in the test is unnecessary** — it's a leftover global-state
  mutation that buys nothing here, and process-wide `os.Chdir` racing against
  any other goroutine's relative-path file I/O (including from this test's own
  background registry/file-I/O-pool workers that haven't fully drained yet) is
  a classic source of environment-sensitive flakiness: the outcome depends on
  exact goroutine scheduling, which varies by machine load / CI runner.
- Confirm this diagnosis before changing anything: run the test alone (passes
  in isolation almost always — that's expected, this is a scheduling race, not
  a deterministic bug) and then run the FULL package test suite repeatedly to
  try to reproduce the flakiness under load:
  ```bash
  go test ./internal/server/... -run TestBackupEndpointsErrors -count=20 -v
  go test ./internal/server/... -count=5 -race
  ```

## Step-by-step

1. In `internal/server/server_extra_test.go`, in `TestBackupEndpointsErrors`,
   remove the `os.Chdir(tempDir)` call and its corresponding `defer os.Chdir(origDir)`
   restore, and remove the now-unused `origDir, err := os.Getwd()` /
   `require.NoError(t, err)` lines that only existed to support the chdir.
2. Keep `config.AppConfig.DatabasePath = filepath.Join(tempDir, "missing.db")`
   (and the `DatabaseType` line) exactly as-is — this absolute path is what
   actually makes the handlers resolve `BackupDir` under `tempDir`; it's the
   real mechanism the test relies on, not the chdir.
3. Keep the `defer func() { config.AppConfig = origConfig }()` restore — that
   part is legitimate and necessary (it undoes the `DatabasePath`/`DatabaseType`
   mutation).
4. Re-run the diagnostic commands from the Background section to confirm the
   three assertions (`500`, `400`, `500`) still pass with the chdir removed —
   this proves the chdir was never load-bearing.
5. If, contrary to the above analysis, you find any OTHER code path reached by
   these three requests that resolves a path relative to cwd (grep for
   `filepath.Join("."` or bare relative literals in the backup create/restore/
   delete call chain), keep the minimal absolute-path fix but replace the
   `os.Chdir` with directly setting that config field to an absolute value
   instead of mutating process-global cwd. Document what you found in the
   commit body.
6. Bump the file header on `internal/server/server_extra_test.go`.

## How to test

```bash
go test ./internal/server/... -run TestBackupEndpointsErrors -count=20 -v
go test ./internal/server/... -race -count=3
go build ./...
go vet ./...
```
All must pass. The `-count=20` run proves determinism in isolation; the
package-level `-race -count=3` run is the closest local approximation of the
full-suite conditions where the flake was reported.

## Acceptance criteria

- [ ] `os.Chdir`/`os.Getwd` process-global mutation removed from
      `TestBackupEndpointsErrors`.
- [ ] Test still exercises absolute paths under `t.TempDir()` and asserts the
      same three status codes (500 / 400 / 500) as before.
- [ ] `go test ./internal/server/... -run TestBackupEndpointsErrors -count=20 -v` passes every iteration.
- [ ] `go test ./internal/server/... -race -count=3` passes.
- [ ] File header bumped.

## Commit message

```
fix(server): remove unnecessary os.Chdir from TestBackupEndpointsErrors (flaky-backup)

The backup handlers already derive BackupDir from the absolute
config.AppConfig.DatabasePath, so the test's os.Chdir(tempDir) was a dead,
process-global mutation that raced against background registry/file-I/O-pool
goroutines and caused environment-sensitive flakiness. Removing it makes the
test deterministic without changing what it verifies.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cf-flaky-backup-endpoints
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency check: `grep -n "os.Chdir" internal/server/server_extra_test.go`
— if `TestBackupEndpointsErrors` no longer calls it, this is done. Rollback =
revert the commit (restores the chdir, and the pre-existing flakiness).
