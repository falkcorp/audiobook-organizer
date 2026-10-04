- [ ] **EDIT-JOIN-WRITE-STRIPE** Run the book edit endpoint's post-commit
      join writes (book_authors from SetBookAuthors, book_narrators from
      SetBookNarrators, in audiobooks.UpdateAudiobook) under the book's write
      stripe. They now run after ModifyBook commits but outside the stripe,
      because PebbleStore.lockBook is unexported and the join writers cannot
      be called from inside ModifyBook's callback. A concurrent apply or
      narrator sync touching the same join in that window can be lost either
      way. Done = a store API that commits the row and its join rows under
      one stripe hold (or a stripe-aware join writer), used by the endpoint,
      with a race test.
