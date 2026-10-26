### Fixed

- A copy of a retired multi-file version that is still tagged with the trashed book's organizer ID is now recognised as retired content. Its segments are hashed before the check, so the scan no longer relinks the trashed book onto the copy and gives it live files.
- The organizer refuses a collision whose byte-identical occupant is a retired version's file, instead of quarantining the live source and marking its row Missing. Nothing moves, and the failure names the retired row.
- Reverting a repoint no longer hands a path key back to a retired owner while any live row, an iTunes row included, carries the path.
- `MockStore.HasRetiredVersions` follows the complete reads a test stubs, so a MockStore scanner test can reach the retired-content check.
- Test fixes: the retired repoint test looks up its row at the right path, the deluge import and lost-update fakes resolve the retired reader, the live-outranks-retired revert test can now fail, a generic path-lookup error stopping a combine is covered again, and a duplicate provenance test is gone.
