### Fixed

- **Interrupted operations now actually resume.** Two stacked bugs meant a job
  killed by a restart never came back. `GetInterruptedOperations` matched only
  `running`/`queued`/`interrupted`, while the registry mints `interrupted_quiesced`
  (every ResumePolicy except ResumeDrop), `interrupted_dropped`,
  `interrupted_restart` and `interrupted_ask` — so the startup sweep was blind to
  the very rows it exists to resume. And even when a row was found, `ResumeRestart`
  only set it to `"queued"` without enqueueing it, so nothing ever ran it. Status
  matching is now a prefix test, and `ResumeRestart` re-enqueues for real.
- **Compound narrator and author names are split into individual people.** The
  splitter was `strings.Split(name, " & ")` and nothing else, duplicated verbatim in
  two packages, and gated behind `strings.Contains(name, " & ")` so it never even ran
  for comma-separated credits. "Kate Reading, Michael Kramer" was therefore one
  narrator. There is now a single implementation in `internal/util` handling `&`,
  `and`, `;`, `,`, `with`, `+` and `/`, de-duplicating results, and deliberately
  refusing to split surname-first names like "Le Guin, Ursula".
