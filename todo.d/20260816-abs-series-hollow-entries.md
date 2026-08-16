- [ ] **ABS series list emits a non-ABS `books[]` shape, and no series render in ABS clients.**
      Measured 2026-08-16 against production with the client's exact query
      (`?page=0&limit=50&sort=name`, what AudioBooth actually sends).

      **Root cause (evidenced):** ABS defines a series' `books` as full
      `LibraryItem` objects. Ours emit six ad-hoc fields only:
      `duration, id, libraryId, libraryItemId, sequence, title` — no `media`,
      no `media.metadata`, no `mediaType`, no `coverPath`, no `path`/`ino`.

      The control that makes this conclusive is the **playlists** endpoint,
      which the same app renders correctly: its items embed a complete
      `libraryItem` with all 20 ABS fields including `media.metadata`,
      `coverPath` and `mediaType`. Same client, same auth, same library — the
      one with the correct shape works, the one with the ad-hoc shape does not.
      A typed (Swift) client decoding `books: [LibraryItem]` fails on the first
      entry and discards the whole response, which is why **23 of 50
      well-formed series still render as zero**.

      Ruled out — do not re-investigate:
      - Not a timeout: series is 20 KB in 0.34s; playlists is 131 KB in 3.2s
        and renders fine.
      - Not auth, not pagination, not the query params: HTTP 200,
        `results=50`, `total=15528`.

      **Secondary bug, worth fixing in the same pass:** 27 of 50 entries have
      `books: []`, and 9 of those are self-contradictory — `numBooks >= 1` with
      `books: []` and `totalDuration: 0` (e.g. "Salem's Lot (read by Ron
      McLarty)" reports `numBooks=1`). The other 18 report `numBooks=0`.

      Fix: build the series `books` array from the same library-item serializer
      the playlists path already uses, rather than a bespoke projection.
