### Fixed

- **A path separator in `file_naming_pattern` no longer manufactures a directory.**
  `BuildRelPath` expanded the file pattern through `BuildPath`, which splits on
  `/` and sanitizes each half independently, and nothing then constrained the
  result to a single component. A separator in the file pattern therefore became
  a real directory on disk, and the two-phase rename parked its payload inside as
  `<n>.<ext>.tmp-rename` and failed. The stem is now collapsed to one component,
  which yields exactly the name the file should have had rather than failing the
  organize outright.

  This is distinct from the `scrubVar` guard, which sanitizes variable *values*
  before substitution and does not see separators that arrive in the *template*.
  The two produce identical wreckage on disk and were conflated for months.
