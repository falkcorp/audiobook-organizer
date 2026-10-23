<!-- file: docs/executive-summaries/2026-10-22-per-file-intro-transcription-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e6c3a58-7b91-4d24-a3f5-8c1d92b47e06 -->
<!-- last-edited: 2026-10-23 -->

# Listening to the start of every audio file, not just the first

## Executive Summary

Until now the library only "listened" to the opening of each book's first audio
file, to read out the title, author and narrator. A book made of dozens of
tracks was judged on track 1 alone. The owner approved listening to the first
90 seconds of **every** audio file. This change adds the tool that does it,
and the supporting pieces that make it safe to run over the whole library.

## What it does

- Each audio file gets its own saved transcript of its opening, with a note of
  which speech model produced it and how long a clip was used.
- Every book's first file goes first, across the whole library, because that is
  where the credits are spoken.
- A book's own transcript is filled in from its first file only when the book
  has none. It never replaces a transcript that is already there.
- Files that are byte-for-byte identical are listened to once and share the
  result.
- The server does no audio work itself. It tells the speech workers which file
  to read and for how long; they open the file and do the rest.

## Safety

- A run states what it would do first. Starting it with no instructions only
  counts the work and estimates the time.
- It can be stopped and started again at any point and carries on with what is
  left; nothing is done twice.
- It waits while a library scan or a large metadata update is running, and it
  limits itself to what the speech workers can take.
- If the workers are unreachable, it records nothing and tries those files
  again next time, rather than marking thousands of files as failed.
- Any run can be rolled back by its run number. The tool first reports how many
  files and book transcripts it would clear, and only then does it. Rolling back
  puts files back to "not yet listened to"; if a run was forced to redo files
  that already had transcripts, rolling it back cannot bring the older text back.

## What was checked, and one correction

The design review in September said that every library rescan wiped saved
transcripts from audio files. On inspection that had already been fixed by an
earlier change to how rescans merge file records; this work adds the new saved
details to that protection and adds tests that name it, so a regression would be
obvious.

Progress is tracked per file, including how many of the library's files now have
a transcript. The time a full run takes is an estimate until it has been
measured against the real workers.
