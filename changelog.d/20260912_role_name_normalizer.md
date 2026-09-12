### Fixed

#### Role names with surrounding spaces are now findable and deletable

Creating a role stored its name-index entry lowercased but not trimmed, while
looking a role up by name and deleting a role both trimmed the name first. A
role created as " Editor " could not be found by "editor" or by " Editor ",
and deleting it removed the index entry for "editor" (possibly another role's)
while leaving its own behind. Role creation now uses the same trim+lowercase
key as the lookup and delete paths, rejects names that are only whitespace,
and reports a storage error from the duplicate-name check instead of treating
it as "no duplicate". Deleting a role now removes the name-index entry only
when that entry points at the role being deleted, so it can no longer remove
another role's entry.

The only production code that creates roles is the built-in seeding of the
admin, editor and viewer roles, whose names have no surrounding spaces, so
existing databases need no re-keying.
