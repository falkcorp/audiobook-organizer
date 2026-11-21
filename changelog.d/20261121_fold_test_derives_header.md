### Fixed

- The split-readiness fold test no longer hard-codes the document's version and last-edited header lines, which broke Repo Guards on main the night the nightly fold bumped them (cc01e963b). It reads the expected "before" lines from the committed document and asserts the fold changes exactly the two marked regions plus a one-patch version bump and the date it was given.
