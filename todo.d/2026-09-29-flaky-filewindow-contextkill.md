- [ ] **FLAKE-FPWINDOW** `TestFileWindow_ContextKill`
      (`internal/fingerprint/window_exec_test.go`) failed once on Woodpecker
      (pipeline 238, #3613's head) and passed on restart (241) with no code
      change. Find the timing assumption (context cancel vs. subprocess exit)
      and make it deterministic; done = 50 consecutive `-count=50 -race` passes
      on the Mac agent.
