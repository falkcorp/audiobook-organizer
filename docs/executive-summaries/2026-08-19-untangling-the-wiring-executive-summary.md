<!-- file: docs/executive-summaries/2026-08-19-untangling-the-wiring-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: 3c1f9a76-5e28-4d0b-9a41-6b2f70d5e8c4 -->
<!-- last-edited: 2026-08-19 -->

# Untangling the wiring

**2026-08-19 — the one giant connector every part of the app was plugged into, why four
features had quietly come unplugged from it, and what changed**

## The short version

Nothing you can see in the app changed. What changed is underneath: the way every part of
the program talks to the database. It used to go through a single connector with **398
sockets on it**, and every component — the scanner, the web pages, the plugins, the
scheduler — plugged into the whole thing whether it needed two sockets or two hundred.

Over the past two days that connector was taken apart. Components now plug into small
connectors carrying only what they actually use. Along the way this surfaced **four
features that had silently come unplugged** and were failing in ways nothing reported.

## Why a giant connector is a problem

Three practical costs, in plain terms:

1. **Nobody could tell what anything needed.** If the diagnostics page plugs into all 398
   sockets, you cannot tell from looking whether changing one of them will break it. Every
   change became a guess.
2. **Test doubles had to fake all 398.** Writing a test for a component meant building a
   stand-in for the entire database. One of those stand-ins was **24,613 lines long** and
   was deleted earlier in this effort.
3. **Things could come unplugged without anyone noticing.** This is the one that actually
   cost you something — see below.

## The four features that had come unplugged

The database is wrapped in layers, the way a letter goes in an envelope and the envelope
goes in a package. Some features need to reach past the wrapping to the thing inside.
The old code did that by asking "is this thing inside exactly what I expect?" — and when
the answer was no, because someone had added another layer of wrapping in between, the
code **quietly took a worse path instead of reporting a problem**.

Found and fixed while narrowing the connectors around them:

| What it was doing | What it should have done |
|---|---|
| The activity log's fast storage was switched off | Use it |
| A fingerprint reset ran one record at a time | Run in batches — roughly **100× faster** |
| Three background services reported "unsupported backend" | Start normally |
| A library warm-up step **sometimes** got skipped on startup | Always run it |

None of these announced themselves. They are the reason this cleanup was worth doing
rather than just tidy: the old shape let a feature disappear without a single error
message.

**A correction, since an earlier version of this page overstated one row.** The
warm-up one is a *timing* problem, not a constant one: whether it got skipped
depended on which of two things happened first during startup, so it worked
sometimes and not others. That is worse to live with than a clean failure, but it
is not the same as "never ran", and the first draft of this page said the wrong
one. The other three rows are unchanged and were confirmed failures.

While checking that, three further places that used the same fragile "is this
exactly what I expect?" question turned out **not** to be failing at all — they are
handed the unwrapped database and always were. They were tidied anyway, because the
question is a trap wherever it appears and the failure is silent when it does fire,
but nothing about them was broken. They are not counted above.

## Where things stand

Of the places that used to plug into the 398-socket connector, **10 remain** — down
from 18 when this page was first written, because the last chain through the
maintenance jobs was finished the same day. They break down as:

- **6 are deliberate and staying.** These are the front door of the program — the point
  where the database is created and handed out, plus the wrapper layer itself, which by
  definition has to hold the whole thing.
- **3 are test helpers**, where a wide connector is the right answer: integration tests
  poke at whatever the scenario needs, and narrowing them would mean editing every test
  file each time a new one is written.
- **1 is deliberately untouched**: it feeds the missing-file repair work, which is a
  separate open question and off-limits until that gets decided.

**Narrowing the components is finished.** Nothing further can be reached that way. The
remaining work is to split the database object itself into pieces, so the giant
connector has nothing left to plug into and can be deleted outright.

**One more loose plug turned up afterwards**, and it is worth recording rather than
quietly fixing, because the sentence above was written before it was found. A
maintenance task that builds a book-lookup index was still assuming it would be handed
the database object directly, rather than asking for the specific abilities it needs.
Nothing was broken by this: as the program is currently wired, it *is* handed that
object directly, so the assumption holds. It would have broken the first time anything
wrapped the database — which the program already does elsewhere, for other callers. It
now asks for what it needs, the same way every other component does, and it still
refuses to run loudly rather than quietly doing nothing if those abilities are missing.

The general lesson is the one this whole page is about: a component that works today
because of how something else happens to be wired is not the same as a component that
works. The first kind fails silently later, and there is nothing in the code that
says which kind you are looking at.

## The tidy-up, and what it is not

The connector itself was then **relabelled**. Its forty sockets are now organised into six
labelled bundles — catalogue, media, accounts, enrichment, operations, platform — so that
anything needing "the catalogue" can ask for that bundle by name instead of taking the
whole panel.

**Be careful how you read that.** No wire was removed. All 398 are still there, and the
giant connector is still giant. This is the difference between a drawer with forty loose
cables and the same forty cables in six labelled bundles: genuinely easier to work with,
much easier to reason about, and the same number of cables. The real work — splitting the
database object so the connector has nothing left to plug into — is still ahead.

It is worth naming because the project's own notes predicted this could not be done
before that bigger job, and the prediction was wrong for an instructive reason: it
confused *how many wires the connector carries* with *how many bundles it presents*. The
automatic check that flags over-large connectors only ever counted the second. It now
reads **zero** for the first time.

## The safety net was broken, and only a clean result revealed it

Getting that count to zero exposed something worth reporting on its own. The automated
check that watches for over-large connectors **had never once run on a clean result** —
there had always been at least one thing to report, from the day it was written.

The first time it had nothing to report, it crashed. Silently. It produced no output at
all and returned the same failure code it uses for "something got worse" — so a clean
result would have been announced as a problem, with nothing on screen to explain it.
Someone would have gone looking for a fault that was not there.

That is now fixed, and the fix was checked by deliberately breaking things to confirm the
alarm still sounds: a too-large connector planted on purpose is still caught, a
miscounted baseline is still caught, and reverting the fix reproduces the silent crash. A
safety net that cannot be shown to catch anything is not a safety net.

## What this does not change

No behaviour you interact with. No data was touched. The four unplugged features above
are the only user-visible difference, and all four are things starting to work that were
supposed to be working already.
