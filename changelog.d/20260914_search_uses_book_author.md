### Fixed

- Batch metadata candidate fetches now search providers by the book's own author. The fetch loaded the book without its author attached, so it always passed an empty author hint, and every provider was queried by title alone. Audible only matches exact titles, so it missed books like "Blood of Elves The Witcher, Book 1 (Unabridged)" that it finds right away with the author. Only the provider queries change: the cache identity hash still uses the hint, so existing cached rows stay valid.
