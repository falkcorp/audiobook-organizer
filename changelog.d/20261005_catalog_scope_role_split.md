### Fixed

- The author-catalog harvest scope no longer harvests a translator, editor or
  illustrator as an author when the credit has a name suffix or a comma-form
  role. "Jane Doe, PhD - translator" and "John Smith, Jr. - editor" keep the
  suffix with the name and are skipped whole. "Jane Doe, illustrator", "Jane
  Doe, ed." and "Haruki Murakami, Jay Rubin, translator" treat the bare role
  word as marking the name before it. Only "Haruki Murakami" is harvested from
  the last one.
- Authority lists: `ref_asin:` rows written before ASIN rows recorded their
  sources take those sources from the stored person rows they point at. They
  are no longer held forever. A row whose person rows are gone stays held and
  is listed in the plan. The duplicate count now counts only dropped payloads:
  the same payload handed in twice is not a duplicate.
