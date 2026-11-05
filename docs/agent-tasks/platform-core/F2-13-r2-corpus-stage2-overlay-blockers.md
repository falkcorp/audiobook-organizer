<!-- file: docs/agent-tasks/platform-core/F2-13-r2-corpus-stage2-overlay-blockers.md -->
<!-- version: 1.1.2 -->
<!-- guid: 22564994-b4e2-44fd-8ac9-918ced1613a1 -->
<!-- last-edited: 2026-11-05 -->

# F2-13 — R2 fixture corpus, stage 2: the labelled hand-built overlay (a program built at the R0 tag against that tag's `internal/database`) and the separate identity-blocker and unfinished-merge corpus

| Field | Value |
|---|---|
| ID | F2-13 |
| Release / flip | R2, foundation PRs (F2); testdata and a test-only program, no product code |
| Wave | W25 |
| Tier | **T3** (Opus / high): hand-built corrupt and edge rows, each labelled with its expected cutover outcome; the blocker corpus must make the preflight refuse |
| Depends on | F2-12 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-13-r2-corpus-overlay
git -C "$REPO" fetch origin --tags
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

## Goal

Add stage 2 of 09 §9.3 item 1: **a separate, labelled overlay** for the cases R0's API cannot write, produced by a Go
program **built at the R0 tag against that tag's own `internal/database`** (so the rows are byte-exact HEAD rows, not this
repo's guess at them), applied to a **copy** of F2-12's stage-1 store; and a **separate blocker corpus** holding an
identity row M3 cannot convert and an unfinished merge journal, which the cutover preflight must refuse (D13; IMPL-8). Every
case is listed in `manifest.json` with its label and expected outcome. Then the R0 binary, booted on the overlay result,
records the answers that change (re-run F2-12's `record_r0_answers.py` against the overlay copy).

## Spec to read first

09 §9.3 item 1 (the overlay sentence lists the cases), §9.2.3 "The cutover preflight"; 05-rbac §6.1 (the atom table), §6.3
(stored data and the identity cutover: custom roles), §12 (`TestIdentityCutover_RolesAndKeyScopes`,
`TestUnknownAtom_ReportedNotGranted`); 05-authn §10 and §11 (identity rows that stop the upgrade; the merge journal states
`applying`/`undoing`); 05-storage §5S.3.13 ("Cutover blockers", `TestCutoverStopsOnUnconvertibleIdentity`,
`TestCutoverRefusedWhileMergeInProgress`, `TestCutoverQuarantinesUndecodableRows`); 05-config §1.12.2 (stage 2: a blob field
no rename-table row names); 05-tasks §5T.9 `TestCutover_UndecodableRowQuarantined`, `TestCutover_TasksFixtureFromR0` (the
run states R0 cannot leave at a clean stop).

## Base state

```bash
python3 -c "import json;m=json.load(open('internal/books/testdata/r2corpus/manifest.json'));print(m['r0_tag'], len(m['stage1']))"   # F2-12's tag and >0 cases, else STOP
test -d internal/books/testdata/r2corpus/stage1 && echo ok    # "ok"
test ! -e internal/books/testdata/r2corpus/overlay && test ! -e internal/books/testdata/r2corpus/blockers && echo absent   # "absent"
git show "$(python3 -c "import json;print(json.load(open('internal/books/testdata/r2corpus/manifest.json'))['r0_tag'])")":internal/database/keyfamilies.go | head -5   # the R0 tag has internal/database (it predates R1-26's rename)
```

## Files

Create: `scripts/r2corpus/overlay/` (the Go program, guarded by the build constraint `//go:build r2corpus_overlay` (tag
coined here) on every file, because it imports `internal/database`, which no longer exists on `main` after R1-26; plus a
Python driver `scripts/r2corpus/overlay/build_overlay.py` that checks out the R0 tag in a temporary worktree, copies the
program in, builds it there, and runs it on a copy of `stage1/`), `internal/books/testdata/r2corpus/overlay/`,
`internal/books/testdata/r2corpus/blockers/`, `scripts/tests/test_build_overlay.py`. Modify:
`internal/books/testdata/r2corpus/manifest.json` (append to `overlay`, add `blockers`; never rewrite stage-1 entries).

The program imports the R0 tag's `internal/database`, which `main` no longer has after R1-26 renames it to
`internal/books/store`; that is why it is built in a temporary worktree at the tag and guarded by a build constraint.

With the build constraint, `go build ./...`, `go vet ./...` and every CI tag set on `main` skip the program. Check that
`scripts/` is inside the root module's package walk (`go list ./scripts/... 2>&1`); if `go list` reports the directory, the
constraint is what keeps it out. Do not add the tag to any CI job.

## Cases (each one a manifest entry `{case, family, key_prefix, label, expected}`)

Overlay (applied to a copy of stage 1):

| Label | What the program writes | Expected cutover outcome |
|---|---|---|
| `corrupt-<family>` | one undecodable row in each family M2–M6 decodes and quarantines (settings blob field, a role, a run record, a `syslog:` row, an integration field, an import root); **no** user, API-key or OAuth-identity row, because an identity row is never quarantined (05-authn §11 "Identity rows stop the upgrade (D13)"): those cases are in the blocker corpus below | quarantined under `c/storage/quarantine/` with its reason; step completes (05-storage `TestCutoverQuarantinesUndecodableRows`; 05-tasks `TestCutover_UndecodableRowQuarantined`) |
| `blob-field-unmapped` | a settings-blob field no rename-table row names | reported, per 05-config §1.12.2's rule for it |
| `role-every-atom` | a custom role holding every atom | imported as `custom.<slug>` without `core:admin` |
| `role-subset` | a custom role holding a subset of atoms | imported with the union of its rows' scopes |
| `role-unknown-atom` | a custom role naming one atom outside 05-rbac §6.1's table | imported, the atom grants nothing, reported |
| `role-all-unknown` | a custom role whose every atom is unknown, plus users whose role lists name it | quarantined together with the role-list entries that name it |
| `key-outside-owner` | an API key whose atoms lie outside its owner's roles | imported with the intersection (05-rbac §6.3) |
| `status-edge-<n>` | user and key status edge values (`" Active"`, `"DISABLED"`, `""`, …: HEAD's predicates trim and lower-case; an empty user status is active) | normalised as 05-authn §11 / 09 §9.2.3 M3 say |
| `run-<state>` | runs R0 cannot leave at a clean stop: `running`, `stopping` (each reason), `interrupted`, quiesced, awaiting decision; with checkpoints, chunk ledgers, Fixer rows and marks; a stand-down marker; debounce buckets; `Requires` stamps | imported per 05-tasks §5T.8.3 (running/stopping → `interrupted`) |

Blocker corpus (its own copy of stage 1; must refuse):

| Label | What | Expected |
|---|---|---|
| `blocker-identity-corrupt-user` | a corrupt `u:` row | the preflight refuses the boot, exit 78, report names the key and never its value; store and directory byte-identical, no checkpoint (05-storage `TestCutoverStopsOnUnconvertibleIdentity`) |
| `blocker-identity-corrupt-apikey` | a corrupt `apikey:` row (written by the R0-tag program) | as above (the `apikey:` cell of `TestCutoverStopsOnUnconvertibleIdentity`; F4-06's preflight) |
| `blocker-identity-corrupt-oauthid` | a corrupt `oauthid:` row (written by the R0-tag program) | as above (the `oauthid:` cell) |
| `blocker-identity-<kind>` | a decodable user row `auth.Importer` would reject; an API key whose user does not exist; an OAuth identity whose user does not exist | as above |
| `blocker-merge-<state>` | a `usermerge:journal:` record at `applying`; another at `undoing`; a terminal one as a control | `applying`/`undoing` refuse; the terminal one does not block (05-storage `TestCutoverRefusedWhileMergeInProgress`) |

Write each blocker case so it can be tested alone (one store per case under `blockers/<label>/`) or state in the manifest
that one store holds all and the test expects every row named.

## Invariants

1. Every overlay row comes from the program built at the R0 tag; nothing is written by `main`'s code.
2. Every case is labelled in the manifest, and the quarantine report expected by later tests is **exactly** the overlay's
   quarantine cases (09 §9.3: "the quarantine report (exactly the overlay's quarantine cases)"). The overlay holds no
   identity row a step cannot convert, so F2-15's `TestCutoverOnR0Corpus` still completes once F4-08 registers M3; every
   such row is a blocker case.
3. No real credential, host or user name; fixture values only.
4. Stage 1's committed bytes are untouched.

## Tests

- `TestR2Corpus_ManifestMatchesFiles` (F2-12's, name coined there, in `internal/books/r2corpus_manifest_test.go`): it
  reads the manifest, so every `overlay` and `blockers` entry's store must exist for it to pass. Do not edit the Go test
  file (it is F2-12's); if it does not walk `overlay` and `blockers` yet, STOP and report.
- `scripts/tests/test_build_overlay.py` (name coined here): the driver's case list equals the manifest's overlay and
  blocker labels.
- A check that `main` does not compile the program: `go list -tags '' ./scripts/r2corpus/overlay/ 2>&1` reports "build
  constraints exclude all Go files".

## Acceptance

```bash
python3 scripts/r2corpus/overlay/build_overlay.py --help                                  # exit 0
python3 -m pytest scripts/tests/test_build_overlay.py -q                                   # passed
go list ./scripts/r2corpus/overlay/ 2>&1 | grep -c 'build constraints exclude all Go files' # 1
python3 -c "import json;m=json.load(open('internal/books/testdata/r2corpus/manifest.json'));print(len(m['overlay'])>0, len(m['blockers'])>0)"   # True True
go test ./internal/books/ -run TestR2Corpus_ManifestMatchesFiles -count=1                  # ok
make ci                                                                                    # exit 0
```

## Out of scope

The cutover tests that consume the corpus (F2-15's harness; each step's own tests in F3-06, F4-06, F5-16 …); the preflight
itself (F3-04, F4-06); the production-shaped store (F2-14).

## Rollback

Revert the PR (testdata and a tag-guarded program only).

## Repo rules

File headers (Go files and the driver; the manifest's `_header` object as in F2-12); conventional commit
(`test(r2): hand-built overlay and blocker corpus at the R0 tag`) with `Co-Authored-By: Claude <noreply@anthropic.com>`;
`gh pr merge <n> --rebase`; `changelog.d/` fragment.
