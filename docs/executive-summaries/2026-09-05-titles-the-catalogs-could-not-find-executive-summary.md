<!-- file: docs/executive-summaries/2026-09-05-titles-the-catalogs-could-not-find-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7c2f9a4e-5d1b-4b8e-a6c3-2e9f0d7b1a54 -->
<!-- last-edited: 2026-09-05 -->

# Titles the catalogs could not find

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3074

## Executive Summary

- Overnight on 5 September the app started fetching details for the 10,080 books in
  the library that still have none. Within the first hundred, 73 came back as "no
  catalog has this book". Every one we checked was a real, popular audiobook that
  Audible sells today.
- The reason was the question, not the catalogs. The library names many books the way
  a folder would: "Eternal Dominion, Book 04 - Assertions", "Path Of The Voidwalker -
  BK07". No catalog files a book under a name like that. Asked for "Assertions" by
  Bern Dean, Audible answers at once; asked for the folder name, it says nothing, and
  so does everyone else.
- One catalog, Google Books, is forgiving enough to match the folder name some of the
  time, and it sits last in the list of places the app asks. So Google was quietly
  carrying the whole job — until its daily allowance ran out at 4:00 in the morning.
  For the next 1,600 books, not one detail was fetched. That explained something we
  had puzzled over on 3 September: why the only catalog ever mentioned in the logs was
  Google. It was not that the app skipped the others. The others had nothing to say.
- We also learned that day exactly what Google's allowance is. Its own project answers
  one thousand questions a day, the standard starting allowance. Earlier probes had
  suggested it was already used up before the day began; those probes had been asking
  without our key, and were measuring the public pool that everyone shares, which is
  always empty. That mistake cost two hours of waiting for a reset that had already
  happened.

## What changed

- **The app now asks the way a person would.** When the folder-style name draws a
  blank, it works out the book's own name from the series name and number, and asks
  for that first, with the author. "Eternal Dominion, Book 04 - Assertions" becomes
  "Assertions" by Bern Dean. Audible answers first, so far fewer books ever reach
  Google and its thousand-a-day limit.
- **It only accepts an answer that names the book.** What the app fetches is
  remembered for a week, and one of the ways it fetches applies the first answer with
  no one watching. Asking for a series name would bring back the series' other books,
  and those could have been filed against the wrong title and never asked about
  again. So a rescued answer is kept only if its title carries the words that name
  this book — not the series' own words, not "novel" or "edition" — and its author
  agrees. A folder name that is nothing but a series and a number — "Path Of The
  Voidwalker - BK07" — is left as "not found" and will be tried again later, because
  no question can tell that book from its siblings. Reviewers caught both of these
  gaps before the change went live; the first version would have accepted a series
  name's answer on the bulk path.
- **A closed door is not knocked on eight times.** When a catalog has said "you have
  used your allowance" or is switched off for being unwell, the app stops asking it
  about that book straight away, and what it reports is the catalog's own words, not
  the note that it is switched off.
- **Each fetched book records how it was found**, so the next bulk run can show how
  many books the new questions rescued.

## What this does not do

- It is not deployed yet. Putting it live restarts the server, and the server would
  then resume a library scan that was cut short earlier in the night. That is a
  decision for the morning.
- It does not raise Google's allowance. A thousand questions a day is a starting
  number Google will increase on request from the account's own console; the app has
  nothing to turn up on its side.
- It does not help the 9,092 books whose audio files the app can no longer find on
  disk. A separate overnight job that transcribes the first minute of each book, to
  confirm what it is, finished in about a minute because it could reach only 14 of
  them. Those books need their files found first; that is already tracked.
