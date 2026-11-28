### Fixed

#### Listening progress kept through merges and deletes after the identity cutover

After the identity cutover moves the accounts to core, books' own user rows are empty. Every merge and every "keep this book if anyone has listened to it" check listed the users from those rows, so on a migrated install a merge carried nobody's progress to the surviving book, and the hard-delete, purge, reconcile and fixer checks saw "no one has progress" and deleted books that users were partway through. Under core identity the store's user list (`ListUsers`, `ListUsersStrict`) now answers from core's account directory: every status, the system user included. When that directory is missing or cannot be read, the list is an error and never an empty list, and every one of those checks keeps the book when it gets an error.
