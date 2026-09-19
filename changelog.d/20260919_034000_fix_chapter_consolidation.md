### Fixed

- The Chapter Consolidation card on the Maintenance page works again. Its buttons used to answer "endpoint not found"; they now start background jobs, follow them to the end, and show what was found: each group of chapter files, how many book records it covers, and for a merge, what happened to each group.
- The card's minimum-files, seconds-per-file and folder settings are now actually used. They were ignored before, so every scan covered the whole library with fixed settings.
- A chapter merge now folds every chapter into the chapter 1 book instead of whichever chapter happened to be listed first.
- A merge no longer replaces a book title that someone set or fetched with a name made from the file name. It only fills in a title that is empty or still just the file name.
- Chapters with no known length no longer count as "short chapters", so full-length books whose length was never read can no longer be merged together by mistake. The result says how many groups were left out for this reason.
- Running a merge a second time no longer picks up chapters that an earlier merge already absorbed.
- Chapter merges can now be undone. Each merge saves an undo record before it changes anything, stays away from the iTunes library, moves iTunes IDs to the kept book, and writes a review record listing the kept book, the merged books, their old titles and the files that moved.
- Merging still defaults to a dry run. A real merge needs a preview first and a confirmation that shows how many book records will be merged.
