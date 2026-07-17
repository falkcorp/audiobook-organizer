<!-- file: docs/agent-tasks/filtering-search/TASK-05-boilerplate-blocklist-config.md -->
<!-- version: 1.0.0 -->
<!-- guid: 923bf946-e14c-4457-a398-eed8c2b92dde -->
<!-- last-edited: 2026-07-10 -->

# TASK-05 — Move boilerplate title blocklist to a config-extendable module (INIT-4 T5)

**Gate:** PLAN -> EXECUTE AUTONOMOUSLY (worktree/PR/CI per task). T1/T2 are user-visible correctness fixes — ship first.
**File-ownership:** T5 moves the boilerplate blocklist OUT of `internal/dedup/engine.go` — engine.go is INIT-2-OWNED; schedule T5 after INIT-2's engine.go waves merge, rebased on top (same partition rule as INIT-1). ⛔ DO NOT START until the dispatcher confirms INIT-2's engine.go waves have merged; then branch from fresh `origin/main` so the move applies on top of INIT-2's edits.

**Priority:** P2 · **Effort:** M · **Recommended subagent:** Sonnet-class · refactor-move subagent · **Why:** mechanical move + config plumbing, but sits on an INIT-2-owned file and must keep 9 call sites and 3 existing test files green untouched · **Depends on:** EXTERNAL — INIT-2 engine.go waves merged

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/filtering-search-boilerplate-config" -b agent/filtering-search-boilerplate-config origin/main
cd "$REPO/.worktrees/filtering-search-boilerplate-config"
git rebase origin/main
```

## Goal

The dedup boilerplate title blocklist (publisher intro/outro "titles" like "this is audible"
that must not seed dedup matches) is two hardcoded English-only slices plus
`isBoilerplateTitle` inside `internal/dedup/engine.go`. Move them VERBATIM into a new
`internal/dedup/boilerplate.go` and make the lists extendable via config
(per-publisher / i18n-ready) with compiled-in defaults that are **byte-identical** when
config is empty. Same package, same symbol names — all existing call sites compile
unchanged. REUSE `util.NormalizeTitle` + `util.CollapseSpaces` for pattern normalization
(exactly as `isBoilerplateTitle` already does); do NOT rename the function or the slices,
do NOT alter any pattern string, do NOT touch any other part of engine.go.

## Background (verify before editing)

- The block to move: `var boilerplateTitlePatterns = []string{...}` (~21 exact patterns),
  `var boilerplateTitlePrefixPatterns = []string{...}` (~10 anchored prefixes), and
  `func isBoilerplateTitle(title string) bool` — contiguous in `internal/dedup/engine.go`
  (anchor grep below). The func normalizes with
  `util.NormalizeTitle(util.CollapseSpaces(title))`, checks exact equality against the
  first list, then `strings.HasPrefix(normalized, pattern+" ")` against the second.
- Call sites that must keep compiling UNCHANGED (same package): several in `engine.go`
  itself + `internal/dedup/drain_stale.go` (grep below shows all; count them and record the
  number in your PR description).
- Behavior is pinned by existing tests: `internal/dedup/engine_exact_guard_test.go`,
  `engine_acoustid_test.go`, `engine_acoustid_parallel_test.go` — these must pass
  UNMODIFIED.
- Config: `internal/config/config.go` holds section structs (e.g. `MetadataScoringConfig`,
  verify: `grep -n "type MetadataScoringConfig struct" internal/config/config.go`) wired
  into `type Config struct` with `mapstructure` tags and defaults set in the config-default
  blocks (find them: `grep -n "MetadataScoring:" internal/config/config.go`). Mirror that
  pattern; global access is `config.AppConfig`.
- INIT-2 owns structural engine.go edits — your diff to engine.go must be ONLY the removal
  of the moved block (no reformatting, no import cleanup beyond what `goimports` forces for
  now-unused imports).

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'var boilerplateTitlePatterns' internal/dedup/engine.go              # move-from block start, ~:40, 1 hit (0 hits = maybe already moved, see Idempotency)
  grep -n "func isBoilerplateTitle" internal/dedup/engine.go                   # move-from func, 1 hit
  grep -rn "isBoilerplateTitle" internal/dedup --include='*.go'                # ALL call sites (record the count), >=8 hits
  grep -n "NormalizeTitle\|CollapseSpaces" internal/dedup/engine.go            # normalization helpers to keep using, >=1 hit
  grep -n "type MetadataScoringConfig struct" internal/config/config.go        # config-section pattern to mirror, 1 hit
  ```
  Zero hits on a move-from grep AND presence in boilerplate.go = task already applied (see
  Idempotency). Zero hits on the config anchor = STOP and report.

## Step-by-step

1. Confirm the external gate: ask the dispatcher / check that INIT-2's engine.go PRs are
   merged (`git log --oneline origin/main -- internal/dedup/engine.go | head` shows their
   commits). If unconfirmed, STOP and report BLOCKED.
2. Create `internal/dedup/boilerplate.go` (4-line version header per repo standard). Move
   the two `var` slices and `isBoilerplateTitle` into it VERBATIM (identical pattern
   strings, identical comments).
3. In the new file, layer config extension WITHOUT changing the function's signature:
   ```go
   var boilerplateInit sync.Once
   var effectiveTitlePatterns []string   // defaults +/- config, normalized
   var effectivePrefixPatterns []string
   ```
   `isBoilerplateTitle` calls `boilerplateInit.Do(loadBoilerplatePatterns)` first.
   `loadBoilerplatePatterns` ALWAYS starts from the compiled-in slices and only APPENDS the
   config extras (extension-ONLY — spec Decision 8 explicitly rejects any replace escape
   hatch: it would silently drop ALL compiled-in Audible/publisher suppression and re-open
   the dedup-seeding bug the list exists to prevent), normalizing EVERY pattern with
   `util.NormalizeTitle(util.CollapseSpaces(p))` and dropping empties. Check first that
   `internal/dedup` may import `internal/config` without a cycle:
   `grep -rn "audiobook-organizer/internal/config" internal/dedup/*.go | head -3` — if it is
   not already imported anywhere in the package, verify `internal/config` does not import
   `internal/dedup` (`grep -rn "internal/dedup" internal/config/*.go`); if a cycle exists,
   inject the extras via an exported `SetBoilerplateExtras(titles, prefixes []string)`
   called from server startup instead (extras only — no replace parameter), and say so in
   the PR.
4. In `internal/dedup/engine.go`: delete ONLY the moved block. Nothing else.
5. In `internal/config/config.go`: add `DedupBoilerplateConfig` (fields
   `ExtraTitlePatterns []string` and `ExtraPrefixPatterns []string` ONLY — the spec's
   locked Data-model struct has NO `ReplaceDefaults`/replace field (Decision 8); do not add
   one — json+mapstructure tags per the spec,
   `docs/specs/2026-07-10-filtering-search-design.md` §Data model) and wire it into the
   config struct + default blocks following the MetadataScoring pattern (empty defaults).
6. Edge semantics (in tests too): empty config → behavior byte-identical to today;
   built-in patterns are ALWAYS retained — no config value can remove or replace them
   (extension-only, Decision 8); config patterns are matched post-normalization exactly
   like defaults; empty/whitespace-only config entries are dropped, never match-everything.
7. Tests — `internal/dedup/boilerplate_test.go` (NEW):
   - every default pattern still flagged (loop over both compiled-in slices);
   - config extra pattern gets flagged after a test-scoped extras injection;
   - anti-over-suppression: real titles "Introduction to Algorithms" (prefix-adjacent) and
     "The End Credits of a Life" survive (NOT flagged) with defaults AND with an extras
     list active;
   - `TestBoilerplateConfigExtension` (spec Testing table): with extras active, built-ins
     are ALWAYS retained — assert a compiled-in pattern still hits alongside the extras.
   Existing `engine_exact_guard_test.go` / `engine_acoustid_test.go` /
   `engine_acoustid_parallel_test.go` pass UNMODIFIED.
8. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
make ci
go test ./internal/dedup/... -race -count=1   # the moved guard sits on -race-tested scan paths
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "var boilerplateTitlePatterns" internal/dedup/boilerplate.go` hits AND `grep -n "var boilerplateTitlePatterns" internal/dedup/engine.go` returns 0 hits
- [ ] `grep -c "isBoilerplateTitle" internal/dedup/engine.go` shows only call sites remain (no `func isBoilerplateTitle` hit in engine.go)
- [ ] All pre-existing dedup boilerplate tests pass without modification
- [ ] Anti-over-suppression: "Introduction to Algorithms" test proves real titles survive with config extras active
- [ ] `grep -n "DedupBoilerplateConfig" internal/config/config.go` hits (struct + wiring)
- [ ] engine.go diff contains ONLY the block removal (`git diff origin/main -- internal/dedup/engine.go` reviewed)
- [ ] Tests green; vet/lint clean (`make ci`)
- [ ] File headers bumped on every changed file

## Commit message

```
refactor(dedup): move boilerplate title blocklist to config-extendable module (INIT-4 T5)

The publisher intro/outro blocklist was two hardcoded English-only
slices inside engine.go. Move them verbatim to boilerplate.go and layer
extension-only config (extra patterns APPEND to the compiled-in
defaults, which are always active) with behavior byte-identical when
config is empty — per-publisher and i18n lists no longer require a code
change.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/filtering-search-boilerplate-config
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "isBoilerplateTitle" internal/dedup/boilerplate.go` hits AND
`grep -n "var boilerplateTitlePatterns" internal/dedup/engine.go` returns 0 hits, the move is
already done (transform polarity: symbol at new location + absence at old) — run acceptance
instead of re-applying. Rollback = revert the commit; the blocklist returns to engine.go with
identical behavior (defaults were byte-identical), and no data, candidate rows, or schema are
touched.
