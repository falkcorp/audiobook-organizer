### Fixed

- Clearing a book's series in the editor now actually removes it. Before, saving an empty series returned success but the book kept showing its old series, because a cached copy of the series name was left on the book. The series number is cleared along with it, and the change is locked so a rescan or metadata fetch does not put the series back. Sending `"series_id": null` to the API now clears the series too.
- Description, genre, ASIN and series number sent as plain fields to the book edit API were ignored. They are now saved, and can be cleared.
- Clearing a narrator now also clears the narrator list Audiobookshelf reads, so the old narrator no longer lingers there.
- An empty author name in a book edit is now ignored. It used to half-remove the author, leaving the book in an inconsistent state.
