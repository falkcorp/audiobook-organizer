### Fixed

- A rename's undo row for `library_state` recorded `organized -> organized` because the previous state was read after it had been overwritten, so undoing a rename could never restore the pre-rename state. The prior value is now captured first.
