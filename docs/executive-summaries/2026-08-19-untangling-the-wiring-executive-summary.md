<!-- file: docs/executive-summaries/2026-08-19-untangling-the-wiring-executive-summary.md -->
<!-- version: 1.0.0 -->
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
| A library warm-up step was skipped on startup | Run it |

None of these announced themselves. They are the reason this cleanup was worth doing
rather than just tidy: the old shape let a feature disappear without a single error
message.

## Where things stand

Of the places that used to plug into the 398-socket connector, **18 remain**, and they
break down as:

- **7 are deliberate and staying.** These are the front door of the program — the point
  where the database is created and handed out. Something has to hold the whole thing.
- **3 are test helpers**, where a wide connector is the right answer: integration tests
  poke at whatever the scenario needs, and narrowing them would mean editing every test
  file each time a new one is written.
- **8 are the next step**, all part of one remaining chain through the maintenance jobs.

The remaining work is to split the database object itself into pieces, so the giant
connector has nothing left to plug into and can be deleted outright.

## What this does not change

No behaviour you interact with. No data was touched. The four unplugged features above
are the only user-visible difference, and all four are things starting to work that were
supposed to be working already.
