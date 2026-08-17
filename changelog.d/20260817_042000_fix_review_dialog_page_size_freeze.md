### Fixed

- **The metadata review dialog could be frozen permanently by its own page-size
  setting.** Choosing "250 per page" locked the dialog up, and because the stored
  preference was only checked for membership in the options list — where 250 was a
  legal member — every reopen restored 250 and froze it again. The size control
  lives inside the dialog, so there was no way back: the only escape was clearing
  `localStorage` by hand. A stored size is now clamped to 50 on read and the
  correction is written back, so existing stuck users recover on next open. The
  selector no longer offers 250 or 500.
