<!-- file: docs/executive-summaries/2026-10-22-whisper-mac-side-extraction-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: b8e27838-815c-4aaf-9ca2-edeff450cc9a -->
<!-- last-edited: 2026-10-22 -->

# Listening to book openings works again, without the server touching audio

## Executive Summary

The tool that listens to the first minute and a half of each audiobook, to read
out the title, author and narrator, had stopped working. On 9 October the main
server was told never to process audio itself, only the Macs may. This tool
still cut those audio clips on the server, so when it was started on 22 October
it stopped within a fraction of a second with "audio decoding is not allowed on
this server". The Mac helpers that do the actual listening were running and
idle the whole time.

It now works the way the rule intends. The server no longer cuts any audio. It
tells a Mac helper which book file to use, and the helper opens that file over
the network from the shared library folder, cuts the clip itself, and listens
to it. The server only checks that the file exists.

## What changed for you

- Nothing to click. The tool picks the Mac route on its own when the server is
  not allowed to process audio, and says so in its preview.
- It will not quietly fall back to doing the work on the server. If no Mac
  helper is set up for this, it stops and tells you why.
- If the Mac loses its connection to the library folder, the tool pauses that
  batch and leaves every book untouched, instead of marking books as failed. A
  book is only marked with the new "path unmapped" note if the helper could not
  reach a file the server can see, and that note is retried on the next run,
  never treated as permanent.
- The longer retries for books that start with music or silence use the Mac
  route too.

## One-time setup on the Mac

The Mac needs the library folder mounted for its own login, and each helper
needs to be told how the server's folder names line up with that mount. The
steps, including a login item that re-mounts the folder if it drops, are in
`docs/whisper-remote-extract.md`. Until that is done the tool refuses to run
rather than guess.

## Why it matters

This keeps the standing rule (no audio processing on the server) intact while
restoring a tool the rest of the library clean-up relies on for identifying
books by what the narrator says at the start.
