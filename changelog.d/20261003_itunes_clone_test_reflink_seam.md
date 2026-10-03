### Fixed

- **CI: the "Frontend Build and Test / Go CI" job failed on every run.** One
  test of the iTunes clone-into-library repair called the real filesystem clone,
  which GitHub's runners cannot do, so the job was red on main and on every pull
  request that touched the web app. The repair's clone step for partly-organized
  books is now injectable: production still clones and never copies, and the
  test supplies a copy. A new test covers a clone that fails part-way: the files
  already made are removed, no file row is repointed, and the group reports
  failed.
