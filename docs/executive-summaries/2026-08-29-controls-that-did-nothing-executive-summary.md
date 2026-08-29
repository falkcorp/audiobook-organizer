<!-- file: docs/executive-summaries/2026-08-29-controls-that-did-nothing-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: fb178b32-a3b2-4a24-b5f9-7a1e609c9fcd -->
<!-- last-edited: 2026-08-29 -->

# Several settings looked like they worked, and did nothing at all

## What was wrong

Three separate controls in the app accepted your input, reported success, and then
ignored you completely.

- **The "force" option on the aggregate recalculation job.** The screen offered it,
  the job description told you to use it to override a previous run, and the value
  never travelled anywhere. Every forced run quietly behaved like an unforced one.
- **The retention depth on the new version-history cleanup.** You could ask it to
  keep 3 old versions per book, or 50. It always kept 10.
- **The similarity search index.** It was being thrown away and rebuilt from scratch
  on every single restart — roughly two minutes of work, repeated every time, for no
  benefit.

None of these announced a problem. That is what made them expensive: a control that
fails loudly gets fixed the same day, while one that succeeds silently can go
unnoticed for months.

## What was actually happening

**The first two shared one cause.** When a request runs a maintenance job, any extra
settings you provide have to be carried from the web request down to the code doing
the work. That delivery route was removed some time ago during unrelated cleanup, and
nothing noticed, because the jobs that depended on it did not complain when they
received nothing — they just fell back to their defaults and carried on.

Worse, the automated tests could not see it. They handed the settings directly to the
job through a side door that only exists in tests. The tests passed, the feature was
dead, and the two facts were entirely compatible.

**The third was a measurement mistake.** Before reusing its saved search index, the
app checks whether the index still matches the library. It compared the number of
entries in the index against the number of records in the database — but the index
is deliberately a *filtered* subset of those records; entries that are duplicates,
outdated, or belong to deleted books are correctly left out. In production the index
held about 17,700 entries against 39,700 records — completely normal, and permanently
indistinguishable from "out of date" to a check written that way.

So the check was always true. The saved index was discarded every boot, without fail,
and the warning it printed was a permanent fixture rather than a signal.

## What changed

The delivery route for job settings was rebuilt, and the jobs now read from it, so
"force" and the retention depth actually take effect.

The retention setting also got a guard: asking to keep **zero** versions no longer
means "keep zero." It falls back to the default instead. The app has already had one
outage caused by a stray zero being taken literally, and a retention control is a bad
place to repeat that.

The index check now compares against the record count captured when the index was
saved — like against like — so a legitimately filtered index is kept, and only a
genuinely outdated one is rebuilt. Restarts are correspondingly faster.

## What this does not fix

Four other maintenance jobs still read settings from the removed route and are being
addressed separately. One of them, the tool for reversing a bulk metadata fetch,
*requires* a setting that can no longer reach it — meaning it currently cannot do
anything at all except report an error. That is a separate piece of work.

Separately, the search index is still missing a large number of entries during its
rebuild for reasons that are correct but entirely unreported. Roughly 22,000 records
are skipped and only one of them is currently accounted for anywhere. The skipping is
mostly legitimate; the silence about it is not, and making those numbers visible is
also in progress.
