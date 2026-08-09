<!-- file: docs/executive-summaries/2026-07-31-july-monthly-roundup-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b3d95c27-4e18-4a70-8f52-6c19e0a4b7d3 -->
<!-- last-edited: 2026-08-09 -->

# Executive Summary: July 2026 Monthly Roundup (July 12–31)

**Period covered:** 2026-07-12 through 2026-07-31.
**Deliberately starts on July 12:** the earlier
[June–July roundup](2026-07-04-monthly-roundup-executive-summary.md) covers 2026-06-05
through 2026-07-11. This picks up where that one stops, so the two do not overlap.
**Individual write-ups this consolidates:** the seventeen dated summaries in this
directory from 2026-07-13 to 2026-07-30, linked inline below.

Each theme groups an arc of related work rather than listing changes one by one, and is
written for a reader who does not work on the code.

---

## The month in one paragraph

July's second half was about **silent data loss**. Not crashes or visible errors — saves
that quietly dropped fields, merges that quietly orphaned files, background jobs that
quietly never ran, and dismissals that quietly came back. Most were found by looking
rather than by being reported, which is the uncomfortable part: they had been happening
for some time without anyone noticing. The month closed on a different note, with the
first real writes into a live iTunes library and the groundwork for listening on a phone.

---

## 1. Edits that quietly threw data away

The largest cluster of the month, and the most serious.

**[Stopping silent author and series loss on book edits](2026-07-13-author-series-data-loss-executive-summary.md)** — several operations save a **lightweight copy** of a book that
deliberately omits heavy fields for speed. The save routine protected most of those, but
not author and series: editing a book through one of those paths **erased them**.

**[Editing a book's author by ID left the old author name showing](2026-07-16-stale-author-on-id-edit-executive-summary.md)** — every book stores both a reference to its author
and a saved copy of the name. They are supposed to agree. Changing one did not update the
other, so the book pointed at the new author while still displaying the old one.

**[Stopping wrong publication year and language](2026-07-13-metadata-year-language-corruption-executive-summary.md)** — there are two different "years" for an audiobook: when the
*recording* was released, and when the *book* was first printed. For a classic those can
be decades apart, and the wrong one was overwriting the right one.

**[Maintenance tasks were only looking at part of the library](2026-07-16-whole-library-truncation-executive-summary.md)** — jobs that were supposed to sweep everything were
silently stopping early. Anything past the cutoff was simply never processed, and nothing
reported a partial run.

## 2. Merges that lost files

**[Preventing merged books from corrupting or disappearing](2026-07-13-merge-serialization-executive-summary.md)** · **[Stopping a split-book merge from orphaning audio files](2026-07-16-split-book-merge-orphan-executive-summary.md)** · **[Fixing how merging duplicate books handled iTunes links](2026-07-18-duplicate-merge-orphaned-itunes-links-executive-summary.md)**

Three separate defects in the same area. Merging two records that the system believed were
the same book could leave audio files attached to a record that no longer existed —
present on disk, unreachable through the app. The iTunes variant is the same failure
reaching into an externally-managed library.

**[Deleted books no longer haunt work and version listings](2026-07-13-index-consistency-executive-summary.md)** — deleted books kept appearing in lists they should have left,
because the deletion updated the record but not every index pointing at it.

## 3. Background jobs that were not running

**[The audio-fingerprinting job had been quietly failing every single restart](2026-07-17-fingerprint-backfill-silently-failing-executive-summary.md)**

A job computes an audio "fingerprint" for each book — a signature of how the recording
actually sounds, and the most reliable way to tell whether two books are genuinely the
same. **It never ran.** Every restart it started, failed to load, and gave up without
saying so. Duplicate detection had been working without its best evidence.

**[Duplicates you dismissed kept coming back](2026-07-17-dismissed-duplicates-came-back-executive-summary.md)** — you review a suspected duplicate pair and say "no, these are
different." The next scan put it back, as if you had never looked. Every review session
started from scratch.

**[The server was burning two CPU cores around the clock while doing nothing](2026-07-18-idle-server-burning-cpu-executive-summary.md)** — with no imports, no scans and nobody
using the app, the machine sat at two cores of constant load. Separately the health check
— the "are you alive?" probe monitoring tools poll — took **5.6 seconds** to answer.

## 4. Duplicate detection got measurably better

**[Duplicate-detection confidence reaches its target](2026-07-13-dedup-precision-executive-summary.md)** · **[Metadata lookups that no longer hammer or hang](2026-07-13-metadata-reliability-executive-summary.md)** · **[Closing two quiet failure gaps in cleanup actions](2026-07-16-apply-path-guard-hardening-executive-summary.md)**

The system rates a pair of books using several independent clues at once — text
similarity, audio fingerprints, durations, shared ISBNs. Tuning that combined rating
reached its accuracy target this month.

**[A quality sweep of 24 fixes cut the duplicate backlog by ~85%](2026-07-17-error-correction-wave-executive-summary.md)** (July 17–18) is the consolidated record of that push.

## 5. Writing back to iTunes, and listening on a phone

The month's forward-looking work, and a deliberate change of posture: from reading an
iTunes library to **writing into it**.

**[Making two-way iTunes sync safe to build](2026-07-24-itunes-2way-sync-p0-and-primitives-executive-summary.md)** — the safety work that had to exist first, given that the target is
a library the user manages in another application.

**[The first real write to your live iTunes library](2026-07-25-itunes-2way-sync-first-live-write-executive-summary.md)** — a single track, deliberately. The smallest possible real
test of everything above.

**[Listening to your library on your phone — the groundwork](2026-07-30-audiobook-app-sync-foundation-executive-summary.md)** — the beginnings of serving the library to a phone app.

---

## Themes worth carrying forward

1. **The dangerous bugs were all silent.** Not one of the data-loss defects announced
   itself. They were found by going and looking. That argues for more checks that verify
   an operation did what it claimed, rather than that it returned without an error.
2. **"Lightweight copy" is a recurring trap.** Several defects trace to the same shape: a
   partial record saved over a complete one. It is fast, it is usually fine, and when it
   is not, the missing fields are simply gone.
3. **A job that fails at startup and continues is indistinguishable from a job with
   nothing to do.** The fingerprint backfill went unnoticed for exactly that reason — the
   same pattern that produced August's transcription-outage mislabelling.
