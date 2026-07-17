<!-- file: docs/agent-tasks/perf-cleanup/TASK-05-retire-flatkey-shim.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1d5c8a3f-7e2b-49a1-bc06-8f3e2d1a9c47 -->
<!-- last-edited: 2026-07-01 -->

# TASK-05 — Retire flat-to-nested config compat shim (CONS-13/CFG-2-D)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** 1+ week of confirmed prod stability of the nested config

> 🚫 **GATED — DO NOT DISPATCH OR MERGE UNTIL EXPLICITLY GREENLIT.**
> This task removes a backward-compatibility shim that lets the frontend (or
> any older client / cached browser tab) keep sending **flat** config keys
> (e.g. `embedding_enabled`) and have them transparently remapped to the
> **nested** config shape (e.g. `{"embedding": {"enabled": ...}}`). Removing
> it before the nested config has been running in production for **at least
> one week without any flat-key payload showing up** will silently break any
> client still sending flat keys — those fields will simply stop applying,
> with no error surfaced to the user. **A human must confirm the 1-week
> stability window before this task starts.** If you are an autonomous agent
> and were dispatched this task without that confirmation being explicitly
> stated in your assignment, STOP and ask before touching any files.

## ⛔ START HERE (do this first, exactly — only after the gate above is confirmed)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-retire-flatkey-shim" -b agent/pc-retire-flatkey-shim origin/main
cd "$REPO/.worktrees/pc-retire-flatkey-shim"
git rebase origin/main
```

## Goal

Remove the flat-to-nested key remap shim (`legacyRemapGroup` /
`configRemapGroups` / `applyLegacyRemaps`) in
`internal/config/update_service.go`. **Keep the separate blob-migration logic
in this file intact** — this task only removes the *key-name* remap, not any
other config migration. This directly reduces the surface area of
`UpdateConfig` and removes a translation table that must otherwise be kept in
sync with the frontend forever.

## Background (verify before editing)

Re-run — line numbers drift:
```bash
grep -n "legacyRemapGroup\|configRemapGroups\|applyLegacyRemaps\|func (us \*UpdateService) UpdateConfig" internal/config/update_service.go
```

At authoring time, `internal/config/update_service.go`:
- Lines 70-76 — `type legacyRemapGroup struct { groupKey string;
  flatToNested map[string]string }` — the shim's data type. **Remove.**
- Lines 77-146 — `var configRemapGroups = []legacyRemapGroup{ ... }` — six
  groups (`embedding`, `dedup`, `metadata_scoring`, `itunes`, `maintenance`,
  `auto_update`), each mapping old flat JSON keys (e.g.
  `"embedding_enabled"`) to new nested keys (e.g. `"enabled"` inside the
  `"embedding"` group). **Remove the whole var.**
- Lines 150-166 — `func applyLegacyRemaps(payload map[string]any)
  map[string]any { ... }` — iterates `configRemapGroups`, moving/merging flat
  keys into their nested group. **Remove the whole function.**
- Find every **call site** of `applyLegacyRemaps` (it is called from inside
  `UpdateConfig` — confirm with `grep -n "applyLegacyRemaps("
  internal/config/update_service.go`) and remove the call, leaving the
  payload to flow through `UpdateConfig` unchanged (nested-only from here on).
- **Do NOT touch** any blob-migration logic elsewhere in this file (anything
  not part of `legacyRemapGroup`/`configRemapGroups`/`applyLegacyRemaps` — if
  unsure whether something is "blob migration" vs "key remap", grep for
  `Migrate` / `migration` in this file and treat those as out of scope).
- Check `internal/config/update_service_test.go` (and any sibling test files
  in `internal/config/`) for tests that exercise flat-key payloads — these
  will need to be removed or converted to nested-key payloads. Find them:
  ```bash
  grep -rln "embedding_enabled\|dedup_book_high_threshold\|itunes_sync_enabled\|maintenance_window_enabled\|auto_update_enabled" internal/config/*_test.go
  ```

## Step-by-step

1. Re-run the grep commands above to confirm anchors and enumerate every test
   touching a flat key.
2. Remove `legacyRemapGroup`, `configRemapGroups`, and `applyLegacyRemaps`
   from `internal/config/update_service.go`.
3. Remove the call to `applyLegacyRemaps` from `UpdateConfig`, leaving the
   payload flowing through unchanged (nested-only).
4. Update or remove any tests found in step 1 that assert flat-key payloads
   get remapped. If a test's *purpose* was verifying that a specific config
   field can be set at all (not specifically testing the remap shim),
   convert it to send the nested-key form instead of deleting it outright —
   preserve test coverage of the underlying field, just via the now-only
   supported (nested) shape.
5. Grep the frontend for any flat-key usage that would now silently stop
   working, purely as a sanity check (do not fix frontend code in this task —
   just confirm nothing obviously still sends flat keys):
   ```bash
   grep -rln "embedding_enabled\|itunes_sync_enabled\|maintenance_window_enabled" web/src/ 2>/dev/null
   ```
   If this turns up hits, **stop and flag it in the PR description** rather
   than silently proceeding — that would mean the frontend itself still
   depends on the shim and the 1-week-stability gate has not actually been
   met.
6. Bump the file header in `internal/config/update_service.go` (version
   3.9.0 → 4.0.0 given this is a behavior-narrowing change, `last-edited` →
   today's date).

## How to test

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer/.worktrees/pc-retire-flatkey-shim
go build ./...
go test ./internal/config/... -v -count=1
go test ./internal/config/... ./internal/server/... -count=1
go vet ./internal/config/...
```

## Acceptance criteria
- [ ] `legacyRemapGroup`, `configRemapGroups`, and `applyLegacyRemaps` are removed from `internal/config/update_service.go`.
- [ ] The blob-migration logic in the same file is untouched.
- [ ] All tests that previously sent flat-key payloads are updated to nested-key payloads (coverage of the underlying config fields preserved) or removed if they were purely testing the remap shim itself.
- [ ] A frontend grep for flat-key usage was run and the result (clean or hits) is stated explicitly in the PR description.
- [ ] `go build`, `go test ./internal/config/...`, `go vet` all green.
- [ ] File headers bumped.
- [ ] PR description explicitly states the 1-week prod-stability gate was confirmed before this task was dispatched (who confirmed it / when).

## Commit message
```
refactor(config): retire flat-to-nested key remap shim (CONS-13/CFG-2-D)

Removes legacyRemapGroup/configRemapGroups/applyLegacyRemaps now that the
nested config shape has been stable in production for 1+ week. Blob migration
is unchanged. Clients must send nested keys going forward.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/pc-retire-flatkey-shim
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback
Idempotency check: `grep -n "configRemapGroups\|applyLegacyRemaps" internal/config/update_service.go` — if no matches, this task is done. Rollback: revert the commit — this restores flat-key compatibility immediately if a client is discovered still depending on it after merge.
