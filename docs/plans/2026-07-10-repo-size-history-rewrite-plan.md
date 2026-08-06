<!-- file: docs/plans/2026-07-10-repo-size-history-rewrite-plan.md -->
<!-- version: 1.0.0 -->
<!-- guid: 61a1e087-cf18-4a18-b716-202d027a5530 -->
<!-- last-edited: 2026-07-11 -->

# REPO-SIZE-1 — Repository-size history-rewrite migration plan (#1650)

**Status gate:** ⛔ STOP-FOR-HUMAN. This document is a decision aid only. No
history-mutating command (`git filter-repo`, `bfg`, `git filter-branch`,
`git push --force*`, `git lfs migrate`) has been run, and none may be run under
this task. The owner decides; execution is a separate, human-initiated task.

**TL;DR recommendation:** Do **not** rewrite history first. The evidence below
shows the destructive rewrite options are *dominated* — they cannot by themselves
remove the bulk of GitHub's reported 1.69 GB, yet they invalidate every active
worktree and make ~889 open/closed PRs un-rebaseable and risk 764 release tags.
The bloat that GitHub reports lives almost entirely in server-owned `refs/pull/*`
(1778 PR refs) and un-gc'd unreachable objects — reclaimable via **GitHub Support
gc + forward-only hygiene**, with zero rewrite. Adopt Option (d).

---

## 1. Audit results (evidence)

All commands were run read-only on 2026-07-11 in a worktree branched from
`origin/main` (`fe38239c`). Reproduce from any clone.

### 1.1 Local size baseline

```
$ git count-objects -vH
count: 615
size: 7.60 MiB
in-pack: 47818
packs: 3
size-pack: 224.86 MiB      # reachable, compressed pack
```

Local reachable history compresses to **~225 MiB**. This is the floor a
GitHub-side gc alone would converge toward, and the ceiling of what any
history rewrite of local history could ever act on.

### 1.2 Top blobs across all history (`git rev-list --objects --all | cat-file --batch-check | sort`)

| Rank | Size (bytes) | Path | Live at HEAD? |
|-----:|-------------:|------|---------------|
| 1 | 56,696,866 | `audiobook-organizer-test` | **historical** (removed) |
| 2 | 56,696,498 | `audiobook-organizer-test` | historical |
| 3 | 56,694,258 | `audiobook-organizer-test` | historical |
| 4 | 56,093,618 | `audiobook-organizer-test` | historical |
| 5 | 56,056,834 | `audiobook-organizer-test` | historical |
| 6 | 33,545,278 | `test-full-v3-fixed.json` | historical |
| 7 |  9,291,954 | `mtls-bridge` | **LIVE** |
| 8 |  4,337,712 | `bin/copilot-agent-util-linux-x86_64` | historical (now gitignored) |
| 9 |  3,754,769 | `docs/security/audit-2026-05-03/raw/code-scanning.json` | historical |
| 10 | 3,687,152 | `bin/copilot-agent-util-macos-x86_64` | historical |
| 11 | 3,295,792 | `bin/copilot-agent-util-linux-arm64` | historical |
| 12 | 3,104,944 | `bin/copilot-agent-util-macos-arm64` | historical |
| 13 | 2,236,392 | `testdata/series_fix.json` | **LIVE** |
| 14 | 1,669,842 | `testdata/series_dump.json` | **LIVE** |
| 15+ | ~1.5M each ×126 | `internal/database/mocks/mock_store.go` | live (current only) |

### 1.3 Aggregate bytes by path across all history (uncompressed, MiB)

| Path | Σ MiB | Nature |
|------|------:|--------|
| `audiobook-organizer-test` | 269.16 | **Compiled test binary**, 5 versions, removed at HEAD |
| `internal/database/mocks/mock_store.go` | 110.80 | Generated mock, **126 versions** of churn; live source |
| `CHANGELOG.md` | 80.32 | Text churn; live source (compresses well, not a target) |
| `internal/server/server.go` | 64.38 | Text churn; live source |
| `internal/database/pebble_store.go` | 40.38 | Text churn; live source |
| `TODO.md` | 37.92 | Text churn; live source |
| `test-full-v3-fixed.json` | 31.99 | Test fixture, removed at HEAD |
| `mtls-bridge` | 8.86 | **LIVE compiled binary** (see §1.5) |
| `bin/copilot-agent-util-*` (4 arch) | ~14.2 | Prebuilt tool binaries, removed + now gitignored |

- **Unique blobs > 1 MiB: 91, totaling 432.96 MiB uncompressed** (compresses into
  the 225 MiB pack). Binaries (`audiobook-organizer-test`, `mtls-bridge`,
  `copilot-agent-util`) barely compress; text churn (`mock_store.go`, `CHANGELOG.md`,
  `server.go`) compresses ~5–10×, so its pack footprint is a fraction of its
  uncompressed sum.

### 1.4 Offender classes (named)

1. **Compiled binaries committed to history** — `audiobook-organizer-test`
   (269 MiB, the single largest class; a compiled Go test binary that should never
   have been committed), `mtls-bridge` (9.3 MiB, **still live**), and the four
   `bin/copilot-agent-util-*` prebuilt tools (~14 MiB, already removed + gitignored).
2. **Large JSON test fixtures** — `test-full-v3-fixed.json` (32 MiB, historical),
   `testdata/series_fix.json` + `series_dump.json` (~3.9 MiB, **still live**),
   `docs/security/.../code-scanning.json` (3.75 MiB, historical).
3. **High-churn generated source** — `mock_store.go` at 126 versions/110 MiB. This
   is live, wanted source; it is churn, not dead weight, and compresses well. Not a
   rewrite target — the forward lever is regenerating less often / `.gitattributes`.

### 1.5 Live vs historical verification

```
$ for p in mtls-bridge audiobook-organizer-test bin/copilot-agent-util-linux-x86_64 \
           testdata/series_dump.json testdata/series_fix.json test-full-v3-fixed.json; do
    git cat-file -e HEAD:$p 2>/dev/null && echo "LIVE $p" || echo "gone $p"; done
LIVE  mtls-bridge (9,291,954 bytes)
gone  audiobook-organizer-test
gone  bin/copilot-agent-util-linux-x86_64   # and: git check-ignore → now .gitignored
LIVE  testdata/series_dump.json (1,669,842 bytes)
LIVE  testdata/series_fix.json (2,236,392 bytes)
gone  test-full-v3-fixed.json
```

**Actionable-now findings (no rewrite needed):** `mtls-bridge` (9.3 MiB compiled
binary) is still tracked at HEAD and is **not** gitignored — it should be removed
going forward. The two live `testdata/series_*.json` fixtures (3.9 MiB) are the
candidates for external hosting per #1650's `testdata/fetch.go` idea.

---

## 2. The 1.69 GB vs 225 MiB discrepancy — RESOLVED with evidence

GitHub reports **1.69 GB**; the local reachable pack is **224.86 MiB** — a ~7.5×
gap of ~1.47 GB. Attribution (measured, not guessed):

| Bucket | Evidence | Removable by local rewrite + force-push? | Removable how |
|--------|----------|:----------------------------------------:|---------------|
| **A. `refs/pull/*` PR refs** | `git ls-remote origin 'refs/pull/*'` → **1778 refs** (≈889 PRs × head+merge). `gh pr list --state all` → highest PR **#1904**. | **NO** — these refs are created and owned by GitHub; a user cannot delete or force-push over them. | GitHub Support gc / ref prune only |
| **B. Unreachable objects (deleted/rebased branch tips not in any PR)** | GitHub retains loose objects until internal gc; local `git fsck --unreachable` shows 8,510 locally as an analogue. | **NO** — reachable-from-nothing on GitHub still isn't pruned by *your* push. | GitHub Support gc |
| **C. Reachable history (main + 764 tags)** | `git count-objects` pack = 225 MiB. | Partially — only this bucket. | Rewrite (this plan's options a/b/c) |
| **D. Actions caches / artifacts** | Not counted in repo size by GitHub (stated in #1650 background). | N/A | N/A |

Remote ref totals confirm bucket A dominates: `git ls-remote origin | wc -l` →
**3345 total remote refs**, of which **1778 are `refs/pull/*`**, versus only
~926 refs locally (162 branch tips + 764 tags). The compiled `audiobook-organizer-test`
binary (269 MiB uncompressed across 5 versions) is reachable from many old PR
branches; those PR head/merge refs pin it on GitHub **permanently until Support acts**.

**Precise mechanics (do not conflate the two):**
- Ordinary `git gc` removes only **unreachable** objects. Bucket A objects are
  *reachable* (from `refs/pull/*`), so even gc will not drop them until the PR refs
  themselves are pruned — a GitHub Support operation.
- A local `filter-repo`/BFG rewrite + `git push --force` rewrites **only your
  branches and tags** (bucket C). It does **not** touch `refs/pull/*` (bucket A) or
  server-side unreachable objects (bucket B). **Therefore a rewrite alone does not
  solve #1650's stated 1.69 GB** — it still requires the same GitHub Support gc that
  the forward-only option relies on, while adding all the destructive cost.

**Consequence for the recommendation:** the lever that actually reclaims the
~1.47 GB is a **GitHub Support-initiated gc / PR-ref prune**, obtainable *without any
rewrite*. That single fact reframes the decision below.

---

## 3. Options compared

Ceilings: gc-only converges GitHub size toward the local reachable pack (**~225 MiB**);
a full rewrite of reachable history that drops the binary classes could reach an
estimated **~100–120 MiB** pack — but only *after* the same Support gc, and only for
bucket C. The marginal rewrite gain over gc-only is ~100 MiB; the cost is enormous.

### (a) `git filter-repo` rewrite (preferred rewrite tool if any rewrite is chosen)
- **Expected size:** reachable pack ~100–120 MiB after removing `audiobook-organizer-test`,
  `mtls-bridge`, `bin/copilot-agent-util-*`, large historical JSON. **Does not reduce
  buckets A/B** — GitHub's reported total stays high until Support gc regardless.
- **Tooling risk:** Low tool risk (`git filter-repo` is the modern, recommended tool,
  strictly better than `filter-branch`), **high blast radius**: rewrites every commit
  SHA on main and all tag-reachable history.
- **Tag/release impact:** ⚠️ **Severe.** Verified: the 269 MiB `audiobook-organizer-test`
  blobs are reachable from tagged history — introducing commits sit in **v0.1.0 →
  v0.13.0+ and v0.56.0**; the live `mtls-bridge` is reachable from **373 tags**. A
  rewrite that touches those paths rewrites those tags → every rewritten tag gets a new
  SHA, breaking GoReleaser's `GORELEASER_CURRENT_TAG` assumptions and any consumer
  pinned to a tag SHA. 764 tags total are at risk.
- **GitHub-side steps:** force-push main + all rewritten tags; **still** open a Support
  ticket to gc buckets A/B; every consumer must re-clone.

### (b) BFG Repo-Cleaner
- **Expected size:** same ceiling as (a) for bucket C.
- **Tooling risk:** BFG is fast and simple for "delete paths/blobs above size," but
  **cannot preserve** commit-message/`refs/pull` nuance and is less precise than
  filter-repo for path-scoped rules; requires a JVM. Same tag-rewrite fallout as (a).
- **Tag/release impact:** identical severe tag breakage as (a).
- **GitHub-side steps:** identical to (a).

### (c) `git lfs migrate` (history-rewriting LFS adoption)
- **Expected size:** moves large blobs to LFS pointers; **git** repo shrinks, but the
  bytes move to LFS storage/bandwidth (paid, separate quota). Still a full history
  rewrite.
- **Tooling risk:** Adds a hard **LFS dependency** to every clone, CI runner, and the
  `//go:embed web/dist` build path; contributors without `git lfs` get pointer files.
  Same SHA/tag rewrite as (a)/(b).
- **Tag/release impact:** severe tag rewrite **plus** ongoing LFS operational burden;
  GoReleaser and CI must install/authenticate LFS.
- **GitHub-side steps:** force-push + Support gc + LFS budget provisioning.
- **Assessment:** worst fit — the offenders are *removable* dead binaries, not assets
  we must keep versioned, so paying LFS to retain them is illogical.

### (d) Forward-only: stop committing binaries/fixtures + external fixture host + GitHub Support gc — **NO rewrite** ✅ RECOMMENDED
- **Expected size:** GitHub Support gc converges the reported size toward the reachable
  pack (**~225 MiB**), reclaiming the bulk of the ~1.47 GB in buckets A/B — the part no
  rewrite can touch anyway. Forward hygiene keeps it from regrowing.
- **Tooling risk:** **Minimal.** No SHA changes, no tag rewrite, no re-clone, no
  invalidated worktrees/PRs.
- **Tag/release impact:** **None.** GoReleaser, all 764 tags, and `jdfalk-ci-bot` tag
  pushes are untouched.
- **GitHub-side steps:**
  1. Open a GitHub Support ticket requesting repository gc / PR-ref prune (buckets A/B).
     Reference this audit: 1778 `refs/pull/*` refs pin removed 56 MiB binaries.
  2. Remove the live `mtls-bridge` binary from HEAD; add it + build outputs to
     `.gitignore`; enable push protection / a pre-commit size guard.
  3. Externalize live large fixtures (`testdata/series_*.json`) via a `testdata/fetch.go`
     downloader (#1650) so they stop being committed.
  4. Optionally add `.gitattributes` to mark generated `mock_store.go` for less churn
     or `-diff` handling.

**Why (d) dominates (a)/(b)/(c):** the rewrite options *do not solve the stated
problem on their own* (they leave buckets A/B, which are ~1.47 GB of the 1.69 GB), and
they *still require* the same Support gc that (d) uses — while adding SHA rewrites, 764
at-risk tags, 14+ invalidated worktrees, and ~889 un-rebaseable PRs. That is a
strictly worse trade for ~100 MiB of extra pack savings.

---

## 4. Recommendation

**Adopt Option (d): forward-only hygiene + GitHub Support gc. Do not rewrite history.**

Reasoning, in order of decisiveness:
1. **A rewrite cannot remove the majority of the reported 1.69 GB.** ~1.47 GB is in
   GitHub-owned `refs/pull/*` (1778 refs) and server unreachable objects — only
   Support gc clears those, and that path needs no rewrite.
2. **The rewrite's own ceiling is small and its cost is large.** Best case it takes the
   *reachable* pack from 225 MiB to ~100–120 MiB (~100 MiB gain) while breaking tag
   SHAs across 764 tags (GoReleaser), invalidating all `.worktrees/` (14+ live now, plus
   other Claude sessions), and making ~889 PRs impossible to rebase.
3. **The real, safe wins are forward-only and available today:** delete the live
   `mtls-bridge` binary, gitignore build outputs, externalize the live `testdata/series_*`
   fixtures, and file the Support gc ticket.

**Only reconsider a rewrite if**, after the Support gc completes, the reported size is
*still* unacceptable **and** a stakeholder specifically needs the reachable-history
binaries purged — in which case use Option (a) `git filter-repo`, following the
coordination protocol in §5 in full, and accept the tag-rewrite fallout in §3(a).

---

## 5. Coordination checklist (the protocol IF the owner chooses a rewrite)

Execute strictly in order. Every step has a verification command. **Owner-gated —
do not begin without explicit sign-off.**

### Phase 0 — Freeze merges
- [ ] Announce freeze; enable branch protection "lock" or pause the merge queue.
      Verify no merges land: `gh pr list --repo falkcorp/audiobook-organizer --state open --json number,title`
- [ ] Pause `jdfalk-ci-bot` tag-push automation and any burndown runners.

### Phase 1 — Inventory everything the rewrite invalidates
- [ ] Worktrees (ALL become invalid — new SHAs): `git worktree list`
      (14+ active at planning time under `.worktrees/`, plus `/private/tmp/abo-mainbase`).
- [ ] Other live Claude sessions' worktrees — coordinate out-of-band before proceeding
      (a rewrite mid-session corrupts their branches).
- [ ] Open PRs (become un-rebaseable): `gh pr list --state open --limit 200 --json number,headRefName`
- [ ] Tags at risk: `git tag | wc -l` (**764**); confirm which the rewrite will touch:
      `git for-each-ref --contains <introducing-commit> refs/tags`
- [ ] `.standards/` submodule pointer (unaffected but verify it survives):
      `git submodule status`  → `-664ae68… .standards`
- [ ] Branch protection `required_linear_history` — confirm it will accept the
      force-push (may need temporary disable): `gh api repos/falkcorp/audiobook-organizer/branches/main/protection`
- [ ] SHA-pinned Actions are unaffected by SHA changes to *repo* history — confirm none
      pin an internal commit: `grep -rn 'uses:.*@' .github/workflows | grep falkcorp`

### Phase 2 — Backup (see §6; must pass before any rewrite)
- [ ] Two independent `git clone --mirror` copies verified (§6).

### Phase 3 — Rewrite (filter-repo; ⛔ owner-run only, NOT this task)
- [ ] Dry-run path list: `git filter-repo --analyze` then targeted
      `--path audiobook-organizer-test --path mtls-bridge --path-glob 'bin/copilot-agent-util-*' --invert-paths` on a **mirror clone**.
- [ ] Verify offenders gone + pack shrank: `git count-objects -vH | grep size-pack`

### Phase 4 — Force-push
- [ ] Temporarily relax branch protection; `git push --force-with-lease origin main`
      and rewritten tags.
- [ ] Re-enable branch protection immediately after.

### Phase 5 — Re-clone protocol for EVERY consumer
- [ ] Every developer/agent: delete local clone + all worktrees, fresh
      `git clone --recurse-submodules`. Old clones must **not** be pushed from (they
      would reintroduce old history). Verify: `git log --oneline -1` matches new main SHA.
- [ ] CI: clear any cached checkouts.
- [ ] File the GitHub Support gc ticket (buckets A/B remain until then).

### Phase 6 — Un-freeze
- [ ] Re-open merges; resume `jdfalk-ci-bot`; verify a test PR rebases and CI is green.

---

## 6. Backup strategy

Before any rewrite, create **two independent mirror backups** and verify both.

- [ ] Backup 1 — the server (<server>):
      `git clone --mirror https://github.com/falkcorp/audiobook-organizer.git /srv/backups/audiobook-organizer-preRewrite-$(date +%Y%m%d).git`
- [ ] Backup 2 — a second host / offline copy (e.g. a lenserv node or external disk):
      `git clone --mirror https://github.com/falkcorp/audiobook-organizer.git /path/to/second/audiobook-organizer-preRewrite-$(date +%Y%m%d).git`
- [ ] Verify each mirror is complete and fsck-clean:
      `git -C <mirror> fsck --full` (expect no missing objects) and
      `git -C <mirror> for-each-ref | wc -l` (expect ≈ 3345, incl. `refs/pull/*`).
- [ ] Record the pre-rewrite main SHA and tag list:
      `git -C <mirror> rev-parse main` and `git -C <mirror> tag > tags-preRewrite.txt`.
- [ ] **Retention:** keep both mirrors until the owner signs off that the rewritten
      repo is healthy in production AND at least one full release cycle has passed. Do
      not delete backups on the same day as the rewrite.

---

## 7. Rollback

The mirrors from §6 are the authoritative pre-rewrite state.

- **If the rewrite must be undone before consumers re-clone:** force-push the mirror
  back over origin: `git -C <mirror> push --force --mirror origin` (restores main +
  all tags to pre-rewrite SHAs). Verify `git ls-remote origin main` matches the
  recorded pre-rewrite SHA.
- **If undone after a GitHub Support gc has run:** some server-side objects may already
  be pruned. Re-pushing the mirror restores all *reachable* objects (main + tags +
  their blobs) from the local mirror, which is complete; `refs/pull/*` history that
  Support pruned is not restorable by a push (it never was user-controllable) — but it
  is not needed for a working repo. Open a follow-up Support ticket only if PR-ref
  archaeology is required.
- **Reintroduction hazard:** ensure no developer force-pushes from a stale pre-rewrite
  clone during rollback — that would re-mix old and new history. Freeze merges during
  any rollback exactly as in §5 Phase 0.

---

## Headline numbers (for the owner)

- GitHub reports **1.69 GB**; local reachable pack is **224.86 MiB**. The ~1.47 GB gap
  is **1778 `refs/pull/*` PR refs + un-gc'd unreachable objects**, reclaimable by
  **GitHub Support gc without any rewrite**.
- Largest offender: **`audiobook-organizer-test`**, a compiled test binary, **269 MiB
  across 5 historical versions**, reachable from **tags v0.1.0–v0.56.0**.
- Live cleanup available today, no rewrite: remove **`mtls-bridge`** (9.3 MiB live
  binary), externalize **`testdata/series_*.json`** (3.9 MiB live fixtures), gitignore
  build outputs, file the Support gc ticket.
- A history rewrite would rewrite up to **764 tags** (breaking GoReleaser), invalidate
  **14+ live worktrees**, and un-rebase **~889 PRs**, for only ~100 MiB of extra pack
  savings over gc-only — a dominated trade.

STATUS: STOP-FOR-HUMAN — no rewrite executed; awaiting owner decision.
