<!-- file: docs/specs/pd-2-itunes-writeback-bisect-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a4b0f1c-9e2d-4b5a-9c7e-3d8b2f5e1a04 -->
<!-- last-edited: 2026-05-29 -->

# PD-2 — iTunes write-back library corruption: bisect plan

**Status:** Awaiting approval before bisect runs.
**Scope:** Find the commit between `f2856e45` (last known-good per
user) and `HEAD` that introduced library corruption on iTunes
write-back. **275 commits** in the suspect range.

## Goal

Identify the single commit (or smallest contiguous range) whose revert
restores correct iTunes write-back behavior, then either revert it on
`main` or land a forward fix.

## Reproduction (required before bisect)

A bisect is useless without a deterministic `is_bad` check. Before
running `git bisect`, define the failure precisely:

- **Input:** A book in the library with an iTunes PID linkage and
  recent metadata-apply.
- **Trigger:** UI write-back (or `POST /api/v1/itunes/writeback` if
  there is a direct endpoint) targeting that book.
- **Failure observation:** What does "corrupts the library" mean?
  Candidates to confirm with user before bisect:
  1. iTunes Library.xml becomes malformed / unparseable
  2. PID→book mapping points to wrong book after write-back
  3. Book metadata in Pebble overwritten with iTunes-side stale data
  4. `external_id_map` rows duplicated or deleted
  5. Library counts (`stats:library`) go negative / wildly off
  6. File path in DB no longer matches filesystem

**Before bisecting, the user must specify which symptom defines
`bad`.** Otherwise we waste 8+ build cycles chasing the wrong signal.

## Bisect harness

Once `is_bad` is defined as a single observable check, encode it as a
script the bisect can run automatically.

```bash
# scripts/bisect-itunes-writeback.sh
#!/usr/bin/env bash
set -euo pipefail

# 1. Build
make build-api || exit 125   # build failure → skip commit

# 2. Boot fresh test env with snapshot DB
./scripts/test-env-up.sh --restore-snapshot=pre-writeback || exit 125

# 3. Trigger write-back against a fixed test book
TEST_BOOK_ID=<known-pid-linked-book>
curl -sS -H "X-API-Key: $TEST_API_KEY" \
  -X POST http://localhost:8484/api/v1/itunes/writeback \
  -d "{\"book_id\":\"$TEST_BOOK_ID\"}"

# 4. Sleep for write-back to settle (op-log "complete")
./scripts/wait-op.sh itunes.writeback || exit 1

# 5. is_bad check (CHANGE THIS based on agreed symptom)
./scripts/verify-no-corruption.sh "$TEST_BOOK_ID"
```

Exit 0 = good, 1 = bad, 125 = skip (build/setup failure).

## Bisect commands

```bash
git worktree add ../audiobook-organizer-bisect bisect/itunes-writeback
cd ../audiobook-organizer-bisect
git bisect start
git bisect bad HEAD
git bisect good f2856e45
git bisect run ./scripts/bisect-itunes-writeback.sh
```

Expected steps: `log2(275) ≈ 8.1` build cycles. Each cycle is one
`make build-api` (~30s) + boot + trigger + verify (~2 min). Wall time:
**~20 minutes** for the bisect alone, plus harness authoring.

## Pre-filter to shrink the range

Of the 275 commits, only a subset touch iTunes write-back paths.
Pre-filter before full bisect:

```bash
git log --oneline f2856e45..HEAD -- \
  internal/itunes/ \
  internal/audiobook/writeback.go \
  internal/audiobook/tag_write.go \
  internal/database/itunes_*.go
```

Then mark all *other* commits as skipped via `git bisect skip <sha>`
(or build a custom bisect that only considers iTunes-touching
commits). This typically drops the suspect set to <30 commits and
~5 build cycles.

## Top suspects (manual ranking before bisect)

From the `git log f2856e45..HEAD` scan, commits most likely to
corrupt iTunes write-back:

1. **PR #1185 (d4f720fb)** — `ListBooksByITunesPID` memdb pushdown.
   If memdb projection drops the PID field or returns wrong book ID,
   write-back targets wrong book.
2. **PR #1172 (6ca78388)** — strip chapter prefix from track Name.
   If applied during write-back (not just import), it could rewrite
   user-edited titles.
3. **PR #1190 (5ef08285)** — I2+I3 BookFile field strip. If
   write-back reads a stripped field expecting full Book, it writes
   empty/zero.
4. **PR #1156 (1ecb48d1) / #1181 (f432aeb5)** — Isolate flip-flop.
   If write-back ran in a subprocess that couldn't open Pebble,
   partial writes may have leaked through.
5. **ee180f84** — iTunes streaming XML parser. If parser drops or
   misorders tracks, write-back could match the wrong track.

Quickly inspect each before launching bisect; if one is obviously
the cause, revert directly.

## Rollback (if bisect identifies a commit but revert breaks build)

- `git revert --no-commit <sha>` in a worktree
- Resolve conflicts
- Run `make ci`
- If `make ci` fails: file the surrounding refactor work as
  blocking, do a forward fix instead of revert

## Decision points needing user input

1. **Which corruption symptom defines `bad`?** (see Reproduction)
2. **Is there a pre-writeback DB snapshot we can restore between
   bisect steps?** Without one, each step is destructive and bisect
   on a live DB will compound corruption.
3. **Test against prod-copy DB or a synthesized fixture?** Prod-copy
   is more faithful but takes ~10 min per restore; fixture is fast
   but may miss the trigger.

## Output

When bisect lands a single commit:

- Open `fix/itunes-writeback-bisect-<sha>` worktree
- Either revert the commit cleanly + ship via `/ship`, OR
- Write a forward fix referencing the bisect result in commit msg
- Add regression test that reproduces the bisect harness check
- Update `docs/specs/post-deploy-2026-05-29-verification.md` with
  the new write-back smoke check
