<!-- file: docs/agent-tasks/platform-core/PC-21-04-h3-auth-precedence-cutover.md -->
<!-- version: 1.0.0 -->
<!-- guid: 930594ec-ced6-4613-8c27-70379257bab2 -->
<!-- last-edited: 2026-11-04 -->

# PC-21-04 — H3 auth-precedence cutover: an enabling operator value wins over a stored false; HEADAuthOffWritable cleared on enable_auth; flip

> **SPEC-GATED.** H3's rule is decided (05-config §1.12.5; 05-authn §11.2; 09 §9.13), but it has no stage table, and it
> "ships with or after the settings family's cutover", which is deferred-build with no design. Step 0 checks for both;
> if either is missing, STOP and report.

| Field | Value |
|---|---|
| ID | `PC-21-04` |
| Tier | T3 Opus/high — one precedence rule, but one-way: no rollback can turn auth off again |
| Wave | W21 — release `H3` (or `H2H3`) |
| Depends on | `OWNER-32` |
| Priority / effort | P1 / L |
| Spec | 09 §9.13 (H3 row, release-note text); 05-config §1.12.5 (H3), C3, §1.3.1 (`HEADAuthOffWritable`, `BelowStored`), §1.3.5, §1.3.7; 05-authn §11.2 (H3 row), §4.11 |
| Digest | `docs/plans/platform-core/digests/H1-H3.md` §1, §2, §5 rules 1–3 and 12, §6, §7 |
| `Stage-Release:` | `H3 flip` (or `H2H3 flip`) on the flip PR; `H3 1` (or `H2H3 1`) on content PRs (09 §9.2.1) |

## Step 0 — the spec text this task needs (STOP if missing)

```bash
S=docs/specs/platform-core
awk '/^## 9.13/,/^## Objections/' $S/09-abo-on-core.md | grep -c -E '^#{3,4} +(H3|H2H3)\b'    # HEAD 9b1545c1a: 0 -> STOP
awk '/^## 9.13/,/^## Objections/' $S/09-abo-on-core.md | grep -c '^| Flip PR'                 # HEAD: 0 -> STOP
# The settings-family cutover (setting:config_blob -> c/config/) has a design and a stage, or the owner has dropped H3's dependency on it:
grep -c 'H3 ships with or after the settings' $S/09-abo-on-core.md $S/05-config.md            # HEAD: 1 in each -> the dependency stands
grep -c 'Per-family cutover of the ABO families' $S/README.md                               # HEAD: 1 (the README "Build scope" deferred-build row)
```

**Missing spec text, exactly:**

1. **No H3 stage table** (Flip PR row, PR series, full done-check list; 09 §9.13).
2. **The settings-family cutover has no design.** 09 §9.13's first paragraph and the spec README's "Build scope" list
   the move of the settings blob to `c/config/` as a deferred per-family cutover; 05-config §1.12.2 keeps books' legacy
   config store with its "cutover deferred". 05-config §1.12.5 makes H3 ship "with or after the settings cutover, so no
   older build has to be kept in step by a write into a legacy field"; 09 §9.13 copies that dependency. Until that cutover
   is specified, or the owner drops the dependency in H3's stage table, H3 cannot be planned.
3. **How H3 raises `books`.** H3 changes how books' stored `enable_auth` is read and rewrites no row; the spec does not say
   which step raises the `books` level (a level step needs a registered migration, 05-storage §5S.3.7). The stage table
   must say.

## What the spec already decides (binding)

- **Only `enable_auth` moves, only in the enabling direction** (05-config §1.12.5; 05-authn §11.2; 09 §9.13): an operator
  value that enables auth (`ENABLE_AUTH=true`, `enable_auth: true` in the operator file) **beats a stored `false`**; a
  value that disables auth keeps HEAD's precedence (a stored `true` beats it, so a stale `ENABLE_AUTH=false` still loses).
- `Alias.HEADAuthOffWritable` is **cleared on `enable_auth` only** (R9-5): from H3 the auth-disabled principal can no
  longer store `core.auth.mode` through `Apply`; auth is turned on by an operator value or by an admin once auth is on
  (SEC4-8, now applied to ABO's auth switch).
- The Basic gate keys (`core.http.basic_gate.{enabled, username, password}`, HEAD `basic_auth_*`) keep the flag and HEAD's
  auth-off write rule until the **earlier of H2 and the settings cutover**, which precedes or ships with H3, the three
  keys together, with one release note (05-config §1.3.1, §1.12.5).
- **Every other `BelowStored` flag keeps HEAD's precedence** after H3, until a separate owner-decided cutover per key; a
  name a shipped deploy file sets unconditionally (compose's `BASIC_AUTH_ENABLED`, `BASIC_AUTH_USERNAME`,
  `BASIC_AUTH_PASSWORD`; `docker-compose.yml:20-22` at HEAD, as at 748fd787a) stays `BelowStored` **permanently**, and a
  legacy value that weakens a security control (`basic_auth_enabled=false`, `enable_rate_limit=false`) never locks.
- The canonical `ABO_CORE_AUTH_MODE=required` keeps locking the key (05-config C3) and is unaffected.
- **Owners raised:** `books` only; H3 lifts nothing (no `{core, 0}` bound, no backup encryption) (05-config §1.12.5).
  `soak_record.py`'s table already says so (PC-21-01). 09 §9.13 asks for a fresh verified archive before H1 and H2 only.
- **Floor:** the H3 release; every older build refuses the store, so no rollback can turn auth off
  (`TestHardeningCutoverRefusedByEveryOlderRelease`).

**Done-checks (09 §9.13 H3 row and 05-config §1.12.5; names fixed where the spec names them):**

- auth enabled through the environment stays on across restarts (09 §9.13; name it in the stage table);
- `TestHardeningCutoverRefusedByEveryOlderRelease` with the vector `{books}`;
- `TestComposeEnvKeepsUIBasicGate` stays green (compose's `BASIC_AUTH_ENABLED=false` never turns off a UI-enabled gate);
- `TestLegacyAuthEnablePrecedenceMatchesHEAD`'s **disabling** half stays green (blob `true`, env `ENABLE_AUTH=false` →
  auth stays on); its enabling half (blob `false`, env `ENABLE_AUTH=true` → off at HEAD) is the half H3 changes;
- `TestAuthOff_NativeAppRefusesAuthModeWrite` keeps passing (native apps never had the flag).

**Release note (09 §9.13, verbatim):** "With authentication off, Settings can no longer turn it on; set `ENABLE_AUTH=true`
or `ABO_CORE_AUTH_MODE=required` and restart."

## Files (inventory)

| Action | Path |
|---|---|
| create | `internal/config/h3_precedence_test.go` |
| modify | `internal/config/aliases_gen.go` (through `tools/cmd/genaliases/`), `internal/config/facade.go`, `core/config/`, `ci/stage.txt` |

The stage table may add the settings-cutover files; re-derive then.

## START HERE (only after step 0 passes)

```bash
REPO=<your clone>
TASK=pc-21-04-h3-auth-precedence
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list
npm ci --prefix web
git rev-parse --short HEAD
```

## Steps (after the stage table exists)

1. Re-run step 0; follow H3's stage table (it wins; note differences in the PR).
2. Re-verify at the base: `grep -rn 'HEADAuthOffWritable' internal/config/aliases_gen.go | grep -c enable_auth` (expected
   ≥ 1 before the change, 0 after); `grep -rn 'func TestComposeEnvKeepsUIBasicGate\|func TestLegacyAuthEnablePrecedenceMatchesHEAD' --include='*_test.go' . | wc -l`
   (expected 2).
3. Tests first: split `TestLegacyAuthEnablePrecedenceMatchesHEAD`'s enabling half into the H3 expectation (env `true` beats
   stored `false`) and keep the disabling half; add the restart-persistence test in `internal/config/h3_precedence_test.go`.
4. Implement the precedence change for `enable_auth` only, clear its `HEADAuthOffWritable` through the alias generator,
   add the harness vector, and set `ci/stage.txt` (`shipped_stage:` and `release:` to `H3` or `H2H3`, flip PR only; no
   `levels:` line).
5. Acceptance: every done-check green; `make ci` and `make -C core ci` exit 0; `compat-rehearsal` and `unit-lifecycle`
   green on the flip PR.

## Out of scope

- Any other `BelowStored` flag, the Basic gate keys' precedence, compose's names (permanent).
- The settings-family cutover itself (unless H3's stage table assigns it here).

## Rollback

One-way. Before a production tag contains the flip: a revert PR. After: no binary swap below the H3 release.

## Repo rules

- File headers; one `changelog.d/` fragment; conventional commits with `Stage-Release: H3 flip` / `H3 1` (or the `H2H3`
  forms), ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`, never squash, never
  `--admin` past a required check. Generated alias code is regenerated, never hand-edited.
- Concurrency (CLAUDE.md): no loop over library-scale data.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.

## Planner notes

- **SPEC-GATED:** no H3 stage table; H3 depends on the settings-family cutover, which is deferred-build with no design;
  and the spec does not say which registered step raises `books` for a change that rewrites no row.
- **Depends-on:** no inventory row owns writing H3's stage table or designing the settings cutover; `PC-21-04` should
  depend on such owner rows.
