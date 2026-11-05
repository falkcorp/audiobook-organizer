<!-- file: docs/agent-tasks/platform-core/F2-09-core-config-section-declarations.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7243a8e0-4333-4d9f-afec-3c56f5b47d24 -->
<!-- last-edited: 2026-11-05 -->

# F2-09 — Every core config section declared with its owner's key table, on the kernel's core-section list, dark

| Field | Value |
|---|---|
| ID | F2-09 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W22 |
| Tier | **T2** (Sonnet / high): declarations transcribed from the owners' key tables, plus the small declaration surface of `core/config`; no loader, no store |
| Depends on | OWNER-03 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-09-core-sections
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md).

## Goal

1. Create `github.com/falkcorp/mediacore/config` (`core/config/`) with **only** the declaration surface of 05-config
   §1.3.1: `Path`, `Reload`, `Protection`, `Duration`, `Validator`, `FieldError`, `Section[T]`, `Define`,
   `SectionOption` and the options `Permissions`, `Docs`, `ProtectionWaivers`, `EnvBinding`, `FailClosed`, with the
   struct-tag vocabulary (`json`, `reload`, `protect`, `schema`, `sensitive`). The loader, `Store`, `Register`,
   `KeyState`, locks, `OverrideStore`, `Importer` and `configtest` are F3-01's (04 §8.1 F2: "`config` holds only the
   section declarations until F3").
2. Put **every core section** on the kernel's core-section list in `core/kernel/core_sections.go`, each with the key table
   its owner spec states, whether or not that subsystem is wired (05-config §1.12.1 "Every core section exists from F2",
   normative, IMPL-1), so F3's M2 stages every renamed key into a registered path and `TestRenameTable_EveryKeyRegistered`
   passes from F3.

Registering a section changes no behaviour: nothing reads these keys before their owner adopts them (05-config §1.12.1).

## Spec to read first

05-config §1.3.1 (copy the Go block and the tag table), §1.3.5 (precedence; `FailClosed`), §1.3.6 (key grammar
`[a-z][a-z0-9_]{0,47}`, map keys are data), §1.3.7 (protection classes; the shape rule), §1.7, §1.12.1, §1.12.3 (the
rename table's targets: every one must be a declared path), §2.7; and each owner's key table listed below.

## Base state: re-verify before editing

```bash
test ! -e core/config && echo absent                              # "absent", else see Idempotency
test -f core/kernel/core_sections.go && echo exists || echo absent # "absent"
grep -rn 'jsonschemax' core/internal/ | head -2                   # >=1 (R1-12's reflector; not used by this PR, see Out of scope)
grep -rn 'type Ref struct' core/secrets/ 2>/dev/null | wc -l       # 0 expected (secrets land at F3-03): see "Secret-typed keys"
```

The owners' key tables, with their row counts at the plan commit (`f7ac5b11e`). Re-count at your base with the
command after the table; a different count means the spec moved: transcribe what your base says.

| Owner spec, section | Top-level sections (rows) |
|---|---|
| 05-ai §7 | `core.ai` (11) |
| 05-authn §8 | `core.auth` (26) |
| 05-backup §5B.7 | `core.backup` (16) |
| 05-config §1.7 and §2.7 | `core.paths` (1), `core.config` (2), `core.secrets` (2) |
| 05-download §7 | `core.download` (6; skip the 3 rows marked "(phase 2)"), `core.library.mappings` (also in 05-library: declare once) |
| 05-events §5E.9 | `core.events` (2), `core.outbox` (9) |
| 05-health §5H.7 | `core.health` (3), `core.shutdown` (2), `core.diagnostics` (6) |
| 05-http-api §10 | `core.http` (16) |
| 05-library §5L.7 | `core.library` (12) |
| 05-logging §8 | `core.log` (15) |
| 05-metadata M1.7, M2.7 | `core.integrations` (7), `core.providerhttp` (7) |
| 05-metrics §10 | `core.metrics` (9), `core.tracing` (3) |
| 05-notify §7 | `core.notify` (3) |
| 05-rbac §10 | `core.rbac` (1), `core.audit` (6) |
| 05-realtime §8 | `core.realtime` (8) |
| 05-storage §5S.7 | `core.storage` (17), `core.index` (2) |
| 05-tasks §5T.10 | `core.tasks` (17; the `core.<subsystem>.schedules.<name>` row is a rule, not a key) |
| 05-updater §5U.8 | `core.updater` (4) |
| 06 §6.8 | `core.modules` (1), `core.lifecycle` (2), `core.shutdown` (1, the same `core.shutdown.budget` 05-health tables: declare once) |
| 07 §7.15 | `core.ui` (4) |

```bash
# re-count one table, e.g. 05-tasks §5T.10 (repeat per row above with its heading)
awk '/^## 5T.10 Config keys/{f=1;next} /^#{2,3} /{f=0} f' docs/specs/platform-core/05-tasks.md | grep -c '^| `core\.'   # 18 at f7ac5b11e (17 keys + 1 rule row)
```

Not declared: 05-indexer (the whole file is a non-normative design sketch, deferred-build), the "(phase 2)" rows of
05-download, any `modules.<id>.*` key (modules declare their own).

## Files

Create `core/config/` (`path.go`, `section.go`, `options.go`, `duration.go`, `errors.go`, `doc.go`, tests) and
`core/kernel/core_sections.go` (+ `core/kernel/core_sections_test.go`). One call from the kernel's boot sequence that
builds the list (one line in R1's kernel boot file; say so in the PR body). Nothing else.

## Code contract

- Copy 05-config §1.3.1's declarations exactly (names, constants, ordering of `Protection`, `Reload` values). Leave out
  `Schema`, `SchemaFor` and `Overrides` (they need `secrets.Ref` and the reflector wiring; F3-01).
- `Define[T](path, defaults, opts...)` never fails; it records path, defaults and options. Add
  `func (s Section[T]) Path() Path` and an unexported spec accessor so the kernel and F3-01's `Register` can read it
  (name `Path()` coined here).
- `Path.Validate`: first segment `core` or `modules`, each segment `[a-z][a-z0-9_]{0,47}`.
- `Duration` (un)marshals as a Go duration string (`"90s"`, `"24h"`).
- `FailClosed(p, order...)` records; validation (bool/enum, every value in order, not secret) is `Register`'s at F3-01,
  but add `TestFailClosed_DeclaredOnTwoKeysOnly` here (below).
- `core/kernel/core_sections.go`: one Go struct type per section (field names free; JSON tags are the key segments),
  defaults from the owner table's Default column, `reload:"restart"` / `reload:"boot"` from its Hot-reload column
  ("no" = `restart`; "no — pre-boot" or "boot record" = `boot`), `protect:"…"` from its Protection column (`none`/`—`
  = no tag; `admin`, `interactive`, `owner`, `operator`), and `func coreSections() []any` (name coined here) returning
  every `config.Define(...)` value. Map-typed keys (`core.tasks.kinds`, `core.tasks.windows`) are one map field each
  (05-config §1.3.6). `core.auth.mode` and `core.http.basic_gate.enabled` carry `config.FailClosed` with the orders
  §1.3.1 gives (`["\"required\"","\"disabled\""]`, `["true","false"]`), and no other key does.
- Schedule keys (`core.<subsystem>.schedules.<name>`, type `tasks.ScheduleConfig`) are declared by their owner's
  section here. `tasks.ScheduleConfig` lands with F2-01 in this same wave: if `grep -rn 'type ScheduleConfig struct'
  core/tasks/` has 0 hits at your base, declare those fields with a section-local struct of the same JSON shape
  (`enabled`, `every`, `daily_at`, `cron`, `window`, `order`) and the comment "replace with tasks.ScheduleConfig" (see
  planner note). Record which in the PR body.
- **Secret-typed keys.** Two rows are `secrets.Ref`: `core.auth.oidc.<id>.client_secret` (05-authn §8) and
  `core.http.basic_gate.password` (05-http-api §10). `core/secrets` lands at F3-03. If `type Ref` does not exist at
  your base, **leave these two fields out**, list them in a `// secret-typed keys added with core/secrets (F3)` comment
  above the section, and say so in the PR body. Do not invent a placeholder secret type.
- Paths that are restart-bound or pre-boot keep the owner's rule: `core.paths.state_dir` is `protect:"operator"` and
  pre-boot; `core.backup.dir` is restart-bound, `ProtectAdmin` (05-backup §5B.7); `core.modules.<id>.enabled` is
  `ReloadBoot` (06 §6.8).

## TDD: tests to write first (names coined here; mark them so in the test doc comments)

- `TestCoreSections_EveryOwnerKeyDeclared`: the test holds the full list of declared key paths (transcribed from the
  tables above, the two secret keys and phase-2 rows excepted, duplicates once) and asserts the set of leaf paths
  reflected from `coreSections()` equals it exactly.
- `TestCoreSections_PathsValid`: every section path and every leaf path passes `Path.Validate`, map keys excepted.
- `TestCoreSections_NoOverlap`: no two sections declare the same path (a sub-section only at a segment its parent does
  not declare, §1.3.1).
- `TestFailClosed_DeclaredOnTwoKeysOnly`: exactly `core.auth.mode` and `core.http.basic_gate.enabled` carry `FailClosed`.
- `TestDuration_JSONRoundTrip`, `TestPath_Validate` (in `core/config`).

## Steps

1. Base-state greps and the per-table re-counts; plan in the PR body with the final key count.
2. Tests first.
3. `core/config` declaration surface.
4. Transcribe each owner table into its section struct, one commit per owner table if that helps review.
5. Headers; changelog fragment; acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./config/... ./kernel/...                                # exit 0
go test ./config/... ./kernel/... -race -count=1                                  # ok
go test ./kernel/ -run 'TestCoreSections_EveryOwnerKeyDeclared|TestCoreSections_PathsValid|TestCoreSections_NoOverlap|TestFailClosed_DeclaredOnTwoKeysOnly' -count=1 -v | grep -c '^--- PASS'   # 4
grep -rn 'func Load\|func (s \*Store) Apply\|OverrideStore' config/ | wc -l        # 0 (no loader or store in this PR)
cd ..
make -C core ci                                                                   # exit 0
make ci                                                                           # exit 0
git grep -n 'mediacore/config"' -- ':!core/' | wc -l                              # 0 (ABO does not import core config yet)
```

## Out of scope

The loader, `Store.Apply`, locks, `KeyState`, `OverrideStore`, `Importer`, `configtest`, `Schema`/`SchemaFor`/
`Overrides` (F3-01); env grammar and retired names (F3-02); secrets (F3-03); the rename table (F3-06); any reader of
these keys; any ABO edit.

## Idempotency / rollback

If `core/kernel/core_sections.go` exists, run the acceptance instead. Rollback = revert: nothing reads the sections.

## Repo rules

File headers; conventional commit (`feat(core/config): declare every core config section (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment.

## Planner notes

- Two core keys are `secrets.Ref` (`core.auth.oidc.<id>.client_secret`, `core.http.basic_gate.password`), but
  `core/secrets` lands at F3-03, which depends (through F3-01) on this row. This row therefore cannot declare them; no
  later row lists `core/kernel/core_sections.go` in its files. Suggest F3-03 adds the two fields (add the file to F3-03's
  row), before F3-06's M2 stages them.
- Schedule keys are typed `tasks.ScheduleConfig`, which F2-01 lands in the same wave with no dependency edge; the brief
  uses a same-shape local struct when it is absent. Either add F2-01 as a dependency (W22 → W23) or have F2-04 replace
  the local struct.
- `Schema`, `SchemaFor` and `Overrides` are in 05-config §1.3.1's declaration block but need `secrets.Ref`; this brief
  leaves them to F3-01.
- Shared kernel boot file: see F2-07's planner note.
