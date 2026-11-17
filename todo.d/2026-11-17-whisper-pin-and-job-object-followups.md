- [ ] **WHISPER-WORKER-FOLLOWUPS** Items left from the non-Mac worker review that are
      not defects in the code as shipped: (1) `install-whisper-supervisor.ps1`
      registers the scheduled task with `-RestartCount 3`, which may restart a
      supervisor that deliberately gave up (exit 1) up to three more times, each with
      a fresh crash streak; decide whether Task Scheduler should restart it at all
      (needs a real Task Scheduler to observe). (2) `whisper-windows.yml` installs
      ffmpeg with an unpinned `choco install ffmpeg` and its pytest steps use Python
      3.12 while production runs 3.14 (the `uv run` smoke covers 3.14); pin the ffmpeg
      version once one is chosen. (3) On POSIX nothing reaps a decode child stuck in a
      native call that holds the GIL after its parent dies (the orphan watch is a
      Python thread); Windows has a Job Object for this, Linux would need
      `prctl(PR_SET_PDEATHSIG)`. (4) Mac worker `whisper_mlx_server.py` line 65
      carries a real-looking path example in a comment; reword it in a Mac-worker PR.
