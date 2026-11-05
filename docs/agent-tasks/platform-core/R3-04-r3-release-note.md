<!-- file: docs/agent-tasks/platform-core/R3-04-r3-release-note.md -->
<!-- version: 1.0.6 -->
<!-- guid: 9333a60d-0082-4464-a9b9-c1a2d1cf879b -->
<!-- last-edited: 2026-11-05 -->

# R3-04 — The R3 release note: the upgrade floor (install the R2 release, by its tag, first; exit 78, launchd's 60 s sleep), the `.encryption_key` deletion with the macOS and Docker notes, `library.bleve`'s deletion and the removed `--db-type` and SQLite flags

| Field | Value |
|---|---|
| ID | R3-04 |
| Release | R3 |
| Tier | **T1** (Sonnet / medium): a release note from texts the spec fixes; one new Markdown file |
| Wave | W45 (beside no other row; file-disjoint) |
| Depends on | OWNER-05 (the R2 release is cut and deployed, so its tag is known), R3-01, R3-02, R3-03, R3-05, F9-03 |
| Spec | 09 §9.4 R3 ("Content": the release-note text, verbatim), §9.2.2 ("The R3 floor": exit 78, launchd's 60 s sleep, the macOS and Docker notes, the host-loss procedure), §9.6 row 21 (the shell's paths; F9 ships in R3 by default); 06 §6.11.1 step 6 (the no-op flags deleted) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. This PR writes the R3 release note, which
OWNER-06 prepends to the generated R3 release notes when it cuts R3 (CLAUDE.md: prepend, never replace), and carries
R3's executive summary and TODO check-offs (CLAUDE.md "Post-Task Hygiene"; plan §(n) item 4).

**The shell ships in R3.** F9 ships in R3 by default (plan §(n); 09 lets F9 slip to R3): F9-03 merges after OWNER-05
cuts R2, outside the freeze, and R2-01's note leaves the shell out. So this note always carries the new page
addresses (step 5).

**Merge order.** Write the note as soon as the R2 tag is known, but merge this PR last among R3's agent rows: only
after R3-01, R3-02, R3-03, R3-05 and F9-01 … F9-05 have merged, so that the executive summary and the check-offs cover
all of R3 (rebase first and redo the check-offs). Until then keep it open and say so in the PR description.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-r3-04-note" -b agent/platform-core-r3-04-note origin/main
cd "$REPO/.worktrees/pc-r3-04-note"
git rebase origin/main
```

## Inputs you need before writing

1. **The R2 tag.** Read it only from OWNER-05's tracking-issue record (its `decision: kept (R2 release <tag>, cut from
   <rc tag>)` line), the same source R3-02 writes into `UpgradeFloor.InstallFirst`. Never take it from
   `gh release list` or the newest release: after an R2 patch release that would name a different tag from the one the
   binary prints in `install <tag> first`. Call it `<R2 tag>` below and write the literal tag everywhere the texts say
   `<R2 release>` or `<R2 tag>`. If the record names no production tag, STOP and report "R3-04 needs the R2 tag from
   OWNER-05".
2. **The shell's page paths.** The new paths and the absorbed pages come from 07 §7.11 (the row "all other
   pages/components": URLs move to `/books/...`, and `/dashboard → /`, `/files → /books/files`, `/diagnostics →
   /system/diagnostics`, `/operations → /activity`, `/maintenance → /system`) and from F9-03's
   `seams.d/F9-03.md` or merged PR description, which records the full former → new path table. F9-03 merges before
   this PR (merge order above); if its table and 07 §7.11 disagree, STOP and report the rows.

## File

| Action | Path |
|---|---|
| create | `docs/system/r3-release-note.md` (file name coined by the plan, plan §(l) item 14) |
| create | one `changelog.d/` fragment (headerless) |
| create | `docs/executive-summaries/<today>-r3-cleanup-executive-summary.md` (4-line header; outside the inventory's file list, like the fragment: plan §(n) item 4) |
| modify | `TODO.md` (check-offs only; outside the inventory's file list for the same reason) |

Touch nothing else.

## Re-verify before editing (expected results at your base)

```bash
# The file does not exist yet. Expected: 0
ls docs/system/r3-release-note.md 2>/dev/null | wc -l
# The spec texts you copy. Expected: 1 line each
grep -c 'This release needs a database that ran <R2 release>' docs/specs/platform-core/09-abo-on-core.md
grep -c 'The first start of this release deletes the old settings key file' docs/specs/platform-core/09-abo-on-core.md
grep -c 'the R3 release note tells' docs/specs/platform-core/09-abo-on-core.md
# The launchd sleep and Docker restart policy the notes refer to. Expected: >= 1 each
grep -rn 'XPC_SERVICE_NAME' --include='*.go' . core/ | head -2
grep -n 'restart: unless-stopped' docker-compose.yml
```

STOP if any of the three spec greps returns 0 (the spec text moved: report it, do not paraphrase).

## Steps (write exactly these sections, in this order)

1. Header: the 4-line file header (`file`, `version: 1.0.0`, a fresh `guid` from
   `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`), then `# Release notes: R3 (cleanup and the upgrade
   floor)`.
2. `## Before you upgrade` — copy these two paragraphs from 09 §9.4 R3 "Content" **verbatim**, replacing only
   `<R2 release>` with the literal R2 tag:
   - "This release needs a database that ran <R2 release>. An older database stops the service at start with the
     message `install <R2 release> first`: install that release, start it once, then upgrade."
   - "The first start of this release deletes the old settings key file `.encryption_key` and the old search index
     directory. Keep your off-host copy of `.encryption_key` with any backup taken before <R2 release>: restoring
     such a backup needs it, and needs <R2 release> installed first."
3. `## If the service will not start` — from 09 §9.2.2 "The R3 floor", in plain sentences:
   - the service exits with code 78 and its last log line is `install <R2 tag> first` (under Docker, that line in
     `docker logs` is the message);
   - systemd: the unit does not restart it (`RestartPreventExitStatus=78`);
   - **macOS (launchd):** the binary waits 60 s before exiting 78, because launchd would otherwise restart it at once;
     stop the job with `launchctl bootout gui/$(id -u)/<your job label>` before installing the R2 release;
   - **Docker:** `restart: unless-stopped` restarts the container; stop it (`docker compose stop`) before installing
     the R2 release;
   - then: install the R2 release (the tag above), start it once so it migrates the database, and upgrade again.
4. `## Restoring an old backup` — 09 §9.2.2's host-loss procedure for a pre-R2 archive, as numbered steps: install the
   R2 release (R3 refuses the archive at staging); put the archive's off-host `.encryption_key` and the gap-fill
   `config.yaml` into the state directory and beside the database; restore with `storage restore-offline --archive`;
   start it once so it migrates; then upgrade. Without the off-host `.encryption_key` the R2 release refuses to
   migrate it, because R3 deleted the host's copy.
5. `## Removed options` — one bullet: "The `--db-type` and `--enable-sqlite3-i-know-the-risks` command-line flags are
   removed (they had no effect since PebbleDB became the only backend). A service definition or script that still
   passes either flag fails to start with `unknown flag`: remove the flag." (This sentence is coined here: 09 §9.4 R3
   lists the deletion but fixes no text; 06 §6.11.1 step 6 deletes them; D10 keeps no shim.)
   Then `## New page addresses` (the shell, F9): 09 §9.6 row 21's rule in plain sentences (no old-URL redirect; a
   former bookmark opens the not-found page; update your bookmarks), then the path table from input 2 (former → new
   path), and one sentence that seven interface preferences (theme excepted) reset once with this release (07 §7.11,
   the `lib/storageKeys.ts` row: "listed in that release's note").
6. **R3's executive summary and TODO check-offs.** One dated
   `docs/executive-summaries/<today>-r3-cleanup-executive-summary.md` (name pattern from
   `docs/process/executive-summaries.md`; plain language: the upgrade floor and what to install first, the old key
   file and search index deleted, the removed flags, the new page addresses), and every `TODO.md` entry an R3 or F9
   row finished checked off by hand (grep `TODO.md` for each row's ID and title; CLAUDE.md: assembly may already have
   folded a fragment in).
7. One `changelog.d/` fragment; acceptance.

## Acceptance (commands and expected output)

```bash
F=docs/system/r3-release-note.md
# Header present. Expected: 4
head -4 "$F" | grep -c '^<!-- \(file\|version\|guid\|last-edited\):'
# No placeholder left. Expected: 0
grep -c '<R2 release>\|<R2 tag>' "$F"
# The two verbatim paragraphs, with the tag filled in. Expected: 1 and 1
grep -c 'This release needs a database that ran ' "$F"
grep -c 'The first start of this release deletes the old settings key file `.encryption_key`' "$F"
# The platform notes and the flags. Expected: >= 1 each
grep -c 'launchctl bootout' "$F"; grep -c 'docker compose stop' "$F"; grep -c -- '--db-type' "$F"
# Exit code named. Expected: >= 1
grep -c '78' "$F"
# The shell's addresses. Expected: >= 1 each
grep -c '^## New page addresses' "$F"; grep -c '/books/' "$F"
# R3's executive summary. Expected: 1
ls docs/executive-summaries/ | grep -c -- '-r3-cleanup-executive-summary\.md$'
# The note's tag equals R3-02's InstallFirst literal. R3-02 runs beside this row in W44: run this once R3-02 has
# merged (rebase first); if it has not merged when this PR is otherwise ready, wait for it. Expected: no output
TAG="$(grep -rhoE 'InstallFirst: *"[^"]+"' internal/books/ | head -1 | sed -E 's/.*"([^"]+)"/\1/')"
test -n "$TAG" || echo "R3-02 has not merged: no InstallFirst literal"
grep -q "install $TAG first" "$F" || echo "note does not name $TAG"
grep -oE 'install [^ `]+ first' "$F" | sort -u | grep -vx "install $TAG first"
# Gate. Expected last line: ✅ All CI checks passed!
make ci
```

## Out of scope

- Cutting the release and prepending the note (OWNER-06).
- Any code change (R3-01, R3-02, R3-03, R3-05).
- The R2 release note (R2-01).
- Executive summaries for R1 and R2 (R1-31, R2-01).

## Rollback

Revert the PR.

## Repo rules

- Worktree off fresh `origin/main`; the new file carries the 4-line header; the fragment carries none.
- Conventional commit `docs(release): R3 release note`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; no release or flip trailer (09 §9.2.1 names none);
  `gh pr merge <n> --rebase`; never squash; never `--admin`.
- No hostnames, user names or machine paths in the note: write `<your job label>`, not a real launchd label.
