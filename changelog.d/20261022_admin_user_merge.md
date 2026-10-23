Added an admin tool to merge one user account into another, for when the same
person ended up with two accounts. On the Users page, "Sign-ins & data" shows
an account's linked sign-ins and how much listening data it holds, and "Merge
into..." previews the merge (counts, collisions, sign-ins, who holds the owner's
Cloudflare Access / OAuth identity, refusals) before anything is written. The
confirm step sends the previewed digest, so a merge only runs against the plan
the admin read, and the owner's identity moving needs an explicit
acknowledgement. The merge runs as the `maintenance.user-merge` operation, is
journaled (`usermerge:journal:`), resumes after a crash, and undoes field by
field without overwriting anything written since; undoing it signs the surviving
account out. Routes: `GET /users/:id/footprint`, `GET /users/merges`,
`POST /users/:id/merge-into/:target/{preview,apply}` and
`POST /users/merges/:jid/{resume,undo}`; every POST is a credential route behind
the owner gate, so no API key or Audiobookshelf token can start one, and the
operation refuses to write without a single-use grant those routes mint. The
operation row carries only counts and ids, never usernames or emails. A
merged-away account cannot be reactivated (undo the merge instead), and
`ResolveMergedUser` lets book-merge undo journals and Audible read-status
reverts that name a merged-away account act on the account that holds its data
now.
