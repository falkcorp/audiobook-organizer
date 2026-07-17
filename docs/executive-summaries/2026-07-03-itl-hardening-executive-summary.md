<!-- file: docs/executive-summaries/2026-07-03-itl-hardening-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: d6272ad6-0339-46b6-af79-209576b0cf24 -->
<!-- last-edited: 2026-07-17 -->

# Executive Summary: iTunes Library (.itl) Write-Back Hardening

**Shipped:** PR [#1793](https://github.com/falkcorp/audiobook-organizer/pull/1793), merged `496a3ba`
**Related spec:** [2026-07-03-itl-identity-and-external-truth-hardening.md](../archive/2026-07-consolidation/specs/2026-07-03-itl-identity-and-external-truth-hardening.md), [2026-07-03-itl-format-and-foolproofing-deep-dive.md](../archive/2026-07-consolidation/specs/2026-07-03-itl-format-and-foolproofing-deep-dive.md)

## Executive Summary

We hardened the iTunes library file (`.itl`) reader/writer against several
classes of data corruption that could have silently destroyed or scrambled a
user's iTunes library during automated write-back. The work included:

- Fixing a **reversed encoding bug** that was actively writing text in the
  wrong character set into music libraries.
- Adding an **identity fingerprint system** so the tool can tell "this is the
  same library, just updated" from "this is a totally different library that
  happens to share a filename."
- Closing a **safety bypass** where a "force" flag could skip corruption
  checks it should never skip.
- Adding a **plausibility check** so a write that would nuke way more tracks
  than expected gets blocked instead of applied.
- Fixing **three places where errors were being silently swallowed**, which
  had been causing wasted retries and masked failures in the background
  write-back service.
- Writing two detailed spec documents so this format knowledge doesn't live
  only in one person's head.

Verified against real data: a 90,900-track production library and a
95,238-track live library both now pass every safety check cleanly — the
first time that's ever been true.

## What changed, in plain terms

### 1. The reversed-encoding bug (K16)

**What it was:** iTunes text data can be stored two ways — as "special
characters" text (like foreign accents) or as "plain English" text. The code
that reads and writes this had the two swapped: it was writing "plain
English" data with the flag that means "special characters," and vice versa.

**Why it mattered:** iTunes would read these swapped flags and interpret
filenames and metadata using the wrong alphabet, turning readable text into
garbled nonsense — or worse, corrupting file paths so iTunes couldn't find
the actual audio files anymore.

**The fix:** Found the bug by writing a small test that looked at the raw
bytes iTunes itself produces, confirmed the flags were backwards, and
swapped them back to match what iTunes actually expects.

### 2. Library identity fingerprinting (K13)

**What it was:** Before, the tool only checked "does this file have the same
internal ID as last time?" to decide whether it was safe to write. But that
ID doesn't change even if iTunes fails and rebuilds a totally empty library
from scratch — a case that actually happened to this user.

**Why it mattered:** Without this, the tool could write data meant for the
user's real 90,000-track library into an empty rebuilt one, thinking they
were the same file.

**The fix:** Added a "fingerprint" file that's saved alongside each library —
it stores a sample of track IDs, counts, and a checksum. Before writing, the
tool compares the current file against this fingerprint. If they don't
overlap enough, it refuses to write instead of guessing.

### 3. No more bypassing safety checks (K15)

**What it was:** There was a "Force" setting meant for emergency overrides.
But it was accidentally letting a rebuild operation skip the identity check
entirely.

**Why it mattered:** "Force" should mean "override a warning I've reviewed,"
not "skip the check that would have caught a real problem."

**The fix:** Changed the code so identity verification always runs, no
matter what Force is set to.

### 4. Blocking implausible changes (K14)

**What it was:** A new check that estimates how many tracks *should* change
based on the operation being performed, then compares that to how many
tracks *actually* changed.

**Why it mattered:** If a bug caused a write operation to accidentally wipe
out 80% of a library when it should have only touched a handful of tracks,
nothing was stopping that from happening.

**The fix:** The tool now flags and blocks writes where the actual change is
wildly bigger than expected, with a tolerance for normal variation.

### 5. Catching if something else touched the library first (K17)

**What it was:** A check that records a checksum of the library file every
time our tool writes to it, then compares that checksum the next time it's
about to write.

**Why it mattered:** If iTunes (or the user) modified the library in between
our tool's writes, our tool was blind to that and could overwrite those
changes without knowing they happened.

**The fix:** Now it logs a warning when it detects the file changed from
something other than itself, so that activity isn't invisible.

### 6. Fixed three silently-swallowed errors

**What it was:** In the background service that writes changes back to
iTunes, three different failure paths were just logging a warning and moving
on instead of actually retrying or stopping.

**Why it mattered:** This caused wasted retry attempts, and in one case,
could drop a write entirely without ever succeeding or alerting anyone.

**The fix:** Failures now get retried up to a limit, and if they keep
failing, the tool logs a clear error and stops instead of quietly giving up.

### 7. Spec documentation

**What it was:** Two new documents in `docs/specs/` — one is a full technical
map of the `.itl` binary format (down to byte offsets), and the other
formally defines each corruption scenario (K13–K17) and the rule meant to
catch it.

**Why it mattered:** This knowledge previously existed only as scattered code
comments and one person's memory. Now it's written down so future work (or
future you) doesn't have to rediscover it from scratch.
