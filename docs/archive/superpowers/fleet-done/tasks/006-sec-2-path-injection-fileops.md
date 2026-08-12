# Task 006: SEC-AUDIT-2 — Fix path injection in fileops layer

**Depends on:** task 005 (SafePath package must be merged first)
**Estimated effort:** M (4–6 hours)
**Wave:** 4 (parallel with tasks 007–010, after task 005 merges)

## Goal

Fix ~9 path-injection CodeQL alerts in `internal/fileops/` by replacing raw
`filepath.Join` calls with `safepath.Join` from `internal/security/safepath`.

## Context

- Alerts: #625–#620, #543, #542, #539, #538–#536 (exact numbers may differ post MaD pack)
- Files: `internal/fileops/service.go`, `internal/fileops/hash.go`,
  `internal/fileops/write_tags_safe.go`, `internal/fileops/safe_operations.go`
- The existing `util.SafeJoin` free function already guards some paths; the remaining alerts
  are call sites that use plain `filepath.Join` or `os.Open` with unvalidated user input
- Pattern to apply: replace `filepath.Join(someRoot, userInput)` with
  `safepath.Join(someRoot, userInput)` and propagate the error

## Files to modify

- `internal/fileops/service.go`
- `internal/fileops/hash.go`
- `internal/fileops/write_tags_safe.go`
- `internal/fileops/safe_operations.go`

## Instructions

1. Run the current CodeQL alerts to get a precise list:
   ```bash
   gh api "repos/jdfalk/audiobook-organizer/code-scanning/alerts?state=open&tool_name=CodeQL&per_page=100" \
     | jq '.[] | select(.rule.id == "go/path-injection") | {number:.number, location:.most_recent_instance.location}'
   ```
   Filter results to only those in `internal/fileops/`.

2. For each alert in fileops, find the flagged line. It will be a pattern like:
   ```go
   path := filepath.Join(root, userInput)
   f, err := os.Open(path)
   ```

3. Replace with:
   ```go
   import "github.com/jdfalk/audiobook-organizer/internal/security/safepath"

   sp, err := safepath.Join(root, userInput)
   if err != nil {
       return fmt.Errorf("invalid path: %w", err)
   }
   f, err := os.Open(sp.String())
   ```

4. If the function signature must also change (e.g., it currently returns the path as string),
   consider whether to change the return type to `safepath.SafePath` or keep it as string
   (returning `sp.String()`). For internal helpers, returning `SafePath` is preferred.
   For handler-layer functions that return JSON paths, returning `string` is fine.

5. Update all call sites within the fileops package to handle the new error return.

6. Bump version headers on every modified file.

## Test

```bash
go test ./internal/fileops/... -v -count=1
make ci
```

Check CodeQL alert count drops for fileops files after the PR's scan completes.

## Commit

```
fix(fileops): replace filepath.Join with safepath.Join to fix path injection (SEC-AUDIT-2)
```

## PR title

`fix(security): path injection in fileops layer — SEC-AUDIT-2`

## After merging

Mark `- [ ] **SEC-AUDIT-2**` as `- [x]` in `TODO.md`.
