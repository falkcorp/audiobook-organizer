<!-- file: docs/executive-summaries/2026-09-01-the-setting-nothing-read-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 4c2f81ad-6e3b-4d90-9a17-2b8e05f7c341 -->
<!-- last-edited: 2026-09-01 -->

# The setting almost nothing read

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3019 (open, not yet merged)

## Executive Summary

- The app has a setting listing which audio file types belong to your library. Fifteen
  types ship enabled by default. The part of the app that finds and imports books reads
  that setting. Almost nothing else did — fifteen other places each carried their own
  shorter, hand-written list instead.
- The result was a library that was half-served rather than broken. Books in the seven
  formats the shorter lists had never heard of got imported normally, and were then never
  watched for changes, never given their file records, never reconnected to iTunes when
  their location moved, never repaired when their path went stale, and never recorded in
  the file-history log. Every one of those jobs finished and reported success over work it
  had quietly skipped.
- Emptying the setting switched file recognition off entirely and reported nothing. Saving
  an empty list was accepted and stored as-is, after which every check answered "not an
  audio file" for every file on disk.
- One of the affected lookups could not see capitalised file extensions on the live server
  at all, and could not see any book at all in a folder whose name contains a square
  bracket — a common shape in this library.
- Two lists were narrow for a good reason and were deliberately left narrow: they answer
  "can this tool actually read this file?", not "does this file belong here." Widening
  those would have made things worse, not better.
- Verified by building for both this machine and the server, running the full test suite,
  and then deliberately breaking each new safeguard one at a time to confirm a test
  actually catches it — fourteen out of fourteen were caught, two of them only after
  writing tests that had been missing.

## 1. One setting, a dozen private copies of it

**What it was.** The list of audio file types that count as part of your library is a
setting you can edit. Only the scanner that imports books consulted it. Everywhere else —
the folder watcher, the iTunes reconnection paths, five separate maintenance jobs, the
file-history recorder, and the routine that lists the audio inside a book folder — carried
its own list, written by hand at some point in the past and never revisited. The lists
disagreed with each other and all of them were shorter than the setting.

**Why it mattered.** Seven of the fifteen shipped defaults were missing from most of those
private lists. A library containing books in those formats got them imported and then
stranded: not watched, not catalogued at the file level, not repairable, not reconnectable.
Nothing errored, because from each job's point of view there was simply nothing there to
do. The count of files needing attention read as zero, which is indistinguishable from
finished.

**The fix.** The list now lives in exactly one place, and every check that asks "does this
file belong to the library?" reads the setting through a single shared lookup.

## 2. An empty setting silently switched file recognition off

**What it was.** If you saved the setting with nothing in it, the app accepted the empty
list and stored it. It also starts life empty before the configuration file has been read
at all. In both states every check answered "not an audio file" for everything.

**Why it mattered.** This is the same shape as a problem this project already hit once,
where a single setting left at zero disabled an entire subsystem for months while every
report looked healthy. An empty list here would have been worse, not better: the whole
library would become invisible to every maintenance job at once, with no error anywhere.

**The fix.** Two independent guards. An empty list saved into the configuration file is now
ignored in favour of the shipped defaults, matching what a neighbouring part of the app
already did. And separately, any lookup that finds itself with an empty list falls back to
the shipped defaults rather than matching nothing. Doing the default amount of work is
always recoverable; silently doing none is not.

## 3. A folder listing that could not see half its own contents

**What it was.** The routine that lists the audio files inside a book folder asked the
operating system to match filename patterns. That approach has two failure modes, and this
code hit both. Pattern matching distinguishes upper from lower case on the server (but not
on a developer's Mac), so a file named with a capitalised extension was invisible in
production and perfectly visible during development. And a square bracket in a folder's own
name is itself a pattern character, so a folder named with "[Unabridged]" — a very common
shape in this library — matched nothing at all, making the book appear to contain no audio.

**Why it mattered.** Three separate jobs build a book's file records from this listing. A
book it could not see got no file records, which is precisely the condition behind a
long-standing complaint that some books have no route to their audio.

**The fix.** It now reads the folder's contents directly and checks each name's extension,
which has neither failure mode. As a side effect the order of the results changed, so every
place that consumes the list was checked to confirm nothing derives a track or disc number
from position — nothing does — and the new order is now held in place by a test rather than
left as an accident.

## 4. What was deliberately left narrow

Not every short list was a bug. Some of them answer a different question: not "does this
file belong to the library?" but "can this particular tool actually read this file?" The
fingerprinting tool, the transcription worker and the cover-art writer each have real
format limits, and those lists were renamed to say so plainly rather than widened.

This distinction is load-bearing. Two of the shipped default formats are Audible's
encrypted formats, which this application documents that it cannot decode at all. Widening
the fingerprinting and transcription lists to match the setting — which at first glance
looks like exactly the same de-duplication — would have bought one guaranteed failure per
Audible file. The tables that map a file extension to a content type or to a copy-protection
scheme were left untouched for the same reason: they are reference tables, not decisions.

One format, the general-purpose MPEG-4 container, was deliberately not added to the shared
list. It can hold audio, but it overwhelmingly holds video, and this list feeds the importer
— adding it would mean a trailer sitting in a library folder gets imported as an audiobook.
The two places that already recognise it keep their own list rather than being converted,
because converting them would have quietly removed that recognition.

## How we know

The build was checked for both this machine and the server. Every affected area's tests were
run and pass. Then each new safeguard was deliberately broken, one at a time, to confirm a
test actually fails when it is — narrowing the shared list, removing the empty-list fallback,
reversing the folder ordering, and restoring each old private list in turn. Fourteen were
tested and fourteen were caught, but two of them were caught only after new tests were
written: on the first pass, two of the converted lookups had no test that could observe the
difference at all, which is exactly the blind spot this exercise exists to find.
