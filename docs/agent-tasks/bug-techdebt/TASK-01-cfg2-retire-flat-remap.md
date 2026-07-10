<!-- file: docs/agent-tasks/bug-techdebt/TASK-01-cfg2-retire-flat-remap.md -->
<!-- version: 1.0.0 -->
<!-- guid: a670638a-624e-4866-adf3-6227d8f1c6ff -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Retire the CFG-2 flat-key compat shim (CFG-2 Phase D, #1536/CONS-13)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · code-removal + regression-test subagent · **Why:** deletion needs judgment about what stays (`remapScheduledKeys`, blob path) vs what goes · **Depends on:** none (stability check is step 1)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY per item (worktree/PR/CI) EXCEPT REPO-SIZE-1 (#1650) which is STOP-FOR-HUMAN: a git-history rewrite is destructive and invalidates every clone/worktree — produce the migration plan (BFG/filter-repo vs LFS options, coordination checklist, backup strategy) as a TASK brief whose ONLY deliverable is the plan document, then STOP.
**File-ownership:** none — no other INIT-9 task or sibling initiative touches `internal/config/`. TODO.md/CHANGELOG.md are shared docs-ledgers: rebase keep-both before merge.

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/bug-techdebt-cfg2-retire-flat-remap" -b agent/bug-techdebt-cfg2-retire-flat-remap origin/main
cd "$REPO/.worktrees/bug-techdebt-cfg2-retire-flat-remap"
git rebase origin/main
```

## Goal

Remove the flat→nested config migration shim (`legacyRemapGroup` type,
`configRemapGroups` var, `applyLegacyRemaps` func and its single call) from
`internal/config/update_service.go`, so config updates accept ONLY nested keys.
Because the post-removal path is fail-OPEN (a flat-only POST is silently dropped by
`json.Unmarshal`), ship a **detection-only warn-log** in the same PR (spec Decision 3):
a `retiredLegacyFlatKeys` list of the deleted flat key names drives a per-key warning
so a lost config write is observable for one release — log only, no remapping.
KEEP `remapScheduledKeys` (owned by INIT-6/WF-3) and KEEP the JSON round-trip
("blob") apply block untouched. Also fix TODO.md's stale path claim (it says the shim
lives in `internal/server/update_service.go`, a file that does not exist).

## Background (verify before editing)

- Phases B+C shipped in PR #1514 (2026-06-19): the frontend `loadConfig`/`handleSave`
  send nested keys (and redundantly also flat ones). Retiring the backend remap means
  flat-only keys are silently dropped by `json.Unmarshal` (no matching top-level json
  tags) — nested keys carry all data, so nothing is lost. Do NOT touch the frontend.
- `remapScheduledKeys` (defined in `internal/config/persistence.go`, ~:538) handles the
  deeper `scheduled_*` two-level nesting and is explicitly OUT of scope — it stays.
- The JSON round-trip block right after the remap calls (comment "Apply all remaining
  fields via JSON round-trip", ~:231-239) is the persistence path that STAYS.
- Stability gate (Phase D "gate on Phase B+C proven stable"): PR #1514 merged
  2026-06-19 — 3 weeks before this brief was written; re-verify in step 1.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'legacyRemapGroup\|applyLegacyRemaps' internal/config/update_service.go
  # Expected: hits ~:70 (comment), :72 (type), :80 (var configRemapGroups), :147/:150 (func), :228 (call)
  grep -n 'remapScheduledKeys' internal/config/update_service.go internal/config/persistence.go
  # Expected: call in update_service.go ~:229, definition in persistence.go ~:538 — BOTH STAY
  grep -n 'Apply all remaining fields via JSON round-trip' internal/config/update_service.go
  # Expected: 1 hit ~:231 — this block STAYS
  grep -rn 'applyLegacyRemaps\|configRemapGroups\|legacyRemapGroup' --include='*.go' . | grep -v .worktrees
  # Expected: hits ONLY in internal/config/update_service.go and internal/config/persistence_test.go (~:866-:1116)
  grep -n 'CONS-13\|CFG-2 Phase D' TODO.md
  # Expected: ~:481 (stale internal/server/ path) and ~:542 (Phase D checkbox)
  ```

## Step-by-step

1. **Stability check (do not skip):** Run:
   `gh pr view 1514 --repo falkcorp/audiobook-organizer --json mergedAt`
   Expected: `mergedAt` ≥ 7 days before today. Also run:
   `gh issue list --repo falkcorp/audiobook-organizer --state open --search "settings flat key regression"`
   Expected: no open regression issue. If either fails, STOP and report BLOCKED.
2. In `internal/config/update_service.go`: delete the `legacyRemapGroup` type (+ its
   doc comment), the `configRemapGroups` var (the whole literal), the
   `applyLegacyRemaps` func, and the single call line `filtered = applyLegacyRemaps(filtered)`.
   Update the adjacent comment (it names `configRemapGroups` as "the single source of
   truth" — rewrite to describe only `remapScheduledKeys`). BEFORE deleting the
   `configRemapGroups` literal, copy out its flat key names — you need them for step 3.
   Other than steps 2-3's changes, touch NOTHING else in the file:
   `remapScheduledKeys` call and the JSON round-trip block stay byte-identical.
3. Add the detection-only warn-log (spec Decision 3) in the same file: a
   `retiredLegacyFlatKeys = []string{...}` var holding the flat key names from the
   deleted literal, and — at the point where the `applyLegacyRemaps` call was — a
   loop that warn-logs each of those keys present in the incoming payload, message:
   `legacy flat config key %q received; no longer remapped, dropped`. Log only: no
   remapping, no payload mutation. Mirror the file's existing logging convention.
   The log lives for ONE release; step 6 files the removal follow-up.
4. In `internal/config/persistence_test.go`: delete every test function that calls
   `applyLegacyRemaps` (locate: `grep -n 'applyLegacyRemaps' internal/config/persistence_test.go`;
   expected ~10 call sites clustered :866-:1116). Delete whole functions, not lines.
   (The two NEW tests go in `update_service_test.go` — step 5 — NOT here; the spec's
   files-modified table and the plan's exact_files agree on this split.)
5. Add two NEW tests (in `internal/config/update_service_test.go`, which currently has
   no legacy-remap tests — verify: `grep -c 'applyLegacyRemaps' internal/config/update_service_test.go`
   Expected: 0):
   - `TestUpdateService_FlatKeysDropped` — build a payload containing ONLY a formerly
     remapped flat key (pick one from the deleted `configRemapGroups` literal, e.g. the
     dedup-embeddings flat key you saw in step 2) and assert the corresponding NESTED
     config field is UNCHANGED after the update call. If the package's logging is
     capturable in tests, also assert the step-3 warn-log fired for that key;
     otherwise the acceptance grep below is the check.
   - `TestUpdateService_NestedKeysStillApply` — the nested form of the same key IS
     applied (anti-regression for the kept JSON round-trip path). Mirror the calling
     convention of an existing test in the same file rather than inventing a new fixture.
6. Fix TODO.md: at the CONS-13 line (~:481) replace `internal/server/update_service.go`
   with `internal/config/update_service.go`, and mark BOTH the CONS-13 line (~:481) and
   the "CFG-2 Phase D" checkbox (~:542) as `[x]` with a `✅ done 2026-07-10 (TASK-01)` note.
   Also ADD a new follow-up item: "CFG-2-D-LOG — remove the `retiredLegacyFlatKeys`
   detection warn-log from `internal/config/update_service.go` after one stable
   release with zero flat-only-key warnings in prod logs (added by TASK-01)".
7. Update CHANGELOG.md (prepend an entry, never replace existing content).
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.
9. Run the gate (below).

Anti-over-suppression: the "guard" this task effectively adds is *dropping* flat keys —
step 5's `TestUpdateService_NestedKeysStillApply` is the required proof that the happy
(nested) path still works, and step 3's detection warn-log is the required proof that
a drop is observable rather than silent.

## How to test

```bash
go test ./internal/config/ -race
# Expected: ok, including the two new tests
make ci
```

staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files
you changed; the merge gate is Minimal CI green. The `sdkguard` step is ALSO red on
main (#1795, fixed by TASK-03) — a failure listing only `internal/logger` +
`internal/dedup/unified` is pre-existing, not yours.

## Acceptance criteria

- [ ] `grep -rn 'applyLegacyRemaps\|configRemapGroups\|legacyRemapGroup' --include='*.go' . | grep -v .worktrees` returns 0 hits
- [ ] `grep -n 'retiredLegacyFlatKeys' internal/config/update_service.go` hits AND `grep -n 'no longer remapped' internal/config/update_service.go` hits (detection warn-log in place)
- [ ] `grep -n 'CFG-2-D-LOG' TODO.md` returns 1 hit (one-release removal follow-up filed)
- [ ] `grep -n 'remapScheduledKeys' internal/config/update_service.go` still hits (kept)
- [ ] `grep -n 'Apply all remaining fields via JSON round-trip' internal/config/update_service.go` still hits (kept)
- [ ] `go test ./internal/config/ -race` green including `TestUpdateService_FlatKeysDropped` and `TestUpdateService_NestedKeysStillApply` (anti-over-suppression: nested path proven alive)
- [ ] `grep -n 'internal/server/update_service.go' TODO.md` returns 0 hits
- [ ] Tests green; vet/lint clean (scoped staticcheck: `staticcheck ./internal/config/...` — only pre-existing findings outside your diff, none in it).
- [ ] File headers bumped on every changed file (`grep -n 'last-edited:' <file>` shows 2026-07-10 or later).

## Commit message

```
refactor(config): retire CFG-2 Phase D flat-key compat shim (#1536, CONS-13)

Phases B+C (PR #1514) have been stable in prod since 2026-06-19; the frontend
sends nested keys. Removes legacyRemapGroup/configRemapGroups/applyLegacyRemaps;
keeps remapScheduledKeys (INIT-6/WF-3) and the JSON round-trip apply path. Adds
a one-release detection-only warn-log (retiredLegacyFlatKeys) so any flat-only
POST is observable rather than silently dropped. Also fixes TODO.md's stale
internal/server/update_service.go path claim.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/bug-techdebt-cfg2-retire-flat-remap
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -rn "applyLegacyRemaps" --include='*.go' .` (excluding `.worktrees/`) returns
0 hits AND `grep -n 'internal/server/update_service.go' TODO.md` returns 0 hits, the
removal is already done — run the acceptance checks instead of re-applying. Rollback =
revert the single commit to restore the shim + its tests; the revert is a **true
inverse** — no data-shape migration in either direction (config on disk is untouched
by both the removal and the revert), which is why no feature flag gates this removal.
No data or schema is touched, and clients sending nested keys are unaffected in both
directions.
