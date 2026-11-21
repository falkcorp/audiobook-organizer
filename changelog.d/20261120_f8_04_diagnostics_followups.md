### Fixed

#### Diagnostics bundle retention follow-ups (F8-04, still dark)

Review findings on the dark diagnostics prune and `/metrics` slice. The bundle download route now reads `core.diagnostics.retention` on every request instead of once, so lowering it takes effect at once and the route never serves a bundle the prune is about to delete. The prune no longer deletes the unfinished file of an export that is still running when the retention is shorter than the export's 30 minute timeout: an unfinished file goes only once it is older than the longer of the two. The `/metrics` gate has tests that catch losing either half of its permission check, including over checkers built by the real authorization compiler. A build with no version number now labels `build_info` as `dev` instead of an empty string. Nothing here is reachable until the F8-04 fold wires it.
