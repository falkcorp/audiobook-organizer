### Fixed

- **Approving two regroup holds in quick succession could bring one of them
  back.** Row busy state is per item, so a reviewer can approve a second hold
  while the first is still applying. Both refreshes are in flight for the same
  kind, and if the earlier one — read from the server before the second
  approval landed — resolves last, it overwrote the newer list and the
  just-decided hold reappeared, with no error and no spinner. Clicking it again
  either failed or re-applied a destructive merge. Both writers of the row list
  now carry a monotonic request token and a response older than what is on
  screen is dropped, which also covers the kind-switch case a previous fix
  handled on its own.
