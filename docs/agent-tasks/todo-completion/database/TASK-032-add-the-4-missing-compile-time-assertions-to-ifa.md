<!-- file: docs/agent-tasks/todo-completion/database/TASK-032-add-the-4-missing-compile-time-assertions-to-ifa.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5cde206e-434a-4fbc-b157-44d9a8e9e0bb -->
<!-- last-edited: 2026-08-21 -->

# TASK-032 — Add the 4 missing compile-time assertions to iface_assert.go (TODO.md L4694)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Haiku-class · database subagent · **Why:** 4-line mechanical addition following the file's exact existing pattern. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 4694 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "`internal/database/iface_assert.go` — its comment " TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-09.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/database-032-add-the-4-missing-compile-time-assertions-to-ifa" -b agent/database-032-add-the-4-missing-compile-time-assertions-to-ifa origin/main
cd "$REPO/.worktrees/database-032-add-the-4-missing-compile-time-assertions-to-ifa"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add `_ OAuthIdentityStore = (*PebbleStore)(nil)`, `_ MetadataCacheStore = (*PebbleStore)(nil)`, `_ RejectedMetadataStore = (*PebbleStore)(nil)`, `_ ReviewStore = (*PebbleStore)(nil)` to the var block in internal/database/iface_assert.go so PebbleStore's conformance to all 40 sub-interfaces is compiler-proven, not just 36 of them.

## Background (verify before editing)

- iface_assert.go's comment states its purpose: 'Compile-time proof that PebbleStore satisfies every sub-interface defined in iface_*.go.'
- The var block currently lists 36 interfaces alphabetically-by-domain (Store, LifecycleStore, BookStore, ... OpsV2Store).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -c "_ .* = (\*PebbleStore)(nil)" internal/database/iface_assert.go   # 36 — iface_assert.go currently has 36 assertions
  grep -n "^type OAuthIdentityStore interface" internal/database/iface_oauth.go   # 1 hit ~L9 — OAuthIdentityStore interface exists
  grep -n "^type MetadataCacheStore interface" internal/database/iface_metadata.go   # 1 hit ~L65 — MetadataCacheStore interface exists
  grep -n "^type RejectedMetadataStore interface" internal/database/iface_metadata.go   # 1 hit ~L123 — RejectedMetadataStore interface exists
  grep -n "^type ReviewStore interface" internal/database/iface_review.go   # 1 hit ~L12 — ReviewStore interface exists
  grep -n "func (p \*PebbleStore) CreateOAuthIdentity\|func (p \*PebbleStore) GetMetadataCache(\|func (p \*PebbleStore) AddMetadataRejection\|func (p \*PebbleStore) UpsertReviewItem" internal/database/*.go   # 4 hits across oauth_identity.go, pebble_store_metadata_cache.go, pebble_store_metadata.go, review_store.go — PebbleStore implements CreateOAuthIdentity, GetMetadataCache, AddMetadataRejection, UpsertReviewItem (one method each of the 4 interfaces, enough to prove they compile)
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. Open internal/database/iface_assert.go.
2. Inside the `var ( ... )` block (currently ending with `_ OpsV2Store = (*PebbleStore)(nil)`), add 4 new lines, keeping the existing alignment style: `_ OAuthIdentityStore  = (*PebbleStore)(nil)`, `_ MetadataCacheStore  = (*PebbleStore)(nil)`, `_ RejectedMetadataStore = (*PebbleStore)(nil)`, `_ ReviewStore         = (*PebbleStore)(nil)`.
3. Run `gofmt -w internal/database/iface_assert.go` to re-align the `=` columns across all 40 entries.
4. Bump the file's version header and last-edited date.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_database_032.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- (none)

## Tests

- No new test file needed — `go build ./internal/database/...` itself is the test: if PebbleStore ever drops a method from one of these 4 interfaces, the build fails here instead of surfacing as a runtime nil from a failed type assertion elsewhere.

Anti-over-suppression: N/A

## How to test

```bash
make ci
```
If `make ci` is too slow for iteration, first run `go build ./... && go vet ./<changed-pkg>/... && go test ./<changed-pkg>/... -count=1` (or `npm --prefix web test -- <file>` for web), then the full gate once before reporting done.

## Acceptance criteria

- [ ] go build ./internal/database/... exits 0.
- [ ] grep -c "_ .* = (\*PebbleStore)(nil)" internal/database/iface_assert.go returns 40.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `make ci` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_database_032.md`.

## Commit message

```
feat(database): Add the 4 missing compile-time assertions to iface_assert.go (TODO L4694)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If the first acceptance check below already passes at HEAD (`go build ./internal/database/... exits 0.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Pure hardening; catches a future silent regression at compile time instead of at a runtime type-assertion failure.
