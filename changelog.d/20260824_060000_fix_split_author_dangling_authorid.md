### Fixed

#### Splitting a composite author no longer leaves books with a broken author link

Splitting an author like `"Author1 / Author2"` into separate records relinked
the books correctly in one place and forgot them in another. Books whose main
author was the composite kept an internal pointer to the author record that the
split had just deleted.

The effect depended on which screen you were looking at. In the library list
those books showed **no author at all**; open one directly and it still showed
the old composite name. The same book gave two different answers, and nothing
reported an error either way.

The same flaw is present in the "reclassify author as narrator" action, which
is not fixed here — see the notes on that work for why it needs a decision
first. A repair pass for books already affected is also still outstanding; this
change stops new ones being created by the split path.

Two other copies of this logic (the scheduled author-split job and the
maintenance author-split scan) were already correct; the HTTP handler was the
one that had drifted.
