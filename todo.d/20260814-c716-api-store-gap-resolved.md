## C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument + 2 quarantined + 0 unexplained

Measured on production 2026-08-14 (~10:30 EDT), every instrument controlled:

- **3,953 of the gap was the measuring instrument.** The 67,824 "live" store
  count (2026-08-13, search reconciler log) came from the leaky Pebble
  `ListBookIDs` that still counted soft-deleted books — the drift fixed on
  main (264585b5, PR #2408). The 10:01 post-fix boot logs now say
  `books=63871` (search coverage) and `totalBooks=63871` (iTunes PID
  backfill — a second, independent caller). 63,871 + 3,953 (trash set,
  re-verified intact via `/audiobooks/soft-deleted` total) = 67,824 exactly.
- **2 books remain store-visible but list-invisible, and both are
  QUARANTINED** (`quarantine_reason: "taglib permanently unreadable after
  transcode attempt"`, quarantined 2026-04-24):
  `01KNDC17RY2ATJFRACA50N9AMJ`, `01KNDC4VB60GTBQ137A0YJ29KX`.
  The default list applies `ExcludeQuarantined` unless
  `?show_quarantined=true` (`audiobooks_helpers.go` /
  `buildAudiobookListResponse`) — a hidden-but-intentional default filter.
  Note the **inconsistent visibility**: both books are still served by
  direct GET `/audiobooks/:id` AND by `/authors/:id/books` (author path does
  not exclude quarantined). Decide whether that asymmetry is wanted.
- **The API list is internally consistent**: full page-through returned
  63,869 rows, 63,869 distinct ids, zero duplicates; `count` stayed 63,869
  at an off-the-end offset (control); metadata-export (`GetAllBooksCore`,
  independent store path) returned 63,871 with the two quarantined ids as
  the exact set difference.

Follow-up bugs found by the controls (route to C1/C3, do NOT fix here):

- [ ] **`show_quarantined=true` SHRINKS the list.** A flag that can only
      widen the set returned 41,319 books against the default 63,869.
      41,319 = 41,317 (`is_primary_version=true` count) + the 2 quarantined
      — i.e. with the quarantine exclusion off and no explicit
      `is_primary_version` param, the scan path serves only primary-flagged
      books and silently drops the 22,552-book nil-flag population. Same
      family as the filed nil/false `is_primary_version` divergence
      (`effectiveBoolFieldIndex{Default:true}` vs raw `*bool`); this is a
      second concrete symptom, on the main list path. With an explicit
      `is_primary_version=false` the flag behaves (22,552 with or without
      quarantine).
- [ ] `is_primary_version=false` answers 22,552 — exactly the known
      nil-flag population size. Establish whether explicit-false books are
      currently 0 in prod or whether the false-filter is returning nils
      (memory says the census counted ~765 explicit-false in one path).
