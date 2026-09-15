- [ ] **Repoint the path index to the surviving row when the owner moves away.**
      When book_file rows X and Y share path P and `book_file_path:P` names X,
      X moving away (rename, repoint) deletes the index entry while Y is still
      live at P. `GetBookFileByPath` callers (internal/organizer/collision.go,
      internal/deluge/import.go) read nil as "path free", so a later import
      can land on an occupied folder. Fix: on owner-checked delete, look for
      another live row at P and repoint the index to it. Found in the #3426
      review, 2026-09-14.
