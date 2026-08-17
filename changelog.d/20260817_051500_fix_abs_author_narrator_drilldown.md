### Fixed

- **Tapping an author or a narrator in the mobile app now shows their books.** Both
  drill-downs answered "No Books Found" for every contributor in the library. The
  `?filter=` handler implemented exactly one group, `series`; `authors` and
  `narrators` fell through to the branch that deliberately serves an empty page
  rather than the whole library, and were never implemented behind it. Reproduced on
  production and confirmed by the server's own warning log, which had been recording
  both groups by name.
- **Compound narrator credits are split into individual people in the Narrators
  tab.** A single stored string like "Jeff Hays, Annie Ellicott" was its own entry
  reading "1 book" — the library had entries naming eight narrators — and every book
  behind one was missing from the real narrators' counts. The tab now lists each
  person once with a correct count. This changes the presentation only: the stored
  narrator rows still hold the compound string, and the web UI still shows it.
- Author and narrator counts on the tab and the number of books behind the tap are
  now computed from one map, so they cannot drift apart.
