- The retired-versions preview now honours the owner's rejects when it plans a
  book in the trash (retired-version plan, PR 8 of 9). A reject of a book's
  `repairs:retired-versions` proposal (the book is the entity) holds that book
  whatever its target, with `owner_veto_family` `shell_rejected`. A pair veto in
  `same_work` or `same_copy` between the book and its target holds the row too.
- A reject keeps holding after the books it names move. Every stored id of a
  reject is resolved to a path: `decisions.RetiredBookResolver` follows
  `idx:bv:from:<shell>` to the book that holds the shell's files (a live book is
  never redirected), and `maintenance.NewOwnerVetoReader` chains it with
  `merge.ResolveMergeSurvivorPath` until the id stops moving, keeping every book
  the id went through. `decisions.Vetoes` keeps a reject whose two ends collapsed
  into one book, with both paths, and `PairVetoed` holds a pair when one end's
  path took one of the books and the other end's path took the other. So a reject
  of (A, Y) still holds Y's row after A was retired into, merged into or absorbed
  by (a combine's sync redirect) Y's target, and the unresolved-name lookup the
  first version of this PR kept beside the snapshot is gone: the snapshot is the
  one lookup. A generic `Vetoes.PairVetoed(7, 9)` with 9 merged into 7 now
  answers true, because the owner said they are not the same.
- An id that cannot be resolved no longer refuses the whole run or fails open. A
  shell whose retired versions name two holders that each still hold rows, and a
  merge chain that loops, are held per id: only the rows whose pair a stored
  reject touches (or whose own ids are such an id) become the new category
  `owner_veto_unresolvable`, risk `review`, with every unresolvable id in the
  row's reason, evidence and `owner_veto_unresolved_ids`, and once in the plan
  log. The other rows are planned as usual. A definite veto on the same pair
  still wins. A run is refused only when the rejects cannot be READ (the decisions
  store or a merge journal failed), as before. When BOTH ends of a stored reject
  are unresolvable, the rows held are the ones that touch a book the failed
  resolutions went through; that is best effort, and the log lists the ids.
- `RetiredBookResolver` ignores a retired version that holds no rows. A revert
  puts the rows back and leaves the version, so a shell that was retired,
  reverted and retired again used to look as if it had two holders.
- A re-plan of one row no longer re-reads the whole decision log. The Server
  hands each plan or re-plan its own owner-veto reader (a re-plan must see the
  books as they are now) over a shared `decisions.RowCache`, which keeps only the
  raw rows of the last scan and drops them when a record is written (every write
  of a `decision:` key goes through `Store.commit`, held there by
  `TestDecisionKeysHaveOneWriter`). What a re-plan still pays: the merge-journal
  scan for the flag-holder follows, the combine-journal scan, and resolving every
  stored id (a few reads each, memoised within the reader). A resolved snapshot is
  deliberately not cached across re-plans: an Apply changes the book and retire
  state between them, and a stale snapshot would drop a reject.
- `decisions.RecordRetiredApplied` and `decisions.RecordRetiredUndone` write an
  `applied` or `undone` record on the shell's subject, for the Apply and the
  revert path. Both are labels, not vetoes. They need a shell id, a version id
  and the id of the op that performed the event, and a retry that finds the same
  (decision, version, op) already on the subject writes nothing, the check and
  the write under one subject lock (`Store.RecordUnlessSeen`). They are not
  called yet: the Apply and the revert land in their own PRs.
