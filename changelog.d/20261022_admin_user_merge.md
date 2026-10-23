Added an admin tool to merge one user account into another, for when the same
person ended up with two accounts. On the Users page, "Sign-ins & data" shows
an account's linked sign-ins and how much listening data it holds, and "Merge
into..." previews the merge (counts, collisions, sign-ins, who holds the owner's
Cloudflare Access / OAuth identity, refusals) before anything is written. The
confirm step sends the previewed digest, so a merge only runs against the plan
the admin read, and the owner's identity moving needs an explicit
acknowledgement, which is also required when the plan cannot tell whether it
holds the owner's sign-in (no `owner_email`, or a sign-in with no address). The
survivor keeps an address the merged-away account had verified, as verified. The
merge runs as the `maintenance.user-merge` operation, is journaled
(`usermerge:journal:`), and resumes after a crash by planning the work that is
left again; that is safe to repeat because each sign-in, API key, book and
listening-seconds counter is written in a single batch with the index entries
or the delete that go with it, so a stop never leaves a sum added but its
source still there. It undoes from the journal without overwriting anything written
since: a sign-in, API key, playlist, collection, invite or invite email grant
goes back with only its owner field changed (an API key's use counters, or a
playlist renamed since, are kept), a book's listening state goes back whole or, if it changed
since, stays whole with its status index rebuilt; undoing it signs the surviving
account out when sign-ins or keys move back. Routes: `GET /users/:id/footprint`, `GET /users/merges`,
`POST /users/:id/merge-into/:target/{preview,apply}` and
`POST /users/merges/:jid/{resume,undo}`; every POST is a credential route behind
the owner gate, so no API key or Audiobookshelf token can start one, and the
operation refuses to write without a single-use grant those routes mint. The
operation row carries only counts and account ids, never usernames, emails or
row keys. A
merged-away account cannot be reactivated (undo the merge instead), and
`ResolveMergedUser` lets book-merge undo journals and Audible read-status
reverts that name a merged-away account act on the account that holds its data
now.
