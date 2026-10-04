### Fixed

#### Swapped title/author fixer: one corrupt journal row no longer errors every continuation candidate

The continuation check (`swapContinuation`) read the book's journal rows with
`GetBookChanges`, which fails when any journal row anywhere is undecodable
(both the indexed path's undecodable-row gate and the full scan refuse). One
corrupt row in an unrelated operation turned every continuation candidate
into an error row. On that error it now reads the repair operation's own rows
(`GetOperationChanges`) filtered to the book, and stays fail-closed (an error
row, never "no continuation") when that read fails too.

#### Single-book edit reports a partial save instead of a plain success

`PUT /audiobooks/:id` returned 200 with only a log line when the edit's row
committed but its field locks and overrides, its change history or its author
credits were not saved. The response now carries `warnings` beside the
(still flat) book object, with the same "the edit was saved but …" wording the
batch update uses per book, and the book page and library edit dialogs show
it as a warning toast instead of "Metadata saved".
