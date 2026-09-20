- [ ] **The dry-run flag spelling is split across the op registry.**
      Measured 2026-09-20: 11 params structs tag it `dryRun`, 18 tag it
      `dry_run`. `encoding/json` drops an unrecognised field silently, so an
      operator who reaches for the wrong one for a given op gets that op's
      default with no warning. `maintenance.author-path-link` and
      `maintenance.author-id-repair` already handle this correctly — `*bool`,
      absent means dry run, both spellings accepted, disagreement refused — and
      `maintenance.duration-reextract` was brought in line after it silently
      previewed a run meant as an apply. Sweep the rest onto the same pattern,
      and check each one's default: a `bool` field with no explicit default
      means Go's zero value (false = APPLY) decides when the flag is dropped.
