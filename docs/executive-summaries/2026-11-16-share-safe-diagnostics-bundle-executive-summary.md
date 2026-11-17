<!-- file: docs/executive-summaries/2026-11-16-share-safe-diagnostics-bundle-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 83fb8459-963c-4432-b2f8-ffd4fd4bd16d -->
<!-- last-edited: 2026-11-16 -->

# The support bundle no longer names the folders and people on your computer

**Date:** 16 November 2026. Closes the findings of the review of the new
health and support-bundle work (the F8-03 pull request) and lifts the hold that
was placed on switching it on. Nothing in the running server uses the bundle
yet: a later change connects it, and it can now be connected.

## Executive Summary

- The new support bundle is a zip file an administrator can send to someone
  else when something goes wrong. Its job is to say what happened without
  saying where on your computer it happened, or whose account it belongs to.
- The first version tried to find file paths inside free text by guessing where
  each one starts and ends. That cannot be decided when a folder is called
  `My Books` or `Bruce Wayne`: the part after the space was left in, and with it
  often the user name. The review found it leaking for names with spaces, for
  `~user` shortcuts, for network paths such as `smb://`, for build flags, and
  for web-address-style spellings of a path. A second review found the same
  fault in the first fix: the words after the last slash of a line were still
  left in, and a folder name that contained a colon, a quote or a backtick
  defeated it.
- The bundle now decides only where a location STARTS, and wipes from there to
  the end of the line. A folder name with spaces, colons or quotes disappears
  whole, because the bundle no longer tries to guess where it stops. The one
  thing that is kept after a location is a file error it recognises exactly, at
  the very end of the line, so `open /Volumes/My Books/x: permission denied`
  becomes `open <path>: permission denied`: you still see what failed and why,
  not where.
- A file inside one of your library folders is shown as the name of that
  folder and nothing after it, because what follows is usually an author or a
  title.
- Fields that are not free text (log level, time, component, request route,
  reason) keep their value when it has the expected shape, so a bundle stays
  readable.
- The same rule now applies to the health page's detail text, which had no path
  protection at all, and to the names of the files inside the zip. The health
  page now takes the list of library folders and builds the rule itself, so it
  cannot be given a rule that lets a path through.
- It is tested the way the leak was found: a marker and the words of the folder
  names are planted in generated locations in every spelling (spaces, quotes,
  colons, backticks, relative forms, text inside a log line), and must never
  appear in the output. Two fuzzers run the same checks on random input. A test
  builds a whole bundle from a made-up log and scans every byte of the zip for
  the planted names.
- This is what the bundle costs, and none of it was a decision the owner made:
  the rest of a line after a location is lost, so `copied /a/b to the shelf`
  becomes `copied <path>`; a web address, a request route or a word such as
  `text/plain` in free text becomes `<path>`; and a file error that is not one
  of the recognised ones loses its ending. The earlier wording that the owner
  had accepted a leftover word (`Wayne` in `/Users/Bruce Wayne`) was wrong: it
  was an assumption in the plan, and that leftover is gone.
- Three shapes remain that no rule can tell from ordinary prose: a relative
  path whose first folder name ends in a space or an opening quote, a folder
  name that contains a line break (the next line is not recognised), and an old
  Mac-style `Macintosh HD:Users:bruce` path. A bare user name or host name
  written outside any path is also not removed.

## What it was

The bundle's path scanner walked through text looking for something that looked
like the start of a path, and decided where it stopped at the first space or
punctuation mark. Folder names are allowed to contain both. The first
replacement still guessed where a name ended, and left the words after the last
slash in the text.

## Why it mattered

A support bundle is meant to be handed to a third party. A user name, a home
folder or a mounted network share in it is personal information, and the
administrator would not know it was there.

## The fix

The scanner is deleted. From the first word that could hold a location to the end
of its line, the text is replaced (including every encoded spelling); only a
file error that ends the line in a recognised wording is kept after it. The old
hold on the feature, which refused to build a bundle at all, is removed in the
same change.

## What it still costs

Log and error text is less detailed than before, and the module list in the
bundle's build section reads `<path>` because module names contain slashes. The
build section can be given a typed form later.
