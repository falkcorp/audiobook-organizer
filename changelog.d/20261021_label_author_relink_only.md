### Fixed

#### Junk-author repair never removes a part/track/book label credit without a replacement

A credit whose name is only a part, track or book label ("Book 2 (Unabridged)",
"Part 2", "Disc 1", "Vol. 01", "[Track 003] 2") used to be unlinked when the
repair found no real author, leaving the book with none. These names now get
their own relink-only rule (`label_only`): the credit is swapped only when the
provider (or two independent kinds of evidence) names a real author, and is
kept otherwise. A label inside a longer name ("Night Angel Book 1") keeps its
old rule and behaviour.
