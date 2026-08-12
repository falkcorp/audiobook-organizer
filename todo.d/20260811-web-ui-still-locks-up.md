- [ ] **UI-LOCKUP-2** The web interface still locks up despite the virtualization
      work and the earlier backend fixes. Reported 2026-08-11.

      **Do not assume this is still a frontend/DOM-volume problem.** Measured on
      prod the same night, the backend alone can account for a UI that appears
      frozen:

      - `GET /api/v1/audiobooks?library_state=imported&limit=1` took **36
        seconds** — for one row.
      - `GET /api/libraries/{id}/personalized` took **2m10s**.
      - The server was OOM-killed **four times** in ninety minutes, and memdb
        warmup takes **568 s (9.5 min)** during which the library is unusable —
        `library list warm-up: memdb not ready after 5 min, skipping` fires
        because warmup outlives its own waiter, so the list cache never warms.
      - The activity-log query ignores client disconnect, so abandoned requests
        keep scanning; 30 such goroutines were pinning 30 GB with zero clients
        connected.

      A frontend cannot render what it cannot fetch, and a browser tab whose
      requests never return looks exactly like a frozen UI. So the first job is
      to **separate the two**, not to add more virtualization:

      1. Reproduce with DevTools Network open. Are requests **pending** (backend)
        or returning fast while the page janks (frontend)?
      2. If backend: which endpoint, and is it one of the known-unbounded ones?
      3. If frontend: profile it. Is virtualization actually active on the list
        that janks, or only on the one that was fixed before?
      4. Check whether the lock-up correlates with server restarts / warmup
        windows — if it only happens in the ~10 min after a restart, it is
        startup sequencing, not the UI.

      State which of the two it is, with evidence, before writing any fix. The
      previous round of this task was closed against a DOM-volume hypothesis; if
      that hypothesis was wrong, or was right then and is not the binding
      constraint now, say so explicitly.
