# Task 007: SEC-AUDIT-3 — Fix path injection in cover handlers

**Depends on:** task 005 (SafePath package must be merged first)
**Estimated effort:** S (2–3 hours)
**Wave:** 4 (parallel with tasks 006, 008–010, after task 005 merges)

## Goal

Fix ~9 path-injection CodeQL alerts in `internal/server/covers.go` and
`internal/server/cover_history.go` by replacing raw filepath operations with `safepath.Join`.

## Context

- Alerts: #602–#594
- These handlers serve cover art images from the filesystem. User-supplied book IDs or cover
  filenames flow into filesystem reads without proper path containment checks.
- The `IsAllowedCoverSource` domain allowlist already exists for URL-based SSRF, but file paths
  inside the covers directory still use plain `filepath.Join`.

## Files to modify

- `internal/server/covers.go`
- `internal/server/cover_history.go`

## Instructions

1. Get precise alert locations:
   ```bash
   gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&tool_name=CodeQL&per_page=100" \
     | jq '.[] | select(.rule.id == "go/path-injection") | select(.most_recent_instance.location.path | contains("covers")) | {number:.number, location:.most_recent_instance.location}'
   ```

2. For each flagged line, replace raw `filepath.Join(coversDir, userInput)` with:
   ```go
   import "github.com/jdfalk/audiobook-organizer/internal/security/safepath"

   sp, err := safepath.Join(coversDir, userInput)
   if err != nil {
       c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
       return
   }
   // use sp.String() for filesystem operations
   ```

3. The covers directory root likely comes from `AppConfig.RootDir` or a derived covers path.
   Ensure this root is used consistently (no hardcoded paths).

4. For cover history lookups: same pattern — validate the history file path against the
   history directory root before opening.

5. Bump version headers on every modified file.

## Test

```bash
go test ./internal/server/... -run TestCover -v -count=1
make ci
```

## Commit

```
fix(covers): replace filepath.Join with safepath.Join in cover handlers (SEC-AUDIT-3)
```

## PR title

`fix(security): path injection in cover handlers — SEC-AUDIT-3`

## After merging

Mark `- [ ] **SEC-AUDIT-3**` as `- [x]` in `TODO.md`.
