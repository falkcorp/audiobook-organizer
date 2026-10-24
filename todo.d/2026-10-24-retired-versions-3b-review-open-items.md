- [ ] **TODO-RETIRED-3B-REVIEW-OPEN-ITEMS** left open by the review fixes on the
      retired-versions callers change (3b), each needs an owner call or is
      outside that change: (1) `merge/service.go` `planVirtualFile` still
      mints a NEW live row at a path a retired row holds ("the path stays
      shared with it"), which contradicts the rule that no two rows name one
      file; the lookup error is now honoured, the shared-path decision is not
      changed. (2) The claim rule is still split: `liveOwnersOf`
      (`build_folder_book_files.go`), `rfOtherOwner`
      (`repoint_missing_to_folder_audio.go`) and `leftoverFreshPathOwners`
      (`consolidation_leftovers_samepath.go`) skip retired rows as owners while
      the repoint, recover, rewrite-prefix and unrecorded-rename ops count them;
      write the one rule in `iface_retired.go` and align these three.
      (3) `merge/service.go` `planOwnFile` reads `ms.db.GetBookFiles(b.ID)` and
      drops its error, so a failed read plans a second row for a book that has
      files. (4) `database.BookFilesHoldingHash` turns a MockStore's unstubbed
      (fail-closed) `BookFilesWithHash` into "empty" for a store declaring
      `NoRetiredBookFiles`; correct for the retired-holder question it answers,
      but a scanner test that never stubs the hash lookup passes without
      reaching the retired check. Making it fail closed again needs a stub in
      each MockStore scanner test that imports a hashed file (15 scanner test
      files use MockStore; how many reach the check is unmeasured).
