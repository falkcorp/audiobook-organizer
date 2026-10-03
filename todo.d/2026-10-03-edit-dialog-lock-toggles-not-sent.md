- [ ] **EDIT-DIALOG-LOCK-TOGGLES** The lock buttons in the metadata edit
      dialog do nothing on save. MetadataEditDialog keeps them in local
      `lockOverrides` state (toggleLock / isFieldLocked) but handleSave calls
      `onSave(audiobook, dirtyFields)` only, and BookDetail.handleEditSave
      builds overrides from dirtyFields alone, so a lock or unlock the user
      toggled without editing the field never reaches the server. Send the
      toggles (lock-only overrides `{"locked": true|false}` are already
      handled by PUT /audiobooks/:id), with a Vitest covering a toggle-only
      save.
