<!-- file: docs/executive-summaries/2026-10-25-whisper-timeout-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 3e5a91c4-72d8-4b06-8f1e-a4c2d70b95e6 -->
<!-- last-edited: 2026-10-26 -->

# One bad audio clip can no longer freeze all the listening helpers

Branch `fix/whisper-transcribe-timeout-r2`, [PR #4168](https://github.com/falkcorp/audiobook-organizer/pull/4168). No separate spec.

## Executive Summary

The Mac helpers that listen to the opening of each audiobook (to read out the
title, author and narrator) all stopped working on 23 October. The cause was a
single audio file. When a helper tried to listen to it, the speech engine got
stuck in a loop and never finished. The helper had no time limit, so it simply
stayed stuck.

Our side made it worse. After ten minutes without an answer, the server decided
the helpers were "unreachable" and sent the same batch of files to the next
helper. Each one got stuck on the same file in turn, until all four were busy
forever and the listening jobs made no progress.

Five things changed:

- Each helper now listens inside a separate worker process with a time limit
  (a minute, or four times the length of the clip if that is longer). If the
  limit passes, that worker process is stopped and a fresh one takes over. The
  file is marked as "timed out" and the helper moves on.
- The server can tell "a helper is down" from "a helper is stuck on a file". For
  a stuck helper it splits the batch in half and tries the pieces elsewhere,
  which pins the problem down to one file without re-sending everything. Files
  that were already done are kept.
- A file that times out twice is set aside for good, so neither listening tool
  keeps trying it. It stays set aside until someone asks for a retry or the file
  itself is replaced. A file that timed out only once is tried again next time.
- A file is blamed at most once per run, and only when the helper really looks
  healthy. If most of the files in one request time out together, the helper is
  the problem, not the files, so nothing is held against them and they are tried
  again later. A file whose listening later succeeds has its earlier strikes
  wiped.
- When a request fails part-way, the files the helpers had already finished are
  kept and saved, and only the unfinished ones are tried again. Before, a single
  stall threw the finished ones away too.

## What changed for you

- A helper that is busy still answers its health check, and the check now says
  how long it has been working on the current clip. A helper past its limit is
  treated as unhealthy.
- Both listening tools keep reporting progress while they wait on a long
  request, so a slow-but-alive request is not mistaken for a hung tool.
- The tool now watches a long request by asking the helper how long it has been
  working, rather than waiting a fixed ten minutes. A helper running the older
  script cannot report that, so for it the old fixed wait still applies.
- If a helper's worker process cannot start or keeps crashing, the files are
  left alone ("worker problem") instead of being marked as failed. A worker that
  crashes on every clip therefore leaves its files waiting until it is fixed.
- If every helper stays stuck for three batches in a row, the tool stops and
  says so, instead of marking the whole library "not done" one batch at a time.
- The new "timed out" count appears next to the other outcomes in the listening
  statistics.
- Operation logs now show the actual error text. They showed an empty `{}`
  before, which hid what went wrong.

## Rolling it out

The helpers need the new script. Either order of deployment is safe: an older
server treats the new "timed out" answer as an ordinary retryable error, and an
older helper simply does not report how long it has been busy. Restart the
helpers one at a time and check that each one reports itself idle.

## Why it matters

A single unreadable file could silently stop all audiobook identification
work, and the retry behaviour spread the damage to every helper. Now the damage
is limited to that one file, the helpers recover on their own, and a stuck pool
is reported plainly instead of looking like an outage.
