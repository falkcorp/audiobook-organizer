<!-- file: docs/audits/2026-08-17-orphan-destination-rows-root-cause.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6b21d4e8-90fa-4c37-a5e2-8d3419cb70f1 -->
<!-- last-edited: 2026-08-17 -->

# Where the orphan destination rows come from

**Question asked:** why did the organizer record `book_file` rows pointing at
paths it never populated? `maintenance.missing-file-repair` deletes those rows;
this is about what creates them.

**Status:** mechanism identified in code and quoted below. **Not yet confirmed as
the cause of the observed population** — that needs the audit data, which is
gated on the running scan. The discriminating test is at the end.

## The mechanism

`internal/organizer/service.go:1254`, `resolveOrganizedFilePath`, has three
branches. Its own doc comment states the third one plainly:

> The plan says where organize INTENDED to put the file; it does not prove the
> copy happened. […] Disk is the tiebreaker: prefer the target if it exists, fall
> back to the source if THAT still exists, **and only then take the plan on faith
> (the file is missing either way, and the planned path is where a restore should
> put it).**

So when **neither** the planned target **nor** the source exists on disk, the
function returns the planned path. That value goes straight into a new row at
`service.go:1444`:

```go
newBF.FilePath = resolveOrganizedFilePath(bf.FilePath, planned, log)
…
_ = orgSvc.db.CreateBookFile(&newBF)
```

That is, precisely, a destination row recorded for bytes that were never written.

Two properties of this path match the observed shape of the problem:

- **A planned path is always under `RootDir`** — it is built by `planTargetPaths`
  from `config.RootDir` plus the folder/file naming patterns. So every row
  created this way points into the organizer's own tree, and none can point into
  the iTunes tree. That matches "all missing rows under the organizer's tree,
  none under iTunes" exactly.
- **`CreateBookFile` is unconditional.** The `_ =` discards the error, and no
  branch above it declines to create the row.

### The single-file case has no disk check at all

Immediately below, for non-directory books:

```go
} else if !isDir {
    newBF.FilePath = newPath
}
```

`newPath` is the organizer's returned target. `OrganizeBook` returns a target
path with a nil error from several no-op branches, including
`if book.FilePath == targetPath { return targetPath, "", nil }` — which does
**not** stat the file. A book whose row already names its organized path, whose
bytes have since gone, takes that branch and the new row is written from it.

## Why #2479 is the suspect that makes this fire in bulk

`resolveOrganizedFilePath` **recomputes** a plan for an organize that already
ran, and its doc comment names the fragility:

> This RECOMPUTES a plan for an organize that already ran, so it is only correct
> while both calls see the same inputs.

#2479 changed those inputs. Before it, `OrganizeBookDirectory` "applied the
folder pattern and never the file pattern" and kept `filepath.Base(src)` as the
destination filename; after it, both paths run `planTargetPaths` and the **file
naming pattern decides the filename**. So for any multi-file book organized
*before* #2479:

1. The bytes were moved to `<dir>/<original basename>` by the old planner.
2. The source path no longer exists — the move consumed it.
3. A later pass recomputes `planned` with the **new** planner, yielding
   `<dir>/<pattern-derived name>`, which was never written.
4. Target missing, source missing → third branch → the row records the
   pattern-derived path.

The file is sitting right there in the same directory under its old name. The
row names a sibling that does not exist.

This is consistent with the memory note that #2479 "only MOVED the divergence, to
which rows each caller passes," and with the expectation of "a one-time
library-wide move."

## What this does NOT establish

- **It does not prove #2479 produced the 41.8%.** That figure is extrapolated
  from a 120-book sample, and this document identifies a mechanism, not a
  measured population. Rows could equally come from genuine deletions, failed
  copies, or an unmounted share during an earlier run.
- **It does not date the rows.** If most orphan rows predate 2026-08-15, #2479
  is not the trigger and the third branch has simply always done this.

## The discriminating test

Run once the scan is terminal, alongside `maintenance.missing-file-audit`:

For a sample of missing `book_file` rows, list the **actual directory contents**
of `filepath.Dir(row.FilePath)`.

| observation | conclusion |
|---|---|
| the directory holds a file of the same size/extension under a **different** name, and the missing name matches the current file-naming pattern | **#2479 confirmed.** The bytes exist; only the name in the row is wrong. These rows should be **repointed, not deleted.** |
| the directory is empty or absent | not this mechanism — genuine loss or a never-run copy. Deletion is the right repair. |
| the directory holds the file under the **same** name | the row is fine and the audit's probe is wrong — re-verify the instrument. |

🔴 **This changes the repair.** `maintenance.missing-file-repair` deletes dead
rows. If the first row of that table is what production looks like, deleting is
the **wrong** action for those books — the bytes are present under a different
filename, and the correct repair is to repoint the row (or re-run organize so the
file moves to the planned name). Deleting would discard the only pointer to a
file that still exists.

**Recommendation: run the audit and this directory-listing check BEFORE running
`missing-file-repair` with `{"apply": true}`,** even though the repair is already
approved. The approval was given against the understanding that these rows point
at nothing; if they point at something under a different name, that premise is
wrong for some fraction of them.

## Suggested fixes, once the population is known

1. **Make the third branch loud.** Taking the plan on faith is defensible for a
   restore hint, but it currently creates an unmarked row indistinguishable from
   a verified one. It should at minimum warn (it does not today — only the
   source-exists branch logs), and preferably set a flag the audit can select on.
2. **Look for the file under its old name before giving up.** In the #2479 case
   the bytes are in the directory the plan already computed. A single `ReadDir`
   of the target directory, matching on size, would recover the row instead of
   orphaning it.
3. **Stat before trusting `newPath` in the single-file branch**, which has no
   disk check at all.
