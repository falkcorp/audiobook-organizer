- [ ] **ABS-JUNK-AUTHOR-NAMES** Owner review: trial and apply the junk-author repair for the
      tag-shrapnel author rows the ABS Newest Authors shelf exposed on 2026-10-10 (`&#169`,
      `(c) 2001 <name>`, `10 831MB] #<tag>`, `13 short stories`). They ARE credited on visible
      books (the ABS author list is built from visible books only), so the shelf fix in
      `fix/abs-newest-authors-shelf` hides them only while their books are old; a junk row
      on a recent import still shows. No rows were changed by that PR.
      **Already covered (no new code needed to find them):** `authorjunk.ClassifyName` flags
      all four as strong `other_non_person` — the first, second and fourth through the
      creation gate (`personname.IsPlausibleAuthorName`: `html_entity`, `copyright`,
      `story_count`), the size/hashtag one through `filename_shape` (and from that PR on also
      through the gate's new `release_tag` rule). The fixer is `maintenance.repair-junk-authors`
      in /review Repairs; with no evidence of the real author it plans an UNLINK at
      `RiskReview`, so every row needs the owner's approval.
      **Where they came from:** rows created before the 2026-08-14 / 2026-09-25 creation gates
      (`IsDirtyAuthorName`, then `IsPlausibleAuthorName` in `PebbleStore.CreateAuthor`).
      `&#169` matches the known `;`-split of an HTML-escaped copyright line in an artist tag
      (`metadata.splitMultipleAuthors`, entity-protected since 04066efbc; still open in the
      "Author table: copyright text and HTML entities" section above, author id 46583). The
      size/hashtag shape (a torrent/forum release title in a tag) passed every gate until the
      `release_tag` rule. No author path HTML-unescapes; the 2026-09-10 decision was "reject,
      do not unescape", and that still holds — decoding `&#169` yields `©`, which is no more
      an author.
      **Steps:** (0) BEFORE deploying the `release_tag` gate rule, dry-run
      `personname.IsPlausibleAuthorName` over the whole prod author table and confirm every
      `release_tag` refusal is junk — the rule was only checked on synthetic names, and the
      gate runs inside `PebbleStore.CreateAuthor` before the existing-row lookup, so a false
      positive would drop a real author's credit on every re-import, not just new mints;
      (1) get counts by pattern from a prod Plan run of the fixer (preview only, bulk-apply
      gate rules apply); (2) owner approves per row in /review Repairs; (3) after apply,
      confirm the ABS Authors tab and Newest Authors shelf no longer list them.
      Remaining gap: `metadata.splitMultipleAuthors` only protects entities that still end
      in `;`; an already-truncated `&#169 2001 X` in a tag is now refused by the gate rather
      than repaired.
