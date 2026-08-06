<!-- file: todo.d/20260806_150400_multidisc_holds_are_duplicates.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5a1e83c7-42b9-4dc6-8e07-6c39f2a1b5d8 -->
<!-- last-edited: 2026-08-06 -->

- [ ] **The 3 dangerous multidisc holds are DUPLICATES, not series — feed them to
  the duplicate-detection track.** Measured 2026-08-06 from a full pre-apply
  snapshot of all 132 pending `regroup.multidisc` holds (4,146 member books,
  zero unreadable).

  `TODO.md` predicted ~9 holds with book-length members, presumed to be series
  the guard would have caught. There are **3**, and all three are two-member
  holds whose members have near-identical runtimes:

  | hold | members |
  |---|---|
  | `01KXF8BNKENR530AKMMKJYD5E1` | `Brother Wulf` 6.30 h + `Brother Wulf - Joseph Delaney` 6.30 h |
  | `01KXF8BNKACGA6ZAEBPCQK09FX` | `Sevenfold Sword` 20.56 h + `Sevenfold Sword` 21.47 h |
  | `01KXF8BNHY7AE56CPZWY9VW9VF` | `The Warring Son` 11.77 h + `The Warring Son` 11.77 h |

  Same title, same runtime, two rows. That is the never-delete / re-associate
  shape, not a series of distinct novels.

  **The recommender emits `separate` for them, and that is the correct default.**
  Separate destroys nothing and leaves them for a signal that can actually
  establish identity. 🔴 Do NOT tune the recommender toward `duplicate-of` on
  runtime similarity — equal runtimes are not identity evidence. Two different
  books can share a runtime, and acting on that would merge distinct works
  through a path that hard-deletes the absorbed row.

  These 3 are a clean, small, real test set for
  [[never-delete-re-associate]] — use them rather than inventing fixtures.
