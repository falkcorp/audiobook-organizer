### Changed

#### `TagComparison` — remove dead expanded state and Collapse wrapper

The `expanded` state in `TagComparison.tsx` was always true and never toggled — it was initialized with `useState(true)` and only ever set back to true. The e2e test that used to verify toggle behavior (`tag-comparison-toggle`) was intentionally deleted on 2026-08-09, indicating the UI toggle was deliberately dropped rather than merely lost. Removed the state variable, the `setExpanded(true)` call in the snapshot-select effect, and the surrounding `<Collapse>` wrapper, allowing the metadata table to always render without conditional visibility.
