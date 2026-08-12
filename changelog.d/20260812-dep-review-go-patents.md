### Fixed

- **Dependency Review blocked every `golang.org/x/*` bump on a licence that is not a
  problem.** Those modules are BSD-3-Clause — already allowed — plus Go's standard
  `PATENTS` file, an *additional* grant of patent rights that makes them strictly more
  permissive than BSD-3-Clause alone. ScanCode reports the pair as the single compound
  expression `BSD-3-Clause AND LicenseRef-scancode-google-patent-license-golang`, and
  `allow-licenses` matches the whole expression rather than each term, so the compound
  string failed the policy with both halves acceptable.

  It surfaced on #2305, where `bytedance/sonic 1.15.1 → 1.15.2` pulled
  `golang.org/x/arch`. Dependency Review only reviews *changed* dependencies, which is
  why nine modules with this licence sat in `go.mod` for months without tripping it —
  and why each of the other eight was one Dependabot PR away from an identical surprise.
  All nine are listed, without versions.

  Named as packages rather than widened in `allow-licenses`: adding the `LicenseRef-`
  term there would not have fixed it (the compound expression still equals no single
  allowed entry) and would have loosened the policy for every ecosystem at once.
