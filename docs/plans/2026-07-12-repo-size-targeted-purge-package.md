<!-- file: docs/plans/2026-07-12-repo-size-targeted-purge-package.md -->
<!-- version: 1.0.0 -->
<!-- guid: b7e41a2f-3c58-4d9a-8e16-2f9c0a7d5b84 -->
<!-- last-edited: 2026-07-12 -->

# Repo-size targeted blob purge — ready-to-run package (REPO-SIZE-1, #1650)

**Companion to** [`docs/plans/2026-07-10-repo-size-history-rewrite-plan.md`](2026-07-10-repo-size-history-rewrite-plan.md)
(PR #1907). That doc chose the direction (targeted purge, NOT squash) + Support gc. This is the
executable package. **NOTHING here is run without an explicit owner "go" — it ends in a
force-push and a GitHub Support ticket.**

## What actually shrinks (measured 2026-07-12)
Local reachable pack = **226.5 MiB**. The full 1.69 GB is dominated by GitHub-owned `refs/pull/*`
(1,778 PR refs) + un-gc'd unreachable objects — a client-side rewrite CANNOT remove those. So:
- **`git filter-repo` reclaims only the local pack bloat (~this 226 MiB shrinks toward ~40 MiB).**
- **The 1.47 GB PR-ref/unreachable bulk requires the GitHub Support `gc` ticket (Step 5).** Both
  are needed for the full win; neither substitutes for the other.

## Blob inventory (purge targets)
History-only (already gone from HEAD — pure history bloat, safe):
| Path | Size | Notes |
|---|---|---|
| `audiobook-organizer-test` | ~270 MB (5 versions) | compiled test binary, reachable from tags v0.1.0–v0.56.0 |
| `test-full-v3-fixed.json` | 32 MB | old test fixture |
| `bin/copilot-agent-util-*` (4) | ~14 MB | old tool binaries |

Live in HEAD (rewrite removes them from the tree too — confirm first):
| Path | Size | Pre-purge action |
|---|---|---|
| `mtls-bridge` | 8.9 MB | committed binary — confirm unused, then purge |
| `testdata/series_fix.json` | 2.1 MB | **grep tests first** — if referenced, externalize/regen before purge |
| `testdata/series_dump.json` | 1.6 MB | same |
| `docs/security/audit-2026-05-03/raw/code-scanning.json` | 3.6 MB | audit artifact — safe to drop |

Verify live-file test deps BEFORE running:
```bash
grep -rn "series_fix.json\|series_dump.json\|mtls-bridge" --include=*.go --include=*.ts .
```
Any hit → externalize (move to release asset / regenerate in-test) in a normal PR FIRST.

## Step 1 — backup (mandatory)
```bash
git clone --mirror git@github.com:falkcorp/audiobook-organizer.git ../abk-mirror-backup.git
```

## Step 2 — coordination gate (mandatory)
- All worktrees removed (`git worktree list` shows only the primary).
- No other Claude sessions / CI jobs mid-write.
- Announce a short freeze — every existing clone must re-clone or `git reset --hard` after.

## Step 3 — the rewrite (fresh clone; NEVER in a working checkout)
```bash
pip install git-filter-repo   # if absent
git clone git@github.com:falkcorp/audiobook-organizer.git abk-rewrite && cd abk-rewrite
git filter-repo \
  --path audiobook-organizer-test \
  --path test-full-v3-fixed.json \
  --path mtls-bridge \
  --path docs/security/audit-2026-05-03/raw/code-scanning.json \
  --path testdata/series_fix.json \
  --path testdata/series_dump.json \
  --path-glob 'bin/copilot-agent-util-*' \
  --invert-paths
# (add --strip-blobs-bigger-than 5M to also catch stragglers)
git count-objects -vH   # confirm size-pack dropped
```

## Step 4 — tag/ref reality + force-push (⚠ blast radius)
`filter-repo` rewrites EVERY commit that touched those paths → **new SHAs for those commits and
every descendant, including up to 764 tags.** Consequences to accept:
- **GoReleaser release tags get new SHAs** — historical release assets still exist, but tag→commit
  mapping changes; re-verify the release workflow before the next tag.
- Every open PR branch must be rebased; ~889 PR refs get un-rebased server-side.
- Force-push: `git push --force --mirror` (or `--force --tags` + branches). Requires branch-protection
  temporarily relaxed on `main`.

## Step 5 — GitHub Support gc ticket (does the 1.47 GB the rewrite can't)
Submit to GitHub Support (the ONLY way to reclaim `refs/pull/*` + unreachable bulk):
> Repo `falkcorp/audiobook-organizer` is ~1.69 GB, mostly stale `refs/pull/*` and unreachable
> objects after history maintenance. We've force-pushed a `git filter-repo` history rewrite
> removing large historical blobs. Please run server-side `git gc --aggressive` / repack to
> reclaim the unreachable + PR-ref bulk. Local reachable pack is now ~40 MiB.

## Step 6 — verify
- `git count-objects -vH` local ~40 MiB; GitHub repo-size (Settings) drops after Support gc.
- CI green on the rewritten `main`; next release tag builds.

## Rollback
Restore from the Step-1 mirror: `git push --force --mirror` from `../abk-mirror-backup.git`.

## Recommendation
Do the **live-file dependency check (Step 0 grep) + Step 1 backup** now (zero-risk). Hold Steps
3–5 for a scheduled low-traffic window with branch-protection access. The Support ticket (Step 5)
can be filed in parallel — it's the bigger win and carries no rewrite risk.
