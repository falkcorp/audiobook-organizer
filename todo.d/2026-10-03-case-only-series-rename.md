- [ ] **EDIT-SERIES-CASE-RENAME** A case-only series rename in the book editor
      ("the saga" -> "The Saga") does not rename anything. On a book with no
      author the case-insensitive lookup resolves to the same row and the
      edit is silently dropped (200, name unchanged). On a book with an
      author the lookup is scoped by that author, so it creates a second
      series row "The Saga" and moves the book onto it. Decide whether a
      case-only edit should rename the shared series row or be refused with
      a message, then implement and test both shapes.
