<!-- file: docs/executive-summaries/2026-09-02-moving-to-the-current-go-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d3e9a41-2b6c-4f58-a1e7-9c0d5b2f8e63 -->
<!-- last-edited: 2026-09-02 -->

# Moving to the current version of Go

**Pull requests:** [#3039](https://github.com/falkcorp/audiobook-organizer/pull/3039)
(toolchain), [#3040](https://github.com/falkcorp/audiobook-organizer/pull/3040)
(standards), [#3043](https://github.com/falkcorp/audiobook-organizer/pull/3043)
(the code sweep).

## What this was about

The program is written in Go, and Go releases a new version twice a year. We
had been held back on an older one for months because a single library the
database layer depends on did not work with the new release. That library
was fixed upstream, so this week we moved to the current release
(Go 1.27) and then took advantage of it.

Staying current matters for two plain reasons. The security fixes for the
language's own standard library only ship for the two most recent versions;
being behind meant known, patched problems stayed in our binary. And each
release adds clearer, safer ways to write common things — the longer we
wait, the bigger the eventual catch-up.

## What changed

1. **The build now uses Go 1.27.1 everywhere** — the developer setup, the
   automated checks, and the containers that run on the server all pin the
   same version, so "works on my machine" and "works in production" mean
   the same thing.

2. **The code was modernised with Go's own tool.** Go ships a program
   (`go fix`) that rewrites older idioms into their current equivalents. We
   ran it across the whole codebase: 557 files, roughly 3,400 lines
   changed. Nothing about what the program *does* changed; how it is
   written did. About 70 tiny helper functions that existed only to work
   around an old language gap are gone, because the language now does that
   itself.

3. **Every rewrite class was reviewed by hand where the tool could have
   been wrong.** A handful of the rewrites are only safe under specific
   conditions (for example, one relies on a loop-variable rule that changed
   in Go 1.22). We checked each of those sites individually rather than
   trusting the tool, ran the race detector on the packages it touched,
   and confirmed that the static analyser reports exactly the same nine
   pre-existing notes before and after — none added, none removed.

4. **One thing the tool undid had to be put back.** Two places in the code
   were deliberately written "the long way" so that our security scanner
   could see that a request-supplied number is capped before it sizes an
   allocation. The modernising tool helpfully shortened them, the scanner
   lost sight of the cap, and it raised a high-severity alert. Both were
   rewritten in a form the tool leaves alone, with a comment saying why, so
   the next run will not repeat it.

## What it means for you

Nothing visible. Pages, files, and data behave exactly as before. The
change is that the program is now on a supported, patched version of its
language, the code is written the way current Go is written (so future
changes are easier and less error-prone), and the automated checks that
gate every change now run against the same version production does.

## Follow-ups (not in these PRs)

- Five fields in JSON responses could use a newer, more precise "omit when
  empty" rule. That changes the wire format, so it is deferred to a
  separate, hand-reviewed change rather than swept in mechanically.
- The nine pre-existing static-analysis notes (unused test helpers, one
  misnamed error variable, one deprecated-field use in a test) make the
  local `make ci` gate red on `main` independent of this work. They are
  listed on #3043 for a decision: delete, rename, or suppress.
- A single test in the scanner package fails locally on `main` for reasons
  unrelated to this work (a 0.4-second disagreement about an MP3's
  duration, which depends on the installed `ffprobe`). Recorded for the bug
  hunt.
