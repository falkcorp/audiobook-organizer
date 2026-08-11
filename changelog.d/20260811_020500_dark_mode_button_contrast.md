### Fixed

- Dark-mode buttons on the library pages were nearly invisible against the
  background. `primary.main` was hardcoded to `#1976d2` in **both** colour modes
  — MUI's light-mode blue — which measures **3.89:1** against the dark
  background, below the 4.5:1 WCAG AA floor. The library toolbar is built from
  `variant="outlined"` buttons, which draw their label in `primary.main` and
  their border at 50% alpha, so the border sat near 2:1. Dark mode now uses
  MUI's dark-palette values (**9.94:1** primary, **8.77:1** secondary); light
  mode keeps the original brand colours.
