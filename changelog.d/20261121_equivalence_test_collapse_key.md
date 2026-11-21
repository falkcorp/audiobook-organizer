### Fixed

- The operations bell equivalence test (`operationsIndicator.equivalence.test.tsx`) reads the shell frame's collapse key through `storageKey('operations', 'indicatorCollapse')` instead of the pre-shell literal `ops-indicator-collapse-v1`, so the "saves a toggle under the layout key" and "starts from the saved state" cells pass again and cannot drift from the frame. Test-only; no product code changed.
