- [ ] **ABS-JUNK-AUTHOR-NAMES** Owner review: trial and apply the junk-author repair for the
      tag-shrapnel author rows the ABS Newest Authors shelf exposed on 2026-10-10 (an HTML
      entity, a copyright line with a year, `<digits> <size>] #<tag>`, a story-count phrase).
      They ARE credited on visible books (the ABS author list is built from visible books only), so the shelf fix in
      `fix/abs-newest-authors-shelf` hides them only while their books are old; a junk row
      on a recent import still shows. No rows were changed by that PR.
      **What the classifier does with them:** `authorjunk.ClassifyName` flags all four as strong
      `other_non_person` through the creation gate (`personname.IsPlausibleAuthorName`:
      `html_entity`, `copyright`, `story_count`, and the new `release_tag`). The size/hashtag
      shape used to be caught by `filename_shape`, a rule the repair treats like any other
      unlink-eligible one; from that PR on the gate's `release_tag` rule fires first, so the
      verdict's rule is `author_creation_gate` (the label changes, the strength does not). The
      same rule now also refuses names it never refused before: a real name with a release-post
      group appended (`<name> [<bitrate> <timecode> <size>]`, `<name> {<size>}`,
      `<name> (<size>)`, `<name> #<tag>`). Those are junk rows with a real author inside, and
      they are unlink-eligible, so `authorjunk.CleanedName` strips a trailing group that holds
      a size or a hashtag and the repair proposes RELINK to the cleaned name (source
      `cleaned_name`) when an author of that name with books exists. `<name> #<tag>` (a hashtag
      outside a group) is NOT cleaned: it is refused by the gate and, with no evidence of the
      real author, planned as UNLINK, so step (2) must check each such row for an embedded name.
      The fixer is `maintenance.repair-junk-authors` in /review Repairs; with no evidence of the
      real author it plans an UNLINK at `RiskReview`, so every row needs the owner's approval.
      **Where they came from:** rows created before the 2026-08-14 / 2026-09-25 creation gates
      (`IsDirtyAuthorName`, then `IsPlausibleAuthorName` in `PebbleStore.CreateAuthor`).
      The HTML-entity row matches the known `;`-split of an HTML-escaped copyright line in an
      artist tag (`metadata.splitMultipleAuthors`, entity-protected since 04066efbc; still open in the
      "Author table: copyright text and HTML entities" section above, author id 46583). The
      size/hashtag shape (a torrent/forum release title in a tag) passed every gate until the
      `release_tag` rule. No author path HTML-unescapes; the 2026-09-10 decision was "reject,
      do not unescape", and that still holds — decoding a copyright entity yields the
      copyright sign, which is no more an author.
      **Steps:** (0) BEFORE deploying the `release_tag` gate rule, dry-run
      `personname.IsPlausibleAuthorName` over the whole prod author table and confirm every
      `release_tag` refusal is junk — the rule was only checked on synthetic names, and the
      gate runs inside `PebbleStore.CreateAuthor` before the existing-row lookup, so a false
      positive would drop a real author's credit on every re-import, not just new mints;
      DONE 2026-10-11 02:25 against the live author list (15,690 authors via
      GET /api/v1/authors): 18 `release_tag` refusals, all junk (strings of the shapes
      `<bitrate> <timecode> <size>`, `<digits> <size>] #<tag>` and
      `<name> [<bitrate> <timecode> <size>]`), zero real names. All checks together flag 776
      existing rows (leading_punctuation 177, edition_marker 151, read_by 114,
      timecode_or_track 93, too_long 40, structural_word 30, release_tag 18,
      pure_number 17, placeholder 11, era_tag 10, story_count 8, html_entity 5,
      copyright 2) — counts only; step (1) still decides per row;
      (1) get counts by pattern from a prod Plan run of the fixer (preview only, bulk-apply
      gate rules apply); (2) owner approves per row in /review Repairs; (3) after apply,
      confirm the ABS Authors tab and Newest Authors shelf no longer list them.
      Note: Newest Authors refreshes on the author index's 5-minute timer, not on each import, so
      a cleaned row can take up to about 5 minutes to leave the shelf and a new author up to about
      5 minutes to appear.
      Remaining gap: `metadata.splitMultipleAuthors` only protects entities that still end
      in `;`; an already-truncated `<entity> <year> <name>` in a tag is now refused by the gate rather
      than repaired.
