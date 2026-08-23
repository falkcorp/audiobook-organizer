- [ ] **RECORD-DEMO-TEMPDIR-LEAK: `scripts/record_demo.js` never removes the temp directory
      it creates.** There is no `rmSync`/`rmdirSync`/`unlinkSync` anywhere in the file; the only
      `finally` closes the browser. One directory leaks per run. Pre-existing — the old
      `mkdirSync` path leaked identically — and #2798 only changed how the path is chosen, not
      whether it is cleaned up. Low priority; fix alongside DEMO-RECORDING-BROKEN, since the
      script does not currently get far enough to matter.
