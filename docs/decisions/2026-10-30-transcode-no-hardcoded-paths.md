<!-- file: docs/decisions/2026-10-30-transcode-no-hardcoded-paths.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1a882187-9084-4ef6-9031-24796e602370 -->
<!-- last-edited: 2026-10-30 -->

# Transcode pass: no hardcoded permanently-unfixable paths

## Decision log

**Chosen.** Delete the `permanentlyUnfixable` list from `TranscodeMalformedFiles`
(`internal/remux/transcode.go`). Whether a file is permanently unfixable comes only
from the per-file skip setting (`TranscodeSkipKey(path)`) that the pass already writes
when a full transcode produces output taglib still cannot parse, and that it already
reads at the top of each file's turn.

The same two install-specific paths survived as two hashed skip keys, with author and
title in comments, in `cmd/pebble-inject-skip`. That tool now takes the file paths as
command-line arguments and derives the keys with `remux.TranscodeSkipKey`, so the tool
carries no install data either.

**Why.** The TODO entry's first option: those two keys are already set on the install
that needed them, so the list changes nothing there, and on every other install it wrote
skip flags for paths that do not exist. The list was install data in production logic in
a public repo.

**Rejected.**
- Moving the list into a setting or config file: the skip setting already is that
  per-file data store. A second list would duplicate it and need its own loader and UI.
- Narrowing `pebble-inject-skip` comments only: leaves the same hardcoded install data
  in the tool; arguments are the correct fix.

**Left alone.** The TODO entry's "scrub from history" question needs an owner call
(force-pushing public history); not done. Author names in the authority seed, the
transcription replay fixture and an archived plan are unrelated to the removed list.
