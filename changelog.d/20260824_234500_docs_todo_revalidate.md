### Fixed

- **Corrected a false claim in `TODO.md`'s aggregate section.** It said *"Still open:
  the `CreateBookFile`-per-row half, i.e. the 92.1% above"* — but #2866 converted
  exactly that site; `relink_unlinked.go:369` now calls `BatchCreateBookFiles`, which
  coalesces to one recompute per book, and a test pins it. The section now states what
  genuinely remains (the singular `CreateBookFile`, and the never-built
  `BeginAggregateBatch` scope) instead of a scope a later PR had already closed.
- Checked off 8 `TODO.md` entries verified complete against the code at HEAD.
