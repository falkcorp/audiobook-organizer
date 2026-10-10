### Fixed

#### Four task briefs no longer render as one long HTML comment

The `last-edited` header line of four holistic-roadmap task briefs had lost its closing
`-->`, so everything after it rendered as a comment. `scripts/check_task_briefs.py` now
fails any brief whose four header lines are not closed one-line comments, or whose
`file:` line does not match its path.
