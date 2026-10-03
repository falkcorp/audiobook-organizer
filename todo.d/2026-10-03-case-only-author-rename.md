- [ ] **EDIT-AUTHOR-CASE-RENAME** A case-only author edit in the book editor
      ("alice able" -> "Alice Able") resolves to the same author row and does
      not rename it; the book keeps showing the row's spelling. Series got
      this fix in PR #3698 (a sole-member row is renamed after the commit
      through the guarded RenameSeriesIf; a shared row keeps its name). The
      store has no guarded author rename yet (only UpdateAuthorName), so
      authors need a RenameAuthorIf (compare-and-set under the author name
      index lock, refusing a name another author answers to), then the same
      sole-credited rule in audiobooks.UpdateAudiobook, with tests.
