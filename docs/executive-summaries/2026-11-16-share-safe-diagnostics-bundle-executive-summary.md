<!-- file: docs/executive-summaries/2026-11-16-share-safe-diagnostics-bundle-executive-summary.md -->
<!-- version: 1.0.0 -->
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
  for web-address-style spellings of a path.
- The bundle now works the other way round. Any word that could be part of a
  location is replaced, and the words in between go with it, so a folder name
  with spaces disappears whole. The text of a file error is understood exactly,
  so `open /Volumes/My Books/x: permission denied` becomes
  `open <path>: permission denied`: you still see what failed and why, not
  where.
- A file inside one of your library folders is shown as the name of that
  folder and nothing after it, because what follows is usually an author or a
  title.
- Fields that are not free text (log level, time, component, request route,
  reason) keep their value when it has the expected shape, so a bundle stays
  readable.
- The same rule now applies to the health page's detail text, which had no path
  protection at all, and to the names of the files inside the zip.
- It is tested the way the leak was found: a marker is planted at the end of
  more than a hundred thousand generated locations in every spelling, and must never
  appear in the output. A fuzzer runs the same check on random input. A test
  builds a whole bundle from a made-up log and scans every byte of the zip for
  the planted names.
- Two things were accepted by the owner and are still true. A prose sentence
  that starts with a path whose last folder has a space in it can keep the last
  word (`user dir /Users/Bruce Wayne is full` keeps `Wayne is full`), and a bare
  user name written outside any path is not removed. In return, free text loses
  every web address, route and `a/b` word, because the bundle prefers losing a
  useful clue to keeping a private one.

## What it was

The bundle's path scanner walked through text looking for something that looked
like the start of a path, and decided where it stopped at the first space or
punctuation mark. Folder names are allowed to contain both.

## Why it mattered

A support bundle is meant to be handed to a third party. A user name, a home
folder or a mounted network share in it is personal information, and the
administrator would not know it was there.

## The fix

The scanner is deleted. Words that could hold a location are replaced as a unit
(including every encoded spelling), file errors are read by their fixed
structure, and the old hold on the feature, which refused to build a bundle at
all, is removed in the same change.

## What it still costs

Log and error text is less detailed than before, and the module list in the
bundle's build section reads `<path>` because module names contain slashes. The
build section can be given a typed form later.
