<!-- file: docs/executive-summaries/2026-08-08-two-controls-that-lied-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4b81e07c-59da-42f6-96c3-8e1d7a3f0b52 -->
<!-- last-edited: 2026-08-08 -->

# Two controls that lied — and a release that could not say what was in it

## The library told you it was empty

Whenever the server was restarted, the library page would announce **"No
Audiobooks Found"**. Not "loading", not "can't reach the server" — it stated
plainly that you owned nothing, to someone with forty-four thousand books.

It was never true. The page had simply asked the server for your books, got no
answer, and drew the wrong conclusion.

Two things made it that bad. When a request failed, the page **deliberately
threw away the books already on screen** — so a brief hiccup mid-session
blanked a shelf that had been sitting there perfectly fine. And the page had no
way to tell "the request failed" apart from "there is genuinely nothing here";
those two situations arrived looking identical, and only one of them has an
honest answer.

This was not rare. After an update the server spends roughly **forty seconds**
loading the library into memory before it can answer anything. Every single
update hit this. Anyone looking at the page during that window was told their
collection was gone.

**What it does now.** A failed request leaves your books exactly where they
are. If there is nothing to show yet, the page says it is still loading, notes
that this is normal for up to a minute after an update, and quietly keeps
trying on its own until the server answers. It repopulates without you touching
anything. The only circumstance in which it will now say the library is empty
is when the server successfully answered and genuinely had nothing to send —
which, for a first-time setup, is the one case where that message is the right
one and the setup instructions underneath it are worth reading.

## The "In Progress" button did nothing at all

Clicking **In Progress** in the sidebar did not move the highlight, did not
filter anything, and did not add so much as a marker that a filter was active.
It was, in the owner's words, "kind of a useless button". **Finished** was
broken in exactly the same way.

These turned out to be two entirely separate faults that happened to produce
one symptom.

The highlight could never move, no matter what you clicked, because of how the
sidebar decided which entry to light up — the comparison it used could only
ever match "All Books", which is why that one always looked selected.

Separately, the click itself was being **silently discarded**. The page has a
mechanism for ignoring changes it made to its own address bar, so it does not
react to its own echo. That mechanism got permanently stuck in the "ignore"
position shortly after the page loaded, and from then on it threw away every
genuine navigation as though it were an echo. Your click was read, discarded,
and the address bar quietly rewritten to undo it.

The revealing detail: **"All Books" kept working the whole time**, from the same
machinery, because of where its handling sits relative to that stuck switch.
That asymmetry is what confirmed the diagnosis.

**Both are fixed**, and both now have automated tests that click the real
buttons in a real browser — so if either breaks again, a test says so instead of
you noticing.

Worth knowing: the part of the system that does the actual filtering was never
at fault. It was correct all along; the instruction simply never reached it.

## The release notes could not say what had changed

Separately, every release this project produced listed its own contents as
**"No commits available"**. The draft for the next release had also quietly
stopped updating — it was still describing work from weeks earlier — and three
duplicate, half-written drafts had piled up behind it.

The cause was a stale pointer: the release process was running a months-old
copy of the script that writes the notes, and that old version compared each
release against **itself**, which is empty by definition. It also ignored any
attempt to tell it otherwise, which is why earlier corrections appeared to be
accepted and then did nothing.

**What happened.** The pointer is fixed, so future releases will describe
themselves properly. The three broken drafts are gone. And **v0.218.0 has been
published** — the first proper release since July 6th — with its notes
reconstructed by hand: **484 changes**, including 142 fixes and 95 new
capabilities, grouped so they can actually be read.

## What to watch for next

The owner's standing instruction from this session — never let more than **ten**
release candidates pile up before cutting a real release — is recorded as a
task. The situation that prompted it was eighty-seven candidates stacked on a
single version while the last real release sat a month behind, which meant "go
back to the last known-good version" would have thrown away a month of fixes.
Smaller, more frequent releases make that a survivable choice rather than a
painful one.
