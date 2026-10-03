- [ ] **EDIT-STALE-SERIES-NOTE** Tell the user when a book edit resolved a
      field to something other than what they typed. Today the only case is
      a series name that matched the book's stale embedded series name after
      the series row was renamed: the edit keeps the link and shows the row's
      current name, and logs it (audiobooks.planSeriesEdit). PUT
      /audiobooks/:id returns the bare book with no notes or warnings field,
      so the editor cannot show it. Done = a notes/warnings field on the
      response, filled by UpdateAudiobook, shown by BookDetail.
