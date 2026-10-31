<!-- file: docs/decisions/2026-10-30-revert-result-user-ids-structured.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b9e2d70-c1a4-4f36-8e07-d2a6f3b81c49 -->
<!-- last-edited: 2026-10-30 -->

# Revert report: withhold user notes by field, not by word

TODO-REVERT-RESULT-WORD-MATCH. `RevertResult.WithoutUserData` dropped any note whose text contained "user" (`undo.NamesUser`).

## Decision log

- Chosen: a `merge.Warning` type (`Text`, `UserIDs`) replaces the bare string in the merge undo results (`CombineUndoResult.Warnings`, `SiblingUndoResult.Warnings`) and in what `RestoreFollowedProgress` / `restoreAbsorbedSide` / `restoreSurvivorSide` return. The four producers that write an account id into the text (survivor put-back x3, absorbed-side "written after the follow") build it with `UserWarning(text, userID)`; every other warning is `PlainWarning`.
- Chosen: `audiobooks.RevertNote` (`Text`, `UserIDs`) replaces the bare string in `RevertResult.Kept` and the unexported `combineKept`. `partialTagRestore` carries `userIDs` (the union of the warnings it was built from) so the `Kept` line inherits them. `WithoutUserData` withholds a note when `UserIDs` is non-empty and never looks at the text.
- Chosen: JSON served to clients is unchanged. `RevertNote` marshals as its bare text (the ids stay server side), so `kept` is still an array of strings and there is no TypeScript reader of it to change. `merge.Warning` marshals as a bare string when it names no user and as `{"text","user_ids"}` when it does; that form exists only so the undo's persisted applied record keeps the ids across a store round trip, and the merge undo results are not served directly (only through `RevertResult`).
- Chosen: `SupersededDetail`, `HandOffFailed` and `SettleSkipped` stay `[]string` and are no longer stripped. They are version-group and primary-flag notes; no producer puts an account into them.
- Chosen: `undo.NamesUser` is kept. The duplicates handler's combine and sibling journal listings still use it: the journals persist `Warnings` as bare strings and a stored journal has no field to read. Its comment now says so and points to the structured types.
- Rejected: parallel index sets (`UserWarningIdx []int`) next to the `[]string` lists. They drift the moment a list is filtered or appended to in only one place.
- Rejected: converting the persisted journals' `Warnings` in the same change. It is a stored-format change with its own migration question and a separate handler; filed as a todo fragment.
- Rejected: a legacy shim that guesses "names a user" from the text of an applied record written before this change. It would put the word match back; the window is the moments between an undo's last write and its status write, after which the record is deleted.
- Known coarseness, unchanged: a `Kept` line for a combine row joins that row's warnings, so a row with one user warning and one plain warning is withheld as one line. The combine notes `Summary` lists are per warning and are split correctly.
