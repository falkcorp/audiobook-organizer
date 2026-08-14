<!-- file: docs/executive-summaries/2026-08-13-when-quotes-did-not-mean-quotes-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 4a2e7b19-6c38-4d51-8f07-b3d9e2c14a86 -->
<!-- last-edited: 2026-08-13 -->

# Executive Summary: When Quotes Did Not Mean Quotes

**Date:** 2026-08-13
**In one line:** three separate defects made library search return far too much — a `*`
that silently matched nothing, quotation marks that were ignored, and common words like
*all* and *the* being thrown away before the search ever ran.

---

## What you saw

Three complaints, on the same evening, that turned out to be three different bugs:

1. *"It's always trying to add `*` to the end of my queries but then nothing shows up."*
2. *"Why the fuck don't quotes work? If I said I want `"All Jobs"` then the results
   better only have that."*
3. Searching `"All Jobs"` returned **300 books**.

All three are now fixed and running.

## What was actually happening

### The `*` that matched nothing

The search box adds a `*` to the end of what you type, meaning "and anything that starts
this way". That worked — but only if you typed in lowercase.

The catalogue stores every word in lowercase. Most searches get lowercased on the way in
to match. The "starts with" search skipped that step and compared your text as typed. So
`hyperion*` found 21 books and `Hyperion*` found **zero**. `dragon*` found 1,757;
`Dragon*` found zero. Since the app added the `*` for you, ordinary typing with a capital
letter returned an empty screen.

### Quotation marks that were never noticed

Putting quotes around words is supposed to mean "these words, in this order, together".
The machinery to do that existed and worked — but nothing ever switched it on for a
plain quoted search, because the part that reads your query split it into words *before*
it looked for quotes. Your quotation marks were treated as stray punctuation and thrown
out. The search you got was the same one you'd have got without them.

### Common words being deleted

This is the one that produced 300 results, and it is the least obvious of the three.

Search catalogues normally discard very common words — *a*, *the*, *of*, *and*, *all* —
because they appear nearly everywhere and cost space without helping. That is a sensible
default and it is why searching *shards of oblivion* works even though *of* is useless as
a search term.

But it interacts badly with quoted phrases. Picture the catalogue writing down each word
with its position: *All*(1) *Jobs*(2). Delete the common word and you are left with a
single note — *Jobs*, position 2. The search then has nothing to compare positions
*against*. `"All Jobs"` quietly became "any book containing the word Jobs", which is 300
of them.

Longer titles failed differently. `"Lord of the Rings"` kept *Lord* at position 1 and
*Rings* at position 4, with positions 2 and 3 now empty — and empty positions match
**anything**. So it was really searching for "Lord, then any two words, then Rings", and
a book called *Lord of All Rings* would have matched just as well.

## What was done

The catalogue now keeps the common words instead of discarding them, so quoted phrases
can be checked properly, word by word and in order.

Ordinary searches were deliberately left alone. Typing *shards of oblivion* without
quotes still ignores the *of*, exactly as before — a change there would have broken
searching for the many book titles that contain small connecting words. Quoted searches
became strict; unquoted searches did not change at all.

Making that change meant rebuilding the whole search catalogue, because the rules for
writing the cards are baked into the catalogue when it is created. The system now records
which version of the rules built it, notices when they no longer match, and rebuilds
itself automatically.

## Rebuilding safely

That rebuild is the part with real risk attached, and it is worth explaining why.

Earlier the same day we fixed a bug where the catalogue had been left **a quarter empty**
— a rebuild had been interrupted by a restart, and the system's only check was "is it
completely empty?", so it saw a partly-filled catalogue and concluded there was nothing
to do. Permanently.

Wiping the catalogue to rebuild it puts you right back at that starting line. So the new
rebuild does not use the old, interruptible process. It writes a to-do list to disk
first, then works through it. If the server restarts halfway, the list is still there and
it picks up where it stopped.

**This was not a theory — it was tested by accident.** The server was redeployed
mid-rebuild. The 3,497 books already catalogued survived, the catalogue was not wiped a
second time, and the work simply resumed. Under the old behaviour that same restart would
have left another permanent hole.

The full rebuild covered **67,824 books in about 36 minutes with zero failures**.

## Where things stand

| search | before | after |
|---|---|---|
| `"All Jobs"` | 300 books | **3**, all the right one |
| `Hyperion*` | 0 books | **21** |
| `Dragon*` | 0 books | **1,757** |
| `shards of oblivion` | 3 books | **3** (unchanged, as intended) |

The `"Lord of the Rings"` search now returns 7 books, and every one of them genuinely
contains that exact phrase — four mention it in the plot summary, one has it in the
folder name, one has it as the series name. None are false matches.

## Still open

- **Fuzzy search (the `~` option) has the same capital-letter bug** the `*` had. It was
  left out on purpose so the `*` fix could be reviewed on its own. Same one-line fix.
- **Fields meant to be exact — genre, tags, ISBN — are not.** They are being broken into
  words and having common words stripped, the same treatment the descriptions get. This
  was found while fixing the above and deliberately not bundled in, because changing two
  things at once would have made it impossible to prove which change did what. It needs
  its own before-and-after measurement.

## What this cost

Three pull requests. The largest single risk was not the search logic — it was that
rebuilding the catalogue could have recreated a worse bug than the one being fixed, and
the design work went into making that rebuild interruptible rather than into the search
change itself.
