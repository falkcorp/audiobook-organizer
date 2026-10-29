### Fixed

- The retired-versions `applied` and `undone` decision records now say who ran
  the apply or the revert. An apply records the user who enqueued the apply op
  and the plan op it applied; a revert records the signed-in user who asked for
  it, including through the combine undo's retire reversal. Both read as a human
  decision. An apply or revert no user ran is still recorded as automatic. A
  retire finished by a later op's tail records the retire op's own user and plan,
  not the finisher's.
