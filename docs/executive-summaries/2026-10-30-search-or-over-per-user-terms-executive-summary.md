<!-- file: docs/executive-summaries/2026-10-30-search-or-over-per-user-terms-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 814eac40-0c4c-492f-a3db-9025d802d7e9 -->
<!-- last-edited: 2026-10-30 -->

# "Or" now works with your own listening state in searches, collections and playlists

## Executive Summary

A search such as "title:Alpha OR read_status:finished" means "books titled Alpha, plus every
book I have finished". The app used to read it as "books titled Alpha that I have also
finished", which quietly dropped most of the books a person expected to see. A later safety
check stopped that wrong answer by refusing the search with an error. Neither is needed now:
the search is answered the way it is written.

## What changed

- **The three personal fields work with "or" and "not".** Reading status, progress and last
  played can sit on either side of an "or", and a "not" in front of a group ("not both finished
  and by this author") means what it says.
- **Each person sees their own answer.** A collection or playlist built this way follows whoever
  is looking at it; with nobody signed in it shows nothing, as before.
- **Ordinary searches are untouched.** Searches that only combine terms with "and" take the same
  path as before.
- **The checks on values stay.** A reading status that does not exist, or a date that cannot be
  read, is still refused with a message, wherever it appears. A tag inside an "or" is still
  refused.

## Why it mattered

The wrong answer carried no warning, so a collection or playlist defined with an "or" simply
showed too few books. Replacing the refusal with a correct answer means people can write the
query they mean.
