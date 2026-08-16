### Fixed

#### Books stopped being dragged back and forth between two different locations

The app computed a book's destination path in **three** different places, and
they disagreed. `organize` used the folder + file naming patterns; the
metadata-apply rename used a separate `path_format` setting that produced a path
two directory levels shallower; and the multi-file organize path expanded the
folder pattern but then kept whatever filename the file already had.

Because the rename is a real move, each of these dragged files toward its own
answer. Run organize, and the next metadata apply moved the book back. Run the
apply, and the next organize moved it again. There was no state at which both
were satisfied, so a book could be moved indefinitely.

All three now go through one builder (`BuildRelPath`) driven by the same two
patterns, so they cannot arrive at different answers. A conformance test asserts
all three agree on the same fixture, including the multi-file case, which is the
leg nothing had ever compared.

Along the way this fixed a real bug in the surviving builder: `{series_prefix}`
had its intentional trailing `" - "` trimmed away, so every book in a series was
being named `MySeries -Book` rather than `MySeries - Book`.

#### Multi-file books are now named by the file naming pattern, not their old filenames

Organizing a multi-file (directory) book put it in the right folder but left
every file inside under whatever name it arrived with. Only single-file books
ever had the file naming pattern applied. Files of a multi-file book are now
named by the pattern and numbered by their track number.

A guard comes with it: a file naming pattern containing no `{track}` placeholder
gives every file of a multi-file book the same name. Now that organize writes to
these paths, that would have merged a 40-part book into a single file. When the
pattern does not distinguish the files, a zero-padded track number is appended
and the situation is logged.

#### Organized book records no longer point at files that were never created

When organize created the "organized" copy of a multi-file book, the per-file
database rows were filled in by *assuming* each file kept its original name in
the new folder. That assumption is no longer true, and it was never verified
against the disk. The rows now come from the same planner that did the copying,
and each is checked against what is actually on disk before being written — a
file that organize skipped keeps a row pointing at where it really is.

### Removed

#### The `path_format` and `segment_title_format` settings

These drove the second, conflicting path builder. Both are gone from the
settings UI, the API and the config file; the folder and file naming patterns
are now the only things that decide where a book goes. The default file naming
pattern is `{title} - {track:02d}`.

**Existing libraries will see files move once** as books settle onto the single
scheme. That is the intended one-time cost of ending the back-and-forth.
