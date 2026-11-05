<!-- file: docs/agent-tasks/platform-core/F4-07-reach-oracle-r0-baseline.md -->
<!-- version: 1.1.0 -->
<!-- guid: 67c2453a-bb34-4e9d-aa7d-456a7e6a8d60 -->
<!-- last-edited: 2026-11-05 -->

# F4-07 — The reach oracle: allow and deny for every gin route, every seed and custom role, scoped keys and auth-off, computed by the R0 binary on the corpus and by R2 on the migrated corpus

| Field | Value |
|---|---|
| ID | F4-07 |
| Release / flip | R2, identity flip F4 (dark PR; the test bites from F4-08, which registers M3, and stays as the atom table's regression test) |
| Tier | **T3** (Opus, high): the oracle proves that no principal gains or loses reach |
| Wave | W30 |
| Depends on | F4-06, F4-05, F2-12 |
| Owns gate test | `TestIdentityCutover_ReachOracle` |
| Gate | Plan, then execute autonomously in your own worktree and PR. |

## ⛔ START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f4-07-reach-oracle
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
make web-deps
git rev-parse HEAD       # BASE
```

Confirm F4-06, F4-05 and F2-12 are merged, and that the R0 release tag is the one F2-12 recorded in
`internal/books/testdata/r2corpus/manifest.json`. If not, stop.

## Goal

1. `scripts/r2corpus/record_r0_reach.py`: drive the **R0 release binary** (HEAD plus the six early fixes; never HEAD
   itself) over F2-12's R0-written corpus with F2-13's overlay, and record allow or deny for every route of R0's router
   dump, every books task start, and every principal kind of the spec's list, into the data directory
   `internal/books/rbacreach/` (a data directory read by the test, **not** a Go package).
2. `internal/books/reach_oracle_test.go`: `TestIdentityCutover_ReachOracle` runs M3 (F4-06) on a copy of the same corpus
   through F2-15's harness, builds the R2 router (the legacy mount with stage 13 enforcing each manifest `Perm`, plus every
   core route of F4-05), evaluates the same principals, and asserts **after = R0's**, mapped to each route's successor,
   except the listed expected differences, each asserted as such.

## Spec

- 05-rbac §12 row `TestIdentityCutover_ReachOracle` (the whole row: copy its principal list and its expected
  differences into the test's table), §6.1 (the atom table), §6.2 (admin-gated routes, the deleted HEAD reset routes),
  §6.3 ("The reach oracle"), §4.11 (auth-off tightenings (a)–(d)).
- 05-http-api §8.1 (the F4 paragraph after the table: every manifest route, 572 at `748fd787a`, a count the test
  generates at the base commit; the custom-role half over random native roles), §8.3 (the bridge).
- 09 §9.2.6 ("The reach oracle": R0 is the baseline; the auth-disabled principal is also checked against every core
  route); 09 §9.3 (the corpus, the R0 binary writes it).

## Files

| Action | Path |
|---|---|
| create | `internal/books/rbacreach/` (recorded R0 answers: data files only, no `.go` file) |
| create | `internal/books/reach_oracle_test.go` |
| create | `scripts/r2corpus/record_r0_reach.py` |
| create | `scripts/tests/test_record_r0_reach.py` (pytest; the convention of F2-12's `scripts/tests/test_record_r0_answers.py`) |

## Re-verify at BASE

```bash
ls internal/books/testdata/r2corpus/manifest.json internal/books/testdata/r2corpus/stage1 internal/books/testdata/r2corpus/overlay | head -3   # F2-12, F2-13
ls scripts/r2corpus/write_r0_corpus.py scripts/r2corpus/record_r0_answers.py | wc -l   # 2 (F2-12)
grep -rn 'TestLegacyManifest_RegistersAsMuxPatterns' internal/server/ | wc -l          # 1 (R1-14: the manifest)
grep -rn 'TestAtomTable_Golden' internal/books/ | wc -l                                # 1 (R1-15)
ls -d internal/books/cutover/m3 | wc -l                                                # 1 (F4-06)
ls -d core/internal/authhttp | wc -l                                                   # 1 (F4-05)
ls -d internal/books/rbacreach 2>/dev/null | wc -l                                     # 0
ls scripts/tests/test_record_r0_answers.py scripts/tests/test_record_r0_reach.py 2>/dev/null | wc -l   # 1 (F2-12's; yours is new)
# the manifest's route count at BASE; record it (05-http-api §8.1: 572 at 748fd787a)
go test -count=1 -run 'TestLegacyManifest_RegistersAsMuxPatterns' -v ./internal/server/ 2>&1 | grep -c -- '--- PASS'
```

## STOP conditions

1. The baseline would be HEAD rather than the R0 release binary.
2. A difference between R0 and R2 that is not on the spec's expected-difference list: do not add it to the list; report
   it (it is a defect in the atom table, the mapping or a route's `Perm`).
3. The data directory would need a Go package (it must stay data; 05-rbac §6.3).

## Steps

1. Write `record_r0_reach.py`: fetch the R0 binary (the tag in the corpus manifest), boot it on a copy of the corpus with
   each principal's credential (sessions, keys of each scope shape, the custom role, auth off), request every route of
   its router dump with a probe body that reaches the authorization decision, and record `allow`/`deny` per (route,
   principal) plus books task starts; write the result under `internal/books/rbacreach/` with the R0 tag and the route
   count. Commit the recorded data. Write `scripts/tests/test_record_r0_reach.py` first: with a fake R0 binary (a stub
   HTTP server answering from a fixture router dump, as F2-12's script tests fake theirs) it checks that every route of
   the dump and every principal of step 2 gets one recorded verdict, that the output names the R0 tag and the route
   count, that `--check` exits non-zero on a one-cell diff, and that no credential appears in the output.
2. Write the test's principal table exactly as 05-rbac §12 lists: a session of each seed role; keys scoped
   `["library.view"]`, `["settings.manage"]`, `["users.manage"]`, `["fingerprint.worker"]`, every atom and an empty
   list, each owned by an admin, an editor, a viewer and an `fp-worker` user; an editor-owned every-atom key; a key whose
   atoms lie outside its owner's roles; a custom role holding every atom; the auth-disabled principal. Add the
   custom-role half over random native roles (05-http-api §8.1).
3. Map each HEAD route to its successor (a gin route deleted at F4 maps to its core route; the deleted HEAD reset routes
   map to the core reset route per §6.2).
4. Encode the expected differences as asserted rows, never as skips: §4.11's auth-off tightenings (a)–(d) (credential
   metadata and audit routes 401 for the auth-disabled principal among them); §6.2's tightened `settings.manage` system
   routes; the `core:system.status` readiness reach of `editor` and `viewer`; dominance and `CanAssign` on
   `core:users.manage`; the deleted HEAD reset routes; and the one reviewed gain (an admin-owned `["settings.manage"]`
   key's `GET /cache/stats/keys`). Assert that viewer sessions and every non-`Confined()` key keep their playlist writes,
   and that an every-atom or empty-list key of an admin still approves every run it approved on R0.
5. Also check the auth-disabled principal against **every core route** (09 §9.2.6).
6. Headers on the test and the script (the recorded data files: see Repo rules), one `changelog.d/` fragment, Acceptance.

## Tests first

`TestIdentityCutover_ReachOracle` is the deliverable. Until F4-08 registers M3 and switches the mount to
`LegacyAuthCore`, the test drives M3 and builds the R2 router directly from the dark packages; F4-08 makes it run on the
registered path. It must fail if any single atom-table row is changed (mutation check: flip one row in a test-only copy
and assert the oracle reports it).

## Invariants

- R0 is the baseline; no principal gains or loses an allow outside the listed differences.
- The recorded data is regenerated only when R0 is re-cut (F2-12's rule).

## Acceptance

```bash
python3 -m pytest scripts/tests/test_record_r0_reach.py -q     # all passed
python3 scripts/r2corpus/record_r0_reach.py --check            # (flag coined here) re-derives the recorded data from the R0 binary; exit 0, no diff
go test -count=1 -run 'TestIdentityCutover_ReachOracle' -v ./internal/books/ 2>&1 | tail -3   # --- PASS, with the route count printed
find internal/books/rbacreach -name '*.go' | wc -l              # 0
make ci                                                         # exit 0
```

Run time: the oracle runs in `extraction-gates` within its 30-minute job budget (09 §9.2.10); shard by route range with a
bounded worker pool if it does not (CLAUDE.md concurrency rules), never by dropping cells.

## Out of scope

Changing the atom table (R1-15's, a separate reviewed PR); M3 (F4-06); registering it (F4-08).

## Rollback

Revert the PR; it adds a test and data only.

## Repo rules

Worktree only; file headers on the script, its pytest and the Go test; the recorded data files are generated: give them a header exactly as
F2-12's generated corpus files carry one at BASE (follow that convention; do not invent another); fragments headerless; conventional commit ending
`Co-Authored-By: Claude <noreply@anthropic.com>`, no release or flip trailer; `gh pr merge <n> --rebase`, never squash,
never `--admin`. Python for the script (CLAUDE.md: Python for anything non-trivial).

```
test(books): reach oracle against the R0 binary over the migrated corpus

Co-Authored-By: Claude <noreply@anthropic.com>
```
