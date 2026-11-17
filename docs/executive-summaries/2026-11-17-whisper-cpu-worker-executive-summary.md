<!-- file: docs/executive-summaries/2026-11-17-whisper-cpu-worker-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7b3e5a90-1c64-4d28-9e0f-a52d8c7b1f46 -->
<!-- last-edited: 2026-11-17 -->

# The Windows transcription machine can now work without a graphics card

**Date:** 17 November 2026 (the day the change was written; the merge date
replaces it when the pull request lands). Nothing in this change has been run on
a Windows machine.

## Executive Summary

- The computer that listens to the first minute or two of each book and writes
  down what it hears was built around a graphics card. That card is likely dead,
  and the machine was left unable to do the newer, faster style of work the Mac
  workers do (being handed a file location instead of the audio itself). It now
  can, and it can do it with its ordinary processor.
- The worker chooses its own hardware: it uses the graphics card when one works
  and says in one log line when it falls back to the processor. It can also be
  told to use the processor only, which is the right setting for a machine whose
  card is gone. A worker told to use the card, if the card fails, stops and says
  so instead of quietly running slowly.
- The part of the Mac worker that decides which files it may open, and how it
  reports its health, now lives in shared code that both workers use, so a fix in
  one is a fix in both. The Mac worker's answers were recorded before the move
  and checked again after it: unchanged.
- The rules that stop a request from reaching files outside the library folder
  were extended for Windows (drive letters, network shares, upper and lower case
  folder names, and special names such as CON and NUL that Windows treats as
  devices) and tested with about a hundred cases. The same checks now cap how
  many files, how long a path and how large a request a worker will accept.
- The Windows start-up script restarts a crashed worker after a growing pause
  instead of immediately, refuses to start when a needed file is missing, and no
  longer assumes a particular user folder.
- A transcription that gets stuck no longer freezes the Windows worker: the work
  runs in a separate process that is stopped and restarted when it takes too long,
  as on the Macs.
- The worker now listens only to the machine it runs on unless someone deliberately
  opens it to the network. It has no password, so opening it would let anything on
  the network ask it to transcribe library files; a password is filed as follow-up
  work for both workers. A machine that already used the graphics card keeps the
  same model and the same fingerprint, so none of its earlier results are thrown away.
- Not verified: anything on a real Windows machine, how fast a real desktop
  processor transcribes (the starting settings are guesses to measure), and a
  real end-to-end run from the server.

## The Windows worker could not take path-based work

**What it was.** Two kinds of worker existed. The Mac worker received a file
location, read the file itself and listened to the opening. The Windows worker
only accepted the audio uploaded to it, and insisted on a graphics card.

**Why it mattered.** The graphics card is probably gone, so one of the machines
available for the transcription backlog sat idle, and the server is not allowed
to decode audio itself.

**The fix.** The Windows worker now accepts file locations, maps them onto its
own view of the library, and can run on the processor alone.

## Keeping a request inside the library folder

**What it was.** A worker opens whatever location the server names. On Windows
there are extra ways a name can mean something else than it looks: a backslash,
a drive letter, a trailing dot, or a device name.

**Why it mattered.** A mistaken or hostile request could otherwise make a worker
read a file outside the library.

**The fix.** The checks were extended and tested for each of these cases, and
requests that are too large or have too many files are refused.
