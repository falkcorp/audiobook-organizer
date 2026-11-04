<!-- file: docs/agent-tasks/platform-core/PC-20-02-delete-contract-alias-code.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5db59334-7301-494a-9fd3-9dc2590c5529 -->
<!-- last-edited: 2026-11-04 -->

# PC-20-02 — Delete the alias code of every contract whose Gone minor has passed; close the ledger rows

| Field | Value |
|---|---|
| ID | `PC-20-02` |
| Tier | T2 Sonnet/high — touches every subsystem's alias table and the merged ledger, which must end with permanent rows only; several "keep" rules make a blind delete wrong |
| Wave | W20 — S10, release `S10` |
| Depends on | `PC-20-01`, `PC-19-01`, `PC-19-02`, `PC-19-03`, `PC-19-04`, `PC-19-05`, `PC-19-06`, `PC-19-07` (plus one production minor after each Gone PR; see Gate) |
| Priority / effort | P1 / M |
| Spec | 09 S10 ("alias code of every surface contract that has run", "Compat-ledger rows closed", "Done when"); 09 §9.5; 09 §9.6; 09 §9.2.1 "Contract"; 05-http-api §4.6; 05-config §1.3.6; 05-rbac §6.3; 05-realtime §4.9 |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §5 rule 19, §7 S10 |
| `Stage-Release:` | `S10 1` (S10 content after its flip, 09 §9.2.1 "The trailer") |

## Gate

- **One production minor after each Gone step.** 09 §9.2.1: after W the old form answers 410 or is refused "for one more
  production minor, then is deleted"; 05-http-api §4.6: "the stub is deleted one minor release later". For each
  contract C3–C8, find the merge commit of its PC-19 PR and check that at least one **production minor** tag contains it
  (`git tag --contains <commit>`, read against the release list the owner keeps). A contract whose Gone PR has not yet
  shipped in a production minor is **not** deleted here: leave its code and rows, and say so in the PR. C9 has no Gone
  step (PC-19-07 removed its redirects outright).
- `ci/soak.txt` needs no new row for this PR (its trailer names `S10`, whose predecessor `S9` row PC-20-01 already
  needed); `check_soak.py` check (b) still requires that `S9` row to be current.

## Goal

Delete the `Gone` stubs, retired alias entries, projections and compat registrations of every contract that has run,
and close their ledger rows (`removed`, removal PR = this PR), so that 09 S10's done-check holds: 09 §9.6 has no row in
state `aliased` except those marked permanent, and `TestCompatLedgerMatchesCode` shows only permanent entries.

## Keep rules (each is a spec rule; deleting any of these is a defect)

| Keep | Rule |
|---|---|
| `FormerKinds` / `LegacyDefID` stored-row resolution | 09 §9.5 C5; 09 §9.6 row 28 ("stored-row resolution **permanent**") |
| `app.Options.LegacyPermissionAliases`, `rbac.LegacyAtom`, the read/write mapping of stored atoms | 05-rbac §6.3: the alias table stays as long as rows hold atoms, until H1 |
| retired config rows of `ProtectAdmin`+ and secret keys, with their alias entries | 05-config §1.3.6: kept until the next major (boot keeps refusing them) |
| `legacy_headers` on webhook instances; permanent env names; the operator overlay | 09 §9.5 C7; 05-config C11 |
| the legacy mount, `/api/*` → `/api/v1/*` redirect, ABS paths, `/health`, `/metrics`, books' legacy stores, `config.AppConfig` and logger façades | 09 S10 "Not deleted" |
| `localStorage` keys | 09 §9.5 C9 |
| every row whose gate is `permanent` (and core's `core/compat/core-ledger.md`, all permanent) | 09 §9.2.9 |

## START HERE (worktree off fresh `origin/main`)

```bash
REPO=<your clone>
TASK=pc-20-02-delete-alias-code
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-$TASK" -b "agent/$TASK" origin/main
cd "$REPO/../audiobook-organizer-$TASK"
git rebase origin/main
git worktree list
npm ci --prefix web                     # or `make web-deps`
git rev-parse --short HEAD              # record: the base commit
```

Do not run `go work init` (CLAUDE.md).

## Files

| Action | Path | Contract |
|---|---|---|
| modify | `core/compat/` (only if an entry type or test fixture names a deleted surface) | all |
| modify | `internal/config/aliases_gen.go` through its generator `tools/cmd/genaliases/` (never hand-edit generated code) | C3 |
| modify | `core/internal/authhttp/`, `core/internal/rbachttp/`, `core/rbac/` | C4 |
| modify | `core/internal/taskhttp/` | C5 |
| modify | `core/realtime/`, `internal/books/module.go`, the gin manifest file holding the stream entries | C6 |
| modify | `core/internal/integrationshttp/` | C7 |
| modify | `core/internal/backuphttp/`, `core/internal/updaterhttp/`, `core/internal/healthhttp/` | C8 |
| modify | `internal/server/legacy_manifest.go` (a 302 left by PC-19-07 whose window has now ended); `web/src/modules/books/redirects.ts` only if it exists and still lists a removed path | C9 |
| modify | `docs/compat-surfaces.md`, `internal/books/compat-surfaces.md` (generated) | all |
| create | `changelog.d/<date>_s10_alias_code.md` | — |

## Re-verify at your base commit

Paste this helper once:

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

```bash
# R1 every row that is not permanent, by state (sections: all).
python3 - <<'PY'
import re, collections
cnt = collections.Counter(); cur, hdr = '', None
for ln in open('docs/compat-surfaces.md', encoding='utf-8'):
    if ln.startswith('#'): cur, hdr = ln.strip(), None; continue
    if not ln.startswith('|'): hdr = None if not ln.strip() else hdr; continue
    cells = [c.strip().strip('`') for c in ln.strip().strip('|').split('|')]
    if hdr is None: hdr = [c.lower() for c in cells]; continue
    if all(re.fullmatch(r':?-+:?', c or '-') for c in cells) or 'state' not in hdr: continue
    r = dict(zip(hdr, cells))
    if r.get('gate') != 'permanent': cnt[(cur[:40], r.get('surface'), r.get('state'))] += 1
for k, v in sorted(cnt.items()): print(v, *k, sep=' | ')
PY
#   expected: only states gone, retired and removed. Any "aliased" non-permanent row: see step 0.
# R2 the Gone stubs and the registrations left to delete.
grep -rn 'httpapi.Gone(' core/ --include='*.go' | grep -v _test.go | wc -l                 # record N_GONE (C4, C5, C7, C8 stubs)
grep -rn 'Projection(' internal/books internal/server core/realtime --include='*.go' | grep -v _test.go   # C6 projections still defined; record
grep -rn 'gone("GET /api/core/v1/events")' internal/ --include='*.go' | grep -v _test.go | wc -l   # expected: 3 (PC-19-04)
# R3 the keep rules hold before you start.
grep -rn 'FormerKinds' core/tasks --include='*.go' | grep -v _test.go | wc -l              # expected: >= 1
grep -rn 'LegacyPermissionAliases' core/ internal/ main.go --include='*.go' | grep -v _test.go | wc -l   # expected: >= 1
# R4 the done-check tests exist.
for t in TestCompatLedgerMatchesCode TestNoContractBeforeSunset TestCompatLedgerCopyFresh TestCompatLedgerHoldsOnlyRows; do
  echo "$t $(grep -rn "func $t(" --include='*_test.go' . | wc -l)"; done                 # expected: each ends in " 1"
grep -E '^(shipped_stage|release): ' ci/stage.txt                                          # expected: S10 / S10 (PC-20-01 merged)
```

## Steps

0. **STOP gates.** (a) R1 shows a non-permanent row still `aliased`: that surface has had no contract (for example the
   S4 aliases `/api/v1/me`, `/api/v1/setup`, `/api/v1/config`, `/api/v1/preferences`, or `config`/`env` rows added at
   S5, S7 or S8). Deleting it would skip W. STOP and report the list: 09 S10's done-check cannot be met until the owner
   assigns those rows a contract. (b) If `TestCompatLedgerMatchesCode` at base counts a kept retired row (protected or
   secret config key) as a failure of "only permanent entries", STOP and report the conflict with 05-config §1.3.6; do
   not delete those rows early.
1. Per contract whose Gone minor has passed (Gate), in this order, one commit each (every commit builds):
   - **C8, C7, C5, C4 (HTTP):** delete each `httpapi.Gone(…)` registration of the contract's rows and any helper that
     only it used. Delete the `TestAliasHeadersAndShape` cases for those paths; add the 404 cases of "Tests".
   - **C4 (atoms):** remove the `rbac_atom` compat `Entry` registrations; keep the 400 `unknown_permission` refusal and
     every keep rule above.
   - **C5 (kinds):** remove the `task_kind` input compat registrations; keep `FormerKinds`/`LegacyDefID`.
   - **C6:** delete the three `gone("GET /api/core/v1/events")` manifest entries, the `Registrar.Realtime().Projection`
     calls if any remain, and the projections A, B and C with their framing code and goldens in `core/realtime/` (09 §9.5
     C6 "delete projections"); the native stream and the hub stay.
   - **C3:** delete the retired rows of keys that are neither `ProtectAdmin`+ nor secret, and their alias entries through
     the generator input of `tools/cmd/genaliases/` (regenerate `aliases_gen.go`); keep protected and secret retired rows.
   - **C9:** for a 302 PC-19-07 left in place, delete it now only if `TestNoContractBeforeSunset` passes with its row
     `removed`; otherwise leave it and report.
2. Ledger: every row you deleted code for → `state` `removed`, removal PR = this PR (fill after `gh pr create`, amend).
   `make compat-ledger`.
3. Run R1 again: expected only `retired` (protected/secret config keys) and `removed` among non-permanent rows.
4. "Acceptance". PR description: the list of deleted surfaces per contract, the minors that served each Gone step, and
   every row left untouched with its reason.

## Code contract

```go
// core/httpapi (05-http-api §3): Gone stubs are deleted; Alias/Gone APIs themselves stay (other apps use them).
// core/compat (09 §9.2.9): Ledger.Register / Handle.Use unchanged; fewer entries are registered.
```

## Tests to write first (TDD)

- `TestS10_RetiredLegacyPathsAreGoneForGood` (name coined here): for one path per deleted HTTP contract (C4, C5, C7,
  C8) and the three streams (C6), a request with an admin credential answers 404 and never `text/html` or
  `text/event-stream`.
- `TestCompatLedgerMatchesCode` passes and reports only permanent entries (plus the kept retired rows, per step 0 (b)).
- `TestRoleRow_PrevBuildSeesAdmittedAtomsOnly`, `TestLegacyRoleReachUnchanged` and the stored-row kind resolution tests
  of 05-tasks keep passing (keep rules).

## Acceptance (commands and expected output)

```bash
python3 - <<'PY'                       # no non-permanent row is aliased or gone
import re; bad=[]; cur, hdr = '', None
for ln in open('docs/compat-surfaces.md', encoding='utf-8'):
    if ln.startswith('#'): cur, hdr = ln.strip(), None; continue
    if not ln.startswith('|'): hdr = None if not ln.strip() else hdr; continue
    c = [x.strip().strip('`') for x in ln.strip().strip('|').split('|')]
    if hdr is None: hdr = [x.lower() for x in c]; continue
    if all(re.fullmatch(r':?-+:?', x or '-') for x in c) or 'state' not in hdr: continue
    r = dict(zip(hdr, c))
    if r.get('gate') != 'permanent' and r.get('state') in ('aliased', 'gone'): bad.append(ln.strip()[:120])
print(len(bad)); [print(b) for b in bad]
PY
# expected: 0
grep -rn 'httpapi.Gone(' core/ --include='*.go' | grep -v _test.go | wc -l                         # expected: 0 for ABO's retired surfaces
for t in TestCompatLedgerMatchesCode TestNoContractBeforeSunset TestCompatLedgerCopyFresh TestS10_RetiredLegacyPathsAreGoneForGood; do
  go test ./... -run "^${t}\$" -count=1 -v 2>&1 | grep -c -- "--- PASS: ${t}" ; done              # expected: four lines, each >= 1
make compat-ledger && git diff --exit-code internal/books/compat-surfaces.md && echo FRESH         # expected: FRESH
make ci; echo "make ci exit=$?"                                                                    # expected: make ci exit=0
make -C core ci; echo "core ci exit=$?"                                                            # expected: core ci exit=0
```

## Out of scope

- Every keep rule above; the deletions of PC-20-01; any change to stored data; `ci/stage.txt` (PC-20-01 set `S10`).
- Rows with no contract (step 0 (a)): reported, never deleted.

## Rollback

Revert PR before a release. After release: `make rollback` to the previous binary, which still serves the 410 stubs and
retired-alias refusals (binary swap; S10 has no rollback row, 09 §9.2.1).

## Repo rules

- **File headers** bumped on every changed file; generated files regenerated, not hand-edited; fragment without header.
- **Commits:** conventional (`refactor(compat): …`), trailer `Stage-Release: S10 1` on every commit, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; one commit per contract, each building.
- **PR:** required checks green on an up-to-date branch (`extraction-gates`, `docker-image`, `vet-darwin`,
  `vet-integration`, `core-ci`); `gh pr merge <n> --rebase`; never squash, never `--admin` past a required check.
- Never edit `ci/soak.txt`, `ci/soak_health_allow.txt`, `ci/prev_ref.txt`.
- Concurrency (CLAUDE.md): no loop is added.
- No hostnames, IPs, paths or secrets in commits and PRs.

## Planner notes

- **Files missing from the inventory row:** `core/internal/updaterhttp/` and `core/internal/healthhttp/` (C8 stubs),
  `core/internal/rbachttp/` and `core/rbac/` (C4), `internal/books/module.go` and the gin manifest (C6 stream stubs and
  projections), `internal/server/legacy_manifest.go` (a C9 302 left in place).
- **Done-check conflict:** "TestCompatLedgerMatchesCode shows only permanent entries" (09 S10) versus 05-config §1.3.6,
  which keeps retired rows of `ProtectAdmin`+ and secret keys until the next major. Step 0 (b) STOPs rather than delete
  them early.
- **Unassigned window rows** (S4 `/api/v1/me`, `/api/v1/setup`, `/api/v1/config`, `/api/v1/preferences`; `config`/`env`
  rows added at S5, S7 and S8) have no contract in 09 §9.5, so the done-check cannot pass until the owner assigns them.
- The "one production minor after each Gone PR" wait cannot be written as a depends-on; the Gate checks it by tag.
