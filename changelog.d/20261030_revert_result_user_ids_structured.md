### Fixed

- The operation revert report no longer decides which notes to hide from a caller without users.manage by looking for the word "user" in the text. Each merge-undo warning now carries the accounts it names, set where the warning is written, and the report withholds a note by that. A note that uses the word "user" for another reason is shown, and a note that names an account without the word is hidden. The JSON served is unchanged.
