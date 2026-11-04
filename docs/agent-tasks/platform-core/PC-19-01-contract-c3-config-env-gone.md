<!-- file: docs/agent-tasks/platform-core/PC-19-01-contract-c3-config-env-gone.md -->
<!-- version: 1.0.0 -->
<!-- guid: 841c821e-d904-4e1f-9a8e-5a5239cc72ac -->
<!-- last-edited: 2026-11-04 -->

# PC-19-01 — Contract C3 (Gone step): config and env aliases: legacy YAML keys refused at boot for one minor; retired env names; secret-class aliases refuse boot

| Field | Value |
|---|---|
| ID | `PC-19-01` |
| Tier | T1 Sonnet/medium — retires surfaces by changing ledger states that the config code already reads; no new mechanism (one STOP gate below covers the one spec conflict) |
| Wave | W19 — contracts C3–C9, the Gone step (no release ID of its own) |
| Depends on | `OWNER-23` (the W owner check for C3) |
| Priority / effort | P2 / S |
| Spec | 09 §9.5 C3; 09 §9.2.1 "Contract" step and W (a)–(c); 09 §9.6 rows 13 and 15; 09 §9.2.5; 05-config §1.3.6 (alias lifetime table), §1.12.4, §1.11 (`TestAlias_RetiredProtectedFailsBoot`) |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19 and §8 (contract checkpoints) |
| `Stage-Release:` | `none` (plan §(b) decision 4: contract PRs touch only packages of flipped stages and 09 names no stage for them) |

## Gate (read before anything else)

- **Do not start without the owner's W readings.** `OWNER-23` (NOT AGENT WORK) hands the coordinator the counter readings of `audiobook_organizer_compat_legacy_use_total` for this contract's surfaces, each with its process start time, covering 14 consecutive days with no use, plus the dates for W (a) and the release-note check for W (b) (09 §9.2.1 W, §9.7 P8). If the coordinator has not given you that text to paste into the PR description, STOP and ask. You never take the readings yourself.
- **The date gate is a test.** `TestNoContractBeforeSunset` fails a build whose code removes or retires an entry before its ledger row's earliest removal date (09 §9.2.9). If it fails after your change, the window has not run: STOP, revert your working tree, report.
- **One contract at a time** (plan §(d) "W19 (contracts)"): the coordinator dispatches this PR only when no open PR touches one of its files. Files this task shares: `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` and `internal/config/aliases_gen.go` (with PC-11-01, PC-11-03, PC-12-06, PC-13-14, PC-16-06, PC-17-04 and PC-19-02 … PC-19-07).
- **No stored data changes.** A contract retires surfaces only; it never deletes, rewrites or re-levels stored data (09 §9.5).

## Goal

After window W for the S3 config and env aliases, move every **window** ledger row of surface `config` and `env`
in the ledger's S3 section from state `aliased` to state `retired`, so that config's existing lifetime input
(`config.Options.Retired`) treats each as retired: a retired legacy name of a `ProtectAdmin`-or-higher key or of a
secret key **fails boot** with `*RetiredAliasError` naming the canonical variable, and every other retired name is
handled as the spec's Gone step requires (see the STOP gate in step 0 for legacy YAML keys). The blob's field names
are untouched: they are the stored format (09 §9.5 C3).

## Background (the rules, cited)

- 09 §9.5 C3: earliest R(S3) + W; action "YAML legacy keys refused at boot for one minor, then their alias rows
  deleted; retired env names per §9.2.5. The blob's field names are untouched (stored format)".
- 09 §9.2.5 last bullets: after W, a window alias of a key at `ProtectAdmin` or higher, or of a **secret-class** key,
  is retired: boot refuses to start if it is set and names the canonical variable; storage refuses to *create* a
  store at the resolved path when none exists there and a retired database-path name is set. Other legacy env names
  after W are ignored with one boot WARN. 09 §9.2.1 (after W): every window alias of a secret-class key is treated like
  a `ProtectAdmin` key.
- 09 §9.6 row 13 (config keys in YAML): "refuse-to-start naming canonical (one minor) → alias deleted (C3, C7)".
  Row 15 (env window names): "not read; `ProtectAdmin`+ and secret-class keys refuse to start".
- 05-config §1.3.6 table: a row in `Retired` state is not read; key `ProtectAdmin` or higher (incl.
  `ProtectOperator`) **or a secret key**: boot fails with `*RetiredAliasError`; other keys: one warning per boot.
  Retired rows of protected and secret keys are kept until the next major; others are deleted one minor after the
  contract. 05-config §1.12.4 last bullet: "After contract, window rows of keys with `ProtectAdmin` or higher, and of
  secret keys, move to state `Retired`".
- Config holds no lifetime itself: an `Alias` carries only `Ledger string`; the row's gate, dates and `Retired`
  state live in the ledger (05-config §1.3.6, 09 §9.2.9). So the contract is, mechanically, a ledger edit plus the
  tests that prove the code reacts to it.
- **Spec conflict (unresolved at the time of writing):** for a legacy **YAML** key of a key that is neither
  `ProtectAdmin`+ nor secret, 09 (§9.5 C3, §9.6 row 13) says boot refuses; 05-config §1.3.6 says one warning per
  boot. Step 0 checks whether the spec has been reconciled.

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>                       # never write a real machine path into a commit or PR
TASK=pc-19-01-contract-c3
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list                       # expected: this path is listed and is NOT the primary checkout
npm ci --prefix web                     # or `make web-deps` once PC-01-06 has merged
git rev-parse --short HEAD              # record it: the BASE commit every count below is measured at
```

Do not run `go work init` in the worktree (CLAUDE.md: it breaks `go build ./...`). Never edit on `main`.

## Files

| Action | Path | Why |
|---|---|---|
| modify | `docs/compat-surfaces.md` | the S3-section `config`/`env` window rows: `aliased` → `retired` |
| modify (generated) | `internal/books/compat-surfaces.md` | `make compat-ledger` output; never hand-edited |
| modify (test) | the test file that holds `TestAlias_RetiredProtectedFailsBoot` (find it in R-C3.3) | add the C3 cases of "Tests" |
| inventory lists, **no diff expected** | `internal/config/aliases_gen.go`, `internal/config/facade.go` | the alias table carries only `Ledger`; if `go build ./...` and the tests pass without touching them, do not touch them |
| create | `changelog.d/<date>_contract_c3.md` | fragment, no header |

## Re-verify at your base commit

Paste this helper once; later steps use it.

```bash
# ledger_rows SECTION SURFACES  -- prints every ledger row of docs/compat-surfaces.md whose section heading
# matches SECTION (a regex) and whose surface cell is in SURFACES (comma list). Output: "<state> | <whole row>".
ledger_rows() {
python3 - "$1" "$2" <<'PY'
import re, sys
sec_re, surfaces = sys.argv[1], set(sys.argv[2].split(','))
cur, hdr = '', None
for ln in open('docs/compat-surfaces.md', encoding='utf-8'):
    if ln.startswith('#'):
        cur, hdr = ln.strip(), None
        continue
    if not ln.startswith('|'):
        hdr = None if not ln.strip() else hdr
        continue
    cells = [c.strip().strip('`') for c in ln.strip().strip('|').split('|')]
    if hdr is None:
        hdr = [c.lower() for c in cells]
        continue
    if all(re.fullmatch(r':?-+:?', c or '-') for c in cells):
        continue
    if 'surface' not in hdr or 'state' not in hdr:
        continue
    row = dict(zip(hdr, cells))
    if re.search(sec_re, cur) and row.get('surface') in surfaces:
        print(row.get('state'), '|', ' | '.join(cells))
PY
}
```

Common anchors (every contract):

```bash
# R1. The ledger source and its embedded copy (PC-07-06). At HEAD 9b1545c1a the source exists in its pre-ledger
#     v1.1.0 form and the copy is absent.
test -f docs/compat-surfaces.md && test -f internal/books/compat-surfaces.md && echo OK      # expected: OK
# R2. The compat package (PC-07-06). At HEAD core/ does not exist.
ls core/compat/*.go | wc -l                                                                 # expected: >= 1
# R3. The four ledger tests, each defined exactly once (09 §9.2.9, §9.10).
for t in TestCompatLedgerMatchesCode TestNoContractBeforeSunset TestCompatLedgerCopyFresh TestCompatLedgerHoldsOnlyRows; do
  echo "$t $(grep -rn "func $t(" --include='*_test.go' . | wc -l)"; done                    # expected: every line ends in " 1"
# R4. The ledger's state vocabulary (09 §9.2.9: aliased | gone | retired | removed).
grep -c -E 'aliased|retired' core/compat/*.go | awk -F: '{s+=$2} END {print s}'               # expected: >= 2
# R5. The regeneration target for the embedded copy.
grep -c '^compat-ledger:' Makefile                                                          # expected: 1
# R6. The ledger header row names the columns of 09 §9.2.9 (surface, legacy, canonical, flip PR, shipped release
#     and date, earliest removal date, gate, state, removal PR) plus the row ID. Read it once:
grep -n -m3 '^| ' docs/compat-surfaces.md
```
Any expected value that does not hold at your base commit: **STOP and report** (the predecessor task has not landed
or has drifted).

Anchors for this contract:

```bash
# R-C3.1 config's lifetime input exists (05-config §1.3.3 Options.Retired; PC-09-03/PC-09-06).
grep -rn 'Retired *func(ledger string) bool' core/config/ | wc -l                           # expected: 1
grep -rn 'ErrRetiredAlias\|type RetiredAliasError struct' core/config/ | wc -l              # expected: >= 2
# R-C3.2 the generated alias table and the Facade (PC-09-03, PC-09-06). Absent at HEAD.
test -f internal/config/aliases_gen.go && test -f internal/config/facade.go && echo OK      # expected: OK
# R-C3.3 the named retired-alias test (05-config §1.11).
grep -rln 'func TestAlias_RetiredProtectedFailsBoot(' --include='*_test.go' . | wc -l       # expected: 1
# R-C3.4 the rows this contract owns, by state. Section regex: the ledger's S3 section heading.
ledger_rows '\bS3\b' config,env | grep '| window |' | awk '{print $1}' | sort | uniq -c
#   expected: one line "<N> aliased"; record N as N_C3 (N_C3 > 0). Permanent-gated rows (DATABASE_PATH, OTEL_*, …) are
#   excluded by the grep and are never touched.
#   The S3 alias table is generated from HEAD's lists (05-config §1.12.4: 142 non-test BindEnv names at 748fd787a,
#   the AutomaticEnv upper-case forms, the ABK_* names, ~251 flat YAML keys); counts are generated, not fixed here.
# R-C3.5 the one secret example the spec names is a window env alias at HEAD (05-config §1.3.6).
grep -n 'BindEnv("abs_jwt_secret", "ABS_JWT_SECRET")' internal/config/abs_config.go | wc -l  # expected at HEAD: 1
# R-C3.6 the spec conflict of step 0 (run in the worktree, which carries the spec).
grep -c 'other keys: one warning per boot' docs/specs/platform-core/05-config.md              # HEAD: 1
grep -c 'YAML legacy keys refused at boot for one minor' docs/specs/platform-core/09-abo-on-core.md  # HEAD: 1
```

## Steps

0. **STOP gate (spec conflict).** Run R-C3.6. If **both** counts are still `1` and neither file has gained a sentence
   that reconciles legacy YAML keys of non-protected, non-secret keys (search both files for `YAML` near `Retired`),
   STOP and report: "C3 YAML behaviour conflicts: 09 §9.5/§9.6 row 13 refuse at boot, 05-config §1.3.6 warns; owner
   to decide". Do not pick one. If the spec now agrees, follow the agreed text and note which revision you followed in
   the PR description.
1. Run every re-verify command; record `N_C3` (R-C3.4) and your base commit in the PR description.
2. Print the rows: `ledger_rows '\bS3\b' config,env > /tmp/c3_rows.txt` (a scratch file outside the repo). Keep only
   rows whose `gate` cell is `window` and `state` cell is `aliased`; their count must equal `N_C3`.
3. In `docs/compat-surfaces.md`, for exactly those rows, change the `state` cell from `aliased` to `retired`. Change no
   other cell (dates, gate and removal PR stay as they are; PC-20-02 fills the removal PR).
4. `make compat-ledger` (regenerates `internal/books/compat-surfaces.md`). Bump the header of
   `docs/compat-surfaces.md` (minor version, `last-edited`); the generated copy follows from the source.
5. Write the tests of "Tests" (they fail before step 3 on a fixture ledger with the rows `aliased`, pass after).
6. Run R-C3.4 again: expected one line `<N_C3> retired` and no `aliased` line.
7. Run "Acceptance". In the PR description add a **Release notes** section listing every retired legacy name with its
   canonical name (09 §9.5: the contract build's notes list every removed surface; the coordinator prepends it to the
   generated notes, never replacing them), and paste the owner's W readings verbatim.

## Code contract (from the spec; do not rename)

```go
// core/config (05-config §1.3.3, §1.3.6) — exists at the base commit; this task calls nothing new.
Retired func(ledger string) bool                          // config.Options field; wired by the kernel to compat
ErrRetiredAlias = errors.New("config: a retired legacy name is set")
type RetiredAliasError struct{ Name string; Canonical Path; CanonicalEnv string }
```
Ledger states (09 §9.2.9): `aliased` | `gone` | `retired` | `removed`. C3 uses `retired` (05-config §1.12.4).

## Tests to write first (TDD)

- Extend the file of `TestAlias_RetiredProtectedFailsBoot` (05-config §1.11) with table cases driven by a fixture
  ledger in which the C3 rows are `retired`:
  - `ABS_JWT_SECRET` set (a secret-class `BindEnv` window name, 05-config §1.3.6) → boot fails with
    `*RetiredAliasError` whose `CanonicalEnv` is the canonical variable;
  - one window env name of a `ProtectAdmin`-or-higher key (pick the first such row from `/tmp/c3_rows.txt` and name it
    in the test) → boot fails the same way;
  - one window env name of an unprotected, non-secret key → boot succeeds and exactly one WARN is logged per boot.
  Name the new test `TestC3_RetiredAliasesAtBoot` (name coined here) unless the existing test's table is the
  natural home.
- `TestCompatLedgerMatchesCode` and `TestNoContractBeforeSunset` must pass with the rows `retired` (no new test).

## Acceptance (commands and expected output)

```bash
# A1. Ledger tests: each ran and passed (a "no tests to run" line is a failure).
for t in TestCompatLedgerMatchesCode TestNoContractBeforeSunset TestCompatLedgerCopyFresh TestCompatLedgerHoldsOnlyRows; do
  go test ./... -run "^${t}\$" -count=1 -v 2>&1 | grep -c -- "--- PASS: ${t}" ; done      # expected: four lines, each >= 1
go test ./... -run '^TestCompat|^TestNoContractBeforeSunset$' -count=1 2>&1 | grep -c '^FAIL'  # expected: 0
# A2. The embedded copy is byte-equal to the source.
make compat-ledger && git diff --exit-code internal/books/compat-surfaces.md && echo FRESH      # expected: FRESH
# A3. Full gates.
make ci; echo "make ci exit=$?"                                                               # expected: make ci exit=0
make -C core ci; echo "core ci exit=$?"                                                       # expected: core ci exit=0 (only when core/ changed)
```
```bash
# A-C3.1 no window row of the S3 config/env set is still aliased; the retired count is N_C3.
ledger_rows '\bS3\b' config,env | grep '| window |' | grep -c '^aliased'                    # expected: 0
ledger_rows '\bS3\b' config,env | grep '| window |' | grep -c '^retired'                    # expected: N_C3
ledger_rows '\bS3\b' config,env | grep '| permanent |' | grep -c -v '^aliased'              # expected: 0 (permanent rows untouched)
# A-C3.2 the named retired-alias tests pass.
go test ./... -run '^TestAlias_RetiredProtectedFailsBoot$|^TestC3_RetiredAliasesAtBoot$' -count=1 -v 2>&1 | grep -c -- '--- PASS'   # expected: >= 2
# A-C3.3 no change to the alias table or the Facade unless the build needed one.
git diff --stat origin/main -- internal/config/aliases_gen.go internal/config/facade.go | tail -1   # expected: empty
```

## Out of scope

- Deleting the `Gone` stubs or the alias code: that is PC-20-02, one production minor later (09 §9.5, 05-http-api §4.6).
- Any ledger row outside this contract's set, any row whose gate is `permanent`, and every other contract's rows.
- `ci/stage.txt`: a contract is not a stage release and changes no marker line.
- Stored data of any kind: rows, blob fields, sidecars, levels (09 §9.5; 09 §9.2.1 "Stored data has no window").
- The S3 `http` aliases `/api/v1/config` (2), `/api/v1/preferences` (3), `/api/v1/me`, `/api/v1/setup` (09 S4 "Endpoints")
  and every `config`/`env` window row of a later section (S5 schedule keys, S7 instance keys, S8 `auto_update*` and
  `backup_*`): 09 §9.5 assigns them to no contract C3 covers. List them in the PR description under "not retired by C3".
- Deleting retired rows or alias entries (PC-20-02, one minor later; protected and secret rows are kept until the next
  major, 05-config §1.3.6).
- The blob's field names and the gap-fill file (stored format until the H2 and settings cutovers).

## Rollback

Before merge: close the PR and remove the worktree. After merge and before a release: a revert PR (an exact inverse
of the commit). After a production release: `make rollback` to the previous binary, which still serves every surface
this PR retired (binary swap; plan §(b) W19 row). No data step exists to undo.

## Repo rules

- **File headers:** every new or changed file gets or bumps its 4-line versioned header
  (`.standards/instructions/file-headers.md`); keep an existing `guid`. `changelog.d/` and `todo.d/` fragments get
  **no** header (CLAUDE.md).
- **Commits:** conventional commits. Every commit of this PR carries the trailer line `Stage-Release: none`
  (09 §9.2.1 "The trailer") and ends with `Co-Authored-By: Claude <noreply@anthropic.com>`. The commit-msg hook and
  `.gitmessage` from PC-01-07 carry the line; `check_soak.py` check (d) fails a PR whose commit lacks it.
- **PR and merge:** `gh pr create`; the required checks of 09 §9.2.1 (`extraction-gates`, `docker-image`,
  `vet-darwin`, `vet-integration`, `core-ci` when `core/` changes) green on an up-to-date branch; then
  `gh pr merge <n> --rebase`. Never squash, never `--admin` past a required check (14 Q-I). Push workflow-file changes
  with `git push`, never the MCP contents API (CLAUDE.md).
- **Bookkeeping in the same PR:** one `changelog.d/` fragment (`scriv create` or by hand, `changelog.d/README.md`);
  edit `TODO.md` if the PR closes a tracked item; check the executive-summary criteria
  (`docs/process/executive-summaries.md`).
- **Owner files:** never edit `ci/soak.txt`, `ci/soak_health_allow.txt` or `ci/prev_ref.txt`, and never run
  `scripts/soak_record.py` (09 §9.2.1 "Soaks"; NOT AGENT WORK).
- **Concurrency (CLAUDE.md):** this task adds no loop over a library-scale collection. If you find you need one, STOP and report.
- **Confidentiality:** no hostnames, IPs, usernames, emails, secrets or absolute machine paths in commits, PRs or
  test fixtures.

## Planner notes

- **Spec conflict:** 09 §9.5 C3 and §9.6 row 13 refuse legacy YAML keys at boot; 05-config §1.3.6 warns once per boot
  for a retired name of a key that is neither `ProtectAdmin`+ nor secret. Step 0 STOPs until the owner reconciles them.
- **Done-check wording:** the inventory says "TestCompatLedgerMatchesCode with the rows in state gone" and
  "TestAliasHeadersAndShape for the 410 answers". C3's rows move to `retired` (05-config §1.12.4), not `gone`, and C3
  retires no HTTP surface, so no 410 answer exists to test.
- **Unassigned window rows:** the S4 `http` aliases `/api/v1/config`, `/api/v1/preferences`, `/api/v1/me` and
  `/api/v1/setup`, and the `config`/`env` window rows added at S5 (PC-13-14), S7 and S8, belong to no contract in 09 §9.5
  (row 13 names C3 and C7 for YAML keys only). Until one is assigned, PC-20-02's done-check cannot pass.
- **Files:** `internal/config/aliases_gen.go` and `internal/config/facade.go` are listed, but the lifetime lives in the
  ledger only (05-config §1.3.6), so no diff is expected in them.
