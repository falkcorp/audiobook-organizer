### Fixed

- **Cover lookups no longer read the whole covers directory once per book.**
  `metadata.CoverPathForBook` resolved a cover by globbing `<rootDir>/covers/<bookID>.*`.
  Because that pattern contains a `*`, Go's `filepath.Glob` cannot do a point lookup — it
  reads the entire covers directory with `Readdirnames(-1)`, sorts every filename, and
  pattern-matches each one, just to find a file whose name was already known apart from its
  extension. The cost scaled with the size of the covers directory, not with the query.

  It was on the ABS search path once per result. Measured on production 2026-09-08 with
  **9,288 files** in that directory, a 35-second CPU profile attributed **27.96 s — 6.4 % of
  all process CPU** — to this one call: 18.00 s reading directory entries, 5.57 s sorting
  them, 4.17 s matching, and only 0.08 s of actual `os.Stat`. A search page rendering ~100
  items did ~100 full directory reads and ~928,000 name comparisons.

  The lookup now stats the five candidate extensions directly, which is constant in the
  directory size (`os.Stat` measured 0.01 ms on that host). The identical glob on the cover
  **download** path — the "do we already have this cover?" check — was replaced by the same
  helper, so it stops paying the cost too.

  Two details preserved deliberately. Precedence when a book has more than one cover on disk
  is **alphabetical by extension** (`.gif` before `.jpg`), because that is what `Glob` did by
  sorting its matches — not the order the old `if` statement listed them in; reordering the
  new list would silently change which file is served, and a test now fails if anyone does.
  Extensions remain lowercase-only, which is guaranteed by the writer rather than assumed:
  every cover is written through `extensionFromContentType`, which returns a hardcoded
  lowercase extension (0 of the 9,288 production files had a non-lowercase extension).

  Two cases now return *less* than before, both of which were unservable anyway: a directory
  named `<bookID>.jpg`, and a dangling symlink.

  This does not make search fast on its own — the same profile showed 47 % of CPU going to GC
  and 18 % to the metadata apply jobs' SHA-256 hashing — but it removes work that grows as the
  library gains covers.

### Security

- **Cover paths are now confined to the covers directory.** The cover download path built both
  its "do we already have this?" lookup and its `os.Create` destination from an unsanitized
  book ID, so an ID containing `../` resolved outside the covers directory. `CoverPathForBook`
  had always reduced the ID with `filepath.Base`; the download path had not, and nothing made
  the two agree.

  The ID is now validated once at the top of the download path, so the existence check and the
  `os.Create` destination can no longer disagree about which file they mean, and a traversing
  book ID is rejected before any network request is made. Each path then gets a second,
  independent guard: the lookup probes through an `fs.FS` rooted at the covers directory, which
  `io/fs` confines by construction (`os.DirFS` rejects any name containing a separator or `..`),
  and the write resolves its destination with `pathvalidation.SecureJoin`, which fails rather
  than escaping its root.

  The validation **rejects rather than truncates**. Reducing `a/b/c` to `c` the way
  `filepath.Base` does would be worse than useless on a write path: two different book IDs
  sharing a last segment would resolve to the same cover filename, and one book's art would
  overwrite the other's. Book IDs are DB-minted ULIDs — both cover-writing paths run only
  after `GetBookByID` returns a real book — so a separator means the caller is wrong, not that
  the right book should be guessed.

  This exposure was not introduced by the performance change above; `filepath.Glob` resolved
  traversal the same way. It surfaced because CodeQL models `os.Stat` as a path sink and does
  not model `Glob`, so replacing one with the other turned a silent pre-existing issue into a
  reported one. It was fixed rather than annotated away.
