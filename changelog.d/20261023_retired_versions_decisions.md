- The retired-versions preview now honours the owner's rejects when it plans a
  book in the trash (retired-version plan, PR 8 of 9). A reject of a book's
  `repairs:retired-versions` proposal (the book is the entity) holds that book
  whatever its target, with `owner_veto_family` `shell_rejected`. A pair veto
  in `same_work` or `same_copy` between the book and its target still holds the
  row, as before.
- A veto that names a book which was later merged away, retired into another
  book or purged no longer fails open. `decisions.RetiredBookResolver` follows
  `idx:bv:from:<shell>` to the book that holds the shell's files, a live book is
  never redirected, and `decisions.ChainResolvers` chains it with
  `merge.ResolveMergeSurvivor` (in `maintenance.NewOwnerVetoReader`) until the
  id stops moving. The plan reads the rejects from one snapshot per run; a run
  that cannot load them is refused instead of becoming a plan of error rows. The
  exact, unresolved pair lookup stays beside the resolved one, so a veto on a
  book and the book it was merged into still holds the row.
- New helpers for the Apply and the revert path:
  `decisions.RecordRetiredApplied` and `decisions.RecordRetiredUndone` write an
  `applied` or `undone` record on the shell's subject. Both are labels, not
  vetoes, and a retry writes no duplicate. They are not called yet: the Apply
  and the revert land in their own PRs.
