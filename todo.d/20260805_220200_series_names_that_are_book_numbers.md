<!-- file: todo.d/20260805_220200_series_names_that_are_book_numbers.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f57e91b-8c04-4d73-a6e8-95b013fc287d -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Series names that are really book numbers (~874 books)** — owner item 4
  (2026-08-05).

  Shapes still unhandled: `<Series> N: <Title>`, `N - <Title>`, and `N (paren)`.
  `the world 4` means **book 4 of `the world`** — the number belongs in the
  series *position* field, not baked into the series *name*.

  🔴 **This is a DATA bug, not a display bug.** The owner has corrected that
  reading twice. Do not re-derive it, and do not "fix" it in the frontend.

  `maintenance.series-denumber` already exists and handles the trailing-number
  shape; these are the remaining shapes.

  🔴 **Extend `IsJunkSeriesBase` ALONGSIDE the parser, not after it.** That guard
  is what stopped **285 bad merges** in the dry run. A parser extension that
  lands before the guard extension will happily collapse series bases the guard
  would have rejected — and series merges are destructive.

  Dry-run first and read the numbers; independent of the First Aid track, so it
  can proceed in parallel.
