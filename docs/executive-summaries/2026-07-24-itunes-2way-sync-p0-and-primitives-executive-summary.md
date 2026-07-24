<!-- file: docs/executive-summaries/2026-07-24-itunes-2way-sync-p0-and-primitives-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3a7f9c21-8b64-4d50-9e17-2c6b5a0e1d84 -->
<!-- last-edited: 2026-07-24 -->

# Executive Summary: Making Two-Way iTunes Sync Safe to Build

**Shipped:** PRs [#2041](https://github.com/falkcorp/audiobook-organizer/pull/2041),
[#2042](https://github.com/falkcorp/audiobook-organizer/pull/2042),
[#2043](https://github.com/falkcorp/audiobook-organizer/pull/2043),
[#2044](https://github.com/falkcorp/audiobook-organizer/pull/2044),
[#2045](https://github.com/falkcorp/audiobook-organizer/pull/2045) — all merged.
**Related spec:** `docs/specs/2026-07-23-itunes-2way-sync-system-design.md`,
findings `docs/specs/2026-07-23-itunes-2way-p0-findings.md`.

## In plain language

We are working toward keeping your iTunes library and the audiobook organizer in
sync in **both** directions. Before writing any code that changes your live iTunes
library, we spent this round **proving the changes would be safe** — and we found and
fixed the one thing that would have blocked it.

What we established, each backed by running against a copy of your real 97,999-track
library:

- **We won't quietly delete tracks that look like leftovers.** We checked whether
  old "merged-away" duplicate books had left orphan tracks in iTunes that we could
  clean up. The honest answer: your library keeps **no reliable record** of what was
  merged into what, so we **cannot** safely auto-delete anything — and we won't. The
  cleanup feature is deliberately switched off rather than guessing.

- **We can't accidentally touch your music or podcasts.** The organizer only ever
  points at audiobook tracks — we verified there is **not a single** case where it
  would rewrite a music or podcast track by mistake.

- **Moving a book's file won't lose your place.** When the organizer updates where a
  book's file lives, we proved — byte for byte, on every one of your 97,999 tracks —
  that **nothing else changes**: not your play position/bookmark, not play counts,
  ratings, or dates. Only the file location moves; everything else is left exactly as
  iTunes had it.

- **We added a safety net that catches any mistake before it lands.** Every future
  update will be checked track-by-track against what it was *supposed* to do; if
  anything unexpected changed, the update is automatically rolled back.

- **We found and fixed the one real blocker.** A safety check meant to catch a
  specific past corruption was, as a side effect, refusing to write your library at
  all — because your iTunes library legitimately lives in a folder whose name it
  treated as suspicious. We taught that check to tell "this is your library's own
  home folder" apart from a genuine problem, so it now allows your library while
  still blocking the real danger.

## Why it matters

Your iTunes library is externally managed and easy to damage irreversibly. Rather
than build the sync and hope, we made "prove it's safe first" the gate. The result:
the risky guesswork (bulk deletion) is off by design, the two things a sync must
never do (touch non-audiobooks, disturb bookmarks/metadata) are proven impossible,
and there's an automatic rollback if a future change ever misbehaves. The actual
two-way sync can now be built on top of these guarantees rather than on assumptions.

## What's next

The building blocks are all in place. The remaining work is to assemble them into
the actual sync cycle (read → plan the file-location updates → write with all the
guards → verify → keep or roll back). Nothing about that step re-opens the safety
questions above — they're settled.
