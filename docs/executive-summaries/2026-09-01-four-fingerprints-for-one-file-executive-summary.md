<!-- file: docs/executive-summaries/2026-09-01-four-fingerprints-for-one-file-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8f1c4a72-6e93-4d18-b0a5-2c7d9e41f306 -->
<!-- last-edited: 2026-09-01 -->

# Four different fingerprints for the same file

## What was wrong

To decide whether two audiobook files are the same file, the app takes a fingerprint of
each one and compares them. Same fingerprint, same file — that is the single strongest
piece of evidence the duplicate finder has, and it is treated as certainty rather than a
guess.

For that to work, every part of the app has to take the fingerprint the *same way*. Four
parts of the app did not.

The agreed method, for a large file, is to read the first ten megabytes and the last ten
megabytes and fold in the file's exact size — fast, and enough to tell any two real
audiobooks apart. The library scanner did it that way. But three other places wrote into
the very same field using their own method:

- the job that pulls a short sample clip out of a book read the **whole** file
- the job that files a new copy of a book as a new version read the **whole** file
- the iTunes importer read only the **first megabyte**

Nothing failed. No error appeared anywhere. Each of them wrote a perfectly ordinary
64-character fingerprint into the field, and the app stored it without complaint.

## Why it mattered

Two ways, in opposite directions.

**Duplicates went unnoticed.** A file fingerprinted by the whole-file method can never
match the same file fingerprinted by the scanner's method. So whenever one of those jobs
ran, that file quietly dropped out of duplicate detection — not flagged as "unsure",
simply never considered. You would keep two copies of a book and never be told.

**And, from the iTunes importer, the reverse.** A fingerprint of only the first megabyte
is a fingerprint of the opening seconds. Two different chapters of the same audiobook, or
two books from the same publisher with the same intro, can easily share those opening
seconds — and would then have produced *identical* fingerprints. That is a false
duplicate, presented with the same certainty as a true one.

The second is the more serious of the two, because a wrong "these are the same book" is
acted on, while a missed duplicate is only a duplicate you still have.

## What changed

There is now exactly one place in the app that fingerprints a book file, and all four
callers go through it. The two rival copies of the method that had been written out by
hand elsewhere are gone, along with a third unused copy, so there is no longer a
similarly-named function sitting next to the right one waiting to be picked by mistake.

The clip-extraction job also had a description that promised it would only fill in a
missing fingerprint. It did not — it overwrote whatever was there. The description now
matches the behaviour, and the behaviour is the one the description promised.

## What is still owed

This stops any new bad fingerprints from being written. It does **not** repair the ones
already stored — a wrong fingerprint and a right one look exactly alike, so telling them
apart means re-reading the files and comparing. That repair is tracked separately, and
until it runs, existing books touched by those three jobs stay mis-fingerprinted.
