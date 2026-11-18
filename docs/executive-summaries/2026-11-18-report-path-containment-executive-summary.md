<!-- file: docs/executive-summaries/2026-11-18-report-path-containment-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0b6f2c1e-7a34-4d58-9e21-c5a8d3f40b97 -->
<!-- last-edited: 2026-11-18 -->

# Maintenance reports can only be written into the report folder

**Date:** 18 November 2026. A fix to the maintenance operations that write a
report file, found while reviewing the shape-report operation.

## Executive Summary

- Many maintenance operations write a report of what they found or did. By
  default it goes into a hidden reports folder inside the library. Each
  operation also lets the person starting it name a different file for the
  report, and that name was used as given.
- That meant anyone allowed to start one of these operations could name any file
  the server is able to write, and the operation would write its report over it.
  The report is plain text, so the damage depends on which file was named, but
  an overwritten file the server relies on would be lost.
- Now a report may only be written inside the reports folder. A name without a
  folder is placed there; a full path is accepted only if it already points
  inside it. A path that climbs out with ".." is refused, and so is a path that
  goes through a shortcut (a symbolic link) leading elsewhere, a report file that
  is itself such a shortcut, and a path that names a folder instead of a file.
  A refused path creates nothing. With no library folder configured there is
  nothing to keep a named report inside, so a named report is refused.
- The check sits in the one helper that nine of the operations already use to
  find their report file. Two operations (the series clean-up and the series
  phantom repair) skipped that helper and wrote the named path directly; they
  now go through it and are checked before they read the library or write
  anything.
- What changes for the person using them: a report named outside the reports
  folder is now refused with a message naming the folder. Naming a file inside
  it, or just a file name, works as before.
- Tests cover climbing out with "..", an absolute path elsewhere, a shortcut
  inside the reports folder that leads out of it (whether its target exists or
  not), a report file that is a shortcut, and a path that is accepted; they run
  against the helper and against every operation that writes a report.

## What it was

A report path supplied by the caller was trusted. Only the default path, built
by the program, had its folder checked.

## Why it mattered

An operation started with a chosen report path could overwrite a file outside
the library's report folder, with report text.

## The fix

The caller's path is resolved first (following any shortcuts in its folders) and
accepted only if it ends up inside the reports folder, before anything is
created or written.
