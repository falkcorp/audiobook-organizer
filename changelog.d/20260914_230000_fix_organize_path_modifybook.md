### Fixed

- Organize, batch, quarantine and version-swap book writes now go through
  `ModifyBook`, which sets only the columns each site owns on the stored row
  under the book's write lock. Until now every one of these paths read the
  book, did its work (file moves, a batch edit) and wrote the whole row back,
  so any column another writer committed in between -- a metadata apply's
  duration, a scan's file counts -- was silently reverted (audit A1#15,
  lost-update race). Converted: the organizer's `modifyBook` helper (its six
  callers: already-in-place stamp, post-move stamp, `stampOrganizeMetadata`,
  and the three adopt-into-version-group writes in `inplace_collision.go`)
  and the original-book demote in `CreateOrganizedVersion`; batch
  `UpdateAudiobooks` and the update/soft-delete/restore actions of
  `ExecuteOperations`; `QuarantineBook`, `UnquarantineBook` and
  `ProcessITunesPurgePending`; and the version swap's `file_path` write. The
  stamp of a book `CreateBook` just returned stays a whole-row write, since
  nothing else can have written it yet. One lost-update test per package
  proves a concurrent `Duration` write survives each path.
- Batch update: a request that does not name `series_id` no longer clears the book's series. Until now an absent key read like an explicit null, so every batch edit that omitted the field unlinked the book from its series.
