### Fixed

#### Change Log row — restored a real, keyboard-reachable "Compare snapshot" button

The Change Log row for a `metadata_apply`/`tag_write` entry opened the
snapshot comparison view only on mouse click; there was no way to reach or
activate it from the keyboard, and it had no accessible name for screen
readers. Added a real `Button` labeled "Compare snapshot" alongside the
existing Revert button, so it is focusable and activates on Enter/Space for
free. The row itself keeps its existing mouse-click convenience but is
deliberately not made into a `role="button"` container: it already nests one
interactive control (the Revert button, joined now by Compare snapshot), and
`role="button"` elements have "Children Presentational: True" in the ARIA
spec, so nesting real interactive controls inside one is unsupported and an
`aria-label` on the row would have overridden the accessible name computed
from the row's own text (timestamp, type, summary), leaving every actionable
row announcing as just "Compare snapshot, button." The new button's own
`onClick` calls `stopPropagation()`, mirroring the existing Revert button, so
neither a mouse click nor a native Enter/Space activation on Compare
snapshot also fires the row's own click handler a second time.
