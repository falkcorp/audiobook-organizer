### Fixed

- Undo on a "Rejected by you" search result now reports what the server found. When another rejection still refuses the candidate (a rejection under another source that shares its ISBN, or an older title-wide one), the single and bulk metadata search windows keep the chip, say why in its tooltip and in an info toast, and still refuse to stage the row. When the Undo works, the chip also clears on every other result row with the same ASIN under another source, since the server un-rejected those together.
