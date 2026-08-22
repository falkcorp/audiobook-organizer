<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-066-wire-a-durable-freshness-stamp-for-maintenance-c.md -->
<!-- version: 1.0.0 -->
<!-- guid: bafad512-b7e3-4b39-876f-8a9b643b885d -->
<!-- last-edited: 2026-08-21 -->

# TASK-066 — Wire a durable freshness stamp for maintenance.chapters-backfill before it is ever scheduled (TODO.md L606)

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · maintenance subagent · **Why:** touches 3 files across 2 packages (plugin interface, server wiring, op logic) and must not break the ServerDeps compile-time assertion; needs care around where the *pebble.DB handle is available at server construction time · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 606 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "🔁 **Wire a durable \"probed, found none\" marker bef" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-02.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-066-wire-a-durable-freshness-stamp-for-maintenance-c" -b agent/maintenance-066-wire-a-durable-freshness-stamp-for-maintenance-c origin/main
cd "$REPO/.worktrees/maintenance-066-wire-a-durable-freshness-stamp-for-maintenance-c"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a FreshnessStamper accessor to maintenance.StoreProvider (returning freshness.OpFreshness), implement it on *server.Server backed by a freshness.NewPebbleFreshness constructed once from s.store's *pebble.DB handle, and call Stamp/ShouldProcess from runChaptersBackfill so a 'probed, found none' result is durably distinguishable from 'never examined' -- this is a PREREQUISITE for ever adding a Schedule to this op, not a change to the op's current manual-trigger behavior.

## Background (verify before editing)

- chapters_backfill.go:40-52 (header comment) states the bug precisely: SaveChaptersForBook deletes its key on an empty slice (pebble_store_chapters.go:63), so 'no markers found' and 'never examined' are byte-identical, and every manual run re-ffprobes ~half of single-file containers.
- TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes (chapters_backfill_test.go:258) pins the CURRENT (buggy-for-scheduling) behaviour and is expected to need updating once the stamp lands.
- StoreProvider's five existing accessors (deps.go: OpsStore, ReconcileStore, PlaylistStore, MetadataCacheStore, FileProvenanceStore) are each implemented on *server.Server in server_maintenance_deps.go by returning s.store, or (for FileProvenanceStore) via a database.AsCapability-style type assertion returning nil when unimplemented.
- database.PebbleStore.DB() (pebble_store.go:426) returns the raw *pebble.DB handle freshness.NewPebbleFreshness needs.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -rln "freshness.Stamp\|freshness.ClearStamps" --include=*.go . | grep -v _test   # 0 hits — freshness.Stamp/ClearStamps exist with zero non-test callers
  grep -n 'type StoreProvider interface' internal/plugins/maintenance/deps.go   # 1 hit — StoreProvider is the accessor-composition pattern to extend
  grep -n 'StoreProvider' internal/plugins/maintenance/deps.go   # >=2 hits — ServerDeps composes StoreProvider among 14 interfaces
  grep -n 'func (s \*Server) FileProvenanceStore' internal/server/server_maintenance_deps.go   # 1 hit ~L67 — server implements the accessor pattern via s.store-backed methods
  grep -n 'func (p \*PebbleStore) DB() \*pebble.DB' internal/database/pebble_store.go   # 1 hit L426 — PebbleStore exposes the raw *pebble.DB handle needed to construct NewPebbleFreshness
  grep -n 'func TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes' internal/plugins/maintenance/chapters_backfill_test.go   # 1 hit L258 — the regression test pinning current re-probe behaviour
  ```

### Reuse — don't invent

- Use `freshness.NewPebbleFreshness / OpFreshness interface` in `internal/operations/freshness/freshness.go` (verify: `grep -n 'func NewPebbleFreshness' internal/operations/freshness/freshness.go`) — do NOT write a parallel helper.
- Use `StoreProvider accessor pattern (OpsStore/ReconcileStore/PlaylistStore/MetadataCacheStore/FileProvenanceStore)` in `internal/plugins/maintenance/deps.go` (verify: `grep -n 'type StoreProvider interface' internal/plugins/maintenance/deps.go`) — do NOT write a parallel helper.
- Use `FileProvenanceStore nil-on-not-implemented accessor pattern to mirror` in `internal/server/server_maintenance_deps.go` (verify: `grep -n 'func (s \*Server) FileProvenanceStore' internal/server/server_maintenance_deps.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/database/tag_helpers.go-style narrow-interface fashion, add `type FreshnessStamper interface { Freshness() freshness.OpFreshness }` inside internal/plugins/maintenance/deps.go (import github.com/falkcorp/audiobook-organizer/internal/operations/freshness), grouped near StoreProvider.
2. Add `Freshness() freshness.OpFreshness` as a new method on StoreProvider (or embed the new interface into ServerDeps directly next to StoreProvider -- either is consistent with the existing 14-interface composition style at deps.go's ServerDeps block).
3. In internal/server/server_maintenance_deps.go, add `func (s *Server) Freshness() freshness.OpFreshness { return s.freshness }` where s.freshness is a new *freshness.PebbleFreshness field on *Server, constructed once during server init as `freshness.NewPebbleFreshness(pebbleStore.DB())` -- find the server struct/constructor (grep 'type Server struct' in internal/server/server.go) and the point where s.store is assigned a *database.PebbleStore, and construct s.freshness there, guarding for store implementations that are not *database.PebbleStore (nil freshness, same pattern as FileProvenanceStore).
4. In internal/plugins/maintenance/chapters_backfill.go's runChaptersBackfill, before the per-book RunItems closure, fetch `fresh := p.deps.Freshness()`. Inside the closure, when a probe finds < minPersistableChapters (no markers), call `if fresh != nil { _ = fresh.Stamp("chapters-backfill", id) }` after incrementing c.noChapters, instead of silently returning.
5. Add a ShouldProcess check near the top of the per-book closure: `if fresh != nil && !fresh.ShouldProcess("chapters-backfill", id, freshnessMaxAge, false) { c.skipHasStored.Add(1); return nil }` where freshnessMaxAge is a new const (e.g. 30*24*time.Hour) -- this only matters once/if Schedule is set; for a manual dry/apply run force=true should be threaded from params so an explicit re-run still re-probes everything (add `Force bool json:"force"` to chaptersBackfillParams).
6. Update TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes (chapters_backfill_test.go:258) to reflect the new behaviour: a fresh stamp now exists after a no-markers probe, and re-probing without force must SKIP via ShouldProcess -- add a companion test proving force=true still re-probes.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_066.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- s.store is not a *database.PebbleStore (e.g. a test double / memdb store): Freshness() must return nil, and callers must nil-check exactly like FileProvenanceStore does -- never panic.
- A book stamped 'no markers' later gains embedded markers via a file replace: the ShouldProcess maxAge window (not force) is what re-examines it eventually; document this trade-off in a comment the way the op's header already documents the re-probe cost.

## Tests

- internal/plugins/maintenance/chapters_backfill_test.go: update TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes to assert a freshness stamp is written for a no-markers book and NOT re-probed on a second run without force.
- New test TestChaptersBackfill_Force_ReprobesDespiteFreshStamp: seed a fresh stamp, run with params.Force=true, assert the book WAS re-probed (this is the anti-over-suppression test).

Anti-over-suppression test: `TestChaptersBackfill_Force_ReprobesDespiteFreshStamp -- proves a fresh stamp does not permanently suppress re-probing when the caller explicitly asks.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go build ./... succeeds with the new Freshness() method on ServerDeps and *Server.
- [ ] go test ./internal/plugins/maintenance/... -run TestChaptersBackfill passes.
- [ ] grep -n 'freshness.Stamp\|freshness.ShouldProcess' internal/plugins/maintenance/chapters_backfill.go returns >=2 hits after the change.
- [ ] Anti-over-suppression test: `TestChaptersBackfill_Force_ReprobesDespiteFreshStamp -- proves a fresh stamp does not permanently suppress re-probing when the caller explicitly asks.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_066.md`.

## Commit message

```
feat(maintenance): Wire a durable freshness stamp for maintenance.chapters-back (TODO L606)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `grep -n 'freshness.Stamp\|freshness.ShouldProcess' internal/plugins/maintenance/chapters_backfill.go returns >=2 hits after the change.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is explicitly a PREREQUISITE, not a request to add Schedule to the op -- chapters_backfill.go:49-52 says schedule-adding is a separate, larger decision ('If this op is ever given a Schedule, wire the freshness stamp FIRST'). Do not add sdk.Schedule in this change.
