### Fixed

- dedup.label-refinement on the current engine reports exactly what it did before the F5-04 conversion: no extra progress frame at the start of each step, and its log lines carry no `phase` attribute.
- The native dedup.window-index-build resumes from zero after a restart, as the coordinator's resume ruling says for a kind whose current code reads no checkpoint.
- The run-all re-attach warning logs the step id (for example `find`) again, and a re-attached native child shows its "waiting for" progress note.
