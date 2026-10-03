- [ ] **EDIT-DIALOG-YEAR-CLEAR** Clearing the Year box in the book editor does
      not clear the year. BookDetail.handleEditSave sends
      `audiobook_release_year: updated.audiobook_release_year || updated.year
      || book.audiobook_release_year || undefined`, so an emptied box falls
      back to the stored year, and the dirty-field override sends null, which
      ApplyOverrideToPayload ignores (it only accepts a number for
      audiobook_release_year). Make an emptied box send a clear end to end
      (null top-level and override, applied as a nil year), with a Go test and
      a Vitest.
