<!-- file: docs/diagnostics/2026-08-24-scan-adds-no-new-books-root-cause.md -->
<!-- version: 1.0.0 -->
<!-- guid: 60ab5a31-b0bf-4055-985b-a4b16604e8a6 -->
<!-- last-edited: 2026-08-24 -->

# Why the scan adds no new books

**Answer: the scan root does not contain your new books. The scanner is working
correctly and faithfully scanning a folder that has nothing new in it.**

This is a configuration mismatch on the server, not a bug in the code. **No code
change will fix it.** Nothing here has been changed on the server — the decision
below is yours.

## The mismatch

| | Path |
|---|---|
| Where the scanner looks (`root_dir`) | `/mnt/bigdata/books/audiobook-organizer` |
| Where your new books actually are | `/mnt/bigdata/books/newbooks/audiobooks/` |

`newbooks` is a **sibling** of `audiobook-organizer`, not a folder inside it.
A walk that starts at `audiobook-organizer` can never descend into `newbooks`.

Books currently sitting unseen in `/mnt/bigdata/books/newbooks/audiobooks/` include
*Star Trek [TOS] The Captain's Oath*, *Lindsay Buroker — Under the Ice Blades*,
*The Hierarchies*, *pirateaba — Blood of Liscor (The Wandering Inn, Book 8)*, and
several Skyler Grant titles.

## Two settings that make it worse

Both measured live from `/api/v1/config`:

- `auto_scan_enabled = false`
- `scan_on_startup = false`

So nothing scans on its own. Even pointed at the right folder, a scan only happens
when you ask for one.

## There is no separate "incoming books" setting

Every directory-ish key in the live config was checked. `root_dir` is the **only**
scan root — there is no watch folder, no import folder, no inbox. So this is not
"the inbox setting is wrong"; it is "there is one root and it points somewhere else."

## Your options

**Option A — point the root at the parent, `/mnt/bigdata/books`.**
One setting, and both the organized library and `newbooks` fall under it.
**But** that also brings 27 other top-level entries into scope, including `bkup`,
`New folder`, `CK.rar`, `Mp3tag.zip`, `Ansible From Beginner to Pro.pdf` and a
`booksonic` tree. Those would become candidates for scanning and organizing. That is
a real consequence and it is why this was not changed for you.

**Option B — scan `/mnt/bigdata/books/newbooks` as a one-off targeted scan**, leaving
`root_dir` alone. Lowest risk, nothing permanent changes. Whether a targeted scan is
permitted outside `root_dir` still needs confirming.

**Option C — move or link the new books under the existing root**, so
`/mnt/bigdata/books/newbooks/audiobooks` lands inside
`/mnt/bigdata/books/audiobook-organizer`. Changes no config at all.

Recommendation: **B to get tonight's books in, then decide A vs C at leisure.**

## The real defect, and it is ours

A scan of a root with nothing new in it **completes and reports success**. From your
chair that is indistinguishable from a scan that is broken — which is exactly why this
went unnoticed while the books piled up.

That is the same failure shape as the maintenance plugin silently registering 0 of 105
operations when its directory is unset: the system does nothing, says nothing, and
calls it success. A scan that finds zero new candidates should say so loudly, and it
should name the root it actually looked in, so "I scanned and got nothing" arrives as
"I scanned /mnt/bigdata/books/audiobook-organizer and got nothing."

## Corrections to two earlier claims made during this investigation

Recorded so neither is repeated:

1. **`AUDIOBOOK_ROOT_DIR=/var/lib/audiobooks` in the systemd unit is NOT the cause.**
   That directory genuinely does not exist, which made it look decisive. But
   `viper.AutomaticEnv()` maps that variable to the key `audiobook_root_dir`, not
   `root_dir`, so it is silently ignored. It is config litter worth removing; it is
   not the bug.
2. **The service is NOT stale or dead.** A `timeout` truncated a large journal dump
   mid-stream, so the "nothing logged since Aug 11" reading was an artifact of reading
   the oldest portion rather than the newest. The service restarted 2026-08-24 17:55:20
   EDT and is healthy: `/api/health` 200, 61,046 books served.

## How this was established

- `GET /api/v1/config` → `root_dir`, `auto_scan_enabled`, `scan_on_startup`, and the
  absence of any other directory key.
- `ls` of both the configured root (4,602 entries) and `/mnt/bigdata/books` (28 entries).
- `find /mnt/bigdata/books -maxdepth 3 -type d -newermt 2026-08-01` → the `newbooks` tree.
- `systemctl show` → unit environment and real start time.
