### Fixed

- Metadata search now also tries a part-numbered title without its part number ("Rogue Lawyer - 001" → "Rogue Lawyer") when the literal title finds nothing. Audible returned nothing for the suffixed title, so these books only got Google Books candidates, which the apply gate then blocked. The retry accepts a result only if its title has exactly the stem's significant words, so a stem that is also a series name ("The Wheel of Time - 003") cannot match a companion or a sibling book.
