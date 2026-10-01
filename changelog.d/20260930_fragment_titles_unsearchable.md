### Fixed

- The metadata candidate fetch no longer searches a book row that is one chapter file of a set the scanner filed as separate rows. Such a row is skipped outright, with no stand-in title, so a whole book's candidate is no longer attached to one chapter file. A row counts as a chapter part when it is a short single-file row and its folder holds sibling rows that are not twins at the same path. The shapes covered are:
  - plain chapter numbers ("06 Chapter 6", "98"), which used to borrow the folder's title, beside chapter siblings;
  - `_copyN` titles;
  - "Cobra 100 of 151" beside two or more counted siblings;
  - "The Sunrise Lands 1" or "Sealed to the Flame E" beside two or more stem siblings;
  - a folder name with rip details ("[64k 20;57;42 577MB]") stamped onto chapter rows.
- These titles are still searched as books:
  - dramatized products such as "Golden Son (Part 1 of 2)" and "Dark Age (2 of 3)";
  - any file that runs at least the chapter-consolidation threshold;
  - "Apollo 13", "Plan B" and "Henry V";
  - a lone chapter row (it keeps its stand-ins).
- Rip details on a book's own title are cleaned off.
- Sibling listings are read once per folder per pass and never for a library or import root. A failed listing is logged at Warn.
- `force` on the batch candidate fetch and `?refresh=true` on the search dialog now re-ask the providers instead of replaying the per-source fetch cache (`SearchOptions.BypassFetchCache`). Fresh results still replace the cached rows.
- A title with an unspaced-colon subtitle ("In Fire Forged: Worlds of Honor V") is also searched by its main title, anchored on the author, when the literal searches find nothing. The series' omnibus or box set is never accepted for it.
