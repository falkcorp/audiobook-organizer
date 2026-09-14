### Fixed

- Metadata search now also tries the book's own name out of rip-style titles
  ("Legend of Drizzt Book 03 - The Dark Elf Trilogy - Sojourn" → "Sojourn",
  "Brandon Sanderson - Mistborn 01 - The Final Empire" → "The Final Empire",
  "Knaves over Queens : Wild Cards" → "Knaves over Queens"). Audible returns
  nothing for the full decorated title even with the author. A hit must carry
  every word of that name and no word the full title lacks, so a sibling in
  the same series is never taken for the book.
