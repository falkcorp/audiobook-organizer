### Fixed

- **A worker configured with any root but `libroot` refused to start.** The
  fingerprint hub advertised roots with `Remote: v.Name == "libroot"`, and the
  worker's startup gate refuses to run when a root it was configured with comes
  back `Remote=false` ("the server does not offer root %q to remote workers").
  So adding `--root books=…` — required for the server to hand out work for the
  72% of the library outside `libroot` — made the worker unstartable. Every root
  the server knows is now offered as remote, matching `remoteEligible`, which
  already accepts any root it can split a path against. A root a worker genuinely
  cannot reach still fails, but per job and visibly, instead of as a silent
  whole-population refusal.
